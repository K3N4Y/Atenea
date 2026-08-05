package tool

import (
	"strings"
	"testing"
)

func TestSnapshotProducingDescriptionsAccountForNonEditableOutput(t *testing.T) {
	for _, tc := range []struct {
		name        string
		description string
		wants       []string
	}{
		{name: "read", description: (&ReadTool{}).Description(), wants: []string{"without a header", "collision", "new session", "read"}},
		{name: "write", description: (&WriteTool{}).Description(), wants: []string{"without a header", "collision", "new session", "read"}},
		{name: "grep", description: (&GrepTool{}).Description(), wants: []string{"without a header", "collision", "new session", "read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range tc.wants {
				if !strings.Contains(tc.description, want) {
					t.Fatalf("description must explain non-editable snapshot output; missing %q", want)
				}
			}
		})
	}
}

func TestReadDescriptionMatchesSupportedOhMyPiConventions(t *testing.T) {
	description := (&ReadTool{}).Description()
	for _, want := range []string{"SHOULD parallelize independent reads", ":50", ":50-200", "[path#HASH]", "NEVER fabricate the hash"} {
		if !strings.Contains(description, want) {
			t.Errorf("read description must contain %q", want)
		}
	}
}

func TestWriteDescriptionMatchesSupportedOhMyPiConventions(t *testing.T) {
	description := (&WriteTool{}).Description()
	for _, want := range []string{"Creates a new file", "You SHOULD use the edit tool", "NEVER overwrite an existing file"} {
		if !strings.Contains(description, want) {
			t.Errorf("write description must contain %q", want)
		}
	}
}

func TestReadAndWriteSchemasDescribeTheirInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema string
		want   []string
	}{
		{name: "read", schema: string((&ReadTool{}).Schema()), want: []string{`"path"`, `"minLength":1`, "inclusive 1-indexed line range"}},
		{name: "write", schema: string((&WriteTool{}).Schema()), want: []string{`"path"`, `"content"`, `"minLength":1`, "Complete file content"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range tc.want {
				if !strings.Contains(tc.schema, want) {
					t.Errorf("schema must contain %q", want)
				}
			}
		})
	}
}
