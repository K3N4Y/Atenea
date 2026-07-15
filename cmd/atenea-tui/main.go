// atenea-tui es la interfaz de terminal (estilo Claude Code) del agente atenea.
// Es la frontera delgada equivalente al main.go de Wails: arma el provider desde
// el entorno, ensambla el Engine headless (internal/tui) anclado al cwd y corre
// el programa Bubble Tea. La logica testeable vive en internal/tui.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/checkpoint"
	"atenea/internal/dotenv"
	"atenea/internal/llm"
	"atenea/internal/providerconfig"
	"atenea/internal/session"
	"atenea/internal/tui"
)

const (
	// openRouterBaseURL es el gateway OpenAI-compatible por defecto, el mismo
	// que usa la app Wails.
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	// defaultModel es el modelo por defecto en OpenRouter; override por OPENROUTER_MODEL.
	defaultModel = "openrouter/free"

	// openAIBaseURL es la API oficial de OpenAI, tambien OpenAI-compatible: entra
	// por la misma abstraccion (providerconfig.OpenAICompatible) apuntando el base
	// URL aca, sin adaptador nuevo. A diferencia de OpenRouter NO entiende el campo
	// top-level `reasoning`, asi que su provider se arma con OpenRouterReasoning=false.
	openAIBaseURL = "https://api.openai.com/v1"
	// openAIDefaultModel es el modelo por defecto de OpenAI; override por OPENAI_MODEL.
	openAIDefaultModel = "gpt-5.6-terra"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "atenea-tui:", err)
		os.Exit(1)
	}
}

func run() error {
	// Cargar .env del cwd (si existe) antes de armar el engine: deja
	// OPENROUTER_API_KEY y demas a mano en dev. Las env vars reales tienen prioridad.
	dotenv.Load(".env")

	// El log estandar (fallos de tools, skills no descubiertas) iria a stderr y
	// pintaria sobre la pantalla alternativa de Bubble Tea: se desvia a un archivo.
	redirectLog()

	// El store durable COMPARTIDO con la app Wails (mismo SQLite): las sesiones
	// de la TUI aparecen en su sidebar. Se abre DESPUES de dotenv.Load (ATENEA_DB
	// puede venir del .env) y de redirectLog (el warning va al log desviado, no
	// a la pantalla). Si falla, OpenDefault ya devolvio un store en memoria
	// usable: la TUI sigue funcionando, solo que sin persistir.
	store, err := session.OpenDefault()
	if err != nil {
		log.Printf("atenea-tui: no se pudo abrir el SQLite (%v); las sesiones NO van a persistir (store en memoria)", err)
	}
	closer, _ := store.(io.Closer)

	root, err := os.Getwd()
	if err != nil {
		root = "."
	}

	// El provider y la etiqueta del modelo se resuelven UNA vez: el mismo valor
	// alimenta al engine y al pie del composer (no duplicar la resolucion).
	providerService, warning := openProviderService()
	if warning != nil {
		log.Printf("atenea-tui: provider config: %v", warning)
	}
	active := providerService.Active()

	engine := tui.NewEngine(tui.EngineConfig{
		Root:        root,
		Provider:    providerService.Provider(),
		Store:       store,
		Models:      providerService,
		Checkpoints: checkpoint.NewGitStore(session.DefaultCheckpointPath()),
	})
	history, err := engine.PromptHistory()
	if err != nil {
		log.Printf("atenea-tui: no se pudo cargar el historial del composer: %v", err)
	}

	// Una sesion nueva por corrida de la TUI; el id con timestamp evita chocar
	// entre corridas: el store es durable y cada sesion queda persistida y
	// visible en la sidebar de la app Wails.
	sessionID := "tui-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	// "build" es el modo INICIAL del agente: Tab lo alterna a plan en vivo (el
	// engine fija el modo por sesion via su hook Mode de wiring.Build). El
	// modelo si queda fijo por corrida: no hay forma de cambiarlo desde la TUI.
	// El autocompletado del composer sale del engine: los slash-commands de las
	// skills para el menu "/" y el listado del workspace para el @-menu.
	m := tui.NewModel(engine, sessionID, engine.Events()).
		WithHistory(history).
		WithStatus("build", active.Model).
		WithWorkspaceRoot(gitBranch(root), displayDir(root), root).
		WithCompletions(engine.Commands(), engine.ProjectFiles).
		WithFileReader(tui.WorkspaceFileReader(root))
	// WithMouseCellMotion habilita el mouse tracking: sin el, la terminal nunca
	// reporta la rueda a la app (en pantalla alternativa la traduce a flechas
	// via "alternate scroll"); con la opcion llegan eventos de mouse reales.
	_, runErr := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	shutdownErr := engine.Shutdown(context.Background())
	var closeErr error
	if closer != nil {
		closeErr = closer.Close()
	}
	return errors.Join(runErr, shutdownErr, closeErr)
}

