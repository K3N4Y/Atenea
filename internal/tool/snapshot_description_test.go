package tool

import (
	"strings"
	"testing"
)

func TestSnapshotProducingDescriptionsAccountForNonEditableOutput(t *testing.T) {
	for _, tc := range []struct {
		name        string
		description string
	}{
		{name: "read", description: (&ReadTool{}).Description()},
		{name: "write", description: (&WriteTool{}).Description()},
		{name: "grep", description: (&GrepTool{}).Description()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range []string{"sin header", "colision", "sesion nueva", "read"} {
				if !strings.Contains(tc.description, want) {
					t.Fatalf("description must explain non-editable snapshot output; missing %q", want)
				}
			}
		})
	}
}
