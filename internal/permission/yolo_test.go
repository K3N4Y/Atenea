package permission

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/K3N4Y/atenea/internal/tool"
)

type yoloFixedPolicy Decision

func (p yoloFixedPolicy) Decide(string, tool.Call) Decision { return Decision(p) }

func yoloBashCall(t *testing.T, command string) tool.Call {
	t.Helper()
	input, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return tool.Call{Name: "bash", Input: input}
}

func TestYoloPolicyMonotonicAndProcessLocal(t *testing.T) {
	mode := NewYoloMode(true)
	policy := NewYoloPolicy(yoloFixedPolicy(Ask), mode, t.TempDir(), t.TempDir())
	if got := policy.Decide("main", tool.Call{Name: "write"}); got != Allow {
		t.Fatalf("enabled = %v", got)
	}
	mode.Set(false)
	if got := policy.Decide("child", tool.Call{Name: "write"}); got != Ask {
		t.Fatalf("disabled = %v", got)
	}
	if got := NewYoloPolicy(yoloFixedPolicy(Deny), NewYoloMode(true), "/tmp", "/home/user").Decide("s", tool.Call{Name: "write"}); got != Deny {
		t.Fatalf("deny became %v", got)
	}
	unauthorized := NewYoloMode(false)
	if unauthorized.Set(true) || unauthorized.Enabled() {
		t.Fatal("ordinary launch activated YOLO")
	}
}

func TestYoloPolicyBlocksRecognizedRecursiveRMOfRootOrHome(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	policy := NewYoloPolicy(yoloFixedPolicy(Ask), NewYoloMode(true), root, home)
	blocked := []string{
		"rm -rf /",
		"echo ok && rm -R -- /./",
		"sudo rm --recursive $HOME",
		`rm -rf "$HOME"`,
		"rm -rf ~",
		`rm -rf ~""`,
		`rm -rf "` + home + `"`,
		"env -i rm -rf /",
		"sudo -n rm -rf /",
		"FOO=bar rm -rf /",
		"command -p rm -rf /",
		"env -i FOO=bar sudo -n -- command -- rm -rf /",
		"echo ok; env --ignore-environment FOO=bar rm --recursive ${HOME}",
		"rm -rf />/tmp/out",
		"(rm -rf /)",
		"{ rm -rf /; }",
		"true || rm -rf /",
		"if true; then rm -rf /; fi",
		"echo $(rm -rf /)",
	}

	for _, command := range blocked {
		t.Run(command, func(t *testing.T) {
			if got := policy.Decide("s", yoloBashCall(t, command)); got != Deny {
				t.Fatalf("got %v", got)
			}
		})
	}

	allowed := []string{
		"rm file",
		"rm -rf " + filepath.Join(root, "build"),
		"find / -delete",
		"echo rm -rf /",
		`echo "safe; rm -rf /"`,
		`rm -rf '$HOME'`,
		`rm -rf \$HOME`,
		`rm -rf "\$HOME"`,
		`rm -rf '~'`,
		`rm -rf \~`,
		`rm -rf ""~`,
		`rm -rf $HO""ME`,
		`rm -rf '${HOME}' && echo safe`,
		`sudo -u rm echo -rf /`,
		`env -u rm echo -rf /`,
		`command -v rm -rf /`,
		`command -V rm -rf /`,
		`command --help rm -rf /`,
		`env --help rm -rf /`,
		`env --version rm -rf /`,
		`sudo --help rm -rf /`,
		`f() { rm -rf /; }`,
		`echo rm -rf / >/tmp/out`,
	}
	for _, command := range allowed {
		t.Run(command, func(t *testing.T) {
			if got := policy.Decide("s", yoloBashCall(t, command)); got != Allow {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestYoloPolicyBlocksProtectedRootWithDynamicOperand(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	policy := NewYoloPolicy(yoloFixedPolicy(Ask), NewYoloMode(true), root, home)
	if got := policy.Decide("s", yoloBashCall(t, `rm -rf / "$UNTRUSTED"`)); got != Deny {
		t.Fatalf("got %v, want Deny", got)
	}
}

func TestYoloBreakerOverridesAnExistingAllow(t *testing.T) {
	policy := NewYoloPolicy(yoloFixedPolicy(Allow), NewYoloMode(true), "/work", "/home/user")
	if got := policy.Decide("s", yoloBashCall(t, "rm -rf /")); got != Deny {
		t.Fatalf("breaker = %v, want Deny", got)
	}
}
