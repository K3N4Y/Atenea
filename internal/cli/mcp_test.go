package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/mcpclient"
)

// writeWorkspaceConfig declares servers in <root>/.mcp.json, the file a project
// commits and a person edits by hand.
func writeWorkspaceConfig(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, mcpclient.ConfigFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMCPAdd_WritesTheConfigListAndTheHostsRead: the round trip is the contract.
// A config `add` wrote has to be one `LoadConfig` accepts, because that is the
// function every host starts a server from — a CLI that wrote a file only the CLI
// could read would be a second config path with one spelling.
func TestMCPAdd_WritesTheConfigListAndTheHostsRead(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()

	added := invoke(t, "mcp", "add", "--cwd", root, "--env", "GITHUB_TOKEN=secret",
		"--sensitivity", "reaches-network", "--allow-tool", "search", "github", "--", "npx", "github-mcp")
	if added.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", added.code, ExitOK, added.stderr)
	}
	if strings.Contains(added.stderr, "secret") {
		t.Errorf("the confirmation echoed an env value:\n%s", added.stderr)
	}
	if !strings.Contains(added.stderr, "GITHUB_TOKEN") {
		t.Errorf("the confirmation does not name the env keys it wrote:\n%s", added.stderr)
	}

	configs, err := mcpclient.LoadConfig(root)
	if err != nil {
		t.Fatalf("the config `add` wrote is not one LoadConfig accepts: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("LoadConfig = %+v, want the one declared server", configs)
	}
	got := configs[0]
	if got.Name != "github" || got.Command != "npx" || len(got.Args) != 1 || got.Args[0] != "github-mcp" {
		t.Errorf("the declared server did not round-trip: %+v", got)
	}
	if got.Env["GITHUB_TOKEN"] != "secret" {
		t.Errorf("the env did not round-trip: %+v", got.Env)
	}
	if got.Sensitivity != "reaches-network" || len(got.AllowedTools) != 1 || got.AllowedTools[0] != "search" {
		t.Errorf("the permission declaration did not round-trip: %+v", got)
	}

	listed := invoke(t, "mcp", "list", "--cwd", root)
	if listed.code != ExitOK {
		t.Fatalf("list exit code = %d, want %d\nstderr: %s", listed.code, ExitOK, listed.stderr)
	}
	if !strings.Contains(listed.stdout, "github") || !strings.Contains(listed.stdout, "npx github-mcp") {
		t.Errorf("list does not show what add declared:\n%s", listed.stdout)
	}
	if !strings.Contains(listed.stdout, string(mcpclient.ScopeGlobal)) {
		t.Errorf("list does not say where the server came from:\n%s", listed.stdout)
	}
	if strings.Contains(listed.stdout, "secret") {
		t.Errorf("the listing printed an env value:\n%s", listed.stdout)
	}
}

// TestMCPList_ShowsTheWorkspaceWinnerAndTheShadowedGlobal: precedence is the
// thing this listing exists to make visible. The host connects one of the two and
// nothing else in the product ever shows the other.
func TestMCPList_ShowsTheWorkspaceWinnerAndTheShadowedGlobal(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	if code := invoke(t, "mcp", "add", "--cwd", root, "shared", "--", "global-command").code; code != ExitOK {
		t.Fatalf("add exit code = %d", code)
	}
	writeWorkspaceConfig(t, root, `{"mcpServers": {"shared": {"command": "workspace-command"}}}`)

	got := invoke(t, "mcp", "list", "--cwd", root)
	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	lines := strings.Split(strings.TrimSpace(got.stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and both declarations, got:\n%s", got.stdout)
	}
	if !strings.Contains(lines[1], "workspace") || !strings.Contains(lines[1], "workspace-command") {
		t.Errorf("the winner must come first: %q", lines[1])
	}
	if !strings.Contains(lines[2], "shadowed") || !strings.Contains(lines[2], "global-command") {
		t.Errorf("the shadowed declaration must be listed and marked: %q", lines[2])
	}
}

// TestMCPList_EmptyPrintsNothingOnStdout: a listing is piped, so "there is
// nothing" has to be zero bytes of data rather than a sentence a consumer would
// have to recognize. The help a person needs goes to the other stream.
func TestMCPList_EmptyPrintsNothingOnStdout(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()

	got := invoke(t, "mcp", "list", "--cwd", root)
	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
	if !strings.Contains(got.stderr, filepath.Join(root, mcpclient.ConfigFile)) {
		t.Errorf("stderr does not name the file to declare a server in:\n%s", got.stderr)
	}
}

// TestMCPList_MalformedConfigNamesTheFile: an unreadable config is reported, not
// skipped. A server that silently does not exist is the failure this command is
// for.
func TestMCPList_MalformedConfigNamesTheFile(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	writeWorkspaceConfig(t, root, `{not json`)

	got := invoke(t, "mcp", "list", "--cwd", root)
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d", got.code, ExitFailure)
	}
	if !strings.Contains(got.stderr, mcpclient.ConfigFile) {
		t.Errorf("stderr does not name the unparseable file:\n%s", got.stderr)
	}
}

// TestMCPRemove_WorkspaceDeclaredPointsAtTheFileAndChangesNothing is the case the
// desktop already handles, held to the same promise here: the global config is
// the only file this command writes, so a workspace-declared server is refused
// with the path of the file that does declare it.
//
// Mutation-checked: dropping the workspace lookup in
// mcpclient.RemoveGlobalConfig (returning `false, nil` for a name the global
// config does not have) turns the exit code into 1 with "no MCP server named" and
// this test fails on the message; deleting the error's path argument fails it on
// the path.
func TestMCPRemove_WorkspaceDeclaredPointsAtTheFileAndChangesNothing(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	writeWorkspaceConfig(t, root, `{"mcpServers": {"local": {"command": "npx", "args": ["local-mcp"]}}}`)

	got := invoke(t, "mcp", "remove", "--cwd", root, "local")
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitFailure, got.stderr)
	}
	if !strings.Contains(got.stderr, filepath.Join(root, mcpclient.ConfigFile)) {
		t.Errorf("the error must name the file to edit:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "edit that file") {
		t.Errorf("the error must say what to do about it:\n%s", got.stderr)
	}
	configs, err := mcpclient.LoadConfig(root)
	if err != nil || len(configs) != 1 || configs[0].Name != "local" {
		t.Fatalf("the workspace declaration must survive a refused removal: %+v, %v", configs, err)
	}
}

