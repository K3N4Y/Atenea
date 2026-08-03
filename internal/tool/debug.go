package tool

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const debugOutputLimit = 64 << 10

type DebugTool struct {
	root    string
	mu      sync.Mutex
	session *debugSession
}

type debugInput struct {
	Operation          string   `json:"operation"`
	AdapterCommand     string   `json:"adapter_command"`
	AdapterArgs        []string `json:"adapter_args"`
	Program            string   `json:"program"`
	Args               []string `json:"args"`
	CWD                string   `json:"cwd"`
	PID                int      `json:"pid"`
	Host               string   `json:"host"`
	Port               int      `json:"port"`
	File               string   `json:"file"`
	Line               int      `json:"line"`
	Function           string   `json:"function"`
	Condition          string   `json:"condition"`
	ThreadID           int      `json:"thread_id"`
	FrameID            int      `json:"frame_id"`
	VariablesReference int      `json:"variables_reference"`
	Expression         string   `json:"expression"`
	Context            string   `json:"context"`
	TimeoutSeconds     float64  `json:"timeout_seconds"`
}

type debugBreakpoint struct {
	Line      int    `json:"line"`
	Condition string `json:"condition,omitempty"`
}
type debugSession struct {
	cmd             *exec.Cmd
	in              io.WriteCloser
	mu              sync.Mutex
	seq             int
	pending         map[int]chan debugResponse
	done            chan struct{}
	readErr         error
	initialized     chan struct{}
	initializedOnce sync.Once
	doneOnce        sync.Once
	output          []byte
	events          []string
	stoppedThread   int
	stoppedFrame    int
	breakpoints     map[string][]debugBreakpoint
	functions       map[string]string
	waitOnce        sync.Once
	waitErr         error
}
type debugMessage struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	Command    string          `json:"command"`
	RequestSeq int             `json:"request_seq"`
	Success    bool            `json:"success"`
	Message    string          `json:"message"`
	Event      string          `json:"event"`
	Body       json.RawMessage `json:"body"`
}
type debugResponse struct {
	body json.RawMessage
	err  error
}

func NewDebugTool(root string) *DebugTool {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = filepath.Clean(root)
	}
	return &DebugTool{root: abs}
}
func (*DebugTool) Name() string { return "debug" }

//go:embed debug.txt
var debugDescription string

func (*DebugTool) Description() string { return debugDescription }
func (*DebugTool) Effects() Effects    { return RunsCommands }
func (*DebugTool) CallEffects(call Call) Effects {
	var in debugInput
	if json.Unmarshal(call.Input, &in) == nil {
		switch in.Operation {
		case "threads", "stack_trace", "scopes", "variables", "output", "sessions":
			return NoEffects
		}
	}
	return RunsCommands
}
func (*DebugTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string","enum":["launch","attach","set_breakpoint","remove_breakpoint","continue","next","step_in","step_out","pause","threads","stack_trace","scopes","variables","evaluate","output","sessions","terminate"]},"adapter_command":{"type":"string"},"adapter_args":{"type":"array","items":{"type":"string"}},"program":{"type":"string"},"args":{"type":"array","items":{"type":"string"}},"cwd":{"type":"string"},"pid":{"type":"integer","minimum":1},"host":{"type":"string"},"port":{"type":"integer","minimum":1,"maximum":65535},"file":{"type":"string"},"line":{"type":"integer","minimum":1},"function":{"type":"string"},"condition":{"type":"string"},"thread_id":{"type":"integer","minimum":1},"frame_id":{"type":"integer","minimum":1},"variables_reference":{"type":"integer","minimum":0},"expression":{"type":"string"},"context":{"type":"string"},"timeout_seconds":{"type":"number","exclusiveMinimum":0,"maximum":300}},"required":["operation"],"additionalProperties":false}`)
}

