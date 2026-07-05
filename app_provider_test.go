package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atenea/internal/llm"
)

// TestApp_LocalProviderUsesLocalSystemPrompt: al elegir un provider local, el turno
// debe armarse con el system prompt EXCLUSIVO de locales (protocolo de tools por
// function-calling), no con el default code-gen cuyo patron de salida ("skipped:")
// hacia que el modelo narrara la tool call como texto en vez de ejecutarla. Se
// verifica con un provider que graba el Request: su System debe hablar de
// function-calling y NO traer el patron "skipped:".
func TestApp_LocalProviderUsesLocalSystemPrompt(t *testing.T) {
	rec := &recordingEmit{}
	rebuilt := &requestRecordingProvider{FakeProvider: workspaceFake()}
	app := newApp(demoProvider(), rec.emit)
	app.newProvider = func(cfg ProviderConfig) llm.Provider { return rebuilt }

	if err := app.SetProvider("local", "http://localhost:1234/v1", "qwen2.5-coder"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}
	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	sys := rebuilt.captured().System
	if !strings.Contains(sys, "function-calling") {
		t.Fatalf("el system prompt local debe instruir function-calling; got:\n%s", sys)
	}
	if strings.Contains(sys, "skipped:") {
		t.Fatalf("el system prompt local no debe traer el patron 'skipped:' del default; got:\n%s", sys)
	}
}

// TestApp_SetProviderUpdatesModelAndConfig: SetProvider fija un provider local y la
// UI lo ve reflejado en Model() y ProviderConfig(), sin depender de variables de
// entorno. Es el contrato que el selector del frontend usa al elegir LM Studio.
func TestApp_SetProviderUpdatesModelAndConfig(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})

	if err := app.SetProvider("local", "http://localhost:1234/v1", "qwen2.5-coder"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}

	if got := app.Model(); got != "qwen2.5-coder" {
		t.Errorf("Model() = %q, want %q", got, "qwen2.5-coder")
	}
	cfg := app.ProviderConfig()
	if cfg.Kind != "local" || cfg.BaseURL != "http://localhost:1234/v1" || cfg.Model != "qwen2.5-coder" {
		t.Errorf("ProviderConfig() = %+v, want {local, http://localhost:1234/v1, qwen2.5-coder}", cfg)
	}
}

// TestApp_SetProviderRebuildsActiveProvider: tras SetProvider los turnos pasan por el
// provider reconstruido, no por el inyectado al crear la app. Se verifica con un
// factory inyectado (a.newProvider) que devuelve un provider que graba el request: si
// el turno lo atraviesa, captured() trae el system prompt del turno.
func TestApp_SetProviderRebuildsActiveProvider(t *testing.T) {
	rec := &recordingEmit{}
	rebuilt := &requestRecordingProvider{FakeProvider: workspaceFake()}
	var gotCfg ProviderConfig
	app := newApp(demoProvider(), rec.emit)
	app.newProvider = func(cfg ProviderConfig) llm.Provider {
		gotCfg = cfg
		return rebuilt
	}

	if err := app.SetProvider("local", "http://localhost:1234/v1", "qwen"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}
	if gotCfg.Kind != "local" || gotCfg.Model != "qwen" {
		t.Fatalf("el factory recibio %+v, want kind=local model=qwen", gotCfg)
	}

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	if rebuilt.captured().System == "" {
		t.Fatal("el turno no paso por el provider reconstruido (captured vacio)")
	}
}

// newAppRecordingProviderConfig arma una app sin emisor cuyo factory graba la config
// que recibe y devuelve un fake sin red, para asertar con que config SetProvider
// reconstruye el provider sin construir uno real.
func newAppRecordingProviderConfig() (*App, *ProviderConfig) {
	gotCfg := &ProviderConfig{}
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	app.newProvider = func(cfg ProviderConfig) llm.Provider {
		*gotCfg = cfg
		return demoProvider()
	}
	return app, gotCfg
}

// TestApp_SetProviderOpenRouterIgnoresStaleBaseURL: la UI no ofrece configurar el
// baseURL de OpenRouter, asi que cualquier baseURL entrante con kind openrouter es
// estado viejo del form (p.ej. el endpoint local previo). Si el backend lo acepta,
// las peticiones "openrouter" siguen yendo al endpoint local. SetProvider debe usar
// SIEMPRE openRouterBaseURL para openrouter, ignorando lo que arrastre la UI.
func TestApp_SetProviderOpenRouterIgnoresStaleBaseURL(t *testing.T) {
	app, gotCfg := newAppRecordingProviderConfig()

	// Secuencia de la UI: primero local, luego cambia a openrouter pero el form
	// arrastra el baseURL local viejo.
	if err := app.SetProvider("local", "http://localhost:1234/v1", "qwen2.5-coder"); err != nil {
		t.Fatalf("SetProvider(local): %v", err)
	}
	if err := app.SetProvider("openrouter", "http://localhost:1234/v1", "qwen2.5-coder"); err != nil {
		t.Fatalf("SetProvider(openrouter): %v", err)
	}

	if gotCfg.BaseURL != openRouterBaseURL {
		t.Errorf("el factory recibio BaseURL = %q, want %q (openrouter debe ignorar el baseURL viejo de la UI)", gotCfg.BaseURL, openRouterBaseURL)
	}
	if got := app.ProviderConfig().BaseURL; got != openRouterBaseURL {
		t.Errorf("ProviderConfig().BaseURL = %q, want %q", got, openRouterBaseURL)
	}
}

