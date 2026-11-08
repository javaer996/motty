package completion

import "testing"

// Every command name this build can suggest has a Chinese description; the
// chips under a prefix like "ls" are the main place descriptions are read, so
// none of them may fall back to English.
func TestEveryCommandDescriptionIsTranslated(t *testing.T) {
	catalog, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	essentials := Essentials()
	roots := append(append([]*Node{}, catalog.Roots()...), essentials.Roots()...)
	checked := 0
	for _, root := range roots {
		if root.Desc == "" {
			continue
		}
		checked++
		entry, ok := translations[root.Desc]
		if !ok {
			entry, ok = catalogTranslations[root.Desc]
		}
		if !ok {
			t.Errorf("no Chinese description for command %q (%q)", root.Token, root.Desc)
			continue
		}
		if entry.hans == "" || entry.hant == "" {
			t.Errorf("translation for %q is incomplete: %+v", root.Desc, entry)
		}
	}
	if checked == 0 {
		t.Fatal("the catalog has no command descriptions at all")
	}
}

// The snapshot's option and subcommand descriptions are only partly
// translated. On a Chinese screen an untranslated one is dropped, never shown
// in English; English keeps the catalog text.
func TestUntranslatedDescriptionsAreDroppedInChinese(t *testing.T) {
	const desc = "definitely not a translated description"
	if got := LocalizeDesc(desc, "zh-Hans"); got != "" {
		t.Fatalf("zh-Hans fallback = %q, want an empty subtitle", got)
	}
	if got := LocalizeDesc(desc, "zh-Hant"); got != "" {
		t.Fatalf("zh-Hant fallback = %q, want an empty subtitle", got)
	}
	if got := LocalizeDesc(desc, "en"); got != desc {
		t.Fatalf("English fallback = %q, want the catalog text", got)
	}
	if got := LocalizeDesc("List open files", "zh-Hans"); got != "列出打开的文件" {
		t.Fatalf("translated description = %q", got)
	}
}