func (t *DebugTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var in debugInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{}, fmt.Errorf("debug: invalid input: %w", err)
	}
	if in.TimeoutSeconds < 0 {
		return Result{}, errors.New("debug: timeout_seconds must be positive")
	}
	timeout := 30 * time.Second
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds * float64(time.Second))
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()
	t.mu.Lock()
	defer t.mu.Unlock()
	switch in.Operation {
	case "launch", "attach":
		return t.start(ctx, in)
	case "sessions":
		if t.session == nil {
			return Result{Output: "No active debug session."}, nil
		}
		return Result{Output: t.session.status()}, nil
	case "output":
		if t.session == nil {
			return Result{}, errors.New("debug: no active session")
		}
		return Result{Output: t.session.outputText()}, nil
	case "terminate":
		return t.terminate(ctx)
	}
	s := t.session
	if s == nil {
		return Result{}, errors.New("debug: no active session; use launch or attach first")
	}
	return t.executeSession(ctx, s, in)
}

func (t *DebugTool) start(ctx context.Context, in debugInput) (Result, error) {
	if t.session != nil {
		return Result{}, errors.New("debug: a session is already active; terminate it first")
	}
	if in.AdapterCommand == "" {
		return Result{}, errors.New("debug: adapter_command is required")
	}
	cwd := t.root
	var err error
	if in.CWD != "" {
		cwd, err = t.path(in.CWD)
		if err != nil {
			return Result{}, err
		}
	}
	params := map[string]any{}
	if in.Operation == "launch" {
		if in.Program == "" {
			return Result{}, errors.New("debug: launch requires program")
		}
		program, e := t.path(in.Program)
		if e != nil {
			return Result{}, e
		}
		params = map[string]any{"program": program, "args": in.Args, "cwd": cwd}
	} else {
		if in.PID == 0 && in.Host == "" && in.Port == 0 {
			return Result{}, errors.New("debug: attach requires pid or host/port")
		}
		if (in.Host == "") != (in.Port == 0) {
			return Result{}, errors.New("debug: attach host and port must be supplied together")
		}
		if in.PID > 0 {
			params["processId"] = in.PID
		}
		if in.Host != "" {
			params["host"], params["port"] = in.Host, in.Port
		}
		params["cwd"] = cwd
	}
	binary, err := exec.LookPath(in.AdapterCommand)
	if err != nil {
		return Result{}, fmt.Errorf("debug: adapter %q not found: %w", in.AdapterCommand, err)
	}
	cmd := exec.Command(binary, in.AdapterArgs...)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("debug: adapter stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("debug: adapter stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("debug: start adapter: %w", err)
	}
	s := &debugSession{cmd: cmd, in: stdin, pending: map[int]chan debugResponse{}, done: make(chan struct{}), initialized: make(chan struct{}), breakpoints: map[string][]debugBreakpoint{}, functions: map[string]string{}}
	t.session = s
	go s.readLoop(stdout)
	var init json.RawMessage
	if err = s.request(ctx, "initialize", map[string]any{"clientID": "atenea", "adapterID": "atenea", "pathFormat": "path", "linesStartAt1": true, "columnsStartAt1": true}, &init); err == nil {
		err = s.request(ctx, in.Operation, params, nil)
	}
	if err == nil {
		select {
		case <-s.initialized:
		case <-ctx.Done():
			err = ctx.Err()
		case <-s.done:
			err = s.failure()
		}
	}
	if err == nil {
		err = s.syncBreakpoints(ctx)
	}
	if err == nil {
		err = s.request(ctx, "configurationDone", map[string]any{}, nil)
	}
	if err != nil {
		s.close()
		t.session = nil
		return Result{}, fmt.Errorf("debug: %s handshake: %w%s", in.Operation, err, stderrSuffix(stderr.String()))
	}
	return Result{Output: fmt.Sprintf("Debug session started (%s, adapter pid %d).", in.Operation, cmd.Process.Pid)}, nil
}

func (t *DebugTool) path(p string) (string, error) {
	abs, err := sandboxJoin(t.root, p, "debug")
	if err != nil {
		return "", err
	}
	if err := rejectRealPathOutside(t.root, abs, p, "debug"); err != nil {
		return "", err
	}
	return abs, nil
}

