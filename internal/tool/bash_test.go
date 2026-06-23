package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestBashTool_RunsCommandAndReturnsCombinedOutput afirma el happy path de bash:
// corre el comando con bash dentro de Root, combina stdout+stderr, recorta los
// saltos de linea finales y devuelve el texto como Output con error nil.
func TestBashTool_RunsCommandAndReturnsCombinedOutput(t *testing.T) {
	bt := &BashTool{Root: t.TempDir()}

	res, err := bt.Execute(context.Background(), json.RawMessage(`{"command":"echo hola"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if res.Output != "hola" {
		t.Fatalf("Execute: output\n  se esperaba %q\n  se obtuvo  %q", "hola", res.Output)
	}
}

// TestBashTool_EmptyCommandIsError afirma que un command vacio o solo espacios
// es un error de uso: no se lanza ningun proceso y el Result queda vacio.
func TestBashTool_EmptyCommandIsError(t *testing.T) {
	bt := &BashTool{Root: t.TempDir()}

	res, err := bt.Execute(context.Background(), json.RawMessage(`{"command":"   "}`))
	if err == nil {
		t.Fatalf("Execute: se esperaba error por command vacio, se obtuvo nil")
	}
	if !strings.Contains(err.Error(), "command requerido") {
		t.Fatalf("Execute: error\n  debe contener %q\n  se obtuvo      %q", "command requerido", err.Error())
	}
	if res.Output != "" {
		t.Fatalf("Execute: output debe quedar vacio, se obtuvo %q", res.Output)
	}
}

// TestBashTool_CombinesStdoutAndStderr afirma que stdout y stderr se combinan en
// un solo buffer; el orden no es determinista, asi que se asegura que ambos esten.
func TestBashTool_CombinesStdoutAndStderr(t *testing.T) {
	bt := &BashTool{Root: t.TempDir()}

	res, err := bt.Execute(context.Background(), json.RawMessage(`{"command":"echo out; echo err 1>&2"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !strings.Contains(res.Output, "out") {
		t.Fatalf("Execute: output debe contener %q, se obtuvo %q", "out", res.Output)
	}
	if !strings.Contains(res.Output, "err") {
		t.Fatalf("Execute: output debe contener %q, se obtuvo %q", "err", res.Output)
	}
}

// TestBashTool_NonZeroExitAppendsExitCode afirma que un exit code distinto de
// cero no es error de la tool: se anexa un marcador [exit N] al output.
func TestBashTool_NonZeroExitAppendsExitCode(t *testing.T) {
	bt := &BashTool{Root: t.TempDir()}

	res, err := bt.Execute(context.Background(), json.RawMessage(`{"command":"exit 3"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !strings.Contains(res.Output, "[exit 3]") {
		t.Fatalf("Execute: output debe contener %q, se obtuvo %q", "[exit 3]", res.Output)
	}
}

// TestBashTool_TimeoutKillsAndReturnsPartial afirma que al expirar el timeout se
// mata el grupo de procesos (no se espera al sleep de 5s) y se devuelve la salida
// parcial mas un marcador de timeout. El retorno debe ser muy por debajo de 5s.
func TestBashTool_TimeoutKillsAndReturnsPartial(t *testing.T) {
	bt := &BashTool{Root: t.TempDir(), FastTimeout: 150 * time.Millisecond}

	start := time.Now()
	res, err := bt.Execute(context.Background(), json.RawMessage(`{"command":"echo first; sleep 5"}`))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("Execute: tardo %v; debio matar el grupo y volver en menos de 3s", elapsed)
	}
	if !strings.Contains(res.Output, "first") {
		t.Fatalf("Execute: output debe contener salida parcial %q, se obtuvo %q", "first", res.Output)
	}
	if !strings.Contains(res.Output, "timeout") {
		t.Fatalf("Execute: output debe contener marcador de %q, se obtuvo %q", "timeout", res.Output)
	}
}

// TestBashTool_TruncatesLargeOutput afirma que un output mayor al limite se acota
// head+tail con un marcador y Truncated queda en true.
func TestBashTool_TruncatesLargeOutput(t *testing.T) {
	bt := &BashTool{Root: t.TempDir()}

	res, err := bt.Execute(context.Background(), json.RawMessage(`{"command":"yes a | head -n 40000"}`))
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("Execute: Truncated debe ser true para output grande")
	}
	if !strings.Contains(res.Output, "salida truncada") {
		t.Fatalf("Execute: output debe contener %q, se obtuvo (%d runas)", "salida truncada", utf8.RuneCountInString(res.Output))
	}
	// 40000 lineas de "a\n" son 80000 runas crudas; el acotado debe ser menor.
	if utf8.RuneCountInString(res.Output) >= 80000 {
		t.Fatalf("Execute: output acotado debe ser mas corto que la entrada cruda, tiene %d runas", utf8.RuneCountInString(res.Output))
	}
}

// TestBashTool_NonInteractiveEnvAndScrubsSecrets afirma que el env se fuerza a
// no-interactivo (EDITOR, GIT_PAGER) y que las variables con secretos (API_KEY)
// se eliminan del entorno del comando, sin romper PATH.
func TestBashTool_NonInteractiveEnvAndScrubsSecrets(t *testing.T) {
	t.Setenv("FAKE_API_KEY", "leak-me")
	bt := &BashTool{Root: t.TempDir()}

	const command = `printf 'EDITOR=%s GIT_PAGER=%s SECRET=%s PATH=%s' "$EDITOR" "$GIT_PAGER" "${FAKE_API_KEY:-SCRUBBED}" "${PATH:+present}"`
	input, err := json.Marshal(struct {
		Command string `json:"command"`
	}{Command: command})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	res, err := bt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: error inesperado: %v", err)
	}
	for _, want := range []string{"EDITOR=/bin/false", "GIT_PAGER=cat", "SECRET=SCRUBBED", "PATH=present"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("Execute: output debe contener %q, se obtuvo %q", want, res.Output)
		}
	}
}
