package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestAnthropicProvider_DeclaresItsNativeModelWindows(t *testing.T) {
	capabilities := NewAnthropicProvider("key", "", "claude-opus-4-8").Capabilities()
	for _, model := range []string{"claude-opus-4-8", "claude-fable-5", "claude-sonnet-5", "claude-haiku-4-5"} {
		window, ok := capabilities.ContextWindow(model)
		if !ok || window != 200_000 {
			t.Errorf("ContextWindow(%q) = (%d, %v), want (200000, true)", model, window, ok)
		}
		if !NeedsPreventiveCompaction(160_000, window) {
			t.Errorf("%q must compact at 80%% of its context window", model)
		}
	}
	if _, ok := capabilities.ContextWindow("totally/unknown"); ok {
		t.Error("an unknown model must read as unknown, not as a window")
	}
}

// TestCapabilities_DialectsDoNotAnswerForEachOthersModelIDs is why the windows
// moved out of one global table: the same model reached through two gateways is
// two ids, and a table keyed by id alone had to hold both without being able to
// say which adapter either belonged to.
func TestCapabilities_DialectsDoNotAnswerForEachOthersModelIDs(t *testing.T) {
	openAI := DescribeOpenAI(WithOpenAICompatibility())
	openRouter := DescribeOpenAI(WithOpenRouterCompatibility())
	anthropic := DescribeAnthropic()

	if window, ok := openAI.ContextWindow("gpt-4o"); !ok || window != 128_000 {
		t.Errorf("openai gpt-4o = (%d, %v), want (128000, true)", window, ok)
	}
	if _, ok := openAI.ContextWindow("openai/gpt-4o"); ok {
		t.Error("the OpenAI dialect must not answer for OpenRouter's spelling of its models")
	}
	if window, ok := openRouter.ContextWindow("openai/gpt-4o"); !ok || window != 128_000 {
		t.Errorf("openrouter openai/gpt-4o = (%d, %v), want (128000, true)", window, ok)
	}
	if _, ok := openRouter.ContextWindow("claude-opus-4-8"); ok {
		t.Error("the OpenRouter dialect must not answer for Anthropic's native ids")
	}
	if _, ok := anthropic.ContextWindow("anthropic/claude-opus-4.8"); ok {
		t.Error("the Anthropic adapter must not answer for OpenRouter's prefixed ids")
	}
}

func TestCapabilities_NeutralDialectDeclaresNoCatalogAndNoVendorCaching(t *testing.T) {
	capabilities := DescribeOpenAI(WithoutOpenRouterReasoning())
	if len(capabilities.ContextWindows) != 0 {
		t.Errorf("ContextWindows = %v, want none for an endpoint that could be anything", capabilities.ContextWindows)
	}
	if capabilities.PromptCaching != NoPromptCaching {
		t.Errorf("PromptCaching = %v, want none", capabilities.PromptCaching)
	}
	if !capabilities.Streaming || !capabilities.Tools {
		t.Error("the neutral dialect still streams and still carries tools")
	}
}

func TestCapabilities_DeclareVisionOnlyForChangedAdapters(t *testing.T) {
	if !DescribeAnthropic().Vision {
		t.Error("Anthropic must declare its native image serialization")
	}
	if !DescribeOpenAI(WithOpenAICompatibility()).Vision || !DescribeOpenAI(WithOpenRouterCompatibility()).Vision {
		t.Error("OpenAI chat-completions dialects must declare image serialization")
	}
	if DescribeCodex().Vision {
		t.Error("Codex vision support was not implemented")
	}
}

func TestCapabilities_PromptCachingSaysWhoKeysIt(t *testing.T) {
	for _, test := range []struct {
		name string
		got  PromptCaching
		want PromptCaching
	}{
		{"openai forwards SessionKey as prompt_cache_key", DescribeOpenAI(WithOpenAICompatibility()).PromptCaching, KeyedPromptCaching},
		{"openrouter forwards SessionKey as session_id", DescribeOpenAI(WithOpenRouterCompatibility()).PromptCaching, KeyedPromptCaching},
		{"anthropic caches unconditionally, SessionKey buys nothing", DescribeAnthropic().PromptCaching, ImplicitPromptCaching},
	} {
		if test.got != test.want {
			t.Errorf("%s: PromptCaching = %v, want %v", test.name, test.got, test.want)
		}
	}
}

