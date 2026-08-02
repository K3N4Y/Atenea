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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/K3N4Y/atenea/agentcore/permission"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// LSPTool exposes a small, language-independent set of source navigation and
// refactoring operations. Servers are started lazily and retained per workspace.
type LSPTool struct {
	root         string
	mu           sync.Mutex
	servers      map[string]*lspClient
	commandFor   func(string) ([]string, error)
	commitRename func(string, string) error
}

type lspInput struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	Query     string `json:"query"`
	NewName   string `json:"new_name"`
}

func NewLSPTool(root string) *LSPTool {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = filepath.Clean(root)
	}
	return &LSPTool{root: abs, servers: make(map[string]*lspClient), commandFor: serverCommand, commitRename: os.Rename}
}

// Close stops and reaps every language server started by this tool.
func (t *LSPTool) Close() error {
	t.mu.Lock()
	servers := make([]*lspClient, 0, len(t.servers))
	for key, client := range t.servers {
		servers = append(servers, client)
		delete(t.servers, key)
	}
	t.mu.Unlock()
	for _, client := range servers {
		client.close()
	}
	return nil
}

func (*LSPTool) Name() string { return "lsp" }

//go:embed lsp.txt
var lspDescription string

func (*LSPTool) Description() string { return lspDescription }
func (*LSPTool) Effects() Effects    { return NoEffects }

// CallEffects lets hosts which understand per-call effects distinguish the only
// mutating operation without classifying source navigation as a write.
func (*LSPTool) CallEffects(call Call) Effects {
	var in lspInput
	if json.Unmarshal(call.Input, &in) == nil && in.Operation == "rename" && in.Path != "" && in.Line > 0 && in.Column > 0 && in.NewName != "" {
		return WritesFiles
	}
	return NoEffects
}

func (t *LSPTool) GrantRule(call Call) (permission.Rule, bool) {
	if t.CallEffects(call) != WritesFiles {
		return permission.Rule{}, false
	}
	return permission.Rule{Tool: t.Name(), Prefix: "rename"}, true
}

func (*LSPTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string","enum":["diagnostics","definition","references","symbols","rename"]},"path":{"type":"string","minLength":1,"description":"Workspace-relative path, or an absolute path within the workspace."},"line":{"type":"integer","minimum":1},"column":{"type":"integer","minimum":1},"query":{"type":"string","description":"For symbols: empty selects document symbols; nonempty searches workspace symbols."},"new_name":{"type":"string","minLength":1}},"required":["operation","path"],"additionalProperties":false}`)
}

func (t *LSPTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var in lspInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{}, fmt.Errorf("lsp: invalid input: %w", err)
	}
	if err := validateLSPInput(in); err != nil {
		return Result{}, err
	}
	abs, rel, err := t.resolvePath(in.Path)
	if err != nil {
		return Result{}, err
	}
	client, err := t.client(ctx, abs)
	if err != nil {
		return Result{}, err
	}
	text, err := client.syncFile(ctx, abs)
	if err != nil {
		return Result{}, err
	}
	uri := pathURI(abs)
	pos := lspPosition{Line: in.Line - 1, Character: utf16Column(text, in.Line-1, in.Column-1)}

	switch in.Operation {
	case "diagnostics":
		out, err := client.diagnostics(ctx, uri)
		return Result{Output: t.formatDiagnostics(rel, out)}, err
	case "definition":
		var out json.RawMessage
		err = client.request(ctx, "textDocument/definition", textPositionParams(uri, pos), &out)
		return Result{Output: t.formatLocations(out)}, err
	case "references":
		var out json.RawMessage
		err = client.request(ctx, "textDocument/references", map[string]any{"textDocument": map[string]string{"uri": uri}, "position": pos, "context": map[string]bool{"includeDeclaration": true}}, &out)
		return Result{Output: t.formatLocations(out)}, err
	case "symbols":
		var out json.RawMessage
		if in.Query == "" {
			err = client.request(ctx, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]string{"uri": uri}}, &out)
		} else {
			err = client.request(ctx, "workspace/symbol", map[string]string{"query": in.Query}, &out)
		}
		return Result{Output: t.formatSymbols(out, rel)}, err
	case "rename":
		var edit workspaceEdit
		err = client.request(ctx, "textDocument/rename", map[string]any{"textDocument": map[string]string{"uri": uri}, "position": pos, "newName": in.NewName}, &edit)
		if err != nil {
			return Result{}, err
		}
		return t.applyWorkspaceEdit(edit)
	default:
		return Result{}, fmt.Errorf("lsp: unsupported operation %q", in.Operation)
	}
}

