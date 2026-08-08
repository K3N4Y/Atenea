package tool

import (
	"strings"
	"testing"
)

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
