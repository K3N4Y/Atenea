package tool

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// Limites y defaults de la tool bash. Timeout por tiers (no un numero del
// modelo): rapido por defecto, lento cuando el modelo marca slow_ok. WaitDelay
// corta el Wait si un hijo deja el pipe de stdout abierto. El truncado es por
// runas (no bytes) para no partir UTF-8, igual que boundedString en ripgrep.go.
const (
	defaultBashFastTimeout = 30 * time.Second
	defaultBashSlowTimeout = 15 * time.Minute
	bashWaitDelay          = 15 * time.Second
	maxBashOutputRunes     = 30000
	bashNoOutput           = "no output"
)

// BashTool es la tool de shell: corre un comando con bash -c dentro de Root y
// devuelve su salida combinada (stdout+stderr). Cada call es un proceso fresco
// (bash -c por llamada, sin sesion persistente): el estado (cwd, env, alias) NO
// persiste entre calls. Los timeouts son inyectables para que los tests no
// esperen los defaults; 0 aplica el default.
type BashTool struct {
	Root        string
	FastTimeout time.Duration // 0 => defaultBashFastTimeout
	SlowTimeout time.Duration // 0 => defaultBashSlowTimeout
}

// NewBashTool arma una BashTool sobre Root, por consistencia con NewWriteTool y
// NewGrepTool. Deja los timeouts en 0 para que apliquen los defaults por tier.
func NewBashTool(root string) *BashTool { return &BashTool{Root: root} }

func (*BashTool) Name() string { return "bash" }

//go:embed bash.txt
var bashDescription string

func (*BashTool) Description() string { return bashDescription }

func (*BashTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Comando de shell a ejecutar con bash -c."},"slow_ok":{"type":"boolean","description":"true para comandos potencialmente lentos (builds, instalaciones, tests): usa el timeout extendido."}},"required":["command"]}`)
}

// fastTimeout devuelve el tier rapido, o su default si no se inyecto uno.
func (bt *BashTool) fastTimeout() time.Duration {
	if bt.FastTimeout <= 0 {
		return defaultBashFastTimeout
	}
	return bt.FastTimeout
}

// slowTimeout devuelve el tier lento, o su default si no se inyecto uno.
func (bt *BashTool) slowTimeout() time.Duration {
	if bt.SlowTimeout <= 0 {
		return defaultBashSlowTimeout
	}
	return bt.SlowTimeout
}

// Execute parsea el input JSON y corre el comando con bash -c dentro de Root: un
// proceso fresco por llamada (sin sesion persistente, por eso el cwd y el env no
// sobreviven entre calls). Aplica un timeout por tier (slow_ok elige el lento),
// combina stdout+stderr en un solo buffer, corre con un env no-interactivo y con
// los secretos scrubbeados (ver bashEnv) y mata el GRUPO de procesos al expirar
// el timeout (no solo el hijo, para no dejar huerfanos). Devuelve la salida
// acotada head+tail (ver capBashOutput). Un exit code distinto de cero no es
// error de la tool: se anexa [exit N]. Solo un fallo real de lanzamiento (bash
// ausente, etc.) devuelve error.
func (bt *BashTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Command string `json:"command"`
		SlowOK  bool   `json:"slow_ok"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("bash: input invalido: %w", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return Result{}, fmt.Errorf("bash: command requerido")
	}

	timeout := bt.fastTimeout()
	if in.SlowOK {
		timeout = bt.slowTimeout()
	}
	ctxT, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctxT, "bash", "-c", in.Command)
	cmd.Dir = bt.Root
	cmd.Stdin = nil
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.WaitDelay = bashWaitDelay
	cmd.Env = bashEnv(os.Environ())

	// Mata el GRUPO de procesos (no solo el hijo) al cancelar: sin esto un
	// "sleep 5 &" dejaria huerfanos y el Wait colgaria hasta WaitDelay.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }

	runErr := cmd.Run()
	out := strings.TrimRight(buf.String(), "\n")

	if ctxT.Err() == context.DeadlineExceeded {
		marker := fmt.Sprintf("[bash: comando excedio el timeout de %s]", timeout)
		if out == "" {
			out = marker
		} else {
			out = out + "\n" + marker
		}
		return capBashOutput(out), nil
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			line := fmt.Sprintf("[exit %d]", exitErr.ExitCode())
			if out == "" {
				out = line
			} else {
				out = out + "\n" + line
			}
			return capBashOutput(out), nil
		}
		return Result{}, fmt.Errorf("bash: %w", runErr)
	}

	if out == "" {
		return Result{Output: bashNoOutput}, nil
	}
	return capBashOutput(out), nil
}

// bashEnv prepara el entorno del comando: scrubea variables con secretos (cuyo
// nombre contiene SECRET, TOKEN, PASSWORD o API_KEY) y fuerza overrides
// no-interactivos para que editores y pagers no cuelguen el comando esperando
// input. Marca el entorno como agente con ATENEA=1.
func bashEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+5)
	for _, e := range environ {
		name := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			name = e[:i]
		}
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "TOKEN") ||
			strings.Contains(upper, "PASSWORD") ||
			strings.Contains(upper, "API_KEY") {
			continue
		}
		out = append(out, e)
	}
	out = append(out,
		"ATENEA=1",
		"EDITOR=/bin/false",
		"GIT_EDITOR=false",
		"PAGER=cat",
		"GIT_PAGER=cat",
	)
	return out
}

// capBashOutput acota el output a maxBashOutputRunes conservando HEAD y TAIL (el
// error suele estar al final) con un marcador en medio. Cuenta runas, no bytes,
// para no partir UTF-8.
func capBashOutput(s string) Result {
	if utf8.RuneCountInString(s) <= maxBashOutputRunes {
		return Result{Output: s}
	}
	runes := []rune(s)
	half := maxBashOutputRunes / 2
	head := string(runes[:half])
	tail := string(runes[len(runes)-half:])
	return Result{Output: head + "\n...[salida truncada]...\n" + tail, Truncated: true}
}
