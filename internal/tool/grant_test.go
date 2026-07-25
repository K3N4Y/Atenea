package tool

import (
	"testing"

	"github.com/K3N4Y/atenea/agentcore/permission"
)

func bashGrantInput(command string) []byte {
	return []byte(`{"command":` + quoteJSON(command) + `}`)
}

// quoteJSON is a minimal JSON string quoter for the fixtures: the commands under
// test contain quotes, backslashes and newlines on purpose.
func quoteJSON(value string) string {
	out := []byte{'"'}
	for _, r := range value {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, string(r)...)
		}
	}
	return string(append(out, '"'))
}

// TestBashTool_GrantRulePrefix pins what a session grant on a bash call covers:
// the verb plus its subcommand when the second token is one, the verb alone
// otherwise.
func TestBashTool_GrantRulePrefix(t *testing.T) {
	bash := NewBashTool(t.TempDir())
	cases := []struct {
		command string
		want    string
	}{
		{"go test ./...", "go test"},
		{"git status", "git status"},
		{"npm run build", "npm run"},
		{"go   test   ./...", "go test"},
		{`go test -run "TestFoo" ./internal/permission`, "go test"},
		{"ls -la", "ls"},
		{"gofmt -l .", "gofmt"},
		{"cat notes.md", "cat"},
		{"./scripts/build.sh --release", "./scripts/build.sh"},
		{"sudo rm -rf /tmp/x", "sudo rm"},
		// A bare second token is indistinguishable from a subcommand, so it is
		// kept: the grant ends up narrower than the user may have expected, never
		// wider, and the panel shows the exact prefix it will cover.
		{"echo uno", "echo uno"},
		{"echo -n uno", "echo"},
	}
	for _, tc := range cases {
		rule, ok := bash.GrantRule(Call{Name: "bash", Input: bashGrantInput(tc.command)})
		if !ok {
			t.Errorf("GrantRule(%q) = not grantable, want prefix %q", tc.command, tc.want)
			continue
		}
		if rule.Tool != "bash" || rule.Prefix != tc.want {
			t.Errorf("GrantRule(%q) = %+v, want prefix %q", tc.command, rule, tc.want)
		}
	}
}

// TestBashTool_GrantRuleRefusesCommandsAPrefixCannotDescribe is the security
// core: a command that chains, redirects, substitutes or escapes can run
// something other than what it appears to, so no prefix may stand for it.
func TestBashTool_GrantRuleRefusesCommandsAPrefixCannotDescribe(t *testing.T) {
	bash := NewBashTool(t.TempDir())
	inputs := [][]byte{
		bashGrantInput("echo one && rm -rf /"),
		bashGrantInput("echo one; ls"),
		bashGrantInput("ls | wc -l"),
		bashGrantInput("echo $(whoami)"),
		bashGrantInput("echo `whoami`"),
		bashGrantInput("cat < notes.md"),
		bashGrantInput("echo one > out.txt"),
		bashGrantInput("echo one & "),
		bashGrantInput(`find . -name x -exec rm {} \;`),
		bashGrantInput("go test ./...\nrm -rf /"),
		bashGrantInput("FOO=1 go test ./..."),
		bashGrantInput(""),
		bashGrantInput("   "),
		[]byte(`{}`),
		[]byte(`not json`),
	}
	for _, input := range inputs {
		if rule, ok := bash.GrantRule(Call{Name: "bash", Input: input}); ok {
			t.Errorf("GrantRule(%s) = %+v, want not grantable", input, rule)
		}
	}
}

// TestFilesystemTools_GrantTheWholeTool: write and edit are granted as a whole —
// the decision the user makes is "stop asking me to touch files" — and the rule
// names the tool that derived it, whatever the input was.
func TestFilesystemTools_GrantTheWholeTool(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		tool  permission.Grantable
		input string
		want  permission.Rule
	}{
		{NewWriteTool(root, nil), `{"path":"a.txt","content":"x"}`, permission.Rule{Tool: "write"}},
		{NewEditTool(root, nil, nil), `{"patch":"[a.txt#ab]\n"}`, permission.Rule{Tool: "edit"}},
	}
	for _, tc := range cases {
		rule, ok := tc.tool.GrantRule(Call{Name: tc.tool.Name(), Input: []byte(tc.input)})
		if !ok || rule != tc.want {
			t.Errorf("%s.GrantRule() = %+v, %v; want %+v, true", tc.tool.Name(), rule, ok, tc.want)
		}
	}
}

// TestNonGrantableTools_KeepAsking: a tool whose input cannot be reduced to a
// subject the panel could honestly show must not implement Grantable, so every
// call asks again. web_fetch is the case that matters — a blanket grant on
// outbound network cannot be summarized — and the read-only tools never reach the
// question at all.
func TestNonGrantableTools_KeepAsking(t *testing.T) {
	root := t.TempDir()
	tools := []Tool{
		NewWebFetchTool(nil),
		NewReadTool(root, nil),
		NewGlobTool(root),
		TodoWriteTool{},
	}
	for _, subject := range tools {
		if _, ok := subject.(permission.Grantable); ok {
			t.Errorf("%s implements Grantable: a grant on it would claim more than the panel showed", subject.Name())
		}
	}
}