func (t *DebugTool) executeSession(ctx context.Context, s *debugSession, in debugInput) (Result, error) {
	var body json.RawMessage
	switch in.Operation {
	case "set_breakpoint", "remove_breakpoint":
		return t.breakpoint(ctx, s, in)
	case "continue", "next", "step_in", "step_out", "pause":
		thread := in.ThreadID
		if thread == 0 {
			thread = s.cachedThread()
		}
		if thread == 0 {
			return Result{}, fmt.Errorf("debug: %s requires thread_id (no stopped thread is cached)", in.Operation)
		}
		command := map[string]string{"continue": "continue", "next": "next", "step_in": "stepIn", "step_out": "stepOut", "pause": "pause"}[in.Operation]
		if err := s.request(ctx, command, map[string]any{"threadId": thread}, &body); err != nil {
			return Result{}, err
		}
		return Result{Output: fmt.Sprintf("%s requested for thread %d.", in.Operation, thread)}, nil
	case "threads":
		if err := s.request(ctx, "threads", map[string]any{}, &body); err != nil {
			return Result{}, err
		}
	case "stack_trace":
		thread := in.ThreadID
		if thread == 0 {
			thread = s.cachedThread()
		}
		if thread == 0 {
			return Result{}, errors.New("debug: stack_trace requires thread_id")
		}
		if err := s.request(ctx, "stackTrace", map[string]any{"threadId": thread}, &body); err != nil {
			return Result{}, err
		}
		s.cacheFirstFrame(body)
	case "scopes":
		frame := in.FrameID
		if frame == 0 {
			frame = s.cachedFrame()
		}
		if frame == 0 {
			return Result{}, errors.New("debug: scopes requires frame_id")
		}
		if err := s.request(ctx, "scopes", map[string]any{"frameId": frame}, &body); err != nil {
			return Result{}, err
		}
	case "variables":
		if in.VariablesReference <= 0 {
			return Result{}, errors.New("debug: variables requires a positive variables_reference")
		}
		if err := s.request(ctx, "variables", map[string]any{"variablesReference": in.VariablesReference}, &body); err != nil {
			return Result{}, err
		}
	case "evaluate":
		if in.Expression == "" {
			return Result{}, errors.New("debug: evaluate requires expression")
		}
		p := map[string]any{"expression": in.Expression}
		if in.Context != "" {
			p["context"] = in.Context
		}
		frame := in.FrameID
		if frame == 0 {
			frame = s.cachedFrame()
		}
		if frame > 0 {
			p["frameId"] = frame
		}
		if err := s.request(ctx, "evaluate", p, &body); err != nil {
			return Result{}, err
		}
	default:
		return Result{}, fmt.Errorf("debug: unsupported operation %q", in.Operation)
	}
	return Result{Output: formatDebugBody(in.Operation, body)}, nil
}

func (t *DebugTool) breakpoint(ctx context.Context, s *debugSession, in debugInput) (Result, error) {
	if in.File == "" && in.Function == "" {
		return Result{}, errors.New("debug: breakpoint requires file or function")
	}
	if in.File != "" && in.Function != "" {
		return Result{}, errors.New("debug: breakpoint accepts file or function, not both")
	}
	if in.File != "" {
		if in.Line < 1 {
			return Result{}, errors.New("debug: file breakpoint requires a 1-based line")
		}
		file, err := t.path(in.File)
		if err != nil {
			return Result{}, err
		}
		list := s.breakpoints[file]
		if in.Operation == "set_breakpoint" {
			found := false
			for i := range list {
				if list[i].Line == in.Line {
					list[i].Condition = in.Condition
					found = true
				}
			}
			if !found {
				list = append(list, debugBreakpoint{Line: in.Line, Condition: in.Condition})
			}
		} else {
			n := list[:0]
			for _, b := range list {
				if b.Line != in.Line {
					n = append(n, b)
				}
			}
			list = n
		}
		s.breakpoints[file] = list
		var body json.RawMessage
		if err := s.request(ctx, "setBreakpoints", map[string]any{"source": map[string]any{"path": file}, "breakpoints": list}, &body); err != nil {
			return Result{}, err
		}
	} else {
		if in.Operation == "set_breakpoint" {
			s.functions[in.Function] = in.Condition
		} else {
			delete(s.functions, in.Function)
		}
		if err := s.syncFunctions(ctx); err != nil {
			return Result{}, err
		}
	}
	return Result{Output: fmt.Sprintf("Breakpoint %s: %s.", map[bool]string{true: "set", false: "removed"}[in.Operation == "set_breakpoint"], breakpointLabel(in))}, nil
}
func breakpointLabel(in debugInput) string {
	if in.File != "" {
		return fmt.Sprintf("%s:%d", in.File, in.Line)
	}
	return in.Function
}

