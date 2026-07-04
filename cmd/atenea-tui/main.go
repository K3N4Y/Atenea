// atenea-tui es la interfaz de terminal (estilo Claude Code) del agente atenea.
// Es la frontera delgada equivalente al main.go de Wails: arma el provider desde
// el entorno, ensambla el Engine headless (internal/tui) anclado al cwd y corre
// el programa Bubble Tea. La logica testeable vive en internal/tui.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"atenea/internal/dotenv"
	"atenea/internal/llm"
	"atenea/internal/session"
	"atenea/internal/tui"
)

const (
	// openRouterBaseURL es el gateway OpenAI-compatible por defecto, el mismo
	// que usa la app Wails.
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	// defaultModel es el modelo por defecto en OpenRouter; override por OPENROUTER_MODEL.
	defaultModel = "openrouter/free"
)

func main() {
	// Cargar .env del cwd (si existe) antes de armar el engine: deja
	// OPENROUTER_API_KEY y demas a mano en dev. Las env vars reales tienen prioridad.
	dotenv.Load(".env")

	// El log estandar (fallos de tools, skills no descubiertas) iria a stderr y
	// pintaria sobre la pantalla alternativa de Bubble Tea: se desvia a un archivo.
	redirectLog()

	root, err := os.Getwd()
	if err != nil {
		root = "."
	}

	// El provider y la etiqueta del modelo se resuelven UNA vez: el mismo valor
	// alimenta al engine y al pie del composer (no duplicar la resolucion).
	provider, model := providerFromEnv()

	engine := tui.NewEngine(tui.EngineConfig{
		Root:     root,
		Provider: provider,
		Store:    session.NewMemoryStore(),
	})

	// Una sesion nueva por corrida de la TUI; el id con timestamp evita chocar
	// entre corridas si el store algun dia es durable.
	sessionID := "tui-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	// "build" es el modo INICIAL del agente: Tab lo alterna a plan en vivo (el
	// engine fija el modo por sesion via su hook Mode de wiring.Build). El
	// modelo si queda fijo por corrida: no hay forma de cambiarlo desde la TUI.
	m := tui.NewModel(engine, sessionID, engine.Events()).WithStatus("build", model)
	// WithMouseCellMotion habilita el mouse tracking: sin el, la terminal nunca
	// reporta la rueda a la app (en pantalla alternativa la traduce a flechas
	// via "alternate scroll"); con la opcion llegan eventos de mouse reales.
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "atenea-tui:", err)
		os.Exit(1)
	}
}

// providerFromEnv elige el provider igual que la config inicial de la app Wails:
// OpenRouter si hay OPENROUTER_API_KEY (modelo por OPENROUTER_MODEL), y si no el
// demo sin red para probar la TUI sin configurar nada. Devuelve ademas la
// etiqueta del modelo para el pie del composer: "demo" con el provider fake, o
// el modelo real de OpenRouter.
func providerFromEnv() (llm.Provider, string) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		log.Print("atenea-tui: sin OPENROUTER_API_KEY; usando provider de demo (sin red)")
		return demoProvider(), "demo"
	}
	model := os.Getenv("OPENROUTER_MODEL")
	if model == "" {
		model = defaultModel
	}
	return llm.NewOpenAIProvider(key, openRouterBaseURL, model), model
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
