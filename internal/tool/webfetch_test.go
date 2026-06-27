package tool

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atenea/internal/llm"
)

// recordingProvider es un llm.Provider de test que captura el ultimo Request y
// reproduce un guion fijo de eventos. Sirve para verificar que el destilado de
// web_fetch recibe el contenido de la pagina y el prompt, sin red ni modelo real.
type recordingProvider struct {
	got    llm.Request
	calls  int
	script []llm.Event
}

func (r *recordingProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	r.got = req
	r.calls++
	out := make(chan llm.Event)
	go func() {
		defer close(out)
		for _, ev := range r.script {
			select {
			case <-ctx.Done():
				return
			case out <- ev:
			}
		}
	}()
	return out, nil
}

// webFetchInput arma el input JSON crudo {url, prompt} como lo emite el modelo.
func webFetchInput(t *testing.T, url, prompt string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"url": url, "prompt": prompt})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return b
}

// allowAllIPs es el guard SSRF permisivo de los tests: deja pasar el loopback del
// httptest.Server (que el guard real vedaria por ser 127.0.0.1).
func allowAllIPs(net.IP) bool { return false }

func TestWebFetch_FetchesConvertsAndDistills(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><body><h1>Atenea</h1><p>El framework es Wails.</p></body></html>`)
	}))
	defer srv.Close()

	prov := &recordingProvider{script: []llm.Event{
		{Kind: llm.TextDelta, Text: "El framework es Wails."},
	}}
	wf := NewWebFetchTool(prov)
	wf.client = srv.Client()
	wf.blockIP = allowAllIPs

	res, err := wf.Execute(context.Background(), webFetchInput(t, srv.URL, "Que framework usa?"))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.Output != "El framework es Wails." {
		t.Errorf("Output = %q, quiero la respuesta destilada", res.Output)
	}
	if prov.calls != 1 {
		t.Errorf("provider llamado %d veces, quiero 1", prov.calls)
	}
	// El destilado debe recibir el contenido de la pagina (convertido a markdown) y
	// el prompt; el system prompt debe estar presente.
	if prov.got.System == "" {
		t.Errorf("falta el system prompt de extraccion")
	}
	if len(prov.got.Messages) == 0 {
		t.Fatalf("el destilado no recibio mensajes")
	}
	user := prov.got.Messages[len(prov.got.Messages)-1].Text
	if !strings.Contains(user, "Wails") {
		t.Errorf("el destilado no recibio el contenido de la pagina: %q", user)
	}
	if !strings.Contains(user, "Que framework usa?") {
		t.Errorf("el destilado no recibio el prompt: %q", user)
	}
}

// Contenido no-HTML (text/plain) no se convierte: pasa crudo al destilado.
func TestWebFetch_NonHTMLPassesThrough(t *testing.T) {
	const body = "version 2.5.2 publicada"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, body)
	}))
	defer srv.Close()

	prov := &recordingProvider{script: []llm.Event{{Kind: llm.TextDelta, Text: "2.5.2"}}}
	wf := NewWebFetchTool(prov)
	wf.client = srv.Client()
	wf.blockIP = allowAllIPs

	if _, err := wf.Execute(context.Background(), webFetchInput(t, srv.URL, "que version?")); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	user := prov.got.Messages[len(prov.got.Messages)-1].Text
	if !strings.Contains(user, body) {
		t.Errorf("el destilado no recibio el texto plano crudo: %q", user)
	}
}

// El guard SSRF veda hosts privados/locales ANTES de cualquier GET: error y el
// provider no se llama.
func TestWebFetch_BlocksPrivateHostsBeforeFetching(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1/", "http://169.254.169.254/latest/meta-data/", "http://10.0.0.1/", "http://[::1]/"} {
		prov := &recordingProvider{}
		wf := NewWebFetchTool(prov) // guard SSRF real (default)
		_, err := wf.Execute(context.Background(), webFetchInput(t, target, "leeme secretos"))
		if err == nil {
			t.Errorf("%s: esperaba error de SSRF, no lo hubo", target)
		}
		if !strings.Contains(err.Error(), "bloqueado") {
			t.Errorf("%s: error = %v, quiero que mencione bloqueado", target, err)
		}
		if prov.calls != 0 {
			t.Errorf("%s: el provider se llamo %d veces, no debia llamarse", target, prov.calls)
		}
	}
}

// Una respuesta que excede el tope de tamano es error (no se destila).
func TestWebFetch_RejectsTooLargeResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><body>"+strings.Repeat("x", 1000)+"</body></html>")
	}))
	defer srv.Close()

	prov := &recordingProvider{}
	wf := NewWebFetchTool(prov)
	wf.client = srv.Client()
	wf.blockIP = allowAllIPs
	wf.maxSize = 50 // mucho mas chico que el cuerpo

	_, err := wf.Execute(context.Background(), webFetchInput(t, srv.URL, "que dice?"))
	if err == nil || !strings.Contains(err.Error(), "demasiado grande") {
		t.Errorf("error = %v, quiero 'demasiado grande'", err)
	}
	if prov.calls != 0 {
		t.Errorf("el provider se llamo %d veces, no debia destilar", prov.calls)
	}
}

// Un status fuera de 2xx es error de la tool.
func TestWebFetch_Non2xxIsError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	wf := NewWebFetchTool(&recordingProvider{})
	wf.client = srv.Client()
	wf.blockIP = allowAllIPs

	_, err := wf.Execute(context.Background(), webFetchInput(t, srv.URL, "x"))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %v, quiero que mencione 404", err)
	}
}

// Input invalido: url o prompt vacios, o JSON roto.
func TestWebFetch_InvalidInput(t *testing.T) {
	wf := NewWebFetchTool(&recordingProvider{})
	cases := []json.RawMessage{
		webFetchInput(t, "", "hola"),
		webFetchInput(t, "https://example.com", ""),
		json.RawMessage(`{`),
		webFetchInput(t, "ftp://example.com/x", "hola"),
	}
	for i, in := range cases {
		if _, err := wf.Execute(context.Background(), in); err == nil {
			t.Errorf("caso %d: esperaba error, no lo hubo", i)
		}
	}
}

// http se actualiza a https; un esquema no-web se rechaza.
func TestNormalizeWebFetchURL(t *testing.T) {
	u, err := normalizeWebFetchURL("http://example.com/docs")
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if u.Scheme != "https" {
		t.Errorf("scheme = %q, quiero https (upgrade)", u.Scheme)
	}
	if _, err := normalizeWebFetchURL("file:///etc/passwd"); err == nil {
		t.Errorf("esperaba rechazo de esquema no-web")
	}
}