func validateLSPInput(in lspInput) error {
	if in.Path == "" {
		return errors.New("lsp: path is required")
	}
	switch in.Operation {
	case "diagnostics", "symbols":
	case "definition", "references":
		if in.Line < 1 || in.Column < 1 {
			return fmt.Errorf("lsp: %s requires 1-based line and column", in.Operation)
		}
	case "rename":
		if in.Line < 1 || in.Column < 1 {
			return errors.New("lsp: rename requires 1-based line and column")
		}
		if in.NewName == "" {
			return errors.New("lsp: rename requires new_name")
		}
	default:
		return fmt.Errorf("lsp: unsupported operation %q", in.Operation)
	}
	return nil
}

// DiagnosticsForPath is the middleware-friendly diagnostics entry point.
func (t *LSPTool) DiagnosticsForPath(ctx context.Context, path string) (string, error) {
	res, err := t.Execute(ctx, marshalLSPInput(lspInput{Operation: "diagnostics", Path: path}))
	return res.Output, err
}

func marshalLSPInput(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (t *LSPTool) resolvePath(path string) (string, string, error) {
	abs, err := sandboxJoin(t.root, path, "lsp")
	if err != nil {
		return "", "", err
	}
	if err := rejectRealPathOutside(t.root, abs, path, "lsp"); err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(t.root, abs)
	if err != nil {
		return "", "", err
	}
	return abs, filepath.ToSlash(rel), nil
}

func serverCommand(path string) ([]string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return []string{"gopls"}, nil
	case ".rs":
		return []string{"rust-analyzer"}, nil
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return []string{"typescript-language-server", "--stdio"}, nil
	case ".py", ".pyi":
		return []string{"pyright-langserver", "--stdio"}, nil
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh":
		return []string{"clangd"}, nil
	default:
		return nil, fmt.Errorf("lsp: no language server configured for %s", filepath.Ext(path))
	}
}

func (t *LSPTool) client(ctx context.Context, path string) (*lspClient, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	argv, err := t.commandFor(path)
	if err != nil {
		return nil, err
	}
	key := strings.Join(argv, "\x00")
	t.mu.Lock()
	defer t.mu.Unlock()
	if previous := t.servers[key]; previous != nil {
		if previous.alive() {
			return previous, nil
		}
		previous.close()
		delete(t.servers, key)
	}
	c, err := startLSP(ctx, t.root, argv)
	if err != nil {
		return nil, err
	}
	t.servers[key] = c
	return c, nil
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}
type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}
type lspDiagnostic struct {
	Range    lspRange `json:"range"`
	Severity int      `json:"severity"`
	Message  string   `json:"message"`
	Code     any      `json:"code,omitempty"`
}
type lspResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *lspError       `json:"error,omitempty"`
}
type lspError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *lspError) Error() string { return fmt.Sprintf("LSP error %d: %s", e.Code, e.Message) }

type pendingResponse struct {
	result json.RawMessage
	err    error
}
type lspClient struct {
	root              string
	cmd               *exec.Cmd
	in                io.WriteCloser
	writeMu           sync.Mutex
	stateMu           sync.Mutex
	pending           map[int64]chan pendingResponse
	opened            map[string]string
	diagnosticsByURI  map[string][]lspDiagnostic
	diagnosticWaiters map[string][]chan struct{}
	nextID            atomic.Int64
	done              chan struct{}
	readErr           error
	pullDiagnostics   bool
}

func startLSP(ctx context.Context, root string, argv []string) (*lspClient, error) {
	binary, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("lsp: %s is not installed: %w", argv[0], err)
	}
	cmd := exec.Command(binary, argv[1:]...)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start %s: %w", argv[0], err)
	}
	c := &lspClient{root: root, cmd: cmd, in: stdin, pending: make(map[int64]chan pendingResponse), opened: make(map[string]string), diagnosticsByURI: make(map[string][]lspDiagnostic), diagnosticWaiters: make(map[string][]chan struct{}), done: make(chan struct{})}
	go c.readLoop(stdout)
	var init struct {
		Capabilities struct {
			DiagnosticProvider json.RawMessage `json:"diagnosticProvider"`
		} `json:"capabilities"`
	}
	params := map[string]any{"processId": os.Getpid(), "rootUri": pathURI(root), "capabilities": map[string]any{"textDocument": map[string]any{"diagnostic": map[string]any{}, "definition": map[string]any{"linkSupport": true}}, "workspace": map[string]any{"workspaceEdit": map[string]any{"documentChanges": true}}}}
	if err := c.request(ctx, "initialize", params, &init); err != nil {
		c.close()
		return nil, fmt.Errorf("lsp: initialize %s: %w%s", argv[0], err, stderrSuffix(stderr.String()))
	}
	c.pullDiagnostics = len(init.Capabilities.DiagnosticProvider) > 0 && string(init.Capabilities.DiagnosticProvider) != "null"
	if err := c.notify("initialized", map[string]any{}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}
