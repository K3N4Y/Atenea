package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/internal/agent"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/tool"
)

type orderedBatchProvider struct{}

func (p *orderedBatchProvider) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	text, _ := request.Messages[len(request.Messages)-1].TextOnly()
	if strings.Contains(text, "slow") {
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.TextStarted},
		llm.Event{Kind: llm.TextDelta, Text: text},
		llm.Event{Kind: llm.TextEnded},
		llm.Event{Kind: llm.StepEnded},
	).Stream(ctx, request)
}

func TestBatchClientRunsParallelHarnessesAndOrdersResultsByIndex(t *testing.T) {
	ids := runtimeIDs()
	task := NewTaskTool(Config{
		Definitions: []agent.Def{{Name: "worker", Prompt: "work"}},
		Provider:    &orderedBatchProvider{},
		Children:    tool.NewRegistry(tool.NewOutputStore(0)),
		NextID:      ids,
	})
	server, err := NewBatchServer(task)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(); task.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, entry := range server.Environment(ctx) {
		name, value, _ := strings.Cut(entry, "=")
		t.Setenv(name, value)
	}
	request := `{"version":1,"tasks":[{"index":9,"subagent_type":"worker","prompt":"slow"},{"index":2,"subagent_type":"worker","prompt":"fast"}]}`
	var stdout, stderr bytes.Buffer
	if code := RunBatchClient(strings.NewReader(request), &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var response BatchResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 || response.Results[0].Index != 2 || response.Results[0].Output != "fast" || response.Results[1].Index != 9 || response.Results[1].Output != "slow" {
		t.Fatalf("response=%+v", response)
	}
}

type barrierBatchProvider struct {
	entered chan struct{}
	release chan struct{}
}

func (p *barrierBatchProvider) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	text, _ := request.Messages[len(request.Messages)-1].TextOnly()
	select {
	case p.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return llm.NewFakeProvider(
		llm.Event{Kind: llm.TextDelta, Text: text},
		llm.Event{Kind: llm.StepEnded},
	).Stream(ctx, request)
}

func TestBatchRunsIndependentHarnessesConcurrently(t *testing.T) {
	provider := &barrierBatchProvider{entered: make(chan struct{}, 2), release: make(chan struct{})}
	task := NewTaskTool(Config{
		Definitions: []agent.Def{{Name: "worker"}},
		Provider:    provider,
		Children:    tool.NewRegistry(tool.NewOutputStore(0)),
		NextID:      runtimeIDs(),
		Limits:      &Limits{MaxDepth: 3, MaxConcurrency: 2},
	})
	server, err := NewBatchServer(task)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(); task.Close() })

	done := make(chan BatchResponse, 1)
	go func() {
		done <- server.runBatch(context.Background(), context.Background(), BatchRequest{
			Version: 1,
			Tasks: []BatchTask{
				{Index: 0, SubagentType: "worker", Prompt: "first"},
				{Index: 1, SubagentType: "worker", Prompt: "second"},
			},
		})
	}()
	for range 2 {
		select {
		case <-provider.entered:
		case <-time.After(time.Second):
			t.Fatal("batch serialized independent harnesses")
		}
	}
	close(provider.release)
	response := <-done
	if len(response.Results) != 2 || response.Results[0].Status != "ok" || response.Results[1].Status != "ok" {
		t.Fatalf("response=%+v", response)
	}
}

func TestRecursiveSchedulerDoesNotDeadlockAtOneSlot(t *testing.T) {
	task := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.TextDelta, Text: "ok"}, llm.Event{Kind: llm.StepEnded}))
	task.setMaxConcurrency(1)
	parentLease, err := task.scheduler.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer parentLease.close()

	done := make(chan error, 1)
	go func() {
		_, runErr := task.Execute(withLease(context.Background(), parentLease), json.RawMessage(`{"subagent_type":"worker","prompt":"nested"}`))
		done <- runErr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("recursive task deadlocked behind its parent's concurrency lease")
	}
}

func TestBatchCapabilityIsSingleUseAndDoesNotExposeProviderCredentials(t *testing.T) {
	task := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.TextDelta, Text: "ok"}, llm.Event{Kind: llm.StepEnded}))
	server, err := NewBatchServer(task)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(); task.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var socket, token, client string
	for _, entry := range server.Environment(ctx) {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case BatchSocketEnv:
			socket = value
		case BatchTokenEnv:
			token = value
		case BatchClientEnv:
			client = value
		}
	}
	if socket == "" || token == "" || client == "" {
		t.Fatal("missing capability environment")
	}
	if strings.Contains(socket, "provider") || len(token) < 32 {
		t.Fatalf("unsafe capability socket=%q token_length=%d", socket, len(token))
	}
	request := BatchRequest{Version: 1, Token: token, Tasks: []BatchTask{{Index: 0, SubagentType: "worker", Prompt: "x"}}}
	first := roundTripBatch(t, socket, request)
	if first.Results[0].Status != "ok" {
		t.Fatalf("first response=%+v", first)
	}
	second := roundTripBatch(t, socket, request)
	if !strings.Contains(second.Results[0].Error, "expired") {
		t.Fatalf("reused capability response=%+v", second)
	}
}

func roundTripBatch(t *testing.T, socket string, request BatchRequest) BatchResponse {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		t.Fatal(err)
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		_ = unix.CloseWrite()
	}
	var response BatchResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRunBatchClientAgainstServer(t *testing.T) {
	task := runtimeTool(llm.NewFakeProvider(
		llm.Event{Kind: llm.TextDelta, Text: "smoke"},
		llm.Event{Kind: llm.StepEnded},
	))
	server, err := NewBatchServer(task)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(); task.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, entry := range server.Environment(ctx) {
		name, value, _ := strings.Cut(entry, "=")
		t.Setenv(name, value)
	}

	var stdout, stderr bytes.Buffer
	code := RunBatchClient(strings.NewReader(`{"version":1,"tasks":[{"index":0,"subagent_type":"worker","prompt":"smoke"}]}`), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"status":"ok"`) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestBatchEnvironmentIsUnavailableAtMaxDepth(t *testing.T) {
	task := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}))
	server, err := NewBatchServer(task)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(); task.Close() })
	lease, err := task.scheduler.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()

	ctx := withLease(withDepth(context.Background(), task.maxDepth), lease)
	if env := server.Environment(ctx); env != nil {
		t.Fatalf("max-depth child received batch capability: %v", env)
	}
}

func TestBatchClientRequiresHarnessCapability(t *testing.T) {
	_ = os.Unsetenv(BatchSocketEnv)
	_ = os.Unsetenv(BatchTokenEnv)
	var stdout, stderr bytes.Buffer
	if code := RunBatchClient(strings.NewReader(`{"version":1,"tasks":[]}`), &stdout, &stderr); code != 5 || !strings.Contains(stderr.String(), "unavailable") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestBatchServerCloseUnblocksIncompleteClient(t *testing.T) {
	task := runtimeTool(llm.NewFakeProvider(llm.Event{Kind: llm.StepEnded}))
	server, err := NewBatchServer(task)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", server.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"version":1`)); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- server.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("BatchServer.Close blocked on an incomplete client")
	}
	task.Close()
}

func TestBatchClientRejectsTrailingJSON(t *testing.T) {
	t.Setenv(BatchSocketEnv, "/unused")
	t.Setenv(BatchTokenEnv, "token")
	var stdout, stderr bytes.Buffer
	code := RunBatchClient(strings.NewReader(`{"version":1,"tasks":[]} {}`), &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "trailing") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
