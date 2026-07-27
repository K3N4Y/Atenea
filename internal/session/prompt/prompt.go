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

// Env contains the runtime data rendered in the <env> section.
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
	return fmt.Sprintf("<env>\n"+
		"  Working directory: %s\n"+
		"  Workspace root folder: %s\n"+
		"  Is directory a git repo: %s\n"+
		"  Platform: %s\n"+
		"  Today's date: %s\n"+
		"</env>",
		env.WorkingDir,
		env.WorktreeRoot,
		yesNo(env.IsGitRepo),
		env.Platform,
		env.Date,
	)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// LoadInstructions searches from dir through root and returns the nearest
// AGENTS.md or CLAUDE.md, formatted with its absolute path.
func LoadInstructions(dir, root string) (string, error) {
	candidates := []string{"AGENTS.md", "CLAUDE.md"}
	current := dir
	for {
		for _, name := range candidates {
			path := filepath.Join(current, name)
			if _, err := os.Stat(path); err == nil {
				content, err := os.ReadFile(path)
				if err != nil {
					return "", err
				}
				return "Instructions from: " + path + "\n" + string(content), nil
			}
		}
		// Stop after processing root itself.
		if current == root {
			break
		}
		current = filepath.Dir(current)
	}
	return "", nil
}