// TestMCPRemove_DeletesFromTheGlobalConfig: the verb's actual job, asserted
// against the file rather than against the message.
func TestMCPRemove_DeletesFromTheGlobalConfig(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	if code := invoke(t, "mcp", "add", "--cwd", root, "github", "--", "npx", "github-mcp").code; code != ExitOK {
		t.Fatalf("add exit code = %d", code)
	}

	got := invoke(t, "mcp", "remove", "--cwd", root, "github")
	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	configs, err := mcpclient.LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Fatalf("the server survived the removal: %+v", configs)
	}
}

// TestMCPRemove_ShadowedGlobalSaysWhatIsLeft: removing the global half of a
// shadowed name leaves the server the caller was looking at exactly where it was.
// Reporting only the removal would read as "done" while `mcp list` still shows
// the name.
func TestMCPRemove_ShadowedGlobalSaysWhatIsLeft(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	if code := invoke(t, "mcp", "add", "--cwd", root, "shared", "--", "global-command").code; code != ExitOK {
		t.Fatalf("add exit code = %d", code)
	}
	writeWorkspaceConfig(t, root, `{"mcpServers": {"shared": {"command": "workspace-command"}}}`)

	got := invoke(t, "mcp", "remove", "--cwd", root, "shared")
	if got.code != ExitOK {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitOK, got.stderr)
	}
	if !strings.Contains(got.stderr, "still declared") ||
		!strings.Contains(got.stderr, filepath.Join(root, mcpclient.ConfigFile)) {
		t.Errorf("removing the shadowed half must say the other one is still there:\n%s", got.stderr)
	}
}

// TestMCPRemove_UnknownNameFails: the desktop can treat this as nothing to do,
// because its list may be stale. A person who typed the name meant it.
func TestMCPRemove_UnknownNameFails(t *testing.T) {
	isolateConfig(t)
	got := invoke(t, "mcp", "remove", "--cwd", t.TempDir(), "nope")
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d", got.code, ExitFailure)
	}
	if !strings.Contains(got.stderr, `"nope"`) {
		t.Errorf("stderr does not name what was not found:\n%s", got.stderr)
	}
}

// TestMCPAdd_RefusesToOverwriteADeclaredName: the config is a map, so an upsert
// would succeed and take an env carrying a token with it. Refusing is undone in
// one command; overwriting is not undone at all.
func TestMCPAdd_RefusesToOverwriteADeclaredName(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	if code := invoke(t, "mcp", "add", "--cwd", root, "github", "--", "npx", "github-mcp").code; code != ExitOK {
		t.Fatalf("the first add must succeed")
	}

	got := invoke(t, "mcp", "add", "--cwd", root, "github", "--", "npx", "something-else")
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitFailure, got.stderr)
	}
	configs, err := mcpclient.LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Args[0] != "github-mcp" {
		t.Fatalf("the refused add changed the config: %+v", configs)
	}
}

