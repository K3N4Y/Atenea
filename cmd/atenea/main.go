// atenea es la interfaz de terminal (estilo Claude Code) del agente atenea.
// Es la frontera delgada equivalente al main.go de Wails: arma el provider desde
// el entorno, ensambla el Engine headless (internal/tui/engine) anclado al cwd y
// corre el programa Bubble Tea. La logica testeable vive en internal/tui.
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
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/K3N4Y/atenea/internal/checkpoint"
	"github.com/K3N4Y/atenea/internal/dotenv"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
	"github.com/K3N4Y/atenea/internal/tui"
	"github.com/K3N4Y/atenea/internal/tui/engine"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Fprintln(os.Stdout, versionString())
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "atenea:", err)
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
		log.Printf("atenea: no se pudo abrir el SQLite (%v); las sesiones NO van a persistir (store en memoria)", err)
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
		log.Printf("atenea: provider config: %v", warning)
	}
	active := providerService.Active()

	eng := engine.New(engine.Config{
		Root:        root,
		Provider:    providerService.Provider(),
		Store:       store,
		Models:      providerService,
		Checkpoints: checkpoint.NewGitStore(session.DefaultCheckpointPath()),
	})
	history, err := eng.PromptHistory()
	if err != nil {
		log.Printf("atenea: no se pudo cargar el historial del composer: %v", err)
	}

	// Every launch starts a fresh conversation: no transcript from previous
	// runs on screen. Older sessions of this workspace stay one /resume away.
	sessionID := eng.NewSessionID()

	// El autocompletado del composer sale del engine: los slash-commands de las
	// skills para el menu "/" y el listado del workspace para el @-menu.
	m := tui.NewModel(eng, sessionID, eng.Events()).
		WithHistory(history).
		WithStatus("build", active.Model).
		WithWorkspaceRoot(gitBranch(root), displayDir(root), root).
		WithCompletions(eng.Commands(), eng.ProjectFiles).
		WithFileReader(tui.WorkspaceFileReader(root))
	// Starting on demo means there is no key anywhere (neither environment nor
	// stored credential): say so, and say how to get out of it, instead of
	// letting the user chat with the fake and find out the hard way.
	if active.ProviderID == "demo" {
		m = m.WithNotice("No provider connected — run /connect to connect an LLM provider. Demo mode: replies are canned.")
	}
	// WithMouseCellMotion habilita el mouse tracking: sin el, la terminal nunca
	// reporta la rueda a la app (en pantalla alternativa la traduce a flechas
	// via "alternate scroll"); con la opcion llegan eventos de mouse reales.
	_, runErr := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	shutdownErr := eng.Shutdown(context.Background())
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

// environmentFallbackSnapshot is what the TUI chats with before any provider
// selection exists: the first provider of the built-in catalog whose API-key
// variable is set, and otherwise the offline demo so the TUI can be driven
// without configuring anything.
func environmentFallbackSnapshot() llm.ProviderSnapshot {
	if snapshot, ok := providerconfig.EnvironmentFallback(providerconfig.DefaultCatalog(), os.Getenv, nil); ok {
		return snapshot
	}
	log.Print("atenea: no provider API key in the environment; using the offline demo provider")
	return llm.ProviderSnapshot{ProviderID: "demo", ProviderName: "Demo", BaseURL: "demo://local", Model: "demo", Provider: demoProvider()}
}

func openProviderService() (*providerconfig.Service, error) {
	credentials := providerconfig.NewFileCredentialStore(providerconfig.DefaultCredentialsPath())
	return providerconfig.Open(providerconfig.DefaultPath(), providerconfig.DefaultCachePath(), environmentFallbackSnapshot(), os.Getenv, nil, nil, nil, credentials, providerconfig.DefaultCatalog())
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
	path := filepath.Join(os.TempDir(), "atenea.log")
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