func stderrSuffix(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return ": " + strings.TrimSpace(s)
}
func (c *lspClient) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
func (c *lspClient) close() {
	_ = c.in.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
}

func (c *lspClient) readLoop(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		msg, err := readLSPMessage(br)
		if err != nil {
			c.fail(err)
			return
		}
		var response lspResponse
		if json.Unmarshal(msg, &response) != nil {
			continue
		}
		// Requests initiated by the server use its own ID space. They must not be
		// mistaken for responses to our requests (the numeric IDs commonly overlap).
		if len(response.ID) > 0 && response.Method != "" {
			result := any(nil)
			if response.Method == "workspace/configuration" {
				var p struct {
					Items []json.RawMessage `json:"items"`
				}
				_ = json.Unmarshal(response.Params, &p)
				result = make([]any, len(p.Items))
			}
			_ = c.send(map[string]any{"jsonrpc": "2.0", "id": response.ID, "result": result})
			continue
		}
		if len(response.ID) > 0 {
			var id int64
			if json.Unmarshal(response.ID, &id) == nil {
				c.stateMu.Lock()
				ch := c.pending[id]
				delete(c.pending, id)
				c.stateMu.Unlock()
				if ch != nil {
					if response.Error != nil {
						ch <- pendingResponse{err: response.Error}
					} else {
						ch <- pendingResponse{result: response.Result}
					}
				}
			}
			continue
		}
		if response.Method == "textDocument/publishDiagnostics" {
			var p struct {
				URI         string          `json:"uri"`
				Diagnostics []lspDiagnostic `json:"diagnostics"`
			}
			if json.Unmarshal(response.Params, &p) == nil {
				c.stateMu.Lock()
				c.diagnosticsByURI[p.URI] = p.Diagnostics
				waiters := c.diagnosticWaiters[p.URI]
				delete(c.diagnosticWaiters, p.URI)
				c.stateMu.Unlock()
				for _, ch := range waiters {
					close(ch)
				}
			}
		}
	}
}
func (c *lspClient) fail(err error) {
	c.stateMu.Lock()
	c.readErr = err
	for id, ch := range c.pending {
		ch <- pendingResponse{err: err}
		delete(c.pending, id)
	}
	c.stateMu.Unlock()
	close(c.done)
}

func readLSPMessage(r *bufio.Reader) ([]byte, error) {
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
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(k, "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, err
			}
		}
	}
	if length < 0 || length > 64<<20 {
		return nil, fmt.Errorf("lsp: invalid Content-Length %d", length)
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}
func (c *lspClient) send(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}
func (c *lspClient) notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (c *lspClient) request(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.nextID.Add(1)
	ch := make(chan pendingResponse, 1)
	c.stateMu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.stateMu.Unlock()
		return err
	}
	c.pending[id] = ch
	c.stateMu.Unlock()
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.stateMu.Lock()
		delete(c.pending, id)
		c.stateMu.Unlock()
		return err
	}
	select {
	case response := <-ch:
		if response.err != nil {
			return response.err
		}
		if out != nil && len(response.result) > 0 {
			if err := json.Unmarshal(response.result, out); err != nil {
				return fmt.Errorf("lsp: decode %s response: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.stateMu.Lock()
		delete(c.pending, id)
		c.stateMu.Unlock()
		_ = c.notify("$/cancelRequest", map[string]int64{"id": id})
		return ctx.Err()
	case <-c.done:
		return errors.New("lsp: language server exited")
	}
}
func (c *lspClient) syncFile(ctx context.Context, path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(b)
	uri := pathURI(path)
	c.stateMu.Lock()
	old, opened := c.opened[uri]
	if old == text {
		c.stateMu.Unlock()
		return text, nil
	}
	c.opened[uri] = text
	// Keep state locked until the notification is written, so the read loop
	// cannot register a publication as fresh in the gap before didChange.
	delete(c.diagnosticsByURI, uri)
	if err := ctx.Err(); err != nil {
		c.stateMu.Unlock()
		return "", err
	}
	if !opened {
		err = c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": languageID(path), "version": 1, "text": text}})
	} else {
		err = c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": time.Now().UnixNano()}, "contentChanges": []map[string]string{{"text": text}}})
	}
	c.stateMu.Unlock()
	return text, err
}
func languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py", ".pyi":
		return "python"
	case ".c", ".h":
		return "c"
	default:
		return "cpp"
	}
}
func textPositionParams(uri string, pos lspPosition) any {
	return map[string]any{"textDocument": map[string]string{"uri": uri}, "position": pos}
}

