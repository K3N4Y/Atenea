package session

import "testing"

func TestIsExtensionEventKind_RecognizesReservedNamespaces(t *testing.T) {
	for _, kind := range []EventKind{"x-acme-progress", "ext.acme.progress"} {
		if !IsExtensionEventKind(kind) {
			t.Errorf("IsExtensionEventKind(%q) = false, want true", kind)
		}
	}
	for _, kind := range []EventKind{"Text.Delta", "extension.progress", "ext.", "x-"} {
		if IsExtensionEventKind(kind) {
			t.Errorf("IsExtensionEventKind(%q) = true, want false", kind)
		}
	}
}
