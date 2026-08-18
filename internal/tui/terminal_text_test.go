package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

const terminalAttack = "before\x1b]52;c;YXR0YWNr\a\x1b[2J\x1b[31mowned\x1b[0m\x00\x7fafter"

const terminalAttackVisibleText = "beforeownedafter"

func TestEntryRender_RemovesUntrustedTerminalControlsBeforeStyling(t *testing.T) {
	tests := []struct {
		name      string
		malicious entry
		clean     entry
	}{
		{name: "user", malicious: entry{kind: entryUser, text: terminalAttack}, clean: entry{kind: entryUser, text: terminalAttackVisibleText}},
		{name: "assistant", malicious: entry{kind: entryAssistant, text: terminalAttack}, clean: entry{kind: entryAssistant, text: terminalAttackVisibleText}},
		{name: "reasoning", malicious: entry{kind: entryReasoning, text: terminalAttack, live: true}, clean: entry{kind: entryReasoning, text: terminalAttackVisibleText, live: true}},
		{name: "error", malicious: entry{kind: entryError, text: terminalAttack}, clean: entry{kind: entryError, text: terminalAttackVisibleText}},
		{name: "compaction", malicious: entry{kind: entryCompaction, text: terminalAttack}, clean: entry{kind: entryCompaction, text: terminalAttackVisibleText}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, want := renderEntry(test.malicious, 80), renderEntry(test.clean, 80); got != want {
				t.Fatalf("render with terminal controls = %q, want sanitized render %q", got, want)
			}
		})
	}
}

func TestToolRender_RemovesUntrustedTerminalControlsBeforeStyling(t *testing.T) {
	tests := []struct {
		name          string
		malicious     entry
		clean         entry
		expectVisible bool
	}{
		{
			name:          "input summary",
			malicious:     entry{kind: entryTool, tool: "bash", input: commandInput(t, terminalAttack)},
			clean:         entry{kind: entryTool, tool: "bash", input: commandInput(t, terminalAttackVisibleText)},
			expectVisible: true,
		},
		{
			name:          "success output",
			malicious:     entry{kind: entryTool, tool: "bash", status: toolOK, output: terminalAttack, expanded: true},
			clean:         entry{kind: entryTool, tool: "bash", status: toolOK, output: terminalAttackVisibleText, expanded: true},
			expectVisible: true,
		},
		{
			name:          "syntax-highlighted success diff",
			malicious:     entry{kind: entryTool, tool: "edit", status: toolOK, diff: "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+" + terminalAttack},
			clean:         entry{kind: entryTool, tool: "edit", status: toolOK, diff: "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+" + terminalAttackVisibleText},
			expectVisible: true,
		},
		{
			name:          "failure",
			malicious:     entry{kind: entryTool, tool: "bash", status: toolFailed, err: terminalAttack, expanded: true},
			clean:         entry{kind: entryTool, tool: "bash", status: toolFailed, err: terminalAttackVisibleText, expanded: true},
			expectVisible: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, want := renderEntry(test.malicious, 80), renderEntry(test.clean, 80)
			if got != want {
				t.Fatalf("render with terminal controls = %q, want sanitized render %q", got, want)
			}
			if test.expectVisible && !strings.Contains(got, terminalAttackVisibleText) {
				t.Fatalf("sanitized render = %q, want visible text %q", got, terminalAttackVisibleText)
			}
			for _, control := range []string{"\x1b]52", "\x1b[2J", "\x00", "\x7f"} {
				if strings.Contains(got, control) {
					t.Fatalf("sanitized render = %q, still contains terminal control %q", got, control)
				}
			}
		})
	}
}

func TestAuxiliaryViews_RemoveUntrustedTerminalControlsBeforeStyling(t *testing.T) {
	t.Run("completion menu", func(t *testing.T) {
		malicious := Model{composer: composer{menuItems: []menuItem{{label: terminalAttack, description: terminalAttack}}}}
		clean := Model{composer: composer{menuItems: []menuItem{{label: terminalAttackVisibleText, description: terminalAttackVisibleText}}}}
		if got, want := malicious.menuView(), clean.menuView(); got != want {
			t.Fatalf("menu with terminal controls = %q, want sanitized menu %q", got, want)
		}
	})

	t.Run("top bar", func(t *testing.T) {
		malicious := Model{width: 200, branch: terminalAttack, workDir: terminalAttack}
		clean := Model{width: 200, branch: terminalAttackVisibleText, workDir: terminalAttackVisibleText}
		if got, want := malicious.topBarLine(), clean.topBarLine(); got != want {
			t.Fatalf("top bar with terminal controls = %q, want sanitized top bar %q", got, want)
		}
	})
}

func commandInput(t *testing.T, command string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
