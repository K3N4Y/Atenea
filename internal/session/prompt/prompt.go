package prompt

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed anthropic.txt
var anthropicPrompt string

//go:embed default.txt
var defaultPrompt string

//go:embed local.txt
var localPrompt string

//go:embed plan.txt
var planInstructions string

// Env contains runtime data available to prompt renderers.
type Env struct {
	WorkingDir   string
	WorktreeRoot string
	IsGitRepo    bool
	Platform     string
	Date         string
}

// Context contains all inputs available to a PromptSection renderer.
type Context struct {
	Base             string
	Instructions     string
	Skills           string
	ModeInstructions string
	Env              Env
}

// PromptSection is one independently rendered part of a system prompt. Sections
// with equal orders retain their input order, and empty results are omitted.
type PromptSection struct {
	Order  int
	Name   string
	Render func(Context) string
}

const (
	SectionOrderBase = iota * 100
	SectionOrderInstructions
	SectionOrderSkills
	SectionOrderMode
	SectionOrderEnvironment
)

// Select chooses the embedded base prompt for a model ID.
func Select(modelID string) string {
	if strings.Contains(strings.ToLower(modelID), "claude") {
		return anthropicPrompt
	}
	return defaultPrompt
}

// Build assembles the model prompt, keeping stable sections before runtime data.
func Build(modelID string, env Env, instructions, skills string) string {
	return assemble(Select(modelID), env, instructions, skills, "")
}

// BuildLocal assembles the dedicated tool-calling prompt for local endpoints.
func BuildLocal(env Env, instructions, skills string) string {
	return assemble(localPrompt, env, instructions, skills, "")
}

func assemble(base string, env Env, instructions, skills, modeInstructions string) string {
	return Assemble(Context{
		Base:             base,
		Instructions:     instructions,
		Skills:           skills,
		ModeInstructions: modeInstructions,
		Env:              env,
	}, StandardSections())
}

// StandardSections returns the built-in system-prompt sections. Callers may append
// extension sections and pass the resulting slice to Assemble.
func StandardSections() []PromptSection {
	return []PromptSection{
		{Order: SectionOrderBase, Name: "base", Render: func(ctx Context) string { return ctx.Base }},
		{Order: SectionOrderInstructions, Name: "instructions", Render: func(ctx Context) string { return ctx.Instructions }},
		{Order: SectionOrderSkills, Name: "skills", Render: func(ctx Context) string { return ctx.Skills }},
		{Order: SectionOrderMode, Name: "mode", Render: func(ctx Context) string { return ctx.ModeInstructions }},
		{Order: SectionOrderEnvironment, Name: "environment", Render: func(ctx Context) string { return renderEnv(ctx.Env) }},
	}
}

// Assemble renders sections in stable Order sequence and joins non-empty results.
func Assemble(ctx Context, sections []PromptSection) string {
	ordered := append([]PromptSection(nil), sections...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })

	parts := make([]string, 0, len(ordered))
	for _, section := range ordered {
		if section.Render == nil {
			continue
		}
		if rendered := section.Render(ctx); rendered != "" {
			parts = append(parts, rendered)
		}
	}
	return strings.Join(parts, "\n\n")
}

// BuildPlan adds the stable plan-mode contract before runtime data.
func BuildPlan(modelID string, env Env, instructions, skills string) string {
	return assemble(Select(modelID), env, instructions, skills, planInstructions)
}

// BuildLocalPlan combines the local base prompt with the plan-mode contract.
func BuildLocalPlan(env Env, instructions, skills string) string {
	return assemble(localPrompt, env, instructions, skills, planInstructions)
}

func renderEnv(env Env) string {
	return fmt.Sprintf("Current working directory: %s", env.WorkingDir)
}

// LoadInstructions loads project instructions from root through dir and formats
// them as pi-compatible project context. One file is loaded per directory, with
// AGENTS.md taking precedence over CLAUDE.md.
func LoadInstructions(dir, root string) (string, error) {
	candidates := []string{"AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}
	current := dir
	var files []string
	for {
		for _, name := range candidates {
			path := filepath.Join(current, name)
			if _, err := os.Stat(path); err == nil {
				files = append(files, path)
				break
			}
		}
		if current == root {
			break
		}
		current = filepath.Dir(current)
	}
	if len(files) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("<project_context>\n\nProject-specific instructions and guidelines:\n")
	for i := len(files) - 1; i >= 0; i-- {
		content, err := os.ReadFile(files[i])
		if err != nil {
			return "", err
		}
		b.WriteString("\n<project_instructions path=\"")
		b.WriteString(files[i])
		b.WriteString("\">\n")
		b.Write(content)
		if len(content) == 0 || content[len(content)-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteString("</project_instructions>\n")
	}
	b.WriteString("\n</project_context>")
	return b.String(), nil
}
