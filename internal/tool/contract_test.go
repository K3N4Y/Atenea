package tool

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/K3N4Y/atenea/agentcore/memory"
	"github.com/K3N4Y/atenea/agentcore/tool/tooltest"
	"github.com/K3N4Y/atenea/internal/llm"
	"github.com/K3N4Y/atenea/internal/skill"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// Every builtin goes through the published contract kit. Each tool's own test
// covers what it does — which bytes it writes, which lines it anchors, which
// error it gives the model back; this file covers what the registry needs from
// all of them alike: a name and a schema that can be announced, an input parsed
// as JSON, garbage that does not take the process down, a cancelled context that
// is honored, and an Execute the turn can settle from several goroutines at once.
//
// A new builtin belongs in this table. It is the cheapest place to find out that
// it panics on what a model actually sends.

// contractSearcher and contractGlobSearcher stand in for ripgrep so the contract
// does not depend on a binary being installed. Both are stateless: the recording
// fakes the other tests use would race under the concurrency check, and the race
// would be the fake's, not the tool's.
type contractSearcher struct{ matches []GrepMatch }

func (s contractSearcher) Grep(context.Context, GrepRequest) (GrepResult, error) {
	return GrepResult{Matches: s.matches}, nil
}

type contractGlobSearcher struct{ entries []GlobEntry }

func (s contractGlobSearcher) Glob(context.Context, GlobSearch) (GlobSearchResult, error) {
	return GlobSearchResult{Entries: s.entries}, nil
}

// workspaceWithFile is the world most builtins need: a root with one file in it.
func workspaceWithFile(t *testing.T, name, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return root
}

func input(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return raw
}

