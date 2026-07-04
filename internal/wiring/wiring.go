// Package wiring arma el ensamblado compartido del agente (tools, skills,
// subagentes, runner) anclado a una raiz de workspace. Es la unica fuente de
// verdad del cableado: la app Wails (app.go) y el engine headless de la TUI
// construyen el mismo agente llamando Build con sus propias dependencias.
package wiring

import (
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"sync/atomic"
	"time"

	"atenea/internal/agent"
	"atenea/internal/command"
	"atenea/internal/event"
	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/session/prompt"
	"atenea/internal/session/runner"
	"atenea/internal/session/subagent"
	"atenea/internal/skill"
	"atenea/internal/tool"
	"atenea/internal/tool/hashline"
)

// outputLimit acota la salida persistida de cada tool call.
const outputLimit = 32 * 1024

// Config son las dependencias que el caller aporta al ensamblado.
type Config struct {
	// Root es la raiz del workspace: ancla las file/exec tools, las skills,
	// los subagentes y el system prompt.
	Root string
	// Provider es el modelo: lo comparten el runner, los subagentes (task) y
	// la tool web_fetch.
	Provider llm.Provider
	// Store es el log durable de sesiones que usa el runner. Ya viene decorado
	// por el caller (p.ej. EmittingStore sobre el Bus); Build no lo vuelve a
	// envolver.
	Store session.Store
	// Inbox es la cola de prompts que el runner drena por sesion.
	Inbox session.Inbox
	// Gate es el gate de ask-before-run que comparten el runner principal y
	// los subagentes; el caller entrega por el la decision del usuario.
	Gate session.PermissionGate
	// Snaps es el read-state por sesion que comparten read/write/edit. El
	// caller lo crea una sola vez para que sobreviva a los re-ensamblados.
	Snaps *tool.SessionSnapshots
	// Bus publica los eventos de permiso de los subagentes en el canal del
	// padre (ChildPermissionStore), el mismo que ya escucha el frontend.
	Bus *event.Bus
	// Local marca un provider de endpoint local (LM Studio, Ollama): el
	// system prompt base pasa al protocolo de function-calling explicito.
	Local bool
	// NextID genera los assistantMessageID del runner (ver NewIDGen).
	NextID func() string
	// Mode es el hook de modo por sesion (normal/plan) que el runner consulta
	// cada turno; nil = siempre modo normal.
	Mode func(sessionID string) session.Mode
}

// Built son las piezas mutables que el caller publica tras el ensamblado: el
// runner listo para correr, el glob del @-menu y los slash-commands del composer.
type Built struct {
	Runner   *runner.Runner
	Glob     *tool.GlobTool
	Commands *command.Set
}

// needsApproval decide que tool calls exigen aprobacion del usuario
// (ask-before-run). Unica fuente de verdad: la comparten el runner principal y
// los subagentes, asi un hijo no evade el gate que el chat principal exige.
func needsApproval(c tool.Call) bool { return c.Name == "bash" }

