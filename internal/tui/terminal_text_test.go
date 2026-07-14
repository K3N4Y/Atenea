package tui

import (
	"encoding/json"
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
			if got, want := test.malicious.render(80), test.clean.render(80); got != want {
				t.Fatalf("render with terminal controls = %q, want sanitized render %q", got, want)
			}
		})
	}
}

func TestToolRender_RemovesUntrustedTerminalControlsBeforeStyling(t *testing.T) {
	tests := []struct {
		name      string
		malicious entry
		clean     entry
	}{
		{
			name:      "input summary",
			malicious: entry{kind: entryTool, tool: "bash", input: commandInput(t, terminalAttack)},
			clean:     entry{kind: entryTool, tool: "bash", input: commandInput(t, terminalAttackVisibleText)},
		},
		{
			name:      "success output",
			malicious: entry{kind: entryTool, tool: "bash", status: toolOK, output: terminalAttack},
			clean:     entry{kind: entryTool, tool: "bash", status: toolOK, output: terminalAttackVisibleText},
		},
		{
			name:      "success diff",
			malicious: entry{kind: entryTool, tool: "edit", status: toolOK, diff: "+" + terminalAttack},
			clean:     entry{kind: entryTool, tool: "edit", status: toolOK, diff: "+" + terminalAttackVisibleText},
		},
		{
			name:      "failure",
			malicious: entry{kind: entryTool, tool: "bash", status: toolFailed, err: terminalAttack},
			clean:     entry{kind: entryTool, tool: "bash", status: toolFailed, err: terminalAttackVisibleText},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, want := test.malicious.render(80), test.clean.render(80); got != want {
				t.Fatalf("render with terminal controls = %q, want sanitized render %q", got, want)
			}
		})
	}
}

func TestOpenFileViewer_RemovesUntrustedTerminalControlsBeforeHighlighting(t *testing.T) {
	maliciousText := "before\x1b]52;c;YXR0YWNr\a\x1b[2J\x1b[31mowned\x1b[0mafter"
	malicious := openFileViewer("example.go", []byte("package main // "+maliciousText+"\n"))
	clean := openFileViewer("example.go", []byte("package main // "+terminalAttackVisibleText+"\n"))

	if got, want := malicious.render(200, 2), clean.render(200, 2); got != want {
		t.Fatalf("viewer with terminal controls = %q, want sanitized render %q", got, want)
	}
}

func TestAuxiliaryViews_RemoveUntrustedTerminalControlsBeforeStyling(t *testing.T) {
	t.Run("viewer path", func(t *testing.T) {
		malicious := openFileViewer(terminalAttack+".go", []byte("package main\n"))
		clean := openFileViewer(terminalAttackVisibleText+".go", []byte("package main\n"))
		if got, want := malicious.header(200, 2), clean.header(200, 2); got != want {
			t.Fatalf("header with terminal controls = %q, want sanitized header %q", got, want)
		}
	})

	t.Run("completion menu", func(t *testing.T) {
		malicious := Model{menuItems: []menuItem{{label: terminalAttack, description: terminalAttack}}}
		clean := Model{menuItems: []menuItem{{label: terminalAttackVisibleText, description: terminalAttackVisibleText}}}
		if got, want := malicious.menuView(), clean.menuView(); got != want {
			t.Fatalf("menu with terminal controls = %q, want sanitized menu %q", got, want)
		}
	})

	t.Run("file tree", func(t *testing.T) {
		malicious := Model{tree: newFileTree([]string{terminalAttack + ".go"})}
		clean := Model{tree: newFileTree([]string{terminalAttackVisibleText + ".go"})}
		if got, want := malicious.treeView(), clean.treeView(); got != want {
			t.Fatalf("tree with terminal controls = %q, want sanitized tree %q", got, want)
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
