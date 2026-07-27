package prompt

import "testing"

func TestAssemble_OrdersExtensionSectionsWithoutMutatingInput(t *testing.T) {
	ctx := Context{Base: "base", Env: Env{}}
	sections := []PromptSection{
		{Order: SectionOrderEnvironment, Name: "last", Render: func(Context) string { return "last" }},
		{Order: SectionOrderSkills + 1, Name: "extension-a", Render: func(Context) string { return "extension-a" }},
		{Order: SectionOrderBase, Name: "first", Render: func(Context) string { return "first" }},
		{Order: SectionOrderSkills + 1, Name: "extension-b", Render: func(Context) string { return "extension-b" }},
	}
	wantInput := append([]PromptSection(nil), sections...)

	got := Assemble(ctx, sections)

	if want := "first\n\nextension-a\n\nextension-b\n\nlast"; got != want {
		t.Fatalf("Assemble() = %q, want %q", got, want)
	}
	for i := range sections {
		if sections[i].Order != wantInput[i].Order || sections[i].Name != wantInput[i].Name {
			t.Fatal("Assemble mutated the caller's section order")
		}
	}
}

func TestAssemble_OmitsEmptyAndNilSections(t *testing.T) {
	sections := []PromptSection{
		{Order: 1, Name: "content", Render: func(Context) string { return "content" }},
		{Order: 2, Name: "empty", Render: func(Context) string { return "" }},
		{Order: 3, Name: "disabled"},
	}

	if got := Assemble(Context{}, sections); got != "content" {
		t.Fatalf("Assemble() = %q, want content without extra separators", got)
	}
}

func TestStandardSections_PreserveBuildVariants(t *testing.T) {
	env := localEnv()
	instructions := "repo instructions"
	skills := "available skills"
	tests := map[string]struct {
		got  string
		want string
	}{
		"model": {
			got:  Build("claude", env, instructions, skills),
			want: anthropicPrompt + "\n\n" + instructions + "\n\n" + skills + "\n\n" + renderEnv(env),
		},
		"model plan": {
			got:  BuildPlan("claude", env, instructions, skills),
			want: anthropicPrompt + "\n\n" + instructions + "\n\n" + skills + "\n\n" + planInstructions + "\n\n" + renderEnv(env),
		},
		"local": {
			got:  BuildLocal(env, instructions, skills),
			want: localPrompt + "\n\n" + instructions + "\n\n" + skills + "\n\n" + renderEnv(env),
		},
		"local plan": {
			got:  BuildLocalPlan(env, instructions, skills),
			want: localPrompt + "\n\n" + instructions + "\n\n" + skills + "\n\n" + planInstructions + "\n\n" + renderEnv(env),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("build output changed\nwant:\n%s\ngot:\n%s", test.want, test.got)
			}
		})
	}
}
