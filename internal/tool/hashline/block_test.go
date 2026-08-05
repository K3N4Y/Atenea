package hashline

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestStructuralBlockResolver(t *testing.T) {
	r := StructuralBlockResolver{}
	cases := []struct {
		path       string
		lines      []string
		start, end int
	}{
		{"x.ts", []string{"function x() {", "  if (y) {", "  }", "}"}, 2, 3},
		{"x.go", []string{"func x() {", " println(1)", "}"}, 1, 3},
		{"x.py", []string{"def x():", "    if y:", "        pass", "tail()"}, 1, 3},
		{"plan.md", []string{"# Plan", "intro", "## Context", "why", "### Detail", "deep", "## Next"}, 3, 6},
	}
	for _, tc := range cases {
		end, err := r.ResolveBlock(tc.path, tc.lines, tc.start)
		if err != nil || end != tc.end {
			t.Errorf("%s:%d = %d,%v want %d", tc.path, tc.start, end, err, tc.end)
		}
	}
	if _, err := r.ResolveBlock("x.ts", []string{"statement();"}, 1); err == nil {
		t.Fatal("single line resolved as block")
	}
	if _, err := r.ResolveBlock("x.unknown", []string{"thing {", "}"}, 1); err == nil {
		t.Fatal("unknown language silently degraded")
	}
}

// Fixtures mirror the language and boundary cases in pi-mono
// packages/hashline/test/block.test.ts and coding-agent block replacement tests
// at 5af71dc9cf132538e072806424f71f43f734d9ae.
func TestStructuralBlockResolverLanguageMatrix(t *testing.T) {
	r := StructuralBlockResolver{}
	tests := []struct {
		path string
		text []string
		end  int
	}{
		{"fixture.go", []string{"func greet() {", "\tprintln(\"hi\")", "}"}, 3},
		{"fixture.ts", []string{"export function greet() {", "  return 'hi'", "}"}, 3},
		{"fixture.tsx", []string{"function View() {", "  return <div />", "}"}, 3},
		{"fixture.js", []string{"class Greeter {", "  greet() {}", "}"}, 3},
		{"fixture.py", []string{"def greet():", "    return 'hi'"}, 2},
		{"fixture.rs", []string{"fn greet() {", "    println!(\"hi\");", "}"}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			end, err := r.ResolveBlock(tt.path, tt.text, 1)
			if err != nil || end != tt.end {
				t.Fatalf("ResolveBlock = %d, %v; want %d", end, err, tt.end)
			}
		})
	}
}

func TestStructuralBlockResolverGroupingAndRejections(t *testing.T) {
	r := StructuralBlockResolver{}
	for _, tt := range []struct {
		name  string
		path  string
		lines []string
		start int
		end   int
	}{
		{"python decorator", "x.py", []string{"@logged", "def work():", "    pass"}, 1, 3},
		{"go doc comments", "x.go", []string{"// Work does work.", "// It is useful.", "func Work() {", "}"}, 1, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			end, err := r.ResolveBlock(tt.path, tt.lines, tt.start)
			if err != nil || end != tt.end {
				t.Fatalf("ResolveBlock = %d, %v; want %d", end, err, tt.end)
			}
		})
	}

	bad := []struct {
		name  string
		path  string
		lines []string
		line  int
	}{
		{"blank", "x.go", []string{"func f() {", "", "}"}, 2},
		{"closer", "x.go", []string{"func f() {", "}"}, 2},
		{"inner bare statement", "x.go", []string{"func f() {", "println(1)", "}"}, 2},
		{"single line", "x.py", []string{"value = 1"}, 1},
		{"syntax error", "x.rs", []string{"fn broken( {", "}"}, 1},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.ResolveBlock(tt.path, tt.lines, tt.line)
			var unresolved *UnresolvedBlockError
			if !errors.As(err, &unresolved) {
				t.Fatalf("error = %T %v; want UnresolvedBlockError", err, err)
			}
		})
	}
	_, err := r.ResolveBlock("x.toml", []string{"[table]", "key = 1"}, 1)
	var unsupported *UnsupportedBlockLanguageError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %T %v; want UnsupportedBlockLanguageError", err, err)
	}
}

func TestStructuralBlockResolverCacheConcurrentAndBounded(t *testing.T) {
	state := &blockResolverState{entries: make(map[blockCacheKey]blockCacheValue)}
	r := StructuralBlockResolver{state: state}
	lines := []string{"func f() {", "println(1)", "}"}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			end, err := r.ResolveBlock("race.go", lines, 1)
			if err != nil || end != 3 {
				t.Errorf("ResolveBlock = %d, %v", end, err)
			}
		}()
	}
	wg.Wait()
	for i := 0; i < blockCacheLimit+10; i++ {
		path := fmt.Sprintf("cache-%d.go", i)
		if _, err := r.ResolveBlock(path, lines, 1); err != nil {
			t.Fatal(err)
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.entries) != blockCacheLimit || len(state.order) != blockCacheLimit {
		t.Fatalf("cache sizes = %d/%d; want %d", len(state.entries), len(state.order), blockCacheLimit)
	}
}