// TestCapabilities_ReasoningIsPerInstanceNotPerDialect: OpenRouter's reasoning
// extension is a provider-level opt-out, so two providers of the same wire
// format can honestly answer differently.
func TestCapabilities_ReasoningIsPerInstanceNotPerDialect(t *testing.T) {
	if !DescribeOpenAI(WithOpenRouterCompatibility()).Reasoning {
		t.Error("OpenRouter asks for reasoning by default")
	}
	if DescribeOpenAI(WithOpenRouterCompatibility(), WithoutOpenRouterReasoning()).Reasoning {
		t.Error("opting out of the reasoning extension must be visible in the declaration")
	}
	if DescribeOpenAI(WithOpenAICompatibility()).Reasoning {
		t.Error("official OpenAI chat completions is never asked for reasoning")
	}
	if DescribeAnthropic().Reasoning {
		t.Error("the Anthropic adapter never sends the thinking parameter")
	}
}

// TestDescribeOpenAI_MatchesTheProviderItDescribes is the property that makes it
// safe for a registry to answer for a provider it never built.
func TestDescribeOpenAI_MatchesTheProviderItDescribes(t *testing.T) {
	for name, opts := range map[string][]Option{
		"openai":            {WithOpenAICompatibility()},
		"openrouter":        {WithOpenRouterCompatibility()},
		"openai-compatible": {WithoutOpenRouterReasoning()},
	} {
		built := NewOpenAIProvider("key", "https://example.test/v1", "model", opts...).Capabilities()
		if described := DescribeOpenAI(opts...); !reflect.DeepEqual(described, built) {
			t.Errorf("%s: described = %#v, built = %#v", name, described, built)
		}
	}
}

// TestAnthropicProvider_DeclaredDefaultMaxOutputIsWhatItSends keeps the number a
// host reserves in its context estimate tied to the one that actually goes on the
// wire when a request leaves MaxOutputTokens at zero.
func TestAnthropicProvider_DeclaredDefaultMaxOutputIsWhatItSends(t *testing.T) {
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	p := NewAnthropicProvider("key", server.URL, "claude-test")
	out, err := p.Stream(context.Background(), Request{Model: "claude-test", Messages: []Message{TextMessage("user", "hi")}})
	if err != nil {
		t.Fatal(err)
	}
	drain(out)

	var sent struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(requestBody, &sent); err != nil {
		t.Fatal(err)
	}
	if declared := p.Capabilities().DefaultMaxOutputTokens; sent.MaxTokens != declared {
		t.Fatalf("sent max_tokens = %d, declared default = %d", sent.MaxTokens, declared)
	}
}

// TestActiveCapabilities_ResolvesThroughTheSwitchableHandle: the runner holds the
// switchable provider, not the adapter, so the unwrap has to happen somewhere.
func TestActiveCapabilities_ResolvesThroughTheSwitchableHandle(t *testing.T) {
	delegate := NewAnthropicProvider("key", "", "claude-opus-4-8")
	switcher, err := NewSwitchableProvider(ProviderSnapshot{
		ProviderID: "anthropic", ProviderName: "Anthropic", BaseURL: "https://api.anthropic.com",
		Model: "claude-opus-4-8", Provider: delegate,
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, ok := ActiveCapabilities(switcher)
	if !ok {
		t.Fatal("the active delegate declares capabilities; the handle must not swallow them")
	}
	if window, _ := capabilities.ContextWindow("claude-opus-4-8"); window != 200_000 {
		t.Fatalf("window through the handle = %d, want 200000", window)
	}

	switcher.Swap(ProviderSnapshot{
		ProviderID: "local", ProviderName: "Local", BaseURL: "http://local",
		Model: "qwen", Provider: NewOpenAIProvider("key", "http://local/v1", "qwen", WithoutOpenRouterReasoning()),
	})
	capabilities, ok = ActiveCapabilities(switcher)
	if !ok {
		t.Fatal("after the swap the new delegate answers")
	}
	if _, known := capabilities.ContextWindow("claude-opus-4-8"); known {
		t.Error("the handle must answer for the delegate of the moment, not the previous one")
	}
}

// TestActiveCapabilities_SilenceIsNotADenial: a provider that declares nothing
// must not be reported as having declared the zero value, or a host reads
// "unknown" as "does not stream, serves no model".
func TestActiveCapabilities_SilenceIsNotADenial(t *testing.T) {
	if _, ok := ActiveCapabilities(NewFakeProvider()); ok {
		t.Fatal("a provider that does not implement Describing declared nothing")
	}
	if _, ok := ActiveCapabilities(nil); ok {
		t.Fatal("no provider declares nothing")
	}
}
