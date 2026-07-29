package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/providerconfig"
	"github.com/K3N4Y/atenea/internal/session"
)

// testCatalog es el catalogo de fabrica de los tests del selector: dos providers
// de la nube, uno con descubrimiento de modelos y otro sin el.
func testCatalog() providerconfig.Config {
	return providerconfig.Config{Providers: []providerconfig.Provider{
		{
			ID: "openrouter", Name: "OpenRouter", Type: providerconfig.OpenRouter,
			BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY",
			Models: []string{"openrouter/free", "tencent/hy3:free"},
		},
		{
			ID: "anthropic", Name: "Anthropic", Type: providerconfig.Anthropic,
			BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY",
			DisableModelDiscovery: true, Models: []string{"claude-opus-4-8"},
		},
	}}
}

func entry(entries []ProviderEntry, id string) (ProviderEntry, bool) {
	for _, candidate := range entries {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return ProviderEntry{}, false
}

// TestApp_ProviderCatalogProjectsTheSharedCatalog: el selector del panel de
// ajustes pinta una fila por provider configurado con sus modelos, si atenea lo
// trae de fabrica (y por tanto no se puede quitar) y si se le puede guardar una key
// desde aca. Es lo que el frontend lee al montar el panel.
func TestApp_ProviderCatalogProjectsTheSharedCatalog(t *testing.T) {
	app, _ := newAppWithProviders(t, demoProvider(), testCatalog())

	entries := app.ProviderCatalog()
	if len(entries) != 2 {
		t.Fatalf("ProviderCatalog() = %#v, want una fila por provider configurado", entries)
	}
	openrouter, ok := entry(entries, "openrouter")
	if !ok || openrouter.Name != "OpenRouter" || len(openrouter.Models) != 2 {
		t.Fatalf("fila openrouter = %#v (ok=%v), want su nombre y sus modelos curados", openrouter, ok)
	}
	if !openrouter.BuiltIn {
		t.Error("la fila de openrouter no viene marcada BuiltIn: la UI ofreceria quitar un provider que vuelve en el proximo arranque")
	}
	if !openrouter.Connectable || openrouter.Connected {
		t.Errorf("fila openrouter = %#v, want conectable y aun sin conectar", openrouter)
	}
	if anthropic, _ := entry(entries, "anthropic"); !anthropic.BuiltIn {
		t.Errorf("fila anthropic = %#v, want marcada BuiltIn", anthropic)
	}
}

// TestApp_ActiveProviderReportsTheSelection: la UI lee de aca que provider y que
// modelo estan activos, tanto al montar como despues de cada eleccion.
func TestApp_ActiveProviderReportsTheSelection(t *testing.T) {
	app, _ := newAppWithProviders(t, demoProvider(), testCatalog())

	if err := app.SelectModel("anthropic", "claude-opus-4-8"); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	active := app.ActiveProvider()
	if active.ProviderID != "anthropic" || active.ProviderName != "Anthropic" || active.Model != "claude-opus-4-8" {
		t.Fatalf("ActiveProvider() = %#v, want la seleccion de anthropic", active)
	}

	if err := app.SelectModel("openrouter", "tencent/hy3:free"); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	if got := app.ActiveProvider().Model; got != "tencent/hy3:free" {
		t.Fatalf("ActiveProvider().Model = %q, want la nueva seleccion", got)
	}
}

// TestApp_ActiveProviderReportsTheWindowTheAdapterDeclares: la barra de contexto se
// escala con la ventana que declara el adapter que sirve el modelo, no con una tabla
// propia del frontend. Un modelo cuya ventana nadie declara reporta 0, que la UI lee
// como "sin porcentaje" en vez de inventarse una escala.
func TestApp_ActiveProviderReportsTheWindowTheAdapterDeclares(t *testing.T) {
	app, service := newAppWithProviders(t, demoProvider(), testCatalog())
	if got := app.ActiveProvider().ContextWindow; got != 0 {
		t.Fatalf("ContextWindow = %d, want 0: el fake inyectado no declara ninguna ventana", got)
	}

	// El adapter real de Anthropic es lo que construye produccion; el registry de
	// los tests sustituye la construccion, no lo que un adapter sabe de sus modelos.
	service.Provider().Swap(llm.ProviderSnapshot{
		ProviderID: "anthropic", ProviderName: "Anthropic", BaseURL: "https://api.anthropic.com",
		Model:    "claude-opus-4-8",
		Provider: llm.NewAnthropicProvider("key", "https://api.anthropic.com", "claude-opus-4-8"),
	})
	if got := app.ActiveProvider().ContextWindow; got != 200_000 {
		t.Fatalf("ContextWindow = %d, want la ventana de 200k que declara el adapter de Anthropic", got)
	}
}

// TestApp_SelectModelRebuildsTheActiveProvider: tras elegir un modelo los turnos
// corren con ese modelo, no con el que la app tenia al arrancar.
func TestApp_SelectModelRebuildsTheActiveProvider(t *testing.T) {
	rebuilt := &requestRecordingProvider{FakeProvider: workspaceFake()}
	app, _ := newAppWithProviders(t, rebuilt, testCatalog())

	if err := app.SelectModel("openrouter", "openrouter/free"); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	if got := rebuilt.captured().Model; got != "openrouter/free" {
		t.Fatalf("el turno corrio con Model = %q, want el modelo elegido", got)
	}
}

// TestApp_SelectModelRejectsUnknownProviderAndEmptyModel: una eleccion invalida
// falla y deja la seleccion vigente intacta.
func TestApp_SelectModelRejectsUnknownProviderAndEmptyModel(t *testing.T) {
	app, _ := newAppWithProviders(t, demoProvider(), testCatalog())
	if err := app.SelectModel("openrouter", "openrouter/free"); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	before := app.ActiveProvider()

	if err := app.SelectModel("bogus", "m"); err == nil {
		t.Fatal("SelectModel con un provider desconocido: se esperaba error")
	}
	if err := app.SelectModel("openrouter", ""); err == nil {
		t.Fatal("SelectModel sin modelo: se esperaba error")
	}
	if got := app.ActiveProvider(); got != before {
		t.Fatalf("ActiveProvider() = %#v tras los errores, want %#v", got, before)
	}
}

// TestApp_DeclaredEndpointUsesTheLocalSystemPrompt: al chatear con un endpoint
// declarado por el usuario, el turno debe armarse con el system prompt EXCLUSIVO de
// modelos locales (protocolo de tools por function-calling), no con el default
// code-gen cuyo patron de salida ("skipped:") hacia que el modelo narrara la tool
// call como texto en vez de ejecutarla.
func TestApp_DeclaredEndpointUsesTheLocalSystemPrompt(t *testing.T) {
	local := &requestRecordingProvider{FakeProvider: workspaceFake()}
	app, _ := newAppWithProviders(t, local, testCatalog())

	id, err := app.DeclareEndpoint("LM Studio", "http://localhost:1234/v1", "qwen2.5-coder")
	if err != nil {
		t.Fatalf("DeclareEndpoint: %v", err)
	}
	if id != "lm-studio" {
		t.Fatalf("DeclareEndpoint id = %q, want un slug del nombre", id)
	}
	if err := app.SelectModel(id, "qwen2.5-coder"); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	// "# How you act" es el encabezado propio de local.txt; ni el default ni el de
	// Anthropic lo llevan. Buscar "function-calling" no probaria nada: las tres
	// variantes le piden al modelo que emita tool calls de verdad.
	sys := local.captured().System
	if !strings.Contains(sys, "# How you act") {
		t.Fatalf("el turno de un endpoint local no uso el prompt local; got:\n%s", sys)
	}
	if strings.Contains(sys, "skipped:") {
		t.Fatalf("el system prompt local no debe traer el patron 'skipped:' del default; got:\n%s", sys)
	}
}

// TestApp_SelectingACloudModelLeavesTheLocalPromptBehind: la pregunta se responde
// por turno, asi que volver a un provider de la nube vuelve al prompt elegido por
// familia de modelo sin reensamblar nada.
func TestApp_SelectingACloudModelLeavesTheLocalPromptBehind(t *testing.T) {
	provider := &requestRecordingProvider{FakeProvider: workspaceFake()}
	app, _ := newAppWithProviders(t, provider, testCatalog())

	id, err := app.DeclareEndpoint("LM Studio", "http://localhost:1234/v1", "qwen2.5-coder")
	if err != nil {
		t.Fatalf("DeclareEndpoint: %v", err)
	}
	if err := app.SelectModel(id, "qwen2.5-coder"); err != nil {
		t.Fatalf("SelectModel(local): %v", err)
	}
	if err := app.SelectModel("anthropic", "claude-opus-4-8"); err != nil {
		t.Fatalf("SelectModel(anthropic): %v", err)
	}
	if err := app.SendPrompt("s1", "hola"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	if sys := provider.captured().System; strings.Contains(sys, "# How you act") {
		t.Fatalf("el turno de un modelo de la nube quedo con el prompt local; got:\n%s", sys)
	}
}

// TestApp_DeclaredEndpointReachesTheCatalogAndCanBeForgotten: un endpoint del
// usuario es una fila mas del selector, se distingue de las de fabrica porque SI se
// puede quitar, y quitarlo lo saca del catalogo.
func TestApp_DeclaredEndpointReachesTheCatalogAndCanBeForgotten(t *testing.T) {
	app, _ := newAppWithProviders(t, demoProvider(), testCatalog())

	if _, err := app.DeclareEndpoint("Ollama", "http://localhost:11434/v1", ""); err != nil {
		t.Fatalf("DeclareEndpoint: %v", err)
	}
	declared, ok := entry(app.ProviderCatalog(), "ollama")
	if !ok || declared.BuiltIn || declared.Connectable {
		t.Fatalf("fila ollama = %#v (ok=%v), want una fila declarada: se puede quitar y no pide key", declared, ok)
	}
	// Sin modelo el endpoint queda listo para que RefreshModels descubra su
	// catalogo: declararlo y elegir un modelo son dos pasos.
	if len(declared.Models) != 0 {
		t.Errorf("modelos de ollama = %#v, want ninguno hasta que corra el descubrimiento", declared.Models)
	}

	if err := app.ForgetProvider("ollama"); err != nil {
		t.Fatalf("ForgetProvider: %v", err)
	}
	if got, ok := entry(app.ProviderCatalog(), "ollama"); ok {
		t.Fatalf("el catalogo sigue ofreciendo %#v tras quitarlo", got)
	}
}

// TestApp_DeclareEndpointRejectsWhatTheUserCannotUse: el formulario de "agregar
// endpoint" tiene que fallar con un motivo, no dejar una fila inservible.
func TestApp_DeclareEndpointRejectsWhatTheUserCannotUse(t *testing.T) {
	app, _ := newAppWithProviders(t, demoProvider(), testCatalog())

	tests := []struct {
		name     string
		endpoint string
		baseURL  string
		want     string
	}{
		{"un nombre sin letras ni digitos", "···", "http://localhost:1234/v1", "no letters or digits"},
		{"un baseURL sin esquema", "LM Studio", "localhost:1234", "invalid base URL"},
		{"un baseURL vacio", "LM Studio", "  ", "base URL is required"},
		{"el id de un provider de fabrica", "Anthropic", "http://localhost:1234/v1", "ships with atenea"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := app.DeclareEndpoint(test.endpoint, test.baseURL, "")
			if err == nil {
				t.Fatalf("DeclareEndpoint(%q, %q) = nil, want un error que mencione %q", test.endpoint, test.baseURL, test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DeclareEndpoint error = %q, want que mencione %q", err, test.want)
			}
		})
	}
}

// TestApp_ForgetProviderRefusesBuiltInProviders: la UI no ofrece quitar una fila de
// fabrica, y el backend tampoco: volveria en el proximo arranque.
func TestApp_ForgetProviderRefusesBuiltInProviders(t *testing.T) {
	app, _ := newAppWithProviders(t, demoProvider(), testCatalog())

	if err := app.ForgetProvider("anthropic"); err == nil {
		t.Fatal("ForgetProvider(anthropic): se esperaba error")
	}
	if _, ok := entry(app.ProviderCatalog(), "anthropic"); !ok {
		t.Fatal("anthropic desaparecio del catalogo pese al error")
	}
}

// TestApp_RefreshModelsReportsWhatEachEndpointAnswered: refrescar es una
// advertencia, no un fallo: los endpoints que respondieron ya vienen en el catalogo
// devuelto aunque otro se haya caido.
func TestApp_RefreshModelsReportsWhatEachEndpointAnswered(t *testing.T) {
	app, _ := newAppWithProviders(t, demoProvider(), testCatalog())

	entries, err := app.RefreshModels()
	if err == nil {
		t.Fatal("RefreshModels: se esperaba la advertencia del endpoint offline")
	}
	if len(entries) != 2 {
		t.Fatalf("RefreshModels() = %#v, want el catalogo completo pese a la advertencia", entries)
	}
	if openrouter, _ := entry(entries, "openrouter"); !openrouter.BuiltIn {
		t.Errorf("fila openrouter = %#v, want BuiltIn intacto tras un refresh", openrouter)
	}
}

// TestApp_ConnectProviderStoresTheKeyAndActivatesTheProvider: pegar una key en el
// panel es la unica forma de conectar un provider desde el escritorio (no hay
// /connect donde escribirla), y sin nada seleccionado deja ese provider activo en su
// primer modelo. Una key que el provider rechaza no se guarda ni mueve la seleccion.
func TestApp_ConnectProviderStoresTheKeyAndActivatesTheProvider(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key" || r.Header.Get("Authorization") != "Bearer sk-or-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()
	catalog := testCatalog()
	catalog.Providers[0].BaseURL = gateway.URL
	app, _ := newAppWithProviders(t, demoProvider(), catalog)

	if err := app.ConnectProvider("openrouter", "sk-or-test"); err != nil {
		t.Fatalf("ConnectProvider: %v", err)
	}
	active := app.ActiveProvider()
	if active.ProviderID != "openrouter" || active.Model != "openrouter/free" {
		t.Fatalf("ActiveProvider() = %#v, want openrouter en su primer modelo curado", active)
	}
	if connected, _ := entry(app.ProviderCatalog(), "openrouter"); !connected.Connected {
		t.Fatalf("fila openrouter = %#v, want reportada como conectada", connected)
	}

	if err := app.ConnectProvider("openrouter", "sk-wrong"); err == nil {
		t.Fatal("ConnectProvider con una key que el gateway rechaza: se esperaba error")
	}
	if got := app.ActiveProvider(); got != active {
		t.Fatalf("ActiveProvider() = %#v tras una key rechazada, want %#v", got, active)
	}
}

// TestApp_ProviderLoginConnectsAChatGPTSubscriptionAndActivatesIt: connecting a
// subscription from the desktop is not "paste a key somewhere else" — there is no
// key. The bindings hand the UI a code to show, wait for the user to approve it,
// and leave the provider active on its default model.
func TestApp_ProviderLoginConnectsAChatGPTSubscriptionAndActivatesIt(t *testing.T) {
	issuer := deviceIssuerStub(t)
	catalog := testCatalog()
	catalog.Providers = append(catalog.Providers, providerconfig.Provider{
		ID: "openai-codex", Name: "OpenAI (ChatGPT subscription)", Type: providerconfig.OpenAICodex,
		BaseURL: "https://chatgpt.com/backend-api/codex", OAuthIssuer: issuer.URL,
		DisableModelDiscovery: true, Models: []string{"gpt-5.5", "gpt-5.4"},
	})
	app, _ := newAppWithProviders(t, demoProvider(), catalog)

	// The row tells the UI which affordance to draw, so it cannot be inferred from
	// the id: a password field for something with no password is the failure here.
	subscription, ok := entry(app.ProviderCatalog(), "openai-codex")
	if !ok || subscription.ConnectKind != providerconfig.ConnectDeviceCode {
		t.Fatalf("openai-codex row = %#v (ok=%v), want it reported as device_code", subscription, ok)
	}
	if key, _ := entry(app.ProviderCatalog(), "openrouter"); key.ConnectKind != providerconfig.ConnectAPIKey {
		t.Fatalf("openrouter row = %#v, want it reported as api_key", key)
	}

	login, err := app.StartProviderLogin("openai-codex")
	if err != nil {
		t.Fatalf("StartProviderLogin: %v", err)
	}
	if login.UserCode != "V3H5-1MW96" || login.VerificationURI == "" {
		t.Fatalf("StartProviderLogin() = %#v, want the code and the page to type it into", login)
	}
	if login.ExpiresAt == "" {
		t.Error("StartProviderLogin() names no expiry: the panel cannot then say when the code dies, and the sign-in just fails")
	}
	if _, err := time.Parse(time.RFC3339, login.ExpiresAt); err != nil {
		t.Errorf("StartProviderLogin() expiry = %q: the panel parses it to render a clock time (%v)", login.ExpiresAt, err)
	}
	if err := app.AwaitProviderLogin("openai-codex"); err != nil {
		t.Fatalf("AwaitProviderLogin: %v", err)
	}

	active := app.ActiveProvider()
	if active.ProviderID != "openai-codex" || active.Model != "gpt-5.5" {
		t.Fatalf("ActiveProvider() = %#v, want the subscription on its first curated model", active)
	}
	connected, _ := entry(app.ProviderCatalog(), "openai-codex")
	if !connected.Connected {
		t.Fatalf("openai-codex row = %#v, want it reported as connected", connected)
	}
	// Pasting a key into a subscription would store a credential nothing can
	// refresh and no request can route.
	if err := app.ConnectProvider("openai-codex", "sk-pasted"); err == nil {
		t.Fatal("ConnectProvider on a subscription: expected an error")
	}
}

// TestApp_CancelProviderLoginAbandonsThePendingCode: closing the form has to end
// the polling, and cancelling a login nobody started must not fail.
func TestApp_CancelProviderLoginAbandonsThePendingCode(t *testing.T) {
	issuer := deviceIssuerStub(t)
	catalog := testCatalog()
	catalog.Providers = append(catalog.Providers, providerconfig.Provider{
		ID: "openai-codex", Name: "OpenAI (ChatGPT subscription)", Type: providerconfig.OpenAICodex,
		BaseURL: "https://chatgpt.com/backend-api/codex", OAuthIssuer: issuer.URL,
		DisableModelDiscovery: true, Models: []string{"gpt-5.5"},
	})
	app, _ := newAppWithProviders(t, demoProvider(), catalog)

	if _, err := app.StartProviderLogin("openai-codex"); err != nil {
		t.Fatalf("StartProviderLogin: %v", err)
	}
	app.CancelProviderLogin("openai-codex")
	if err := app.AwaitProviderLogin("openai-codex"); !errors.Is(err, providerconfig.ErrNoPendingLogin) {
		t.Fatalf("AwaitProviderLogin after cancelling = %v, want ErrNoPendingLogin", err)
	}
	if connected, _ := entry(app.ProviderCatalog(), "openai-codex"); connected.Connected {
		t.Fatal("a cancelled login stored a credential")
	}
	// Opening the page of a login that no longer exists is refused rather than
	// opening nothing, and cancelling twice is a no-op.
	if err := app.OpenLoginPage("openai-codex"); !errors.Is(err, providerconfig.ErrNoPendingLogin) {
		t.Fatalf("OpenLoginPage with no pending login = %v, want ErrNoPendingLogin", err)
	}
	app.CancelProviderLogin("openai-codex")
}

// TestApp_AwaitProviderLoginReportsALoginCancelledUnderneathItAsNothingToReport:
// the order the settings panel actually uses is await first, cancel second — it
// calls AwaitProviderLogin the moment the code is on screen and the user presses
// Cancel afterwards. Whatever this rejects with is painted red next to the row, so
// rejecting here blames the user for the button they just pressed.
func TestApp_AwaitProviderLoginReportsALoginCancelledUnderneathItAsNothingToReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		app := newAppWithHeldLogin(t)
		if _, err := app.StartProviderLogin("openai-codex"); err != nil {
			t.Fatalf("StartProviderLogin: %v", err)
		}

		awaited := make(chan error, 1)
		go func() { awaited <- app.AwaitProviderLogin("openai-codex") }()
		// Every goroutine of this test is blocked once Wait returns, so the wait is
		// attached to the pending login before anything cancels it. That ordering is
		// the subject here, and a bare `go` statement would leave it to the scheduler.
		synctest.Wait()

		app.CancelProviderLogin("openai-codex")
		if err := <-awaited; err != nil {
			t.Fatalf("AwaitProviderLogin() = %v, want nothing to report: cancelling is what the user asked for", err)
		}
		if connected, _ := entry(app.ProviderCatalog(), "openai-codex"); connected.Connected {
			t.Fatal("a cancelled login stored a credential")
		}
	})
}

