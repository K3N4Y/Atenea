package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"atenea/internal/command"
	"atenea/internal/session"
)

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
	// el descubrimiento halle la skill demo bajo .claude/skills.
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
