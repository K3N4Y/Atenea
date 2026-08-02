// Package agent owns the shared headless turn lifecycle and discovers
// workspace subagent definitions.
//
// Un subagente se define en un archivo *.md: frontmatter (name, description,
// model, tools) mas un cuerpo Markdown que es el Prompt. Parse separa el
// frontmatter del cuerpo (ver Parse) y Discover escanea los directorios en busca
// de archivos *.md, parseando uno por cada def (ver Discover). El cuerpo (Prompt)
// es el system prompt que el subagente usara cuando se le invoque. Espeja
// internal/skill.
//
// Builtins returns Atenea's canonical definitions from the manifests packaged
// from the repository's agents/*.md directory. Catalog lets discovered user
// definitions override a packaged definition by name.
package agent