// newAppWithHeldLogin is an app whose only provider logs in against no
// authorization server at all: the code is minted at once and the wait ends only
// when the flow's own context is cancelled, which is the shape a poll has while
// the user is still in their browser. No socket, so every goroutine of the test
// blocks on something synctest can see — which is what makes the ordering above
// exact instead of a race.
func newAppWithHeldLogin(t *testing.T) *App {
	t.Helper()
	catalog := providerconfig.Config{Providers: []providerconfig.Provider{{
		ID: "openai-codex", Name: "OpenAI (ChatGPT subscription)", Type: providerconfig.OpenAICodex,
		BaseURL: "https://chatgpt.com/backend-api/codex", DisableModelDiscovery: true,
		Models: []string{"gpt-5.5"},
	}}}
	registry := providerconfig.Registry{providerconfig.OpenAICodex: providerconfig.Format{
		Build: func(providerconfig.BuildParams) (llm.Provider, error) { return demoProvider(), nil },
		OAuth: &providerconfig.OAuthFlow{
			Login: func(context.Context, providerconfig.Provider) (providerconfig.DeviceCode, error) {
				return providerconfig.DeviceCode{
					UserCode: "V3H5-1MW96", VerificationURI: "https://auth.openai.com/codex/device",
					ExpiresAt: time.Now().Add(10 * time.Minute),
					Await: func(ctx context.Context) (providerconfig.OAuthCredential, error) {
						<-ctx.Done()
						return providerconfig.OAuthCredential{}, ctx.Err()
					},
				}, nil
			},
		},
	}}
	dir := t.TempDir()
	offline := func(context.Context, string, string) ([]string, error) {
		return nil, errors.New("model discovery is offline in tests")
	}
	service, err := providerconfig.Open(
		context.Background(), filepath.Join(dir, "providers.json"), "", testSnapshot(demoProvider()),
		envNothing, registry, nil, offline,
		providerconfig.NewFileCredentialStore(filepath.Join(dir, "credentials.json")), catalog,
	)
	if err != nil {
		t.Fatalf("open provider service: %v", err)
	}
	return newAppWithStore(session.NewMemoryStore(), service, func(string, ...interface{}) {})
}