// TestMCPAdd_RefusesANameTheWorkspaceDeclares: the global entry would be written
// and then immediately overridden, so the add would report success and change
// nothing a host reads.
func TestMCPAdd_RefusesANameTheWorkspaceDeclares(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	writeWorkspaceConfig(t, root, `{"mcpServers": {"local": {"command": "npx"}}}`)

	got := invoke(t, "mcp", "add", "--cwd", root, "local", "--", "npx", "other")
	if got.code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\nstderr: %s", got.code, ExitFailure, got.stderr)
	}
	if !strings.Contains(got.stderr, "overrides") {
		t.Errorf("the refusal must explain why adding it globally would do nothing:\n%s", got.stderr)
	}
	if _, err := os.Stat(mcpclient.GlobalConfigPath()); !os.IsNotExist(err) {
		t.Errorf("a refused add must not create the global config, stat err = %v", err)
	}
}

// TestMCPAdd_UsageErrors: an incomplete or malformed invocation is exit 2 and
// nothing is written. The flag-after-the-name case is the one worth having: the
// stdlib stops parsing at NAME, so `--env` would otherwise be taken as the
// server's command and the failure would surface at connect time, far from the
// typo.
func TestMCPAdd_UsageErrors(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"no name", []string{"mcp", "add", "--cwd", root}, "expected a name"},
		{"no command", []string{"mcp", "add", "--cwd", root, "github"}, "expected a command"},
		{"flag after the name", []string{"mcp", "add", "--cwd", root, "github", "--env", "A=B", "npx"}, "flags go before the name"},
		{"env without a value", []string{"mcp", "add", "--cwd", root, "--env", "NOPE", "github", "--", "npx"}, "KEY=VALUE"},
		{"env twice", []string{"mcp", "add", "--cwd", root, "--env", "A=1", "--env", "A=2", "github", "--", "npx"}, "twice"},
		{"empty command", []string{"mcp", "add", "--cwd", root, "github", "--", "   "}, "command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := invoke(t, tc.args...)
			// The empty command is caught by the same validation Connect enforces, one
			// layer down, so it is a failure rather than a usage error; everything else
			// here is the invocation being wrong.
			if got.code != ExitUsage && got.code != ExitFailure {
				t.Fatalf("exit code = %d, want a non-zero one\nstderr: %s", got.code, got.stderr)
			}
			if !strings.Contains(got.stderr, tc.says) {
				t.Errorf("stderr does not explain the mistake (%q):\n%s", tc.says, got.stderr)
			}
			if _, err := os.Stat(mcpclient.GlobalConfigPath()); !os.IsNotExist(err) {
				t.Errorf("a refused add wrote the global config, stat err = %v", err)
			}
		})
	}
}

// TestMCPAdd_AcceptsTheCommandWithoutADoubleDash: the separator is what a user's
// hands type and it separates nothing here, since the stdlib already stopped
// parsing at the name. Both spellings therefore have to mean the same thing.
func TestMCPAdd_AcceptsTheCommandWithoutADoubleDash(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()

	if code := invoke(t, "mcp", "add", "--cwd", root, "github", "npx", "github-mcp").code; code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	configs, err := mcpclient.LoadConfig(root)
	if err != nil || len(configs) != 1 {
		t.Fatalf("LoadConfig = %+v, %v", configs, err)
	}
	if configs[0].Command != "npx" || configs[0].Args[0] != "github-mcp" {
		t.Errorf("the command was not read the same way without `--`: %+v", configs[0])
	}
}

// TestMCP_BadCwdIsAUsageError: --cwd means the same thing in every subcommand,
// including how it fails. Everything downstream of a bad root degrades quietly
// instead — an empty listing of a workspace that does not exist reads exactly
// like a workspace with no servers.
func TestMCP_BadCwdIsAUsageError(t *testing.T) {
	isolateConfig(t)
	missing := filepath.Join(t.TempDir(), "nope")
	for _, args := range [][]string{
		{"mcp", "list", "--cwd", missing},
		{"mcp", "remove", "--cwd", missing, "x"},
		{"skill", "list", "--cwd", missing},
	} {
		got := invoke(t, args...)
		if got.code != ExitUsage {
			t.Errorf("%v exit code = %d, want %d", args, got.code, ExitUsage)
		}
	}
}
