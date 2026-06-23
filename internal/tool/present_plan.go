package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PresentPlanTool deja que el agente presente su plan final: guarda el plan en
// Markdown bajo el workspace y devuelve un mensaje de "detente y espera" que solo
// ve el modelo. Es la mitad de backend del modo plan estilo Cursor: el archivo se
// muestra al usuario, que decide Aceptar o Solicitar cambio. A diferencia del
// write, un plan se revisa EN SITIO: cada call sobrescribe el plan de la sesion.
type PresentPlanTool struct {
	Root string
}

// NewPresentPlanTool arma la tool sobre Root, por consistencia con NewWriteTool y
// NewBashTool. El path del plan se deriva de Root + sessionID, nunca del input del
// modelo, asi que no hay riesgo de traversal y no necesita compuerta de sandbox.
func NewPresentPlanTool(root string) *PresentPlanTool { return &PresentPlanTool{Root: root} }

func (*PresentPlanTool) Name() string { return "present_plan" }

//go:embed present_plan.txt
var presentPlanDescription string

func (*PresentPlanTool) Description() string { return presentPlanDescription }

func (*PresentPlanTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"Titulo corto del plan."},"plan":{"type":"string","description":"El plan final completo en Markdown."}},"required":["plan"]}`)
}

// Execute parsea {title, plan}, exige un plan no vacio, resuelve la sesion del
// contexto (igual que snapshots), guarda el plan en
// <Root>/.atenea/plans/plan-<sessionID>.md (sobrescribiendo) y devuelve un mensaje
// que le indica al modelo detenerse y esperar la decision del usuario. El mensaje
// usa la ruta RELATIVA del plan.
func (pt *PresentPlanTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Title string `json:"title"`
		Plan  string `json:"plan"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("present_plan: input invalido: %w", err)
	}
	if strings.TrimSpace(in.Plan) == "" {
		return Result{}, fmt.Errorf("present_plan: plan requerido")
	}

	sessionID, _ := ctx.Value(sessionIDKey{}).(string)
	if sessionID == "" {
		sessionID = "default"
	}
	// Defensivo: el sessionID nunca deberia traer separadores (es "chat-<uuid>"),
	// pero si los trajera no debe escapar del directorio de planes.
	sessionID = strings.ReplaceAll(sessionID, string(os.PathSeparator), "_")
	sessionID = strings.ReplaceAll(sessionID, "/", "_")

	dir := filepath.Join(pt.Root, ".atenea", "plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	name := "plan-" + sessionID + ".md"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(in.Plan), 0o644); err != nil {
		return Result{}, err
	}

	rel := ".atenea/plans/" + name
	msg := fmt.Sprintf("Plan guardado en %s y mostrado al usuario. DETENTE: no llames mas herramientas; espera a que el usuario pulse Aceptar o Solicitar cambio.", rel)
	return Result{Output: msg}, nil
}