// deviceIssuerStub answers OpenAI's device-code endpoints with a login that is
// already approved, so the whole flow runs without a browser and without a
// ChatGPT account. The access token is an unsigned JWT: nothing verifies these
// signatures, on purpose — we are not their audience.
func deviceIssuerStub(t *testing.T) *httptest.Server {
	t.Helper()
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"chatgpt_account_id":"acct_desktop"}`))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			io.WriteString(w, `{"device_auth_id":"deviceauth_1","user_code":"V3H5-1MW96","interval":"0","expires_at":"`+
				time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339)+`"}`)
		case "/api/accounts/deviceauth/token":
			io.WriteString(w, `{"authorization_code":"ac","code_verifier":"cv"}`)
		case "/oauth/token":
			io.WriteString(w, `{"access_token":"h.`+claims+`.s","refresh_token":"refresh-desktop","expires_in":3600}`)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestApp_ListModelsProbesAnEndpointBeforeItIsDeclared: el formulario de agregar
// endpoint ofrece los modelos que hay en vez de pedirlos de memoria, y eso pasa
// ANTES de que el endpoint exista en la config.
func TestApp_ListModelsProbesAnEndpointBeforeItIsDeclared(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[{"id":"qwen"},{"id":"llama"}]}`)
	}))
	defer server.Close()
	app, _ := newAppWithProviders(t, demoProvider(), testCatalog())

	models, err := app.ListModels(server.URL + "/v1")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0] != "qwen" || models[1] != "llama" {
		t.Fatalf("ListModels = %#v, want [qwen llama]", models)
	}
}

// TestApp_DoesNotAdvertiseEchoTool: echo es una tool de DEBUG; no debe anunciarse al
// modelo en produccion. Un modelo (sobre todo uno local) cae en usarla ante cualquier
// cosa (p. ej. responde "hola" llamando echo con el texto). Las tools reales (read,
// etc.) siguen anunciandose.
func TestApp_DoesNotAdvertiseEchoTool(t *testing.T) {
	rec := &recordingEmit{}
	prov := &requestRecordingProvider{FakeProvider: workspaceFake()}
	app := newApp(t, prov, rec.emit)

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