// gitBranch devuelve la rama git actual del repo en root (git rev-parse
// --abbrev-ref HEAD), o "" ante cualquier error o si root no es un repo. La
// top bar la muestra a la izquierda.
func gitBranch(root string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// displayDir abrevia el prefijo del home a "~" para mostrar el directorio de
// trabajo en la top bar; sin home resoluble o sin prefijo comun devuelve root
// tal cual.
func displayDir(root string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return root
	}
	if root == home {
		return "~"
	}
	if strings.HasPrefix(root, home+"/") {
		return "~/" + root[len(home)+1:]
	}
	return root
}

// providerFromEnv elige el provider por entorno, en orden de precedencia:
// OpenRouter si hay OPENROUTER_API_KEY (modelo por OPENROUTER_MODEL), luego OpenAI
// si hay OPENAI_API_KEY (modelo por OPENAI_MODEL), y si no el demo sin red para
// probar la TUI sin configurar nada. Devuelve ademas la etiqueta del modelo para
// el pie del composer: "demo" con el provider fake, o el modelo real elegido.
func providerFromEnv() (llm.Provider, string) {
	snapshot := environmentFallbackSnapshot()
	return snapshot.Provider, snapshot.Model
}

func environmentFallbackSnapshot() llm.ProviderSnapshot {
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		model := os.Getenv("OPENROUTER_MODEL")
		if model == "" {
			model = defaultModel
		}
		return llm.ProviderSnapshot{ProviderID: "openrouter", ProviderName: "OpenRouter", BaseURL: openRouterBaseURL, Model: model, Provider: llm.NewOpenAIProvider(key, openRouterBaseURL, model)}
	}
	// OpenAI no entiende el campo `reasoning` de OpenRouter: se apaga con
	// WithoutOpenRouterReasoning para no mandar una extension que rechazaria.
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		model := os.Getenv("OPENAI_MODEL")
		if model == "" {
			model = openAIDefaultModel
		}
		return llm.ProviderSnapshot{ProviderID: "openai", ProviderName: "OpenAI", BaseURL: openAIBaseURL, Model: model, Provider: llm.NewOpenAIProvider(key, openAIBaseURL, model, llm.WithoutOpenRouterReasoning())}
	}
	log.Print("atenea-tui: sin OPENROUTER_API_KEY ni OPENAI_API_KEY; usando provider de demo (sin red)")
	return llm.ProviderSnapshot{ProviderID: "demo", ProviderName: "Demo", BaseURL: "demo://local", Model: "demo", Provider: demoProvider()}
}

func openProviderService() (*providerconfig.Service, error) {
	return providerconfig.Open(providerconfig.DefaultPath(), providerconfig.DefaultCachePath(), environmentFallbackSnapshot(), os.Getenv, nil, nil, nil, defaultProviderConfig())
}

func defaultProviderConfig() providerconfig.Config {
	return providerconfig.Config{Providers: []providerconfig.Provider{
		{
			ID: "openrouter", Name: "OpenRouter", Type: providerconfig.OpenAICompatible,
			BaseURL: openRouterBaseURL, APIKeyEnv: "OPENROUTER_API_KEY", OpenRouterReasoning: true,
			Models: []string{"tencent/hy3:free", "poolside/laguna-xs-2.1:free", "cohere/north-mini-code:free"},
		},
		{
			ID: "openai", Name: "OpenAI", Type: providerconfig.OpenAICompatible,
			BaseURL: openAIBaseURL, APIKeyEnv: "OPENAI_API_KEY", DisableModelDiscovery: true,
			Models: []string{"gpt-5.6", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-mini"},
		},
	}}
}

// demoProvider arma un FakeProvider con un guion corto (texto + Step.Ended) para
// ver streaming en la TUI sin red, igual que el demo de la app Wails.
func demoProvider() llm.Provider {
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.StepStarted},
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: "Hola desde atenea."},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	)
}

// redirectLog manda el log estandar a un archivo en el dir temporal para no
// corromper el render de la terminal. Si no se puede abrir, se descarta a
// /dev/null antes que pintar sobre la pantalla.
func redirectLog() {
	path := filepath.Join(os.TempDir(), "atenea-tui.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.SetOutput(devNull{})
		return
	}
	log.SetOutput(f)
}

// devNull descarta el log cuando ni el archivo temporal se pudo abrir.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
