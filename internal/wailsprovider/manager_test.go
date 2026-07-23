package wailsprovider

import (
	"context"
	"errors"
	"testing"

	"atenea/internal/llm"
	"atenea/internal/providerconfig"
)

func TestManager_SetPublishesCompleteSnapshot(t *testing.T) {
	initial := &llm.FakeProvider{}
	local := &llm.FakeProvider{}
	manager := New(initial, Config{}, func(cfg Config) llm.Provider {
		if cfg.Kind != KindLocal || cfg.Model != "qwen" {
			t.Fatalf("factory config = %+v", cfg)
		}
		return local
	}, nil, nil, nil)

	got, err := manager.Set(KindLocal, "http://localhost:1234/v1", "qwen")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != local || !got.Local || got.Config.Model != "qwen" {
		t.Fatalf("snapshot = %+v", got)
	}
	if current := manager.Snapshot(); current.Provider != local || current.Config != got.Config || !current.Local {
		t.Fatalf("current snapshot = %+v, want %+v", current, got)
	}
}

func TestManager_SetValidationFailurePreservesSnapshot(t *testing.T) {
	initial := &llm.FakeProvider{}
	manager := New(initial, Config{Kind: KindDemo, Model: DefaultModel}, func(Config) llm.Provider {
		t.Fatal("factory must not run for invalid config")
		return nil
	}, nil, nil, nil)
	want := manager.Snapshot()

	if _, err := manager.Set(KindLocal, "", "qwen"); err == nil {
		t.Fatal("Set local without base URL: expected error")
	}
	got := manager.Snapshot()
	if got.Provider != want.Provider || got.Config != want.Config || got.Local != want.Local {
		t.Fatalf("snapshot changed after error: got %+v, want %+v", got, want)
	}
}

func TestManager_ListModelsResolvesOpenRouterCredential(t *testing.T) {
	store := providerconfig.NewFileCredentialStore(t.TempDir() + "/credentials.json")
	if err := store.Put(KindOpenRouter, providerconfig.Credential{Type: providerconfig.CredentialTypeAPIKey, APIKey: "stored"}); err != nil {
		t.Fatal(err)
	}
	var gotURL, gotKey string
	manager := New(&llm.FakeProvider{}, Config{}, func(Config) llm.Provider { return nil }, func(string) string { return "" }, store,
		func(_ context.Context, baseURL, apiKey string) ([]string, error) {
			gotURL, gotKey = baseURL, apiKey
			return []string{"model"}, nil
		})

	models, err := manager.ListModels(context.Background(), OpenRouterBaseURL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "model" || gotURL != OpenRouterBaseURL+"/" || gotKey != "stored" {
		t.Fatalf("models=%v url=%q key=%q", models, gotURL, gotKey)
	}

	wantErr := errors.New("offline")
	manager.listModels = func(context.Context, string, string) ([]string, error) { return nil, wantErr }
	if _, err := manager.ListModels(context.Background(), "http://localhost:1234/v1"); !errors.Is(err, wantErr) {
		t.Fatalf("ListModels error = %v, want %v", err, wantErr)
	}
}