// TestApp_SetProviderOpenRouterSanitizesPersistedConfigOnFirstCall: al arrancar,
// restoreProvider re-aplica tal cual la config persistida en localStorage; si esa
// config quedo corrupta (kind openrouter con el baseURL local de una sesion previa),
// la PRIMERA llamada ya trae el baseURL viejo sin pasar antes por local. El backend
// debe sanearla igual: una implementacion que solo resetee el baseURL "al detectar el
// cambio de kind" (comparando contra la config previa, o arreglando solo el form de
// la UI) dejaria esta config apuntando al endpoint local.
func TestApp_SetProviderOpenRouterSanitizesPersistedConfigOnFirstCall(t *testing.T) {
	app, gotCfg := newAppRecordingProviderConfig()

	// Primera y unica llamada: la config corrupta persistida, sin local previo.
	if err := app.SetProvider("openrouter", "http://localhost:1234/v1", "qwen2.5-coder"); err != nil {
		t.Fatalf("SetProvider(openrouter): %v", err)
	}

	if gotCfg.BaseURL != openRouterBaseURL {
		t.Errorf("el factory recibio BaseURL = %q, want %q (debe sanear la config persistida aunque sea la primera llamada)", gotCfg.BaseURL, openRouterBaseURL)
	}
	if got := app.ProviderConfig().BaseURL; got != openRouterBaseURL {
		t.Errorf("ProviderConfig().BaseURL = %q, want %q", got, openRouterBaseURL)
	}
}

// TestApp_SetProviderOpenRouterKeepsDefaultsAndUserModel: forzar el baseURL de
// openrouter no debe degenerar en "forzar todo a defaults". Sin modelo, la config se
// completa con defaultModel; con modelo explicito, el del usuario se respeta. Una
// implementacion que pise el modelo junto con el baseURL rompe el segundo caso.
func TestApp_SetProviderOpenRouterKeepsDefaultsAndUserModel(t *testing.T) {
	t.Run("sin modelo completa con defaults", func(t *testing.T) {
		app, _ := newAppRecordingProviderConfig()

		if err := app.SetProvider("openrouter", "", ""); err != nil {
			t.Fatalf(`SetProvider(openrouter, "", ""): %v`, err)
		}
		cfg := app.ProviderConfig()
		if cfg.BaseURL != openRouterBaseURL || cfg.Model != defaultModel {
			t.Errorf("ProviderConfig() = %+v, want {openrouter, %s, %s}", cfg, openRouterBaseURL, defaultModel)
		}
	})

	t.Run("con modelo explicito lo respeta", func(t *testing.T) {
		app, _ := newAppRecordingProviderConfig()

		if err := app.SetProvider("openrouter", "", "mi/modelo"); err != nil {
			t.Fatalf(`SetProvider(openrouter, "", "mi/modelo"): %v`, err)
		}
		cfg := app.ProviderConfig()
		if cfg.BaseURL != openRouterBaseURL || cfg.Model != "mi/modelo" {
			t.Errorf("ProviderConfig() = %+v, want {openrouter, %s, mi/modelo}", cfg, openRouterBaseURL)
		}
	})
}

// TestApp_SetProviderRejectsUnknownKind: un kind desconocido falla y no cambia el
// estado vigente (ni provider ni config).
func TestApp_SetProviderRejectsUnknownKind(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	before := app.ProviderConfig()

	if err := app.SetProvider("bogus", "http://x/v1", "m"); err == nil {
		t.Fatal("SetProvider con kind desconocido: se esperaba error")
	}
	if app.ProviderConfig() != before {
		t.Fatalf("ProviderConfig cambio tras error: got %+v, want %+v", app.ProviderConfig(), before)
	}
}

// TestApp_SetProviderRejectsLocalWithoutBaseURL: un provider local exige baseURL; sin
// el no hay endpoint al que apuntar.
func TestApp_SetProviderRejectsLocalWithoutBaseURL(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	if err := app.SetProvider("local", "", "qwen"); err == nil {
		t.Fatal("SetProvider local sin baseURL: se esperaba error")
	}
}

// TestApp_DoesNotAdvertiseEchoTool: echo es una tool de DEBUG; no debe anunciarse al
// modelo en produccion. Un modelo (sobre todo uno local) cae en usarla ante cualquier
// cosa (p. ej. responde "hola" llamando echo con el texto). Las tools reales (read,
// etc.) siguen anunciandose.
func TestApp_DoesNotAdvertiseEchoTool(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: workspaceFake()}
	app := newApp(prov, rec.emit)

	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	req := prov.captured()
	if requestHasTool(req, "echo") {
		t.Fatalf("Request.Tools no debe anunciar echo (tool de debug); tools = %+v", req.Tools)
	}
	if !requestHasTool(req, "read") {
		t.Fatalf("Request.Tools deberia seguir anunciando read; tools = %+v", req.Tools)
	}
}

// TestApp_ListModelsBindingDelegates: el binding ListModels devuelve los ids del
// catalogo del endpoint OpenAI-compatible dado, para poblar el dropdown del selector.
func TestApp_ListModelsBindingDelegates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[{"id":"qwen"},{"id":"llama"}]}`)
	}))
	defer server.Close()
	app := newApp(demoProvider(), func(string, ...interface{}) {})

	models, err := app.ListModels(server.URL + "/v1")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0] != "qwen" || models[1] != "llama" {
		t.Fatalf("ListModels = %#v, want [qwen llama]", models)
	}
}