func (t *DebugTool) terminate(ctx context.Context) (Result, error) {
	if t.session == nil {
		return Result{Output: "No active debug session."}, nil
	}
	s := t.session
	t.session = nil
	err := s.request(ctx, "disconnect", map[string]any{"terminateDebuggee": true}, nil)
	s.close()
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("debug: terminate: %w", err)
	}
	return Result{Output: "Debug session terminated and adapter reaped."}, nil
}
func (t *DebugTool) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.session != nil {
		t.session.close()
		t.session = nil
	}
	return nil
}

func (s *debugSession) request(ctx context.Context, command string, args any, out any) error {
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return s.failure()
	default:
	}
	s.seq++
	seq := s.seq
	ch := make(chan debugResponse, 1)
	s.pending[seq] = ch
	err := writeDebugMessage(s.in, map[string]any{"seq": seq, "type": "request", "command": command, "arguments": args})
	if err != nil {
		delete(s.pending, seq)
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("debug: send %s: %w", command, err)
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("debug: %s: %w", command, r.err)
		}
		if out != nil && len(r.body) > 0 {
			if err := json.Unmarshal(r.body, out); err != nil {
				return fmt.Errorf("debug: decode %s response: %w", command, err)
			}
		}
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, seq)
		s.mu.Unlock()
		return fmt.Errorf("debug: %s: %w", command, ctx.Err())
	case <-s.done:
		return fmt.Errorf("debug: %s: %w", command, s.failure())
	}
}
func (s *debugSession) readLoop(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		data, err := readDebugMessage(br)
		if err != nil {
			s.fail(err)
			return
		}
		var m debugMessage
		if err = json.Unmarshal(data, &m); err != nil {
			s.fail(fmt.Errorf("invalid JSON: %w", err))
			return
		}
		switch m.Type {
		case "response":
			s.mu.Lock()
			ch := s.pending[m.RequestSeq]
			delete(s.pending, m.RequestSeq)
			s.mu.Unlock()
			if ch != nil {
				if !m.Success {
					ch <- debugResponse{err: errors.New(firstNonempty(m.Message, "adapter rejected request"))}
				} else {
					ch <- debugResponse{body: m.Body}
				}
			}
		case "event":
			s.handleEvent(m)
		case "request":
			s.mu.Lock()
			s.seq++
			err := writeDebugMessage(s.in, map[string]any{"seq": s.seq, "type": "response", "request_seq": m.Seq, "command": m.Command, "success": false, "message": "client does not support adapter requests"})
			s.mu.Unlock()
			if err != nil {
				s.fail(err)
				return
			}
		}
	}
}
func (s *debugSession) handleEvent(m debugMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	summary := m.Event
	if m.Event == "initialized" {
		s.initializedOnce.Do(func() { close(s.initialized) })
	}
	if m.Event == "output" {
		var b struct {
			Output   string `json:"output"`
			Category string `json:"category"`
		}
		_ = json.Unmarshal(m.Body, &b)
		s.appendOutput([]byte(b.Output))
		summary = "output"
		if b.Category != "" {
			summary += " (" + b.Category + ")"
		}
	}
	if m.Event == "stopped" {
		var b struct {
			ThreadID int    `json:"threadId"`
			Reason   string `json:"reason"`
		}
		_ = json.Unmarshal(m.Body, &b)
		s.stoppedThread = b.ThreadID
		s.stoppedFrame = 0
		if b.Reason != "" {
			summary += " (" + b.Reason + ")"
		}
	}
	s.events = append(s.events, summary)
	if len(s.events) > 100 {
		s.events = append([]string(nil), s.events[len(s.events)-100:]...)
	}
}
func (s *debugSession) appendOutput(p []byte) {
	s.output = append(s.output, p...)
	if len(s.output) > debugOutputLimit {
		s.output = append([]byte(nil), s.output[len(s.output)-debugOutputLimit:]...)
	}
}
func (s *debugSession) fail(err error) {
	s.mu.Lock()
	s.readErr = err
	for id, ch := range s.pending {
		ch <- debugResponse{err: err}
		delete(s.pending, id)
	}
	s.mu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
}
func (s *debugSession) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return s.readErr
	}
	return errors.New("adapter exited")
}
func (s *debugSession) close() {
	_ = s.in.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait() })
	s.doneOnce.Do(func() { close(s.done) })
}
func (s *debugSession) cachedThread() int { s.mu.Lock(); defer s.mu.Unlock(); return s.stoppedThread }
func (s *debugSession) cachedFrame() int  { s.mu.Lock(); defer s.mu.Unlock(); return s.stoppedFrame }
func (s *debugSession) cacheFirstFrame(body json.RawMessage) {
	var b struct {
		StackFrames []struct {
			ID int `json:"id"`
		} `json:"stackFrames"`
	}
	if json.Unmarshal(body, &b) == nil && len(b.StackFrames) > 0 {
		s.mu.Lock()
		s.stoppedFrame = b.StackFrames[0].ID
		s.mu.Unlock()
	}
}
func (s *debugSession) syncBreakpoints(ctx context.Context) error {
	files := make([]string, 0, len(s.breakpoints))
	for f := range s.breakpoints {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		if err := s.request(ctx, "setBreakpoints", map[string]any{"source": map[string]any{"path": f}, "breakpoints": s.breakpoints[f]}, nil); err != nil {
			return err
		}
	}
	if len(s.functions) == 0 {
		return nil
	}
	return s.syncFunctions(ctx)
}
func (s *debugSession) syncFunctions(ctx context.Context) error {
	names := make([]string, 0, len(s.functions))
	for n := range s.functions {
		names = append(names, n)
	}
	sort.Strings(names)
	bps := make([]map[string]any, 0, len(names))
	for _, n := range names {
		b := map[string]any{"name": n}
		if s.functions[n] != "" {
			b["condition"] = s.functions[n]
		}
		bps = append(bps, b)
	}
	return s.request(ctx, "setFunctionBreakpoints", map[string]any{"breakpoints": bps}, nil)
}
func (s *debugSession) status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := "running"
	if s.stoppedThread > 0 {
		state = "stopped on thread " + strconv.Itoa(s.stoppedThread)
	}
	return fmt.Sprintf("1 active debug session: adapter pid %d, %s, %d file breakpoint set(s), %d function breakpoint(s).", s.cmd.Process.Pid, state, len(s.breakpoints), len(s.functions))
}
func (s *debugSession) outputText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	if len(s.output) > 0 {
		b.Write(s.output)
	} else {
		b.WriteString("No adapter output.")
	}
	if len(s.events) > 0 {
		b.WriteString("\nEvents: ")
		b.WriteString(strings.Join(s.events, ", "))
	}
	return strings.TrimSpace(b.String())
}

func writeDebugMessage(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}
func readDebugMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length: %w", err)
			}
		}
	}
	if length < 0 {
		return nil, errors.New("missing Content-Length")
	}
	if length > 16<<20 {
		return nil, errors.New("DAP message exceeds 16 MiB")
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}
func formatDebugBody(operation string, body json.RawMessage) string {
	if len(body) == 0 || string(body) == "null" {
		return operation + ": OK"
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") == nil {
		return pretty.String()
	}
	return string(body)
}
func firstNonempty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "unknown error"
}
