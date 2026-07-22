package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"atenea/internal/llm"
	"atenea/internal/tool/repair"
)

// Tool es una herramienta registrada: su esquema anunciable y su ejecucion. El
// registry la materializa (Name/Description/Schema -> llm.ToolDef) y la asienta
// (Execute). Execute recibe el input JSON crudo del modelo y lo parsea con
// json.Unmarshal (nunca por match de string: el modelo escapa el JSON distinto
// entre turnos, ver llm.Event.Input). Devuelve el Result completo; el registry
// se encarga de acotarlo.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, input json.RawMessage) (Result, error)
}

// Call es una tool call que Settle debe asentar. En M5 el loop de consumo la
// arma desde el evento del proveedor: Call{ID: ev.CallID, Name: ev.ToolName,
// Input: ev.Input}. Un struct nombrado se lee mejor que tres args posicionales y
// crece (p.ej. metadata de epoch en M7) sin cambiar la firma de Settle.
type Call struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Result es el resultado asentado de una tool call. Output es lo que vera el
// modelo en el siguiente turno (acotado por el OutputStore si era grande);
// Truncated marca que es una version acotada y que el output completo quedo en el
// OutputStore, referenciable por el CallID de la Call. Diff es un diff unificado
// SOLO para la UI (edit/write): no lo ve el modelo y no se acota.
type Result struct {
	Output    string
	Truncated bool
	Diff      string
}

// SettleFunc asienta una tool call: valida contra el set materializado,
// repairs the input against the tool's schema (repair.Repair), ejecuta y
// devuelve el Result. Esta cerrada sobre las tools permitidas de una
// materializacion: una tool fuera del set devuelve UnknownToolError sin
// ejecutar nada. M5 la invoca concurrentemente desde consume (errgroup); por
// eso es segura para uso concurrente (no muta estado compartido salvo el
// OutputStore, que tiene su candado).
type SettleFunc func(ctx context.Context, call Call) (Result, error)

// Permissions es el set de tools permitidas por nombre. Materialize solo anuncia
// (y Settle solo asienta) las que estan en true; una tool ausente o en false se
// trata como denegada: el agente declara explicitamente su set anunciado. El
// modelo de permisos rico (ask, edicion/bash por patron) llega cuando el agente
// lo necesite; en M4 alcanza el set de nombres.
type Permissions map[string]bool

// Materialized es el resultado de Materialize: las definiciones anunciables al
// modelo y el asentador cerrado sobre ese set. El runner (M5) pone Definitions en
// llm.Request.Tools y pasa Settle al loop de consumo.
type Materialized struct {
	Definitions []llm.ToolDef
	Settle      SettleFunc
}

// UnknownToolError lo devuelve Settle cuando la Call nombra una tool que no esta
// en el set materializado: desconocida para el registry o denegada por permisos
// (en M7 tambien una stale por epoch). No se ejecuta nada (sin efectos
// laterales). M5 lo traduce a Tool.Failed. Es un tipo (no un sentinel) para que
// el mensaje nombre la tool y el llamador la inspeccione con errors.As.
type UnknownToolError struct{ Name string }

func (e *UnknownToolError) Error() string {
	return fmt.Sprintf("tool %q desconocida o no permitida", e.Name)
}

// Registry es el catalogo de tools del agente y su acotador de output. Es
// inmutable tras NewRegistry (Materialize solo lee), asi que materializar y
// asentar desde varias goroutines es seguro; el unico estado mutable compartido
// es el OutputStore, que se candadea solo.
type Registry struct {
	tools   map[string]Tool
	outputs *OutputStore
}

// NewRegistry arma el registry con su OutputStore y las tools dadas, indexadas por
// nombre. Si dos tools comparten nombre gana la ultima (config del programa, no
// input del modelo).
func NewRegistry(outputs *OutputStore, tools ...Tool) *Registry {
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	return &Registry{tools: m, outputs: outputs}
}

// Permissions returns a permission set containing every registered tool.
// The returned map is independent from the Registry, so callers may narrow it
// without mutating the catalog. Assembly code should use this instead of
// repeating tool names next to NewRegistry: registration then remains the
// single source of truth for the default tool set.
func (r *Registry) Permissions() Permissions {
	permissions := make(Permissions, len(r.tools))
	for name := range r.tools {
		permissions[name] = true
	}
	return permissions
}

// Materialize filtra el catalogo por permisos y devuelve las definiciones
// anunciables y un Settle cerrado sobre las tools permitidas. Las Definitions van
// ordenadas por nombre para que el request sea determinista (estabiliza el cache
// de prompt del proveedor y los tests). El Settle captura solo las permitidas:
// una Call fuera de ese set devuelve UnknownToolError ANTES de ejecutar, asi que
// una tool denegada o desconocida no produce efectos laterales.
func (r *Registry) Materialize(perms Permissions) Materialized {
	allowed := make(map[string]Tool, len(r.tools))
	defs := make([]llm.ToolDef, 0, len(r.tools))
	for name, t := range r.tools {
		if !perms[name] {
			continue
		}
		allowed[name] = t
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })

	settle := func(ctx context.Context, call Call) (Result, error) {
		t, ok := allowed[call.Name]
		if !ok {
			return Result{}, &UnknownToolError{Name: call.Name}
		}
		// The input goes through the repair layer BEFORE executing: an
		// almost-valid input is repaired and an irreparable one returns
		// the error without executing the tool. An empty input (tool
		// with no arguments) skips the layer: there is nothing to repair.
		input, notes := call.Input, []string(nil)
		if len(input) > 0 {
			var err error
			input, notes, err = repair.Repair(call.Name, t.Schema(), call.Input)
			if err != nil {
				return Result{}, err
			}
		}
		res, err := t.Execute(ctx, input)
		if err != nil {
			return Result{}, err
		}
		// Repair notes are prepended BEFORE capping, so the <repair_note>
		// header survives the capping and the model sees it.
		capped := r.outputs.Cap(call.ID, repair.WithNotes(notes, res.Output))
		capped.Diff = res.Diff // el diff (solo-UI) no se acota
		return capped, nil
	}
	return Materialized{Definitions: defs, Settle: settle}
}
