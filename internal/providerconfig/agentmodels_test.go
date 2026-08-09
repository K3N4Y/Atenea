package providerconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/llm"
)

type recordingProvider struct{ requests chan llm.Request }

func (p *recordingProvider) Stream(_ context.Context, request llm.Request) (<-chan llm.Event, error) {
	p.requests <- request
	events := make(chan llm.Event)
	close(events)
	return events, nil
}

func openAgentModelService(t *testing.T, dir string, factory Factory) *Service {
	t.Helper()
	path := filepath.Join(dir, "providers.json")
	body := `{"providers":[{"id":"active","name":"Active","type":"openai-compatible","base_url":"http://active","models":["manifest","implicit"]},{"id":"other","name":"Other","type":"openai-compatible","base_url":"http://other","models":["explicit"]}],"selected":{"provider":"active","model":"implicit"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if factory == nil {
		factory = func(BuildParams) (llm.Provider, error) { return inertProvider{}, nil }
	}
	service, err := Open(context.Background(), path, "", fallbackSnapshot(), nil, everyType(factory), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestServiceOpenLoadsStrictAgentModelConfigBesideProviders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent-models.json"), []byte(`{"agents":{"review":{"model":"implicit"}},"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "providers.json")
	if err := os.WriteFile(path, []byte(`{"providers":[{"id":"p","name":"Provider","type":"openai-compatible","base_url":"http://p","models":["m"]}],"selected":{"provider":"p","model":"m"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := Open(context.Background(), path, "", fallbackSnapshot(), nil, inertRegistry(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Open() error = %v, want strict agent config error", err)
	}
	if service == nil {
		t.Fatal("Open() returned nil service with agent model error")
	}
	if service.Provider() == nil || len(service.AgentModels()) != 0 {
		t.Fatalf("service on error is unusable: provider=%#v models=%#v", service.Provider(), service.AgentModels())
	}
	if active := service.Active(); active.ProviderID != "p" || active.Model != "m" {
		t.Fatalf("service did not activate providers.json after agent model error: %#v", active)
	}
	if catalog := service.Catalog(); len(catalog) != 1 || catalog[0].ID != "p" {
		t.Fatalf("service did not load provider catalog after agent model error: %#v", catalog)
	}
	if err := service.SetAgentModel(context.Background(), "review", AgentModelSelection{Provider: "p", Model: "m"}); err == nil || !strings.Contains(err.Error(), "config is invalid") {
		t.Fatalf("SetAgentModel() error = %v, want invalid config preserved", err)
	}
}

func TestServiceAgentModelPersistenceClearAndDetachedListing(t *testing.T) {
	dir := t.TempDir()
	service := openAgentModelService(t, dir, nil)
	selection := AgentModelSelection{Model: "implicit", ReasoningEffort: llm.ReasoningEffortHigh}
	if err := service.SetAgentModel(context.Background(), "review", selection); err != nil {
		t.Fatal(err)
	}
	listed := service.AgentModels()
	listed["review"] = AgentModelSelection{Model: "changed"}
	if got, _ := service.AgentModel("review"); got != selection {
		t.Fatalf("stored selection mutated through listing: %#v", got)
	}

	reopened := openAgentModelService(t, dir, nil)
	if got, ok := reopened.AgentModel("review"); !ok || got != selection {
		t.Fatalf("reopened selection = %#v, %v", got, ok)
	}
	if err := reopened.ClearAgentModel("review"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.AgentModel("review"); ok {
		t.Fatal("override remains after clear")
	}
}

func TestServiceResolveAgentModelPrecedenceProviderAndReasoning(t *testing.T) {
	dir := t.TempDir()
	recorder := &recordingProvider{requests: make(chan llm.Request, 1)}
	var built []BuildParams
	service := openAgentModelService(t, dir, func(params BuildParams) (llm.Provider, error) {
		built = append(built, params)
		return recorder, nil
	})
	if err := service.SetAgentModel(context.Background(), "review", AgentModelSelection{Provider: "other", Model: "explicit", ReasoningEffort: llm.ReasoningEffortHigh}); err != nil {
		t.Fatal(err)
	}
	provider, err := service.ResolveAgentModel(context.Background(), "review", "manifest")
	if err != nil {
		t.Fatal(err)
	}
	request := llm.Request{Reasoning: &llm.ReasoningPreference{Effort: llm.ReasoningEffortLow}}
	if _, err := provider.Stream(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	gotRequest := <-recorder.requests
	if gotRequest.Model != "explicit" || gotRequest.Reasoning == nil || gotRequest.Reasoning.Effort != llm.ReasoningEffortHigh {
		t.Fatalf("fixed request = %#v", gotRequest)
	}
	if got := service.Active(); got.ProviderID != "active" || got.Model != "implicit" {
		t.Fatalf("explicit override mutated global selection: %#v", got)
	}
	if len(built) == 0 || built[len(built)-1].Provider.ID != "other" {
		t.Fatalf("built providers = %#v", built)
	}

	manifestProvider, err := service.ResolveAgentModel(context.Background(), "unconfigured", "manifest")
	if err != nil {
		t.Fatal(err)
	}
	if manifestProvider == nil || manifestProvider.(*fixedProvider).snapshot.ProviderID != "active" {
		t.Fatalf("manifest provider = %#v", manifestProvider)
	}
	inherited, err := service.ResolveAgentModel(context.Background(), "unconfigured", "")
	if err != nil || inherited != nil {
		t.Fatalf("inheritance = %#v, %v", inherited, err)
	}
}

func TestServiceSetAgentModelRejectsInvalidAndDoesNotPublishSaveFailure(t *testing.T) {
	dir := t.TempDir()
	service := openAgentModelService(t, dir, nil)
	for name, selection := range map[string]AgentModelSelection{
		"missing model":    {},
		"unknown provider": {Provider: "missing", Model: "explicit"},
		"unknown model":    {Provider: "other", Model: "missing"},
		"invalid effort":   {Model: "implicit", ReasoningEffort: "turbo"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := service.SetAgentModel(context.Background(), "review", selection); err == nil {
				t.Fatal("SetAgentModel() succeeded")
			}
		})
	}

	if err := os.Mkdir(filepath.Join(dir, "agent-models.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := service.SetAgentModel(context.Background(), "review", AgentModelSelection{Model: "implicit"}); err == nil {
		t.Fatal("SetAgentModel() save succeeded with destination directory")
	}
	if _, ok := service.AgentModel("review"); ok {
		t.Fatal("failed save mutated in-memory overrides")
	}
}

func TestServiceSetAgentModelValidatesCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	body := `{"providers":[{"id":"p","name":"P","type":"openai-compatible","base_url":"http://p","api_key_env":"P_KEY","models":["m"]}],"selected":{"provider":"p","model":"m"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := Open(context.Background(), path, "", fallbackSnapshot(), func(string) string { return "startup-key" }, inertRegistry(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.getenv = func(string) string { return "" }
	if err := service.SetAgentModel(context.Background(), "review", AgentModelSelection{Model: "m"}); err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("SetAgentModel() error = %v", err)
	}
	if _, ok := service.AgentModel("review"); ok {
		t.Fatal("credential failure published override")
	}
}

func TestServiceAgentModelUsesEffectiveFallbackProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	body := `{"providers":[{"id":"fallback","name":"Fallback","type":"openai-compatible","base_url":"http://fallback","models":["m"]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback := llm.ProviderSnapshot{ProviderID: "fallback", ProviderName: "Fallback", BaseURL: "http://fallback", Model: "m", Provider: inertProvider{}}
	service, err := Open(context.Background(), path, "", fallback, nil, inertRegistry(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetAgentModel(context.Background(), "review", AgentModelSelection{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	effective, ok := service.EffectiveAgentModel("review", "")
	if !ok || effective.Provider != "fallback" || effective.Model != "m" {
		t.Fatalf("effective = %#v, %v", effective, ok)
	}
	resolved, err := service.ResolveAgentModel(context.Background(), "review", "")
	if err != nil || resolved == nil {
		t.Fatalf("ResolveAgentModel() = %#v, %v", resolved, err)
	}
}

func TestServiceAgentModelRejectsUnconfiguredEffectiveFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	if err := os.WriteFile(path, []byte(`{"providers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := Open(context.Background(), path, "", fallbackSnapshot(), nil, inertRegistry(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = service.SetAgentModel(context.Background(), "review", AgentModelSelection{Model: "demo"})
	if err == nil || !strings.Contains(err.Error(), `provider "demo" is not configured`) {
		t.Fatalf("SetAgentModel() error = %v", err)
	}
}
