package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"atenea/internal/command"
	"atenea/internal/session"
)

// writeSkillMD escribe un SKILL.md minimo (frontmatter name+description) en
// <base>/<name>/SKILL.md, creando el arbol. Helper de los tests de descubrimiento.
func writeSkillMD(t *testing.T, base, name, description string) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\ncuerpo\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// commandByName busca un comando por nombre en el resultado de ListCommands.
func commandByName(cmds []command.Command, name string) (command.Command, bool) {
	for _, c := range cmds {
		if c.Name == name {
			return c, true
		}
	}
	return command.Command{}, false
}

// recordingInbox registra cada Admit para inspeccionar que texto llega al inbox
// (el prompt ya expandido cuando es un slash-command). Delega en un MemoryInbox
// real para cumplir la interface; el runner usa su propia referencia de inbox, asi
// que lo registrado no se drena bajo el test.
type recordingInbox struct {
	*session.MemoryInbox
	mu       sync.Mutex
	admitted []session.Prompt
}

func (r *recordingInbox) Admit(ctx context.Context, sessionID string, p session.Prompt, d session.Delivery) error {
	r.mu.Lock()
	r.admitted = append(r.admitted, p)
	r.mu.Unlock()
	return r.MemoryInbox.Admit(ctx, sessionID, p, d)
}

func (r *recordingInbox) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.admitted) == 0 {
		return ""
	}
	return r.admitted[len(r.admitted)-1].Text
}

// TestApp_ListCommandsReturnsRegisteredCommands: el binding devuelve los comandos
// del registro, ordenados por nombre, para el slash-menu del composer.
func TestApp_ListCommandsReturnsRegisteredCommands(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	app.commands = command.New([]command.Command{
		{Name: "commit", Description: "arma el commit"},
		{Name: "abc", Description: "algo"},
	})

	cmds, err := app.ListCommands()
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	if len(cmds) != 2 || cmds[0].Name != "abc" || cmds[1].Name != "commit" {
		t.Fatalf("ListCommands = %+v, want [abc commit]", cmds)
	}
}

// TestApp_ListCommandsDiscoversSkillsFromClaudeDir: un slash-command se deriva de
// las skills descubiertas, incluyendo el estandar <root>/.claude/skills (donde este
// repo guarda su skill). Verificacion end-to-end: el menu del composer ve la skill.
func TestApp_ListCommandsDiscoversSkillsFromClaudeDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: demo\ndescription: skill de prueba\n---\ninstrucciones demo\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	// newAppWithStore ancla el root en os.Getwd(): situarse en el tempdir hace que
	// el descubrimiento halle la skill demo bajo .claude/skills. HOME a un tempdir
	// vacio aisla el test de las skills globales reales del home.
	t.Setenv("HOME", t.TempDir())
	t.Chdir(root)

	app := newApp(demoProvider(), func(string, ...interface{}) {})
	cmds, err := app.ListCommands()
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	var found bool
	for _, c := range cmds {
		if c.Name == "demo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListCommands no incluye la skill de .claude/skills; got %+v", cmds)
	}
}

// TestSkillDirs_ProjectBeforeGlobalDeduped: skillDirs lista primero las rutas del
// proyecto (root) y luego las globales (home), en el orden .atenea/.agents/.claude,
// para que una skill del proyecto override a una global homonima. Rutas identicas
// (root == home) se deduplican.
func TestSkillDirs_ProjectBeforeGlobalDeduped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := skillDirs("/proj")
	want := []string{
		filepath.Join("/proj", ".atenea", "skills"),
		filepath.Join("/proj", ".agents", "skills"),
		filepath.Join("/proj", ".claude", "skills"),
		filepath.Join(home, ".atenea", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skillDirs orden = %v,\n want %v", got, want)
	}
	// root == home: las rutas coinciden, deben deduplicarse a las 3 del home.
	if d := skillDirs(home); len(d) != 3 {
		t.Fatalf("root==home debe deduplicar a 3 dirs, got %v", d)
	}
}

