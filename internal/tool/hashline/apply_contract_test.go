package hashline

import "testing"

// Provenance: oh-my-pi boundary-repair.test.ts and landing-shift.test.ts @ 5af71dc.
func TestApplyBoundaryAndLandingPorts(t *testing.T) {
	parseApply := func(src, diff string) ApplyResult {
		t.Helper()
		p, e := ParsePatch(diff)
		if e != nil {
			t.Fatal(e)
		}
		r, e := ApplyEdits(SplitLines(src), p.Sections[0].Edits)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := parseApply("function f() {\n    if (x) {\n        a();\n    }\n    b();\n}\n", "PUT >3:\n+    c();")
	if r.Text != "function f() {\n    if (x) {\n        a();\n    }\n    c();\n    b();\n}" || len(r.Warnings) != 1 {
		t.Fatalf("%q %#v", r.Text, r.Warnings)
	}
	r = parseApply("it('a', () => {\n\tsetup();\n\trun();\n});\nafter();", "PUT 2-3:\n+\tsetup2();\n+\trun2();\n+});")
	if r.Text != "it('a', () => {\n\tsetup2();\n\trun2();\n});\nafter();" || len(r.Warnings) == 0 {
		t.Fatalf("%q %#v", r.Text, r.Warnings)
	}
	r = parseApply("function f() {\nold();\n}", "PUT 2:\n+function f() {\n+fresh();\n+}")
	if r.Text != "function f() {\nfresh();\n}" {
		t.Fatalf("%q", r.Text)
	}
}
