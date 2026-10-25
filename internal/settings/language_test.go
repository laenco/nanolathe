package settings

import "testing"

// The interface language is kept as a lower-case code; English, the language
// the interface is written in, is no language at all.
func TestNormalizeLanguage(t *testing.T) {
	for in, want := range map[string]string{"": "", " English ": "", "Ukrainian": "ukrainian", "french": "french"} {
		s := Defaults()
		s.Language = in
		s.Normalize()
		if s.Language != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, s.Language, want)
		}
	}
}
