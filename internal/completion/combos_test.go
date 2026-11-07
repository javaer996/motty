package completion

import (
	"strings"
	"testing"
)

func TestCombosContinueTheTypedLineWithoutRewritingIt(t *testing.T) {
	cases := []struct {
		line       string
		wantInsert string
	}{
		{"git st", "atus -sb"},
		{"git commit", " -am \"\""},
		{"docker compose up", " -d"},
		{"kubectl get pods", " -o wide"},
		{"grep -r", "n \"\" ."},
	}
	for _, test := range cases {
		candidates := Combos(test.line, maxCombos)
		if len(candidates) == 0 {
			t.Fatalf("%q produced no combination", test.line)
		}
		if candidates[0].Insert != test.wantInsert {
			t.Fatalf("%q first insert = %q, want %q", test.line, candidates[0].Insert, test.wantInsert)
		}
		for _, candidate := range candidates {
			if candidate.Kind != KindCombo {
				t.Fatalf("%q candidate kind = %v", test.line, candidate.Kind)
			}
			if !strings.HasPrefix(candidate.Text, test.line) {
				t.Fatalf("%q candidate %q does not continue the line", test.line, candidate.Text)
			}
			if test.line+candidate.Insert != candidate.Text {
				t.Fatalf("%q + %q != %q", test.line, candidate.Insert, candidate.Text)
			}
			if candidate.FromCatalog {
				t.Fatalf("%q candidate marked as catalog text", test.line)
			}
		}
	}
}

func TestCombosStayOutOfTheWay(t *testing.T) {
	if got := Combos("", maxCombos); got != nil {
		t.Fatalf("an empty line produced %d combinations", len(got))
	}
	// A line that is exactly a combination has nothing left to insert.
	if got := Combos("git status -sb", maxCombos); len(got) != 0 {
		t.Fatalf("a complete line produced %d combinations", len(got))
	}
	// A line the catalog does not know still gets combinations that extend it.
	if got := Combos("zzz", maxCombos); len(got) != 0 {
		t.Fatalf("an unknown command produced %d combinations", len(got))
	}
	// The limit is honoured and never exceeds the package bound.
	if got := Combos("git ", 2); len(got) != 2 {
		t.Fatalf("limit 2 produced %d combinations", len(got))
	}
	if got := Combos("git ", 1000); len(got) > maxCombos {
		t.Fatalf("the bound was not applied: %d", len(got))
	}
}