// TestApp_ListCommandsDiscoversGlobalSkills: las skills globales del home (p.ej.
// ~/.agents/skills, la convencion estandar entre agentes) tambien se vuelven
// slash-commands, no solo las del proyecto.
func TestApp_ListCommandsDiscoversGlobalSkills(t *testing.T) {
	home := t.TempDir()
	writeSkillMD(t, filepath.Join(home, ".agents", "skills"), "global-demo", "skill global")
	t.Setenv("HOME", home)
	// Proyecto vacio (sin skills propias): solo debe aparecer la global.
	t.Chdir(t.TempDir())

	app := newApp(demoProvider(), func(string, ...interface{}) {})
	cmds, err := app.ListCommands()
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	if _, ok := commandByName(cmds, "global-demo"); !ok {
		t.Fatalf("ListCommands no incluye la skill global ~/.agents/skills; got %+v", cmds)
	}
}

// TestApp_ProjectSkillOverridesGlobalSkill: ante una skill con el mismo nombre en
// el proyecto y en el home, gana la del proyecto (mas local). Se verifica por la
// description del comando resultante.
func TestApp_ProjectSkillOverridesGlobalSkill(t *testing.T) {
	home := t.TempDir()
	writeSkillMD(t, filepath.Join(home, ".agents", "skills"), "dup", "global")
	t.Setenv("HOME", home)
	root := t.TempDir()
	writeSkillMD(t, filepath.Join(root, ".claude", "skills"), "dup", "project")
	t.Chdir(root)

	app := newApp(demoProvider(), func(string, ...interface{}) {})
	cmds, err := app.ListCommands()
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	c, ok := commandByName(cmds, "dup")
	if !ok {
		t.Fatalf("ListCommands no incluye 'dup'; got %+v", cmds)
	}
	if c.Description != "project" {
		t.Fatalf("la skill del proyecto debe ganar; description = %q, want \"project\"", c.Description)
	}
}

// TestApp_SendPromptExpandsSlashCommand: SendPrompt expande un "/name args" de un
// comando registrado antes de admitirlo (el agente recibe el prompt expandido).
func TestApp_SendPromptExpandsSlashCommand(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	rec := &recordingInbox{MemoryInbox: session.NewMemoryInbox()}
	app.inbox = rec
	app.commands = command.New([]command.Command{
		{Name: "foo", Template: "Hace foo.\n\n$ARGUMENTS"},
	})

	if err := app.SendPrompt("s1", "/foo hola mundo"); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	app.wait()

	if got := rec.last(); got != "Hace foo.\n\nhola mundo" {
		t.Fatalf("admitido = %q, want expandido", got)
	}
}

// TestApp_SendPromptLeavesNormalTextUnchanged: un prompt normal (sin "/") o un
// comando desconocido se admite verbatim, sin transformar.
func TestApp_SendPromptLeavesNormalTextUnchanged(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	rec := &recordingInbox{MemoryInbox: session.NewMemoryInbox()}
	app.inbox = rec
	app.commands = command.New([]command.Command{{Name: "foo", Template: "x"}})

	for _, text := range []string{"hola foo", "/desconocido algo"} {
		if err := app.SendPrompt("s1", text); err != nil {
			t.Fatalf("SendPrompt(%q): %v", text, err)
		}
		app.wait()
		if got := rec.last(); got != text {
			t.Fatalf("admitido = %q, want %q (verbatim)", got, text)
		}
	}
}

// TestApp_SendPlanPromptExpandsSlashCommand: en modo plan tambien se expanden los
// slash-commands, para que el comportamiento sea consistente con el envio normal.
func TestApp_SendPlanPromptExpandsSlashCommand(t *testing.T) {
	app := newApp(demoProvider(), func(string, ...interface{}) {})
	rec := &recordingInbox{MemoryInbox: session.NewMemoryInbox()}
	app.inbox = rec
	app.commands = command.New([]command.Command{
		{Name: "foo", Template: "Hace foo.\n\n$ARGUMENTS"},
	})

	if err := app.SendPlanPrompt("s1", "/foo contexto"); err != nil {
		t.Fatalf("SendPlanPrompt: %v", err)
	}
	app.wait()

	if got := rec.last(); got != "Hace foo.\n\ncontexto" {
		t.Fatalf("admitido = %q, want expandido", got)
	}
}
