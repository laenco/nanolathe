package screenkit

import (
	"slices"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/i18n"
)

// Text is set in the interface language unless the style is verbatim, and
// Wrap breaks the translation, not the English (docs/DESIGN_I18N.md).
func TestTextIsSetInTheInterfaceLanguage(t *testing.T) {
	t.Cleanup(func() { _ = i18n.SetLanguage(i18n.English) })
	f := LoadFonts().Body
	plain, verbatim := Style{Size: 12}, Style{Size: 12, Verbatim: true}
	english := f.Measure("Back", plain)
	if err := i18n.SetLanguage("ukrainian"); err != nil {
		t.Fatal(err)
	}
	if got, want := f.Measure("Back", plain), f.Measure("Назад", verbatim); got != want || got == english {
		t.Fatalf("translated width %v, want the translation's %v (English %v)", got, want, english)
	}
	if got := f.Measure("Back", verbatim); got != english {
		t.Fatalf("verbatim width %v, want the English %v", got, english)
	}
	upper := Style{Size: 12, Upper: true}
	if got, want := f.Measure("Back", upper), f.Measure("НАЗАД", verbatim); got != want {
		t.Fatalf("capitals width %v, want the translation in capitals %v", got, want)
	}
	if got := f.Wrap("Back", plain, 1000); !slices.Equal(got, []string{"Назад"}) {
		t.Fatalf("Wrap = %q", got)
	}
}