// Build arma todo el cableado anclado a cfg.Root: las file/exec tools, el glob
// del @-menu, las skills y sus slash-commands, el catalogo de subagentes y un
// runner nuevo con el system prompt apuntando a la raiz. No muta estado global:
// devuelve las piezas para que el caller haga su propio swap.
func Build(cfg Config) Built {
	root := cfg.Root
	// El @-menu de archivos del composer lista el workspace via este glob.
	// Comparte la raiz con las file tools; reusa el searcher de ripgrep ya
	// probado (respeta .gitignore, excluye .git).
	glob := tool.NewGlobTool(root)
	// Skills al estilo opencode (disclosure progresivo): se descubren bajo las rutas
	// del proyecto Y las globales del home (skillDirs). Sus metadatos van en el system
	// prompt (skill.Format), la tool skill carga el cuerpo bajo demanda, y de cada una
	// se deriva un slash-command. Un fallo de descubrimiento no es fatal: sin skills.
	skills, err := skill.Discover(skillDirs(root)...)
	if err != nil {
		log.Printf("atenea: no se pudieron descubrir las skills: %v", err)
	}
	skillsBlock := skill.Format(skills)
	// Slash-commands del composer, derivados de las skills (un "/<name>" por skill).
	commands := command.New(command.FromSkills(skills))
	// Subagentes: catalogo = built-in (explore read-only, general full) mas los .md
	// del workspace (.atenea/agents propio, .agents/agents estandar; el propio override
	// al homonimo). Un fallo de descubrimiento no es fatal: quedan los built-in.
	agentDefs, err := agent.Catalog(
		filepath.Join(root, ".atenea", "agents"),
		filepath.Join(root, ".agents", "agents"),
	)
	if err != nil {
		log.Printf("atenea: no se pudieron descubrir los subagentes: %v", err)
	}
	// Registry de los subagentes: las mismas tools de archivo/busqueda/exec, acotadas
	// por def.Tools de cada agente (un explore read-only solo recibe read/grep/glob).
	// Sin la tool task: los subagentes no anidan en el wiring real.
	childRegistry := tool.NewRegistry(tool.NewOutputStore(outputLimit),
		tool.NewReadToolWithSnapshotProvider(root, cfg.Snaps), tool.NewWriteToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, cfg.Snaps),
		tool.NewGlobTool(root), tool.NewGrepToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewBashTool(root))
	// La tool task levanta subagentes hijos. nextID propio (thread-safe) porque
	// varios subagentes pueden correr en paralelo (cap de concurrencia interno).
	taskTool := subagent.NewTaskTool(agentDefs, cfg.Provider, childRegistry, NewIDGen())
	// Seguridad: propaga el ask-before-run al runner hijo con el MISMO gate y la
	// MISMA needsApproval que el chat principal (solo "bash"). Sin esto el subagente
	// "general" correria bash sin la confirmacion que el chat principal exige,
	// evadiendo el gate. El gate es keyed por (sessionID, callID): el sessionID del
	// hijo es su childID, asi la resolucion de permisos del hijo lo resuelve.
	taskTool.SetPermissionGate(cfg.Gate, needsApproval)
	// Surfacing del permiso del subagente en la UI: decora el store del runner hijo
	// con ChildPermissionStore sobre el MISMO bus, asi los eventos de permiso del hijo
	// (Tool.Permission.Requested y su resolucion) se publican en el canal del PADRE
	// (el que ya escucha el frontend), conservando el SessionID del hijo en el payload.
	// El frontend muestra Aprobar/Denegar y resuelve con (childID, callID) via el gate
	// compartido, que ya keyea por ese par. Sin esto el hijo bloquea en gate.Ask pero
	// la UI nunca ve la solicitud.
	taskTool.SetStoreDecorator(func(parentSessionID string, inner session.Store) session.Store {
		return event.NewChildPermissionStore(parentSessionID, inner, cfg.Bus)
	})
	// present_plan se registra para que el runner pueda ejecutarla, pero NO entra
	// en los Permissions normales: solo se anuncia en plan-mode (SetPlanMode).
	registry := tool.NewRegistry(tool.NewOutputStore(outputLimit),
		tool.NewReadToolWithSnapshotProvider(root, cfg.Snaps), tool.NewWriteToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, cfg.Snaps),
		tool.NewGlobTool(root), tool.NewGrepToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewBashTool(root), tool.NewPresentPlanTool(root), tool.NewSkillTool(skills), taskTool,
		tool.NewWebFetchTool(cfg.Provider), tool.TodoWriteTool{})
	r := runner.NewRunner(cfg.Store, cfg.Inbox, cfg.Provider, registry,
		tool.Permissions{"read": true, "write": true, "edit": true, "glob": true, "grep": true, "bash": true, "skill": true, "task": true, "web_fetch": true, "todo_write": true},
		cfg.NextID)
	r.SetSystemPrompt(systemPromptBuilder(root, skillsBlock, cfg.Local))
	r.SetPermissionGate(cfg.Gate, needsApproval)
	// Plan-mode: investigacion de solo lectura mas present_plan (sin write/edit/bash/
	// echo). El hook de modo decide por sesion; SetMode/SetPlanMode toman efecto solo
	// cuando cfg.Mode reporta ModePlan (nil = siempre normal, el default del runner).
	r.SetMode(cfg.Mode)
	r.SetPlanMode(planSystemPromptBuilder(root, skillsBlock, cfg.Local),
		tool.Permissions{"read": true, "glob": true, "grep": true, "present_plan": true, "skill": true})

	return Built{Runner: r, Glob: glob, Commands: commands}
}

