package tool

import (
	"context"
	"encoding/json"
	"strings"

	contract "github.com/K3N4Y/atenea/agentcore/tool"
	"github.com/K3N4Y/atenea/internal/tool/hashline"
)

// LSPDiagnosticsMiddleware appends language-server diagnostics after successful
// write and edit calls. Diagnostics are advisory: an unavailable or unsupported
// server must not turn a committed filesystem mutation into a failed tool call.
func LSPDiagnosticsMiddleware(lsp *LSPTool) Middleware {
	return func(next SettleFunc) SettleFunc {
		return func(ctx context.Context, call Call) (Result, error) {
			result, err := next(ctx, call)
			if err != nil || lsp == nil {
				return result, err
			}
			paths := make([]string, 0, len(result.Files)+1)
			seen := make(map[string]struct{}, len(result.Files)+1)
			for i := range result.Files {
				file := &result.Files[i]
				if file.Path == "" || !file.Committed || file.Operation == "delete" {
					continue
				}
				if _, exists := seen[file.Path]; !exists {
					seen[file.Path] = struct{}{}
					paths = append(paths, file.Path)
				}
			}
			if len(paths) == 0 {
				if path := mutatedPath(call); path != "" {
					paths = append(paths, path)
				}
			}
			for _, path := range paths {
				diagnostics, diagnosticErr := lsp.DiagnosticsForPath(ctx, path)
				if diagnosticErr != nil || diagnostics == "" || diagnostics == "No diagnostics." {
					continue
				}
				for i := range result.Files {
					if result.Files[i].Path == path {
						result.Files[i].Diagnostics = append(result.Files[i].Diagnostics, contract.Diagnostic{Message: diagnostics})
					}
				}
				if result.Output != "" && !strings.HasSuffix(result.Output, "\n") {
					result.Output += "\n"
				}
				result.Output += "\nLSP diagnostics:\n" + diagnostics
			}
			return result, nil
		}
	}
}

func mutatedPath(call Call) string {
	switch call.Name {
	case "write":
		var input struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(call.Input, &input) == nil {
			return input.Path
		}
	case "edit":
		var input struct {
			Input string `json:"input"`
		}
		if json.Unmarshal(call.Input, &input) != nil {
			return ""
		}
		patch, err := hashline.ParsePatch(input.Input)
		if err == nil && len(patch.Sections) == 1 {
			return patch.Sections[0].Path
		}
	}
	return ""
}
