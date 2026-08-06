// Command selectorprototype is a throwaway interactive prototype for issue #15.
// Run: go run ./internal/learn/selectorprototype
//
// Question: can weighted lexical overlap select relevant approved lessons while
// reliably rejecting weak and exception-matching candidates under fixed count
// and token budgets, without a Provider call?
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/K3N4Y/atenea/internal/learn/selectorprototype/selector"
)

var lessons = []selector.Lesson{
	{ID: "L-01", Statement: "Use ripgrep to search repository text and files", Scope: "codebase exploration and locating Go implementations", Exceptions: "ripgrep is unavailable"},
	{ID: "L-02", Statement: "Run concurrent Go tests with the race detector", Scope: "changes to goroutines channels locks or shared state", Exceptions: "the package cannot run under the race detector"},
	{ID: "L-03", Statement: "Use contract kits for every new store implementation", Scope: "session stores permission backends and provider adapters", Exceptions: "a private helper is not a contract implementation"},
	{ID: "L-04", Statement: "Preserve unrelated worktree changes", Scope: "editing code in a dirty Git worktree", Exceptions: "the user explicitly authorizes destructive cleanup"},
	{ID: "L-05", Statement: "Mirror changes across every system prompt variant", Scope: "exploration and verification rules in prompt text", Exceptions: "the change only adds a runtime prompt section"},
	{ID: "L-06", Statement: "Prefer immutable sequence cuts for background analysis", Scope: "asynchronous work derived from session event history", Exceptions: "the source has no monotonic sequence"},
	{ID: "L-07", Statement: "Validate structured model output and fail closed", Scope: "prompt instructed JSON without structured output support", Exceptions: "a repair interaction is explicitly required"},
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("LESSON SELECTOR PROTOTYPE — enter prompts; blank line or Ctrl-D exits")
	fmt.Println("Try: 'update the concurrent Go session store tests' or 'add a runtime prompt section'")
	fmt.Println()
	for {
		fmt.Print("prompt> ")
		if !scanner.Scan() || strings.TrimSpace(scanner.Text()) == "" {
			return
		}
		results := selector.Select(scanner.Text(), lessons)
		fmt.Printf("normalized: %v\n", selector.Tokens(scanner.Text()))
		fmt.Println("rank  id    score  tokens  decision   hits(statement | scope | exceptions)")
		for i, result := range results {
			decision := "SELECT"
			if !result.Selected {
				decision = "reject: " + result.Reason
			}
			fmt.Printf("%-5d %-5s %-6d %-7d %-18s %v | %v | %v\n", i+1, result.Lesson.ID, result.Score, result.Tokens, decision, result.StatementHits, result.ScopeHits, result.ExceptionHits)
		}
		fmt.Println()
	}
}
