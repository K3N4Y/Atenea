package hashline

import (
	"strings"
	"testing"
)

// Provenance: oh-my-pi packages/hashline/test/landing-shift.test.ts @ 5af71dc.
func TestLandingShiftMatrix(t *testing.T) {
	apply := func(src, patch string) (ApplyResult, error) {
		p, e := ParsePatch(patch)
		if e != nil {
			return ApplyResult{}, e
		}
		return ApplyEdits(SplitLines(src), p.Sections[0].Edits)
	}
	file := "function f() {\n    if (x) {\n        a();\n    }\n    b();\n}"
	r, e := apply(file, "PUT >3:\n+    c();")
	if e != nil || r.Text != "function f() {\n    if (x) {\n        a();\n    }\n    c();\n    b();\n}" || len(r.Warnings) != 1 {
		t.Fatalf("%q %v %#v", r.Text, e, r.Warnings)
	}
	nested := "function f() {\n    if (x) {\n        for (y) {\n            a();\n        }\n    }\n    b();\n}"
	r, e = apply(nested, "PUT >4:\n+    c();")
	if e != nil || !strings.Contains(r.Warnings[0], "past 2 closing") {
		t.Fatalf("%q %v %#v", r.Text, e, r.Warnings)
	}
	r, e = apply(nested, "PUT >4:\n+        c();")
	if e != nil || !strings.Contains(r.Warnings[0], "past 1 closing") {
		t.Fatalf("%q %v %#v", r.Text, e, r.Warnings)
	}
	for _, tc := range []struct{ name, src, patch, want string }{
		{"same depth", file, "PUT >3:\n+        c();", "        a();\n        c();\n    }"},
		{"content barrier", "def f():\n    if x:\n        a()\n    b()", "PUT >3:\n+    c()", "        a()\n    c()\n    b()"},
		{"pure closer body", file, "PUT >3:\n+    }", "        a();\n    }\n    }"},
		{"incomparable indent", "function f() {\n\tif (x) {\n\t\ta();\n\t}\n}", "PUT >3:\n+    c();", "\t\ta();\n    c();\n\t}"},
		{"owned closer", file, "PUT >3:\n+    c();\nCUT 4", "        a();\n    c();\n    b();"},
		{"before literal", file, "PUT <4:\n+    c();", "        a();\n    c();\n    }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := apply(tc.src, tc.patch)
			if err != nil || !strings.Contains(g.Text, tc.want) || len(g.Warnings) != 0 {
				t.Fatalf("%q %v %#v", g.Text, err, g.Warnings)
			}
		})
	}
	gapped := "function f() {\n    if (x) {\n        a();\n\n    }\n}"
	r, e = apply(gapped, "PUT >3:\n+    c();")
	if e != nil || !strings.Contains(r.Warnings[0], "line 5") {
		t.Fatalf("%q %v %#v", r.Text, e, r.Warnings)
	}
}

func TestBlockLoweredInwardLanding(t *testing.T) {
	lines := SplitLines("function f() {\n    afterEach(() => {\n        destroy();\n    });\n}")
	cases := []struct {
		name          string
		start, anchor int
		body, want    string
		warning       bool
	}{
		{"inside after content", 2, 4, "        setup();", "        destroy();\n        setup();\n    });", true},
		{"sibling literal", 2, 4, "    cleanup();", "    });\n    cleanup();\n}", false},
		{"empty block", 2, 3, "        setup();", "    afterEach(() => {\n        setup();\n    });", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := lines
			if tc.name == "empty block" {
				src = SplitLines("function f() {\n    afterEach(() => {\n    });\n}")
			}
			e := Edit{Kind: Insert, Cursor: AfterAnchor, Anchor: tc.anchor, BlockStart: tc.start, Text: tc.body}
			r, err := ApplyEdits(src, []Edit{e})
			if err != nil || !strings.Contains(r.Text, tc.want) || (len(r.Warnings) > 0) != tc.warning {
				t.Fatalf("%q %v %#v", r.Text, err, r.Warnings)
			}
		})
	}
	// Plain inserts on closers remain literal when blockStart evidence is absent.
	r, err := ApplyEdits(lines, []Edit{{Kind: Insert, Cursor: AfterAnchor, Anchor: 4, Text: "        leak();"}})
	if err != nil || !strings.Contains(r.Text, "    });\n        leak();\n}") || len(r.Warnings) != 0 {
		t.Fatalf("%q %v %#v", r.Text, err, r.Warnings)
	}
}
