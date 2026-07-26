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

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/command"
	"github.com/K3N4Y/atenea/internal/event"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/session/prompt"
	"github.com/K3N4Y/atenea/internal/session/runner"
	"github.com/K3N4Y/atenea/internal/session/subagent"
	"github.com/K3N4Y/atenea/internal/skill"
	"github.com/K3N4Y/atenea/internal/tool"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
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
	Gate permission.Gate
	// Grants is the caller-owned store of session-scoped approvals layered over
	// the classification (see permission.GrantedPolicy). It outlives the build so
	// a rewire does not drop the user's grants; nil = no session grants, every
	// gated call asks.
	Grants *permission.SessionGrants
	// Snaps es el read-state por sesion que comparten read/write/edit. El
	// caller lo crea una sola vez para que sobreviva a los re-ensamblados.
	Snaps *tool.SessionSnapshots
	// Bus publica los eventos de permiso de los subagentes en el canal del
	// padre (ChildPermissionStore), el mismo que ya escucha el frontend.
	Bus *event.Bus
	// LocalPrompt answers, once per turn, whether the provider that will serve it
	// runs local models (LM Studio, Ollama): then the base system prompt switches
	// to the explicit function-calling protocol instead of routing by model
	// family, because a local model id says nothing about its family. It is a
	// question rather than a flag so a live provider switch takes effect on the
	// next turn without re-assembling anything. nil means never local.
	LocalPrompt func() bool
	// NextID genera los assistantMessageID del runner (ver NewIDGen).
	NextID func() string
	// Mode es el hook de modo por sesion (normal/plan) que el runner consulta
	// cada turno; nil = siempre modo normal.
	Mode func(sessionID string) session.Mode
	// MCPTools son las tools descubiertas de servidores MCP ya conectados.
	MCPTools []tool.Tool
}

// Built son las piezas mutables que el caller publica tras el ensamblado: el
// runner listo para correr, el glob del @-menu y los slash-commands del composer.
type Built struct {
	Runner   *runner.Runner
	Glob     *tool.GlobTool
	Commands *command.Set
	// Tools is the catalog the UI asks about a tool it only knows by name: what a
	// finished call may have changed, what granting one would authorize, how one
	// should be presented. It is the same registry the runner settles against, so
	// the answers a user sees are the ones the agent acted on. Rebuilt with every
	// Build, hence published here for the caller to swap alongside the runner.
	Tools tool.Catalog
	// Policy is the ask-before-run classification the assembled agent enforces,
	// derived from Tools and shared by the main runner and the subagents. Published
	// so what the agent gates stays answerable from outside the assembly instead of
	// only from a turn.
	Policy permission.Policy
}

