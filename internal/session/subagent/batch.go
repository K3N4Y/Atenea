package subagent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	batchProtocolVersion = 1
	maxBatchTasks        = 4096
	maxBatchRequestBytes = 16 << 20
)

const (
	BatchSocketEnv = "ATENEA_RAH_SOCKET"
	BatchTokenEnv  = "ATENEA_RAH_TOKEN"
	BatchClientEnv = "ATENEA_RAH_CLIENT"
)

type BatchTask struct {
	Index        int             `json:"index"`
	SubagentType string          `json:"subagent_type"`
	Prompt       string          `json:"prompt"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	TimeoutMS    *int            `json:"timeout_ms,omitempty"`
	Worktree     bool            `json:"worktree,omitempty"`
}

type BatchRequest struct {
	Version int         `json:"version"`
	Token   string      `json:"token,omitempty"`
	Tasks   []BatchTask `json:"tasks"`
}

type BatchResult struct {
	Index  int    `json:"index"`
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type BatchResponse struct {
	Version int           `json:"version"`
	Results []BatchResult `json:"results"`
}

type batchCapability struct {
	ctx         context.Context
	metadataCtx context.Context
	cancel      context.CancelFunc
}

type activeBatch struct {
	cancel context.CancelFunc
}

// BatchServer exposes TaskTool to programs launched by bash without putting the
// provider or its credentials in those processes. Possession of a short-lived
// token is required in addition to access to the private Unix socket.
type BatchServer struct {
	task *TaskTool

	listener net.Listener
	dir      string
	socket   string

	mu           sync.Mutex
	capabilities map[string]batchCapability
	active       map[*activeBatch]struct{}
	connections  map[net.Conn]struct{}
	closed       bool
	wg           sync.WaitGroup
}

func NewBatchServer(task *TaskTool) (*BatchServer, error) {
	dir, err := os.MkdirTemp("", "atenea-rah-")
	if err != nil {
		return nil, fmt.Errorf("create RAH socket directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("secure RAH socket directory: %w", err)
	}
	socket := filepath.Join(dir, "rah.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("listen for RAH batches: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("secure RAH socket: %w", err)
	}
	s := &BatchServer{
		task: task, listener: listener, dir: dir, socket: socket,
		capabilities: make(map[string]batchCapability),
		active:       make(map[*activeBatch]struct{}),
		connections:  make(map[net.Conn]struct{}),
	}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

func (s *BatchServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	capabilities := make([]batchCapability, 0, len(s.capabilities))
	for _, capability := range s.capabilities {
		capabilities = append(capabilities, capability)
	}
	active := make([]*activeBatch, 0, len(s.active))
	for batch := range s.active {
		active = append(active, batch)
	}
	connections := make([]net.Conn, 0, len(s.connections))
	for conn := range s.connections {
		connections = append(connections, conn)
	}
	clear(s.capabilities)
	s.mu.Unlock()
	for _, capability := range capabilities {
		capability.cancel()
	}
	for _, batch := range active {
		batch.cancel()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	err := s.listener.Close()
	s.wg.Wait()
	return errors.Join(err, os.RemoveAll(s.dir))
}
func (s *BatchServer) Environment(ctx context.Context) []string {
	// Script spawning is the code-execution surface of an explicitly activated
	// RAH turn, not an independent escape hatch. Wiring checks activation before
	// calling this method; this defensive depth check also protects descendants.
	if leaseFrom(ctx) != nil && depthFrom(ctx) >= s.task.maxDepth {
		return nil
	}
	token, err := randomToken()
	if err != nil {
		return nil
	}
	client, err := os.Executable()
	if err != nil {
		return nil
	}
	capabilityCtx, cancel := context.WithCancel(ctx)
	capability := batchCapability{ctx: capabilityCtx, metadataCtx: ctx, cancel: cancel}
	s.mu.Lock()
	if s.closed || len(s.capabilities) >= maxBatchTasks {
		s.mu.Unlock()
		cancel()
		return nil
	}
	s.capabilities[token] = capability
	s.mu.Unlock()
	go func() {
		<-capabilityCtx.Done()
		s.mu.Lock()
		delete(s.capabilities, token)
		s.mu.Unlock()
	}()
	return []string{
		BatchSocketEnv + "=" + s.socket,
		BatchTokenEnv + "=" + token,
		BatchClientEnv + "=" + client,
	}
}

func (s *BatchServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.connections[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.connections, conn)
				s.mu.Unlock()
				_ = conn.Close()
			}()
			s.handle(conn)
		}()
	}
}

func batchError(message string) BatchResponse {
	return BatchResponse{Version: batchProtocolVersion, Results: []BatchResult{{Index: -1, Status: "error", Error: message}}}
}

func (s *BatchServer) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(15 * time.Minute))
	limited := io.LimitReader(conn, maxBatchRequestBytes+1)
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	var request BatchRequest
	if err := dec.Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(batchError("invalid batch request: " + err.Error()))
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		_ = json.NewEncoder(conn).Encode(batchError("invalid trailing batch data"))
		return
	}
	capability, ok := s.takeCapability(request.Token)
	if !ok {
		_ = json.NewEncoder(conn).Encode(batchError("invalid or expired RAH capability"))
		return
	}
	batch := &activeBatch{cancel: capability.cancel}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		capability.cancel()
		return
	}
	s.active[batch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		capability.cancel()
		s.mu.Lock()
		delete(s.active, batch)
		s.mu.Unlock()
	}()
	response := s.runBatch(capability.ctx, capability.metadataCtx, request)
	_ = json.NewEncoder(conn).Encode(response)
}

func (s *BatchServer) takeCapability(token string) (batchCapability, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	capability, ok := s.capabilities[token]
	delete(s.capabilities, token)
	return capability, ok
}

func (s *BatchServer) runBatch(ctx, metadataCtx context.Context, request BatchRequest) BatchResponse {
	response := BatchResponse{Version: batchProtocolVersion}
	if request.Version != batchProtocolVersion {
		return batchError(fmt.Sprintf("unsupported RAH protocol version %d", request.Version))
	}
	if len(request.Tasks) == 0 || len(request.Tasks) > maxBatchTasks {
		return batchError(fmt.Sprintf("batch must contain 1-%d tasks", maxBatchTasks))
	}
	seen := make(map[int]struct{}, len(request.Tasks))
	for _, item := range request.Tasks {
		if item.Index < 0 {
			return batchError("task index must be non-negative")
		}
		if _, exists := seen[item.Index]; exists {
			return batchError(fmt.Sprintf("duplicate task index %d", item.Index))
		}
		seen[item.Index] = struct{}{}
	}

	response.Results = make([]BatchResult, len(request.Tasks))
	var wg sync.WaitGroup
	for position, item := range request.Tasks {
		position, item := position, item
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := BatchResult{Index: item.Index}
			def, ok := s.task.catalog[item.SubagentType]
			if !ok {
				result.Status = "error"
				result.Error = fmt.Sprintf("unknown subagent_type %q", item.SubagentType)
				response.Results[position] = result
				return
			}
			in := taskInput{SubagentType: item.SubagentType, Prompt: item.Prompt, OutputSchema: item.OutputSchema, TimeoutMS: item.TimeoutMS, Worktree: item.Worktree}
			output, err := s.task.run(ctx, metadataCtx, def, in, nil)
			switch {
			case err == nil:
				result.Status, result.Output = "ok", output
			case errors.Is(err, context.Canceled):
				result.Status, result.Error = "cancelled", err.Error()
			default:
				result.Status, result.Error = "error", err.Error()
			}
			response.Results[position] = result
		}()
	}
	wg.Wait()
	sort.Slice(response.Results, func(i, j int) bool { return response.Results[i].Index < response.Results[j].Index })
	return response
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
