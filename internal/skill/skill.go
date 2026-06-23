package skill

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Info es una skill descubierta: sus metadatos (Name, Description) mas su Content
// (el cuerpo del SKILL.md sin el frontmatter) y Location (ruta absoluta del
// SKILL.md, para resolver el directorio base y listar sus recursos al cargarla).
type Info struct {
	Name        string
	Description string
	Location    string
	Content     string
}

// Parse separa el frontmatter de un SKILL.md de su cuerpo. El frontmatter es el
// bloque delimitado por "---" al inicio del archivo; de el se leen name y
// description (una por linea, "clave: valor"). El resto es Content. Un archivo
// sin frontmatter, o sin name, es un error: una skill sin nombre no es
// referenciable por el modelo. Location no lo fija Parse (lo pone Discover).
func Parse(raw []byte) (Info, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Info{}, fmt.Errorf("skill: falta el frontmatter (--- al inicio)")
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Info{}, fmt.Errorf("skill: frontmatter sin cierre (---)")
	}
	front := rest[:end]
	body := strings.TrimPrefix(rest[end+len("\n---"):], "\n")

	var info Info
	for _, line := range strings.Split(front, "\n") {
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			info.Name = val
		case "description":
			info.Description = val
		}
	}
	if info.Name == "" {
		return Info{}, fmt.Errorf("skill: frontmatter sin 'name'")
	}
	info.Content = body
	return info, nil
}

// Discover escanea recursivamente cada skillsDir en busca de SKILL.md y devuelve
// una Info por cada skill (con Location apuntando al SKILL.md). Acepta varios
// directorios (p.ej. el propio .atenea/skills y el estandar .agents/skills) y los
// fusiona en orden: ante un nombre duplicado gana la PRIMERA ocurrencia, asi que
// un directorio listado antes tiene prioridad sobre los siguientes. Un directorio
// inexistente no es error (no aporta skills); un SKILL.md que no parsea se omite,
// para que una skill rota no tumbe a las demas.
func Discover(skillsDirs ...string) ([]Info, error) {
	var out []Info
	seen := make(map[string]bool)
	for _, skillsDir := range skillsDirs {
		err := filepath.WalkDir(skillsDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil // dir base ausente: no hay skills aqui
				}
				return walkErr
			}
			if d.IsDir() || d.Name() != "SKILL.md" {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			info, perr := Parse(raw)
			if perr != nil {
				return nil // skill ilegible: se omite sin romper el resto
			}
			if seen[info.Name] {
				return nil // duplicado: gana la primera ocurrencia
			}
			seen[info.Name] = true
			info.Location = path
			out = append(out, info)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Format arma el bloque verbose de skills disponibles para el system prompt: un
// preambulo mas <available_skills> con name/description/location por skill,
// ordenado por nombre. Las skills sin description se filtran (no son utiles para
// que el modelo decida cuando cargarlas). Si no queda ninguna, devuelve "" para
// que el ensamblador del prompt omita el bloque por completo.
func Format(list []Info) string {
	described := make([]Info, 0, len(list))
	for _, s := range list {
		if s.Description != "" {
			described = append(described, s)
		}
	}
	if len(described) == 0 {
		return ""
	}
	sort.Slice(described, func(i, j int) bool { return described[i].Name < described[j].Name })

	var b strings.Builder
	b.WriteString("Skills provide specialized instructions and workflows for specific tasks.\n")
	b.WriteString("Use the skill tool to load a skill when a task matches its description.\n")
	b.WriteString("<available_skills>\n")
	for _, s := range described {
		b.WriteString("  <skill>\n")
		b.WriteString("    <name>" + s.Name + "</name>\n")
		b.WriteString("    <description>" + s.Description + "</description>\n")
		b.WriteString("    <location>" + s.Location + "</location>\n")
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}
