package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/K3N4Y/atenea/internal/permission"
	"github.com/K3N4Y/atenea/internal/tool"
)

// TestResolvePermissionMode_DenyIsTheDefaultAndNeedsNothing: the safe end of the
// dial is what an invocation that says nothing gets, and it is complete on its own
// — a mode that needed a second flag to be safe would be a mode nobody reaches.
func TestResolvePermissionMode_DenyIsTheDefaultAndNeedsNothing(t *testing.T) {
	mode, err := resolvePermissionMode(modeDeny, "")
	if err != nil {
		t.Fatalf("resolvePermissionMode(deny) = %v", err)
	}
	if mode.warning != "" {
		t.Errorf("deny warns: %q — a mode that refuses things has nothing to warn about", mode.warning)
	}
	catalog := shippedLikeCatalog()
	policy := mode.policy(catalog)
	if got := policy.Decide("s1", tool.Call{Name: "bash"}); got != permission.Deny {
		t.Errorf("deny mode decided %v for bash, want Deny", got)
	}
	if got := policy.Decide("s1", tool.Call{Name: "read"}); got != permission.Allow {
		t.Errorf("deny mode decided %v for read, want Allow", got)
	}
}

// TestResolvePermissionMode_AllowlistWithoutEffectsIsRefused is the rule that keeps
// the three modes honest. A mode that silently behaved like another would make a CI
// configuration that reads as permissive behave as strict, with nothing anywhere
// saying so.
func TestResolvePermissionMode_AllowlistWithoutEffectsIsRefused(t *testing.T) {
	for _, effects := range []string{"", "  ", ",", " , "} {
		_, err := resolvePermissionMode(modeAllowlist, effects)
		if err == nil {
			t.Fatalf("resolvePermissionMode(allowlist, %q) was accepted; it would be deny under another name", effects)
		}
		if !strings.Contains(err.Error(), "--allow-effects") {
			t.Errorf("err = %v, want it to name the flag that fixes it", err)
		}
	}
}

// TestResolvePermissionMode_AllowEffectsOutsideAllowlistIsRefused: the same rule
// from the other end. A flag that would be silently ignored is a flag that lies
// about what the run allowed.
func TestResolvePermissionMode_AllowEffectsOutsideAllowlistIsRefused(t *testing.T) {
	for _, name := range []string{modeDeny, modeAuto} {
		if _, err := resolvePermissionMode(name, "writes-files"); err == nil {
			t.Errorf("resolvePermissionMode(%s, writes-files) was accepted", name)
		}
	}
}