// NewSessionGrants builds the session-grant store: the UI adds to it when the
// user answers "allow this for the rest of the session". The caller owns it and
// passes it in Config.Grants so it survives a rewire (MCP connect, workspace
// change) instead of being dropped mid-session; a UI without that affordance
// passes nil and gates exactly as before.
func NewSessionGrants() *permission.SessionGrants {
	return permission.NewSessionGrants()
}

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
	// A def that names a tool the child registry does not have used to lose it
	// silently: the name never became a permission and the subagent ran with fewer
	// tools than its author wrote down. Now it is reported, once, against the
	// registry that actually bounds a subagent.
	for _, problem := range agent.Validate(agentDefs, childRegistry) {
		log.Printf("atenea: %v", problem)
	}
	// La tool task levanta subagentes hijos. nextID propio (thread-safe) porque
	// varios subagentes pueden correr en paralelo (cap de concurrencia interno).
	taskTool := subagent.NewTaskTool(agentDefs, cfg.Provider, childRegistry, NewIDGen())
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
	registryTools := []tool.Tool{
		tool.NewReadToolWithSnapshotProvider(root, cfg.Snaps), tool.NewWriteToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewEditToolWithSnapshotProvider(root, hashline.OSFilesystem{}, cfg.Snaps),
		tool.NewGlobTool(root), tool.NewGrepToolWithSnapshotProvider(root, cfg.Snaps),
		tool.NewBashTool(root), tool.NewPresentPlanTool(root), tool.NewSkillTool(skills), taskTool,
		tool.NewWebFetchTool(cfg.Provider), tool.TodoWriteTool{},
	}
	registryTools = append(registryTools, cfg.MCPTools...)
	registry := tool.NewRegistry(tool.NewOutputStore(outputLimit), registryTools...)
	// The effective ask-before-run policy, derived from the registry instead of
	// from a list of names kept here: each tool is classified by the effects it
	// declares about itself (permission.EffectsPolicy), with the user's session
	// grants layered on top when the caller keeps a store for them. It is built
	// here, after the registry, because a classification is only as good as the
	// catalog it reads and that catalog changes with every rewire — an MCP server
	// that just connected contributes tools this policy has to be able to see.
	policy := permission.NewGrantedPolicy(permission.NewEffectsPolicy(registry), cfg.Grants, registry)
	// Security: propagate ask-before-run to the child runner with the SAME gate
	// and the SAME policy as the main chat. Without this a "general" subagent
	// would run gated tools (bash, write, edit, web_fetch) without the
	// confirmation the main chat enforces, evading the gate. The policy reads the
	// main registry, whose tool names are a superset of the child's, so parent and
	// child classify the same call identically. The gate is keyed by (sessionID,
	// callID): the child's sessionID is its childID, so the child's permission
	// resolution finds it. Session grants are keyed by session too, so a subagent
	// does not inherit the chat's grants: it asks on its own behalf, and a grant
	// answered on its prompt covers only that child.
	taskTool.SetPermissionGate(cfg.Gate, policy)
	permissions := registry.Permissions()
	// present_plan is executable by the shared registry but is mode-only: normal
	// mode must not advertise it. Every ordinary and dynamic MCP tool derives
	// from the registry above; this is the sole explicit normal-mode exclusion.
	delete(permissions, "present_plan")
	r := runner.NewRunner(cfg.Store, cfg.Inbox, cfg.Provider, registry,
		permissions,
		cfg.NextID)
	r.SetCompactor(runner.NewContextCompactor(cfg.Store, cfg.Provider))
	r.SetSystemPrompt(systemPromptBuilder(root, skillsBlock, cfg.LocalPrompt))
	r.SetPermissionGate(cfg.Gate, policy)
	// Plan-mode: investigacion de solo lectura mas present_plan (sin write/edit/bash/
	// echo). El hook de modo decide por sesion; SetMode/SetPlanMode toman efecto solo
	// cuando cfg.Mode reporta ModePlan (nil = siempre normal, el default del runner).
	r.SetMode(cfg.Mode)
	r.SetPlanMode(planSystemPromptBuilder(root, skillsBlock, cfg.LocalPrompt),
		tool.Permissions{"read": true, "glob": true, "grep": true, "present_plan": true, "skill": true})

	return Built{Runner: r, Glob: glob, Commands: commands, Tools: registry, Policy: policy}
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
// formateadas), sobre el promptSetup compartido. El base sale del prompt local
// (protocolo de function-calling) cuando local lo reporta en ese turno (LM
// Studio, Ollama); si no, se elige por familia de modelo.
func systemPromptBuilder(root, skills string, local func() bool) func(model string) string {
	env, instructions := promptSetup(root)
	return func(model string) string {
		if local != nil && local() {
			return prompt.BuildLocal(env(), instructions, skills)
		}
		return prompt.Build(model, env(), instructions, skills)
	}
}

// planSystemPromptBuilder arma el builder del system prompt de plan-mode: misma
// forma que systemPromptBuilder pero agrega el contrato de plan-mode
// (present_plan) sobre el prompt base, via BuildLocalPlan con local o BuildPlan
// si no.
func planSystemPromptBuilder(root, skills string, local func() bool) func(model string) string {
	env, instructions := promptSetup(root)
	return func(model string) string {
		if local != nil && local() {
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