func (c *lspClient) diagnostics(ctx context.Context, uri string) ([]lspDiagnostic, error) {
	if c.pullDiagnostics {
		var out struct {
			Items []lspDiagnostic `json:"items"`
		}
		if err := c.request(ctx, "textDocument/diagnostic", map[string]any{"textDocument": map[string]string{"uri": uri}}, &out); err == nil {
			return out.Items, nil
		}
	}
	c.stateMu.Lock()
	if d, ok := c.diagnosticsByURI[uri]; ok {
		c.stateMu.Unlock()
		return d, nil
	}
	waiter := make(chan struct{})
	c.diagnosticWaiters[uri] = append(c.diagnosticWaiters[uri], waiter)
	c.stateMu.Unlock()
	timer := time.NewTimer(350 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-waiter:
		c.stateMu.Lock()
		d := c.diagnosticsByURI[uri]
		c.stateMu.Unlock()
		return d, nil
	case <-timer.C:
		c.removeDiagnosticWaiter(uri, waiter)
		return nil, nil
	case <-ctx.Done():
		c.removeDiagnosticWaiter(uri, waiter)
		return nil, ctx.Err()
	}
}

func (c *lspClient) removeDiagnosticWaiter(uri string, waiter chan struct{}) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	waiters := c.diagnosticWaiters[uri]
	for i, candidate := range waiters {
		if candidate != waiter {
			continue
		}
		waiters = append(waiters[:i], waiters[i+1:]...)
		if len(waiters) == 0 {
			delete(c.diagnosticWaiters, uri)
		} else {
			c.diagnosticWaiters[uri] = waiters
		}
		return
	}
}

func pathURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
func uriPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", fmt.Errorf("lsp: unsupported URI %q", uri)
	}
	return filepath.FromSlash(u.Path), nil
}
func utf16Column(text string, line, runeColumn int) int {
	lines := strings.Split(text, "\n")
	if line < 0 || line >= len(lines) {
		return max(0, runeColumn)
	}
	runes := []rune(lines[line])
	if runeColumn < 0 {
		runeColumn = 0
	}
	if runeColumn > len(runes) {
		runeColumn = len(runes)
	}
	return len(utf16.Encode(runes[:runeColumn]))
}
func byteOffset(text string, p lspPosition) (int, error) {
	lines := strings.SplitAfter(text, "\n")
	if p.Line < 0 || p.Line >= len(lines) {
		return 0, fmt.Errorf("line %d is outside file", p.Line+1)
	}
	line := strings.TrimSuffix(lines[p.Line], "\n")
	units := 0
	off := 0
	for off < len(line) && units < p.Character {
		r, n := utf8.DecodeRuneInString(line[off:])
		u := 1
		if r > 0xffff {
			u = 2
		}
		if units+u > p.Character {
			return 0, errors.New("position splits UTF-16 surrogate pair")
		}
		units += u
		off += n
	}
	if units != p.Character {
		return 0, fmt.Errorf("column %d is outside line", p.Character+1)
	}
	base := 0
	for i := 0; i < p.Line; i++ {
		base += len(lines[i])
	}
	return base + off, nil
}

