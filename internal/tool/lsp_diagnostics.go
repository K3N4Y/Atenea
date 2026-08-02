package tool

import (
	"context"
	"encoding/json"
	"strings"

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
			path := mutatedPath(call)
			if path == "" {
				return result, nil
			}
			diagnostics, diagnosticErr := lsp.DiagnosticsForPath(ctx, path)
			if diagnosticErr != nil || diagnostics == "" || diagnostics == "No diagnostics." {
				return result, nil
			}
			if result.Output != "" && !strings.HasSuffix(result.Output, "\n") {
				result.Output += "\n"
			}
			result.Output += "\nLSP diagnostics:\n" + diagnostics
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
			Patch string `json:"patch"`
		}
		if json.Unmarshal(call.Input, &input) != nil {
			return ""
		}
		patch, err := hashline.ParsePatch(input.Patch)
		if err == nil && len(patch.Sections) == 1 {
			return patch.Sections[0].Path
		}
	}
	return ""
}