func TestResolvePermissionMode_AllowlistParsesTheBudget(t *testing.T) {
	mode, err := resolvePermissionMode(modeAllowlist, "writes-files, reaches-network")
	if err != nil {
		t.Fatalf("resolvePermissionMode: %v", err)
	}
	policy := mode.policy(shippedLikeCatalog())
	cases := []struct {
		name string
		want permission.Decision
	}{
		{"write", permission.Allow},
		{"web_fetch", permission.Allow},
		{"read", permission.Allow},
		{"bash", permission.Deny},
	}
	for _, tc := range cases {
		if got := policy.Decide("s1", tool.Call{Name: tc.name}); got != tc.want {
			t.Errorf("Decide(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolvePermissionMode_UnknownEffectListsTheKnownOnes(t *testing.T) {
	_, err := resolvePermissionMode(modeAllowlist, "writes-files,deletes-the-internet")
	if err == nil {
		t.Fatal("an unknown effect was accepted, silently narrowing the budget")
	}
	for _, name := range effectNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("err = %v, want it to list %q", err, name)
		}
	}
}

// TestResolvePermissionMode_AutoWarnsAboutWhatItDoes: the dangerous mode carries
// its risk with it, so a run cannot be in it without a person being told.
func TestResolvePermissionMode_AutoWarnsAboutWhatItDoes(t *testing.T) {
	mode, err := resolvePermissionMode(modeAuto, "")
	if err != nil {
		t.Fatalf("resolvePermissionMode(auto) = %v", err)
	}
	if mode.warning == "" {
		t.Fatal("auto carries no warning")
	}
	if !strings.Contains(mode.warning, "unattended") {
		t.Errorf("warning = %q, want it to say the calls run unattended", mode.warning)
	}
	if got := mode.policy(nil).Decide("s1", tool.Call{Name: "anything"}); got != permission.Allow {
		t.Errorf("auto decided %v, want Allow even with no catalog", got)
	}
}

// TestResolvePermissionMode_UnknownModeIsRefusedExactly: nothing about a default,
// an empty value or an abbreviation may resolve to auto. Only typing the word does.
func TestResolvePermissionMode_UnknownModeIsRefusedExactly(t *testing.T) {
	for _, name := range []string{"", "a", "au", "AUTO", "Auto", "yes", "allow", "all"} {
		mode, err := resolvePermissionMode(name, "")
		if err == nil {
			t.Fatalf("resolvePermissionMode(%q) resolved to %q instead of failing", name, mode.name)
		}
		for _, valid := range permissionModes {
			if !strings.Contains(err.Error(), valid) {
				t.Errorf("err for %q = %v, want it to list %q", name, err, valid)
			}
		}
	}
}

// TestEffectNames_AreTheVocabularyTheToolsDeclareIn: the flag's values are derived
// from tool.Effects rather than restated here, so a flag added to the vocabulary is
// spellable on the commit that defines it and cannot be misspelled in this package.
func TestEffectNames_AreTheVocabularyTheToolsDeclareIn(t *testing.T) {
	names := effectNames()
	for _, flag := range []tool.Effects{tool.WritesFiles, tool.RunsCommands, tool.ReachesNetwork} {
		if !slices.Contains(names, flag.String()) {
			t.Errorf("effectNames() = %v, missing %q", names, flag.String())
		}
	}
	if slices.Contains(names, unknownEffect) {
		t.Errorf("effectNames() = %v, want no placeholder for the bits this build does not know", names)
	}
	for _, name := range names {
		flag, ok := effectNamed(name)
		if !ok || flag.String() != name {
			t.Errorf("effectNamed(%q) = %v, %v — the two directions disagree", name, flag, ok)
		}
	}
}

// TestDenials_CountsWhatEachAssemblyRefuses: the count belongs to the run and the
// classification belongs to the assembly, so a caller that builds more than once
// keeps one total.
func TestDenials_CountsWhatEachAssemblyRefuses(t *testing.T) {
	refused := &denials{}
	factory := refused.over(func(tool.Catalog) permission.Policy {
		return permission.NewUnattendedPolicy(shippedLikeCatalog(), tool.NoEffects)
	})

	first := factory(nil)
	second := factory(nil)
	first.Decide("s1", tool.Call{Name: "bash"})
	second.Decide("s1", tool.Call{Name: "write"})
	second.Decide("s1", tool.Call{Name: "read"})

	if got := refused.count(); got != 2 {
		t.Fatalf("count() = %d, want 2 refusals across the two assemblies", got)
	}
}

// shippedLikeCatalog mirrors how atenea's own tools declare themselves, so the
// modes are asserted against the real vocabulary rather than an invented one.
func shippedLikeCatalog() tool.Catalog {
	return fakeCatalog{
		"bash":       declaringTool{name: "bash", effects: tool.RunsCommands},
		"write":      declaringTool{name: "write", effects: tool.WritesFiles},
		"edit":       declaringTool{name: "edit", effects: tool.WritesFiles},
		"web_fetch":  declaringTool{name: "web_fetch", effects: tool.ReachesNetwork},
		"read":       declaringTool{name: "read", effects: tool.NoEffects},
		"glob":       declaringTool{name: "glob", effects: tool.NoEffects},
		"todo_write": declaringTool{name: "todo_write", effects: tool.NoEffects},
	}
}
