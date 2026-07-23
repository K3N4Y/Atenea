package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"atenea/internal/llm"
	"atenea/internal/wailsprovider"
	"atenea/internal/workspacegit"
)

type GitChange = workspacegit.Change
type GitStatus = workspacegit.Status

const commitSystemPrompt = "Genera un mensaje de commit conciso (una linea, estilo conventional commits si aplica) para el diff staged que te paso el usuario. Responde SOLO con el mensaje, sin comillas, sin explicaciones."
const maxDiffRunes = 12000

func commitMessageFromProvider(p llm.Provider, model, diff string) string {
	ctx, cancel := context.WithTimeout(context.Background(), auxTurnTimeout)
	defer cancel()
	out, err := p.Stream(ctx, llm.Request{
		Model:    model,
		System:   commitSystemPrompt,
		Messages: []llm.Message{{Role: "user", Text: diff}},
	})
	if err != nil {
		return ""
	}
	var b strings.Builder
	for ev := range out {
		if ev.Kind == llm.TextDelta {
			b.WriteString(ev.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func (a *App) workspaceGit() *workspacegit.Repository {
	return workspacegit.Open(a.workspaceRoot())
}

func (a *App) GitStatus() (GitStatus, error)        { return a.workspaceGit().Status() }
func (a *App) FileDiff(path string) (string, error) { return a.workspaceGit().FileDiff(path) }
func (a *App) InitRepo() error                      { return a.workspaceGit().Init() }
func (a *App) Commit(message string) error          { return a.workspaceGit().Commit(message) }

func (a *App) GenerateCommitMessage() (string, error) {
	diff, err := a.workspaceGit().StagedDiff()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("no hay cambios staged para generar el mensaje")
	}
	if runes := []rune(diff); len(runes) > maxDiffRunes {
		diff = string(runes[:maxDiffRunes])
	}
	return commitMessageFromProvider(a.currentProvider(), wailsprovider.ResolveModel(os.Getenv), diff), nil
}
