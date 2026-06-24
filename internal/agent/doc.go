// Package agent descubre y parsea definiciones de subagente del workspace.
//
// Un subagente se define en un archivo *.md: frontmatter (name, description,
// model, tools) mas un cuerpo Markdown que es el Prompt. Parse separa el
// frontmatter del cuerpo (ver Parse) y Discover escanea los directorios en busca
// de archivos *.md, parseando uno por cada def (ver Discover). El cuerpo (Prompt)
// es el system prompt que el subagente usara cuando se le invoque. Espeja
// internal/skill.
//
// El paquete tambien provee definiciones de subagente canonicas via Builtins()
// (built-in, sin archivo), donde cada una declara su scoping de tools (p.ej. el
// agente read-only solo lista read/grep/glob).
package agent
