package i18n

import (
	"strconv"
	"testing"
)

func withCatalog(t *testing.T, entries map[string]string) {
	t.Helper()
	saved := current.Load()
	t.Cleanup(func() { current.Store(saved) })
	current.Store(build("test", entries))
}

func TestTranslateExactPatternsAndFallback(t *testing.T) {
	withCatalog(t, map[string]string{
		"Back":                     "Назад",
		"the east":                 "сходу",
		"Wave {} has arrived":      "Хвиля {} прибула",
		"Wave {} incoming: {}":     "Хвиля {} наближається: з {}",
		"{} of {} units":           "{2} юнітів, з них {1}",
		"Ground light":             "Світло на землі",
		"{} {#}%":                  "{} — {}%",
		"Wave {} incoming: {} now": "",
	})
	for in, want := range map[string]string{
		"Back":                       "Назад",
		"Untranslated":               "Untranslated",
		"Wave 3 has arrived":         "Хвиля 3 прибула",
		"Wave 4 incoming: the east":  "Хвиля 4 наближається: з сходу",
		"5 of 9 units":               "9 юнітів, з них 5",
		"Ground light 150%":          "Світло на землі — 150%",
		"Ground light full%":         "Ground light full%",
		"Wave 4 incoming: the north": "Хвиля 4 наближається: з the north",
		"":                           "",
	} {
		if got := T(in); got != want {
			t.Errorf("T(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnglishTranslatesNothing(t *testing.T) {
	saved := current.Load()
	t.Cleanup(func() { current.Store(saved) })
	if err := SetLanguage(English); err != nil {
		t.Fatal(err)
	}
	if got := T("Back"); got != "Back" || Language() != English {
		t.Fatalf("English translated %q to %q (language %q)", "Back", got, Language())
	}
	if err := SetLanguage("klingon"); err == nil || Language() != English {
		t.Fatal("an unknown language was accepted")
	}
}

// Every selectable language's catalog loads, and each translation places
// only the runs its pattern captures.
func TestShippedCatalogsLoad(t *testing.T) {
	saved := current.Load()
	t.Cleanup(func() { current.Store(saved) })
	for _, l := range Languages {
		if err := SetLanguage(l.Code); err != nil {
			t.Fatalf("%s: %v", l.Code, err)
		}
		if Language() != l.Code {
			t.Fatalf("selected %q, running %q", l.Code, Language())
		}
		c := current.Load()
		if c == nil {
			continue
		}
		for k, v := range c.exact {
			if placeholder.MatchString(v) {
				t.Errorf("%s: %q translates to the pattern %q", l.Code, k, v)
			}
		}
		for _, p := range c.patterns {
			holes := p.re.NumSubexp()
			next := 0
			for _, m := range placeholder.FindAllStringSubmatch(p.translation, -1) {
				n := next + 1
				if m[1] != "" {
					n, _ = strconv.Atoi(m[1])
				} else {
					next++
				}
				if n < 1 || n > holes {
					t.Errorf("%s: %q places run %d of %d", l.Code, p.translation, n, holes)
				}
			}
		}
	}
}
