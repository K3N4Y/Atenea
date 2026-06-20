package llm

import "encoding/json"

// ToolDef es el esquema anunciable de una tool: lo que el Request lleva al
// proveedor para que el modelo sepa que herramientas puede invocar y con que
// forma de input. El registry (internal/tool) lo materializa desde sus tools
// permitidas; M5 lo pone en Request.Tools al construir el turno. Schema es el
// JSON Schema crudo del input (lo emite cada tool); el proveedor real (M10) lo
// traduce al formato de su SDK.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
}
