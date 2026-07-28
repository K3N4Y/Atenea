package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateOpenRouterKey_AcceptsAuthorizedKey(t *testing.T) {
	gotAuth := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/key" {
			t.Errorf("request = %s %s, want GET /key", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":{"label":"test"}}`))
	}))
	defer server.Close()

	if err := ValidateOpenRouterKey(context.Background(), server.URL, "sk-or-valid"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-or-valid" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestValidateOpenRouterKey_RejectsUnauthorizedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Invalid credentials"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	err := ValidateOpenRouterKey(context.Background(), server.URL, "sk-or-bad")
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !strings.Contains(err.Error(), "invalid API key") {
		t.Fatalf("error = %q, want it to say the key is invalid", err)
	}
}

func TestValidateOpenRouterKey_ReportsUnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer server.Close()

	err := ValidateOpenRouterKey(context.Background(), server.URL, "sk-or-any")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v, want the unexpected status surfaced", err)
	}
}

func TestValidateOpenAIKey_AcceptsAuthorizedKey(t *testing.T) {
	gotAuth := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			t.Errorf("request = %s %s, want GET /models", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	if err := ValidateOpenAIKey(context.Background(), server.URL, "sk-valid"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-valid" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestValidateOpenAIKey_RejectsUnauthorizedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"Incorrect API key provided"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	err := ValidateOpenAIKey(context.Background(), server.URL, "sk-bad")
	if err == nil || !strings.Contains(err.Error(), "invalid API key") {
		t.Fatalf("error = %v, want invalid API key", err)
	}
}

func TestValidateOpenAIKey_ReportsUnexpectedStatusWithoutLeakingKey(t *testing.T) {
	const key = "sk-secret-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited for "+key, http.StatusTooManyRequests)
	}))
	defer server.Close()

	err := ValidateOpenAIKey(context.Background(), server.URL, key)
	if err == nil || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), key) {
		t.Fatalf("error = %v, want status without the API key", err)
	}
}