func (t *LSPTool) displayPath(uri string) (string, error) {
	p, err := uriPath(uri)
	if err != nil {
		return "", err
	}
	p = filepath.Clean(p)
	if !insideRoot(t.root, p) {
		return "", fmt.Errorf("lsp: server returned path outside workspace: %s", p)
	}
	rel, err := filepath.Rel(t.root, p)
	return filepath.ToSlash(rel), err
}
func (t *LSPTool) formatLocations(raw json.RawMessage) string {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		if string(raw) == "null" {
			return "No locations found."
		}
		values = []json.RawMessage{raw}
	}
	var lines []string
	for _, value := range values {
		var loc lspLocation
		if json.Unmarshal(value, &loc) == nil && loc.URI != "" {
			if p, err := t.displayPath(loc.URI); err == nil {
				lines = append(lines, fmt.Sprintf("%s:%d:%d", p, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
			}
			continue
		}
		var link struct {
			TargetURI            string    `json:"targetUri"`
			TargetSelectionRange *lspRange `json:"targetSelectionRange"`
			TargetRange          lspRange  `json:"targetRange"`
		}
		if json.Unmarshal(value, &link) == nil && link.TargetURI != "" {
			r := &link.TargetRange
			if link.TargetSelectionRange != nil {
				r = link.TargetSelectionRange
			}
			if p, err := t.displayPath(link.TargetURI); err == nil {
				lines = append(lines, fmt.Sprintf("%s:%d:%d", p, r.Start.Line+1, r.Start.Character+1))
			}
		}
	}
	if len(lines) == 0 {
		return "No locations found."
	}
	return strings.Join(lines, "\n")
}
func (t *LSPTool) formatDiagnostics(path string, ds []lspDiagnostic) string {
	if len(ds) == 0 {
		return "No diagnostics."
	}
	var lines []string
	for _, d := range ds {
		severity := map[int]string{1: "error", 2: "warning", 3: "info", 4: "hint"}[d.Severity]
		if severity == "" {
			severity = "diagnostic"
		}
		lines = append(lines, fmt.Sprintf("%s:%d:%d: %s: %s", path, d.Range.Start.Line+1, d.Range.Start.Character+1, severity, strings.ReplaceAll(d.Message, "\n", " ")))
	}
	return strings.Join(lines, "\n")
}

type lspSymbol struct {
	Name           string       `json:"name"`
	Location       *lspLocation `json:"location"`
	SelectionRange *lspRange    `json:"selectionRange"`
	Range          *lspRange    `json:"range"`
	Children       []lspSymbol  `json:"children"`
}

func (t *LSPTool) formatSymbols(raw json.RawMessage, defaultPath string) string {
	var values []lspSymbol
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return "No symbols found."
	}
	var lines []string
	var appendSymbol func(lspSymbol)
	appendSymbol = func(symbol lspSymbol) {
		path := defaultPath
		var position *lspRange
		if symbol.Location != nil {
			resolved, err := t.displayPath(symbol.Location.URI)
			if err != nil {
				return
			}
			path = resolved
			position = &symbol.Location.Range
		} else if symbol.SelectionRange != nil {
			position = symbol.SelectionRange
		} else {
			position = symbol.Range
		}
		if position != nil {
			lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", path, position.Start.Line+1, position.Start.Character+1, symbol.Name))
		}
		for _, child := range symbol.Children {
			appendSymbol(child)
		}
	}
	for _, symbol := range values {
		appendSymbol(symbol)
	}
	if len(lines) == 0 {
		return "No symbols found."
	}
	return strings.Join(lines, "\n")
}

// WorkspaceEdit wire types. Resource operations deliberately remain RawMessage
// so they can be rejected before any file is changed.
type textEdit struct {
	Range   lspRange `json:"range"`
	NewText string   `json:"newText"`
}
type workspaceEdit struct {
	Changes         map[string][]textEdit `json:"changes"`
	DocumentChanges []json.RawMessage     `json:"documentChanges"`
}
type fileChange struct {
	path, rel, old, new       string
	stagedNew, stagedOriginal string
	mode                      os.FileMode
}

