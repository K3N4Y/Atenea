package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListModels_ParsesModelIDs exige que ListModels pegue a baseURL/models (formato
// OpenAI) y devuelva los ids de la lista `data` en orden. Es el listado que la UI usa
// para el dropdown de modelos de un endpoint local (LM Studio, Ollama).
func TestListModels_ParsesModelIDs(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"qwen2.5-coder","object":"model"},{"id":"llama-3.1","object":"model"}]}`)
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("ListModels devolvio error: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path consultado: got %q, want %q", gotPath, "/v1/models")
	}
	want := []string{"qwen2.5-coder", "llama-3.1"}
	if len(models) != len(want) {
		t.Fatalf("models: got %#v, want %#v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models[%d]: got %q, want %q", i, models[i], want[i])
		}
	}
}

// TestListModels_EmptyData devuelve un slice vacio sin error cuando el servidor no
// tiene modelos cargados (LM Studio sin modelo abierto): la UI muestra "sin modelos",
// no un error.
func TestListModels_EmptyData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("ListModels devolvio error: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("models: got %#v, want vacio", models)
	}
}

// TestListModels_HTTPErrorStatus convierte un status != 200 en error, asi la UI
// distingue "endpoint mal configurado" de "sin modelos".
func TestListModels_HTTPErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no autorizado", http.StatusUnauthorized)
	}))
	defer server.Close()

	if _, err := ListModels(context.Background(), server.URL+"/v1", ""); err == nil {
		t.Fatalf("ListModels: se esperaba error por status 401, got nil")
	}
}

// TestListModels_SendsBearerWhenKeyGiven exige que la apiKey, si no esta vacia, viaje
// como Bearer (endpoints OpenAI-compatible detras de un proxy con auth). Vacia = sin
// header, que es el caso local tipico.
func TestListModels_SendsBearerWhenKeyGiven(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer server.Close()

	if _, err := ListModels(context.Background(), server.URL+"/v1", "secret-key"); err != nil {
		t.Fatalf("ListModels devolvio error: %v", err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("Authorization: got %q, want %q", gotAuth, "Bearer secret-key")
	}
}
