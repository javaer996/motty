package completion

import "testing"

func TestLocalizeDescSpeaksTheUiLanguage(t *testing.T) {
	const english = "copy files and directories"
	if got := LocalizeDesc(english, "zh-Hans"); got != "复制文件和目录" {
		t.Fatalf("zh-Hans = %q", got)
	}
	if got := LocalizeDesc(english, "zh-Hant"); got != "複製檔案和目錄" {
		t.Fatalf("zh-Hant = %q", got)
	}
	if got := LocalizeDesc(english, "en"); got != english {
		t.Fatalf("en = %q", got)
	}
	// A Chinese screen never mixes in English: an untranslated description
	// leaves the chip without a subtitle.
	if got := LocalizeDesc("something nobody translated", "zh-Hans"); got != "" {
		t.Fatalf("untranslated = %q, want an empty subtitle", got)
	}
	if got := LocalizeDesc("", "zh-Hans"); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{
		"zh-Hans": LocaleHans,
		"zh_CN":   LocaleHans,
		"zh":      LocaleHans,
		"zh-Hant": LocaleHant,
		"zh-TW":   LocaleHant,
		"zh-HK":   LocaleHant,
		"en-US":   LocaleEnglish,
		"ja":      LocaleEnglish,
		"":        LocaleEnglish,
	}
	for input, want := range cases {
		if got := NormalizeLocale(input); got != want {
			t.Fatalf("NormalizeLocale(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEveryCuratedCombinationHasADescription(t *testing.T) {
	for _, entry := range combos {
		if entry.desc == "" {
			t.Fatalf("combination %q has no description", entry.line)
		}
		if _, ok := translations[entry.desc]; !ok {
			t.Fatalf("combination %q: description %q is not translated", entry.line, entry.desc)
		}
	}
}
