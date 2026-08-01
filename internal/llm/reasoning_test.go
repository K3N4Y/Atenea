package llm

import "testing"

func TestReasoningSelectionValidatesAndPreservesPreference(t *testing.T) {
	selection := &ReasoningSelection{}
	if got := selection.Get(); got != nil {
		t.Fatalf("default preference = %#v, want nil", got)
	}
	if err := selection.Set(ReasoningEffortHigh); err != nil {
		t.Fatal(err)
	}
	if got := selection.Get(); got == nil || got.Effort != ReasoningEffortHigh {
		t.Fatalf("preference = %#v, want high", got)
	}
	if err := selection.Set(ReasoningEffort("turbo")); err == nil {
		t.Fatal("unsupported effort was accepted")
	}
	if got := selection.Effort(); got != ReasoningEffortHigh {
		t.Fatalf("failed update changed effort to %q", got)
	}
	if err := selection.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := selection.Get(); got != nil {
		t.Fatalf("reset preference = %#v, want nil", got)
	}
}
