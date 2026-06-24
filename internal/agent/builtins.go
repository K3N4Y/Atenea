package agent

// Builtins devuelve las definiciones de subagente canonicas que vienen con
// atenea, sin pasar por un archivo (por eso no fijan Location). Encodean el
// scoping de tools por agente: cada built-in declara que tools puede usar.
//
//   - explore: solo lectura (read/grep/glob). Investiga sin tocar nada.
//   - general: proposito general con todas las tools (read/grep/glob/edit/
//     write/bash). Sirve de contraste full vs read-only.
//
// No incluyen "task" en sus tools: la delegacion anidada es opt-in y aqui no
// hace falta.
func Builtins() []Def {
	return []Def{
		{
			Name:        "explore",
			Description: "Explora el codigo en modo solo lectura y devuelve un informe.",
			Tools:       []string{"read", "grep", "glob"},
			Prompt:      "Eres un subagente de exploracion de solo lectura. Investiga el codigo del workspace y devuelve un informe conciso. No modificas archivos ni ejecutas comandos.",
		},
		{
			Name:        "general",
			Description: "Subagente de proposito general con acceso completo a las tools.",
			Tools:       []string{"read", "grep", "glob", "edit", "write", "bash"},
			Prompt:      "Eres un subagente de proposito general. Investiga y resuelve la tarea del workspace usando las tools disponibles y devuelve un informe conciso.",
		},
	}
}
