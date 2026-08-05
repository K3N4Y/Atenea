package hashline

import (
	"strings"
	"testing"
)

// Provenance: oh-my-pi packages/hashline/test/boundary-repair.test.ts @
// 5af71dc9cf132538e072806424f71f43f734d9ae. The cases below map every
// language-core behavior group; stale-snapshot recovery remains a facade concern.
func TestBoundaryRepairMatrix(t *testing.T) {
	apply := func(src, patch string) (ApplyResult, error) {
		t.Helper()
		p, err := ParsePatch(patch)
		if err != nil {
			return ApplyResult{}, err
		}
		return ApplyEdits(SplitLines(src), p.Sections[0].Edits)
	}
	tests := []struct{ name, src, patch, want, warning string }{
		{"uniform indent", "    if (x) {\n      a();\n    } else {\n      b();\n    }", "PUT 2-4:\n+  a();\n+} else {\n+  c();", "    if (x) {\n      a();\n    } else {\n      c();\n    }", "Auto-indented"},
		{"two sided echo", "function f() {\nold();\n}", "PUT 2:\n+function f() {\n+fresh();\n+}", "function f() {\nfresh();\n}", "boundary echo"},
		{"trailing structural echo", "it(() => {\n\told();\n});\nafter();", "PUT 2:\n+\tnew();\n+});", "it(() => {\n\tnew();\n});\nafter();", "delimiter-balance"},
		{"leading structural echo", "class C {\n\tm(\n\t\ta: string,\n\t): X {\n\t\treturn x;\n\t}\n}", "PUT 3-4:\n+\tm(\n+\t\ta: number,\n+\t): X {", "class C {\n\tm(\n\t\ta: number,\n\t): X {\n\t\treturn x;\n\t}\n}", "delimiter-balance"},
		{"one sided trailing keeper", "function f() {\n  a();\n  b();\n  const out = [];\n}", "PUT 2-3:\n+  aa();\n+  bb();\n+  const out = [];", "function f() {\n  aa();\n  bb();\n  const out = [];\n}", "boundary echo"},
		{"one sided leading keeper", "setup();\na();\nb();\nc();", "PUT 3-4:\n+a();\n+B();\n+C();", "setup();\na();\nB();\nC();", "boundary echo"},
		{"jsx closer", "const v = (\n  <section>\n    <Old />\n  </section>\n);", "PUT 3:\n+    <New />\n+  </section>", "const v = (\n  <section>\n    <New />\n  </section>\n);", "boundary echo"},
		{"nested jsx stays", "const v = (\n<section>\nold\n</section>\n);", "PUT 3:\n+<section>\n+new\n+</section>", "const v = (\n<section>\n<section>\nnew\n</section>\n</section>\n);", ""},
		{"missing closer spare", "const o = {\n\ta: 1,\n};", "PUT 3:\n+\tb: 2,", "const o = {\n\ta: 1,\n\tb: 2,\n};", "structural closing"},
		{"restated closer not spared", "class C {\n\tok();\n}\n}", "PUT 1-4:\n+class C {\n+\tok();\n+}", "class C {\n\tok();\n}", ""},
		{"strings ignored", "const a = \"}\";\nconst b = \"x\";", "PUT 2:\n+const b = \"}}}\";", "const a = \"}\";\nconst b = \"}}}\";", ""},
		{"intentional neutral duplicate", "a();\nb();\nc();", "PUT 1:\n+a();\n+b();", "a();\nb();\nb();\nc();", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := apply(tc.src, tc.patch)
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != tc.want {
				t.Fatalf("text:\n%s\nwant:\n%s", got.Text, tc.want)
			}
			joined := strings.Join(got.Warnings, "\n")
			if tc.warning != "" && !strings.Contains(joined, tc.warning) {
				t.Fatalf("warnings=%#v", got.Warnings)
			}
			if tc.warning == "" && len(got.Warnings) != 0 {
				t.Fatalf("unexpected warnings=%#v", got.Warnings)
			}
		})
	}
}

func TestBoundaryRepairConservativeFailures(t *testing.T) {
	for _, tc := range []struct{ name, src, patch string }{
		{"leading ambiguity", "{\n    handle();\n    if (x)\n        return;\n    done();\n}", "PUT 3-4:\n+    handle();\n+    if (y)"},
		{"trailing ambiguity", "a();\nb();\nc();\nkeep();", "PUT 2-3:\n+B();\n+keep();"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := ParsePatch(tc.patch)
			if e != nil {
				t.Fatal(e)
			}
			_, e = ApplyEdits(SplitLines(tc.src), p.Sections[0].Edits)
			if e == nil || !strings.Contains(e.Error(), "rejected") {
				t.Fatalf("error=%v", e)
			}
		})
	}
}

func TestBoundaryRepairBOFAndLanguageLightAccounting(t *testing.T) {
	p, _ := ParsePatch("PUT <1:\n+if (a) {\nPUT 1-2:\n+\tnew();")
	r, err := ApplyEdits([]string{"\told();", "}"}, p.Sections[0].Edits)
	if err != nil || r.Text != "if (a) {\n\tnew();\n}" {
		t.Fatalf("%q %v %#v", r.Text, err, r.Warnings)
	}
	p, _ = ParsePatch("PUT 1:\n+const x = `unterminated\nPUT 5-6:\n+\ta: 2")
	r, err = ApplyEdits([]string{"const x = `", "prefix", "`;", "const o = {", "\ta: 1", "};"}, p.Sections[0].Edits)
	if err != nil || !strings.HasSuffix(r.Text, "\ta: 2\n};") {
		t.Fatalf("%q %v", r.Text, err)
	}
}