func TestBuiltins_Contract(t *testing.T) {
	builtins := []struct {
		name       string
		newSubject func(t *testing.T) tooltest.Subject
	}{
		{"echo", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{Tool: Echo{}, Input: input(t, map[string]any{"text": "hola"})}
		}},

		{"read", func(t *testing.T) tooltest.Subject {
			root := workspaceWithFile(t, "foo.go", "package main\n\nfunc main() {}\n")
			return tooltest.Subject{
				Tool:  NewReadTool(root, hashline.NewMemSnapshotStore()),
				Input: input(t, map[string]any{"path": "foo.go"}),
			}
		}},

		{"write", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{
				Tool:  NewWriteTool(t.TempDir(), hashline.NewMemSnapshotStore()),
				Input: input(t, map[string]any{"path": "new.go", "content": "package main\n"}),
			}
		}},

		{"edit", func(t *testing.T) tooltest.Subject {
			// A patch anchors against the lines a previous read recorded, so the
			// subject has to arrive with that read already done.
			const content = "a\nb\nc\nd\n"
			root := workspaceWithFile(t, "foo.go", content)
			snaps := hashline.NewMemSnapshotStore()
			abs := filepath.Join(root, "foo.go")
			hash, _ := snaps.Record(abs, content)
			snaps.RecordSeenLines(abs, hash, []int{1, 2, 3, 4})
			return tooltest.Subject{
				Tool:  NewEditTool(root, hashline.OSFilesystem{}, snaps),
				Input: input(t, map[string]any{"input": "[foo.go#" + hash + "]\nPUT 2.=3:\n+X"}),
			}
		}},

		{"grep", func(t *testing.T) tooltest.Subject {
			root := workspaceWithFile(t, "foo.go", "package main\n\nfunc main() {}\n")
			return tooltest.Subject{
				Tool: &GrepTool{
					Root:       root,
					Searcher:   contractSearcher{matches: []GrepMatch{{Path: "foo.go", Line: 3, Text: "func main() {}"}}},
					FS:         osFS{},
					Snapshots:  hashline.NewMemSnapshotStore(),
					MaxMatches: defaultGrepMaxMatches,
				},
				Input: input(t, map[string]any{"pattern": "func", "path": ".", "include": "*.go"}),
			}
		}},

		{"glob", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{
				Tool: &GlobTool{
					Root:         t.TempDir(),
					Searcher:     contractGlobSearcher{entries: []GlobEntry{{Path: "foo.go"}}},
					DefaultLimit: defaultGlobLimit,
					MaxLimit:     maxGlobLimit,
				},
				Input: input(t, map[string]any{"pattern": "**/*.go"}),
			}
		}},

		{"bash", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{
				Tool:  NewBashTool(t.TempDir()),
				Input: input(t, map[string]any{"command": "echo hola"}),
			}
		}},

		{"present_plan", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{
				Tool:  NewPresentPlanTool(t.TempDir()),
				Input: input(t, map[string]any{"title": "The plan", "plan": "# The plan\n\n- do it\n"}),
			}
		}},

		{"skill", func(t *testing.T) tooltest.Subject {
			root := t.TempDir()
			location := filepath.Join(root, "demo", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(location), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(location, []byte("---\nname: demo\n---\nbody\n"), 0o644); err != nil {
				t.Fatalf("write SKILL.md: %v", err)
			}
			catalog := []skill.Info{{Name: "demo", Description: "A demo skill.", Location: location, Content: "body"}}
			return tooltest.Subject{
				Tool:  NewSkillTool(catalog),
				Input: input(t, map[string]any{"name": "demo"}),
			}
		}},

		{"todo_write", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{
				Tool: TodoWriteTool{},
				Input: input(t, map[string]any{"todos": []map[string]string{
					{"content": "Read the audit", "status": "completed"},
					{"content": "Ship the kits", "status": "in_progress"},
				}}),
			}
		}},

		{"retain_memory", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{
				Tool:  NewRetainMemoryTool(t.TempDir(), &memoryStub{}),
				Input: input(t, map[string]any{"text": "Use SQLite", "source": "docs/architecture.md:12"}),
			}
		}},

		{"recall_memory", func(t *testing.T) tooltest.Subject {
			store := &memoryStub{fact: memory.Fact{ID: 1, Project: "project", Text: "Use SQLite", Source: "docs/architecture.md:12", CreatedAt: time.Now().UTC()}}
			return tooltest.Subject{Tool: NewRecallMemoryTool("project", store), Input: input(t, map[string]any{"query": "sqlite"})}
		}},

		{"checkpoint", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{Tool: NewCheckpointTool(func(string, string) (string, error) { return "checkpoint-1", nil }), Input: input(t, map[string]any{})}
		}},

		{"rewind", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{Tool: NewRewindTool(func(string) (string, error) { return "checkpoint-1", nil }), Input: input(t, map[string]any{})}
		}},

		{"lsp", func(t *testing.T) tooltest.Subject {
			root := workspaceWithFile(t, "main.go", "package main\n")
			return tooltest.Subject{
				Tool:  testLSPTool(t, root),
				Input: input(t, map[string]any{"operation": "diagnostics", "path": "main.go"}),
			}
		}},

		{"ast", func(t *testing.T) tooltest.Subject {
			root := workspaceWithFile(t, "main.go", "package main\n")
			ast := NewASTTool(root)
			// Stub with a real script, never os.Args[0]: the test binary ignores
			// ast-grep's arguments and re-runs the whole suite, fork-bombing the host.
			fake := filepath.Join(t.TempDir(), "fake-ast-grep")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '[]'\n"), 0o755); err != nil {
				t.Fatalf("seed fake ast-grep: %v", err)
			}
			ast.commandFor = func() (string, error) { return fake, nil }
			return tooltest.Subject{
				Tool:  ast,
				Input: input(t, map[string]any{"operation": "search", "path": "main.go", "pattern": "package main", "language": "go"}),
			}
		}},

		{"debug", func(t *testing.T) tooltest.Subject {
			return tooltest.Subject{
				Tool:  NewDebugTool(t.TempDir()),
				Input: input(t, map[string]any{"operation": "sessions"}),
			}
		}},

		{"web_fetch", func(t *testing.T) tooltest.Subject {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				io.WriteString(w, `<html><body><h1>atenea</h1><p>Written in Go.</p></body></html>`)
			}))
			t.Cleanup(server.Close)
			fetch := NewWebFetchTool(contractProvider{answer: "Go."})
			fetch.setClient(server.Client())
			// The stub server answers on the loopback, which the real SSRF guard
			// exists to refuse.
			fetch.blockIP = func(net.IP) bool { return false }
			return tooltest.Subject{
				Tool:  fetch,
				Input: input(t, map[string]any{"url": server.URL, "prompt": "Which language?"}),
			}
		}},
	}

	for _, builtin := range builtins {
		t.Run(builtin.name, func(t *testing.T) {
			tooltest.Contract(t, builtin.newSubject)
		})
	}
}

// contractProvider is the model web_fetch distills its answer with: one text
// event and no shared state, so it is safe under the concurrency check.
type contractProvider struct{ answer string }

func (p contractProvider) Stream(_ context.Context, _ llm.Request) (<-chan llm.Event, error) {
	out := make(chan llm.Event, 3)
	out <- llm.Event{Kind: llm.TextStarted}
	out <- llm.Event{Kind: llm.TextDelta, Text: p.answer}
	out <- llm.Event{Kind: llm.TextEnded}
	close(out)
	return out, nil
}
