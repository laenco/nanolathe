package main

import (
	"io"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
)

func restoreEnglish(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = i18n.SetLanguage(i18n.English) })
}

func TestLanguageFlag(t *testing.T) {
	opts, err := parseFlags([]string{"--language", " Ukrainian "}, io.Discard)
	if err != nil || opts.Language != "ukrainian" {
		t.Fatalf("--language Ukrainian: %q, %v", opts.Language, err)
	}
	if _, err := parseFlags([]string{"--language", "klingon"}, io.Discard); err == nil {
		t.Fatal("an unknown language was accepted")
	}
}

// The Language card's choice applies on Apply, is saved, and comes back with
// the settings; English saves as no language.
func TestLanguageCardAppliesAndSaves(t *testing.T) {
	restoreEnglish(t)
	g, s := settingsRegressionScreen(nil, settings.Defaults())
	card := configurationCard(t, s, "language")
	if card.get(&s.draft) != 0 || i18n.Language() != i18n.English {
		t.Fatalf("a fresh player starts in %q", i18n.Language())
	}
	s.setCard(card, 1)
	if i18n.Language() != i18n.English {
		t.Fatal("the draft changed the language before Apply")
	}
	s.apply()
	if i18n.Language() != "ukrainian" || g.captureSettings().Language != "ukrainian" {
		t.Fatalf("Apply: running %q, saved %q", i18n.Language(), g.captureSettings().Language)
	}
	_ = i18n.SetLanguage(i18n.English)
	g, s = settingsRegressionScreen(nil, g.captureSettings())
	if i18n.Language() != "ukrainian" || configurationCard(t, s, "language").get(&s.draft) != 1 {
		t.Fatalf("restart: running %q", i18n.Language())
	}
	s.setCard(configurationCard(t, s, "language"), 0)
	s.apply()
	if i18n.Language() != i18n.English || g.captureSettings().Language != "" {
		t.Fatalf("back to English: running %q, saved %q", i18n.Language(), g.captureSettings().Language)
	}
}

// --language sets the language for one run without becoming the saved
// choice; choosing on the card replaces it.
func TestLanguageFlagIsForOneRun(t *testing.T) {
	restoreEnglish(t)
	g := &gameShell{cs: &contentSet{}}
	g.opts.Language = "ukrainian"
	g.applySettings(settings.Defaults())
	if i18n.Language() != "ukrainian" || g.captureSettings().Language != "" {
		t.Fatalf("flag: running %q, saved %q", i18n.Language(), g.captureSettings().Language)
	}
	g.chooseLanguage(0)
	if i18n.Language() != i18n.English || g.opts.Language != "" {
		t.Fatalf("card: running %q, flag %q", i18n.Language(), g.opts.Language)
	}
}

// The screen's kept capitals and wrapped lines follow a language change.
func TestKeptTextFollowsTheLanguage(t *testing.T) {
	restoreEnglish(t)
	var c nlUICache
	if got := c.upperCase("Back"); got != "BACK" {
		t.Fatalf("English capitals %q", got)
	}
	if err := i18n.SetLanguage("ukrainian"); err != nil {
		t.Fatal(err)
	}
	if got := c.upperCase("Back"); got != "НАЗАД" {
		t.Fatalf("Ukrainian capitals %q", got)
	}
}