// skillDirs devuelve los directorios donde se buscan skills: primero los del
// proyecto (root) y despues los globales (el home del usuario), asi una skill
// del proyecto pisa a una global homonima (skill.Discover es first-wins). Bajo
// cada base mira .atenea/skills (propio de atenea), .agents/skills (el estandar
// tool-agnostic compartido entre agentes) y .claude/skills (Claude Code). Las
// skills globales viven asi en ~/.agents/skills, ~/.claude/skills, etc. Si no se
// puede resolver el home, quedan solo las del proyecto. Rutas identicas (p.ej.
// si el root ES el home) se deduplican para no recorrer el mismo arbol dos veces.
func skillDirs(root string) []string {
	subdirs := []string{
		filepath.Join(".atenea", "skills"),
		filepath.Join(".agents", "skills"),
		filepath.Join(".claude", "skills"),
	}
	bases := []string{root}
	if home, herr := os.UserHomeDir(); herr != nil {
		log.Printf("atenea: no se pudo resolver el home para skills globales: %v", herr)
	} else if home != "" {
		bases = append(bases, home)
	}
	var dirs []string
	seen := map[string]bool{}
	for _, base := range bases {
		for _, sub := range subdirs {
			dir := filepath.Join(base, sub)
			if seen[dir] {
				continue
			}
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// promptSetup ancla en root la preparacion compartida del system prompt:
// detecta si root es un repo git y carga las instrucciones del repo
// (AGENTS.md/CLAUDE.md) una sola vez, y devuelve una fabrica de Env por llamada
// (la fecha se calcula en cada llamada para que no envejezca en una sesion
// larga) mas las instrucciones cargadas. La reusan el builder normal y el de
// plan-mode; solo difieren en que funcion pura de prompt llaman (prompt.Build
// vs prompt.BuildPlan).
func promptSetup(root string) (env func() prompt.Env, instructions string) {
	_, gitErr := os.Stat(filepath.Join(root, ".git"))
	isGit := gitErr == nil
	instructions, err := prompt.LoadInstructions(root, root)
	if err != nil {
		log.Printf("atenea: no se pudieron cargar las instrucciones del repo: %v", err)
	}
	env = func() prompt.Env {
		return prompt.Env{
			WorkingDir:   root,
			WorktreeRoot: root,
			IsGitRepo:    isGit,
			Platform:     goruntime.GOOS,
			Date:         time.Now().Format("2006-01-02"),
		}
	}
	return env, instructions
}

// systemPromptBuilder arma el builder del system prompt de modo normal anclado
// a root: por turno compone el prompt base + el bloque <env> con la fecha del
// dia + el bloque de skills (descubiertas una vez en el ensamblado y pasadas ya
// formateadas), sobre el promptSetup compartido. Con local true (LM Studio,
// Ollama) el base es el prompt local (protocolo de function-calling); si no, se
// elige por familia de modelo.
func systemPromptBuilder(root, skills string, local bool) func(model string) string {
	env, instructions := promptSetup(root)
	return func(model string) string {
		if local {
			return prompt.BuildLocal(env(), instructions, skills)
		}
		return prompt.Build(model, env(), instructions, skills)
	}
}

// planSystemPromptBuilder arma el builder del system prompt de plan-mode: misma
// forma que systemPromptBuilder pero agrega el contrato de plan-mode
// (present_plan) sobre el prompt base, via BuildLocalPlan con local o BuildPlan
// si no.
func planSystemPromptBuilder(root, skills string, local bool) func(model string) string {
	env, instructions := promptSetup(root)
	return func(model string) string {
		if local {
			return prompt.BuildLocalPlan(env(), instructions, skills)
		}
		return prompt.BuildPlan(model, env(), instructions, skills)
	}
}

// NewIDGen devuelve un generador de assistantMessageID real: un contador atomico
// con prefijo, unico por proceso (suficiente con MemoryStore, que se reinicia con
// la app). Un ID estable entre reinicios llega con el store persistente de M10.
func NewIDGen() func() string {
	var n uint64
	return func() string {
		return "msg-" + strconv.FormatUint(atomic.AddUint64(&n, 1), 10)
	}
}
