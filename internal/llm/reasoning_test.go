package llm

import "testing"

func TestReasoningSelectionValidatesAndPreservesPreference(t *testing.T) {
	selection := &ReasoningSelection{}
	if got := selection.Get(); got != nil {
		t.Fatalf("default preference = %#v, want nil", got)
	}
	for _, effort := range []ReasoningEffort{ReasoningEffortXHigh, ReasoningEffortMax} {
		if err := selection.Set(effort); err != nil {
			t.Fatalf("Set(%q): %v", effort, err)
		}
		if got := selection.Get(); got == nil || got.Effort != effort {
			t.Fatalf("preference = %#v, want %q", got, effort)
		}
	}
	if err := selection.Set(ReasoningEffort("turbo")); err == nil {
		t.Fatal("unsupported effort was accepted")
	}
	if got := selection.Effort(); got != ReasoningEffortMax {
		t.Fatalf("failed update changed effort to %q", got)
	}
	if err := selection.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := selection.Get(); got != nil {
		t.Fatalf("reset preference = %#v, want nil", got)
	}
}
