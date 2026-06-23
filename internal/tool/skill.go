package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"atenea/internal/skill"
)

// SkillTool implementa el disclosure progresivo de skills al estilo opencode: el
// system prompt solo lleva los metadatos (name + description), y esta tool carga
// el cuerpo completo del SKILL.md BAJO DEMANDA cuando el modelo la invoca con el
// nombre de una skill. El catalogo se descubre una vez al ensamblar (skill.Discover)
// y se inyecta; es de solo lectura, asi que Execute es seguro concurrentemente.
type SkillTool struct {
	catalog map[string]skill.Info
}

// NewSkillTool indexa las skills por nombre. Si dos comparten nombre gana la
// ultima (config del programa, no input del modelo).
func NewSkillTool(list []skill.Info) *SkillTool {
	m := make(map[string]skill.Info, len(list))
	for _, s := range list {
		m[s.Name] = s
	}
	return &SkillTool{catalog: m}
}

func (*SkillTool) Name() string { return "skill" }

//go:embed skill.txt
var skillDescription string

func (*SkillTool) Description() string { return skillDescription }

func (*SkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"El nombre de una skill listada en el system prompt."}},"required":["name"]}`)
}

// Execute parsea {name}, busca la skill en el catalogo y devuelve su cuerpo
// envuelto en <skill_content> junto con el directorio base y una muestra de los
// archivos del directorio (excluye SKILL.md, tope skillFilesLimit). Un nombre
// fuera del catalogo es un error de tool accionable que enumera las disponibles,
// para que el modelo reintente con un nombre valido.
func (st *SkillTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("skill: input invalido: %w", err)
	}

	info, ok := st.catalog[in.Name]
	if !ok {
		return Result{}, fmt.Errorf("skill %q no encontrada. Disponibles: %s", in.Name, st.available())
	}

	dir := filepath.Dir(info.Location)
	var b strings.Builder
	b.WriteString(`<skill_content name="` + info.Name + `">` + "\n")
	b.WriteString("# Skill: " + info.Name + "\n\n")
	b.WriteString(strings.TrimSpace(info.Content) + "\n\n")
	b.WriteString("Directorio base de la skill: " + dir + "\n")
	b.WriteString("Las rutas relativas de la skill (p.ej. scripts/, reference/) son relativas a ese directorio.\n")
	b.WriteString("Nota: la lista de archivos es una muestra.\n\n")
	b.WriteString("<skill_files>\n")
	b.WriteString(listSkillFiles(dir))
	b.WriteString("\n</skill_files>\n")
	b.WriteString("</skill_content>")
	return Result{Output: b.String()}, nil
}

// available devuelve los nombres del catalogo ordenados, para el mensaje de error
// cuando el modelo pide una skill inexistente.
func (st *SkillTool) available() string {
	names := make([]string, 0, len(st.catalog))
	for n := range st.catalog {
		names = append(names, n)
	}
	if len(names) == 0 {
		return "ninguna"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

const skillFilesLimit = 10

// listSkillFiles recolecta hasta skillFilesLimit archivos bajo dir (recursivo,
// excluye el propio SKILL.md) como lineas <file>ruta</file>. El orden es lexico
// (determinista). Un error de lectura se ignora: la lista es informativa, no
// debe tumbar la carga de la skill.
func listSkillFiles(dir string) string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || d.Name() == "SKILL.md" {
			return nil
		}
		files = append(files, "<file>"+path+"</file>")
		if len(files) >= skillFilesLimit {
			return fs.SkipAll
		}
		return nil
	})
	return strings.Join(files, "\n")
}
