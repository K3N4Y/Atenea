package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Def es la definicion de un subagente: sus metadatos (Name, Description, Model,
// Tools) mas su Prompt (el cuerpo Markdown sin el frontmatter) y Location (ruta
// absoluta del archivo). Location no lo fija Parse (lo pone Discover), igual que
// skill.Info.
type Def struct {
	Name        string
	Description string
	Tools       []string
	Model       string
	Prompt      string
	Location    string
}

// Parse separa el frontmatter de la definicion de un subagente de su cuerpo. El
// frontmatter es el bloque delimitado por "---" al inicio del archivo; de el se
// leen name, description, model (una por linea, "clave: valor") y tools (una sola
// linea con valores separados por comas). El resto es Prompt. Un archivo sin
// frontmatter, o sin name, es un error: un subagente sin nombre no es
// referenciable. Location no lo fija Parse (lo pone Discover).
func Parse(raw []byte) (Def, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Def{}, fmt.Errorf("agent: falta el frontmatter (--- al inicio)")
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Def{}, fmt.Errorf("agent: frontmatter sin cierre (---)")
	}
	front := rest[:end]
	body := strings.TrimPrefix(rest[end+len("\n---"):], "\n")

	var def Def
	for _, line := range strings.Split(front, "\n") {
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			def.Name = val
		case "description":
			def.Description = val
		case "model":
			def.Model = val
		case "tools":
			for _, t := range strings.Split(val, ",") {
				if t = strings.TrimSpace(t); t != "" {
					def.Tools = append(def.Tools, t)
				}
			}
		}
	}
	if def.Name == "" {
		return Def{}, fmt.Errorf("agent: frontmatter sin 'name'")
	}
	def.Prompt = body
	return def, nil
}

// Discover escanea recursivamente cada agentsDir en busca de definiciones de
// subagente y devuelve una Def por cada archivo *.md que parsea (con Location
// apuntando al archivo). A diferencia de skill.Discover, el archivo no tiene un
// nombre fijo (SKILL.md): cualquier *.md es candidato. Acepta varios directorios
// (p.ej. el propio .atenea/agents y el estandar .agents/agents) y los fusiona en
// orden: ante un nombre duplicado gana la PRIMERA ocurrencia, asi que un
// directorio listado antes tiene prioridad sobre los siguientes. Un directorio
// inexistente no es error (no aporta defs); un *.md que no parsea se omite, para
// que una def rota no tumbe a las demas.
func Discover(agentsDirs ...string) ([]Def, error) {
	var out []Def
	seen := make(map[string]bool)
	for _, agentsDir := range agentsDirs {
		err := filepath.WalkDir(agentsDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil // dir base ausente: no hay defs aqui
				}
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			def, perr := Parse(raw)
			if perr != nil {
				return nil // def ilegible: se omite sin romper el resto
			}
			if seen[def.Name] {
				return nil // duplicado: gana la primera ocurrencia
			}
			seen[def.Name] = true
			def.Location = path
			out = append(out, def)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
