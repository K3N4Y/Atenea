package editmode

import "testing"

// Provenance: oh-my-pi@5af71dc9cf132538e072806424f71f43f734d9ae
// packages/coding-agent/test/edit-mode.test.ts (resolution precedence and model exclusions).
func TestResolvePinnedUpstreamPrecedenceAndFallback(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	tests := []struct {
		name        string
		config      Config
		environment map[string]string
		want        Mode
	}{
		{"default", Config{}, nil, Hashline},
		{"setting", Config{Setting: "patch"}, nil, Patch},
		{"environment variant", Config{Setting: "patch"}, map[string]string{"PI_EDIT_VARIANT": "apply_patch"}, ApplyPatch},
		{"model variant", Config{Model: "model", ModelVariants: map[string]Mode{"model": Replace}, Setting: "patch"}, map[string]string{"PI_EDIT_VARIANT": "apply_patch"}, Replace},
		{"excluded model fallback", Config{Model: "vendor/kimi-k2", Setting: "hashline"}, nil, Replace},
		{"strict excluded model", Config{Model: "vendor/kimi-k2", Setting: "hashline"}, map[string]string{"PI_STRICT_EDIT_MODE": "true"}, Hashline},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Resolve(test.config, env(test.environment))
			if err != nil || got != test.want {
				t.Fatalf("Resolve() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestResolveFuzzyDefaultsAndValidation(t *testing.T) {
	fuzzy, threshold, err := ResolveFuzzy(Config{}, func(string) string { return "" })
	if err != nil || fuzzy || threshold != .95 {
		t.Fatalf("defaults = %v, %v, %v", fuzzy, threshold, err)
	}
	if _, _, err := ResolveFuzzy(Config{Threshold: 1.1}, func(string) string { return "" }); err == nil {
		t.Fatal("invalid threshold accepted")
	}
}