func (t *LSPTool) applyWorkspaceEdit(edit workspaceEdit) (Result, error) {
	byURI := make(map[string][]textEdit)
	for uri, edits := range edit.Changes {
		byURI[uri] = append(byURI[uri], edits...)
	}
	for _, raw := range edit.DocumentChanges {
		var probe struct {
			Kind         string `json:"kind"`
			TextDocument *struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []textEdit `json:"edits"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return Result{}, err
		}
		if probe.Kind != "" || probe.TextDocument == nil {
			return Result{}, errors.New("lsp: rename returned unsupported resource operation")
		}
		byURI[probe.TextDocument.URI] = append(byURI[probe.TextDocument.URI], probe.Edits...)
	}
	changes := make([]fileChange, 0, len(byURI))
	for uri, edits := range byURI {
		path, err := uriPath(uri)
		if err != nil {
			return Result{}, err
		}
		path = filepath.Clean(path)
		if !insideRoot(t.root, path) {
			return Result{}, fmt.Errorf("lsp: rename edit outside workspace: %s", path)
		}
		rel, err := filepath.Rel(t.root, path)
		if err != nil {
			return Result{}, err
		}
		if err = rejectMutableAlias(t.root, path, rel, "lsp"); err != nil {
			return Result{}, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return Result{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return Result{}, err
		}
		newText, err := applyTextEdits(string(b), edits)
		if err != nil {
			return Result{}, fmt.Errorf("lsp: invalid edits for %s: %w", rel, err)
		}
		changes = append(changes, fileChange{path: path, rel: filepath.ToSlash(rel), old: string(b), new: newText, mode: info.Mode()})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].rel < changes[j].rel })
	cleanupStaged := func() {
		for _, c := range changes {
			if c.stagedNew != "" {
				_ = os.Remove(c.stagedNew)
			}
			if c.stagedOriginal != "" {
				_ = os.Remove(c.stagedOriginal)
			}
		}
	}
	defer cleanupStaged()
	stage := func(c *fileChange, pattern, content string) (string, error) {
		tmp, err := os.CreateTemp(filepath.Dir(c.path), pattern)
		if err != nil {
			return "", err
		}
		name := tmp.Name()
		if err = tmp.Chmod(c.mode.Perm()); err == nil {
			_, err = tmp.WriteString(content)
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(name)
			return "", err
		}
		return name, nil
	}
	// Prepare both the replacement and an atomic rollback copy for every file
	// before changing any workspace path.
	for i := range changes {
		c := &changes[i]
		if c.old == c.new {
			continue
		}
		var err error
		if c.stagedNew, err = stage(c, ".atenea-lsp-new-*", c.new); err != nil {
			return Result{}, err
		}
		if c.stagedOriginal, err = stage(c, ".atenea-lsp-old-*", c.old); err != nil {
			return Result{}, err
		}
	}
	committed := make([]*fileChange, 0, len(changes))
	for i := range changes {
		c := &changes[i]
		if c.stagedNew == "" {
			continue
		}
		if err := t.commitRename(c.stagedNew, c.path); err != nil {
			var rollbackErr error
			for j := len(committed) - 1; j >= 0; j-- {
				prior := committed[j]
				if restoreErr := os.Rename(prior.stagedOriginal, prior.path); restoreErr != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore %s: %w", prior.rel, restoreErr))
				} else {
					prior.stagedOriginal = ""
				}
			}
			if rollbackErr != nil {
				return Result{}, fmt.Errorf("lsp: commit rename for %s: %w; rollback failed: %v", c.rel, err, rollbackErr)
			}
			return Result{}, fmt.Errorf("lsp: commit rename for %s: %w", c.rel, err)
		}
		c.stagedNew = ""
		committed = append(committed, c)
	}
	var diffs, summary []string
	for _, c := range changes {
		if d := hashline.UnifiedDiff(c.rel, c.old, c.new, 3); d != "" {
			diffs = append(diffs, d)
			summary = append(summary, c.rel)
		}
	}
	if len(summary) == 0 {
		return Result{Output: "Rename produced no changes."}, nil
	}
	return Result{Output: fmt.Sprintf("Renamed symbol in %d file(s): %s", len(summary), strings.Join(summary, ", ")), Diff: strings.Join(diffs, "\n")}, nil
}
func applyTextEdits(text string, edits []textEdit) (string, error) {
	type resolved struct {
		start, end int
		text       string
	}
	rs := make([]resolved, 0, len(edits))
	for _, e := range edits {
		s, err := byteOffset(text, e.Range.Start)
		if err != nil {
			return "", err
		}
		end, err := byteOffset(text, e.Range.End)
		if err != nil {
			return "", err
		}
		if end < s {
			return "", errors.New("reversed edit range")
		}
		rs = append(rs, resolved{s, end, e.NewText})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].start > rs[j].start })
	last := len(text) + 1
	out := text
	for _, e := range rs {
		if e.end > last {
			return "", errors.New("overlapping edits")
		}
		out = out[:e.start] + e.text + out[e.end:]
		last = e.start
	}
	return out, nil
}
