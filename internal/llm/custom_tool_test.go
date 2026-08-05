package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func customDefinition() ToolDef {
	return ToolDef{Name: "edit", WireName: "apply_patch", Description: "Apply a patch.", Schema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`), CustomFormat: &ToolCustomFormat{Syntax: "lark", Definition: "start: /.+/"}}
}

const customToolTurn = `event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":""}}

event: response.custom_tool_call_input.delta
data: {"type":"response.custom_tool_call_input.delta","sequence_number":2,"item_id":"ct_1","output_index":0,"delta":"*** Begin Patch\\n"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"*** Begin Patch\\n*** End Patch\\n","status":"completed"}}

event: response.completed
data: {"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}

`

func TestCodexToolsUseNativeCustomGrammarAndWireName(t *testing.T) {
	tools := toCodexTools([]ToolDef{customDefinition()})
	if len(tools) != 1 || tools[0].OfCustom == nil {
		t.Fatalf("tools = %#v", tools)
	}
	encoded, err := json.Marshal(tools[0])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	format := got["format"].(map[string]any)
	if got["type"] != "custom" || got["name"] != "apply_patch" || format["syntax"] != "lark" || format["definition"] != "start: /.+/" {
		t.Fatalf("tool = %s", encoded)
	}
}

func TestUnsupportedAdaptersKeepCustomToolAsJSONFallback(t *testing.T) {
	definition := customDefinition()
	openAI := toOpenAITools([]ToolDef{definition})
	if len(openAI) != 1 || openAI[0].OfFunction == nil || openAI[0].OfFunction.Function.Name != "edit" {
		t.Fatalf("openai = %#v", openAI)
	}
	anthropic, err := toAnthropicTools([]ToolDef{definition})
	if err != nil || len(anthropic) != 1 || anthropic[0].OfTool == nil || anthropic[0].OfTool.Name != "edit" {
		t.Fatalf("anthropic = %#v, %v", anthropic, err)
	}
}

func TestUnsupportedAdapterJSONFallbackExecutesCustomDefinition(t *testing.T) {
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"edit","arguments":"{\"input\":\"fallback\"}"}}]},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	stream, err := NewOpenAIProvider("key", server.URL, "unknown-model").Stream(context.Background(), Request{Tools: []ToolDef{customDefinition()}})
	if err != nil {
		t.Fatal(err)
	}
	var call Event
	for event := range stream {
		if event.Kind == ToolCall {
			call = event
		}
	}
	var body struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(requestBody, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Function.Name != "edit" || call.ToolName != "edit" || string(call.Input) != `{"input":"fallback"}` {
		t.Fatalf("tools=%#v call=%+v", body.Tools, call)
	}
}

func TestCodexStreamNormalizesCustomToolInput(t *testing.T) {
	server := newCodexServer(t, customToolTurn)
	provider := newOAuthResponsesProvider(&staticTokens{token: OAuthToken{AccessToken: "token", AccountID: "account"}}, server.server.URL, "gpt-5", time.Second, codexResponsesProfile)
	events := drainCodex(t, provider, Request{Tools: []ToolDef{customDefinition()}})
	for _, event := range events {
		if event.Kind != ToolCall {
			continue
		}
		if event.ToolName != "apply_patch" || string(event.Input) != `{"input":"*** Begin Patch\\n*** End Patch\\n"}` {
			t.Fatalf("tool call = %+v", event)
		}
		return
	}
	t.Fatalf("events = %+v, want custom ToolCall", events)
}
