package main

import (
	"encoding/json"
	"image/color"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
)

// Where a setting's value comes from (docs/DESIGN_MODS_MUTATORS.md §4.6): the
// base settings, the running mod's recommendation, or the player's own
// change for that mod; and whether the mod locks it. The screen marks each
// card and offers to go back to the mod's value.

// nlSource is the running content's layers as drafts, for comparing cards.
type nlSource struct {
	mod        string  // the running mod's name, "" without a config
	base, rec  nlDraft // the base settings, and the base with the mod's recommendation
	locks      []string
	overridden bool
	paths      map[string][]string // settings paths each card writes
}

// bindSource computes the running content's layers.
func (s *nlScreen) bindSource(g *gameShell) {
	s.src = nlSource{paths: map[string][]string{}}
	m := s.modAt(s.draft.mod)
	if m == nil || m.Config == nil {
		return
	}
	base := g.baseSettings
	rec, err := settings.Layer(base, modRecommendations(m))
	if err != nil {
		return
	}
	s.src.mod = m.Name
	s.src.base = s.draftOf(base)
	s.src.rec = s.draftOf(rec)
	s.src.locks = modLocks(m)
	s.src.overridden = g.lockOverridden(m)
	for _, page := range s.pages() {
		for _, c := range page.cards {
			s.src.paths[c.key] = s.cardPaths(c, rec)
		}
	}
}

// draftOf reads a settings block into a draft, the way snapshot reads the
// shell.
func (s *nlScreen) draftOf(st settings.Settings) nlDraft {
	st.Normalize()
	d := nlDraft{
		gameplay: st.Gameplay.Normalize(), pres: st.Presentation,
		glow: st.Display.Glow, glowStrength: st.Display.GlowStrength,
		shadows: st.Display.Shadows, vehicleShadows: st.Display.VehicleShadows,
		unitLimit: st.UnitLimit, switchAlt: st.SwitchAltEnabled(), interfaceType: st.InterfaceType,
		keys:       keyMapFromSettings(st.KeyBindings),
		resolution: retailDisplayMode{st.Display.Width, st.Display.Height},
	}
	if d.glowStrength < 0 {
		d.glowStrength = settings.DefaultGlowStrength
	}
	return d
}

// cardPaths finds the settings paths a card writes by changing it and
// diffing every value. A next-value probe can miss a path that only another
// value changes, such as a page count when Free flow cycles to Original.
func (s *nlScreen) cardPaths(c nlCard, base settings.Settings) []string {
	if c.key == "content" {
		return nil
	}
	from := s.draftOf(base)
	var probes []nlDraft
	if c.kind == nlGroup {
		for _, p := range c.parts {
			for v := range p.steps {
				d := from
				p.set(&d, v)
				probes = append(probes, d)
			}
		}
	} else if len(c.steps) > 1 {
		for v := range c.steps {
			d := from
			c.set(&d, v)
			probes = append(probes, d)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range probes {
		diff, err := settings.Diff(settingsOf(base, d), settingsOf(base, from), settings.ModScoped)
		if err != nil || len(diff) == 0 {
			continue
		}
		var doc map[string]any
		if json.Unmarshal(diff, &doc) != nil {
			continue
		}
		collectPaths(doc, "", func(p string) {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		})
	}
	return out
}

func collectPaths(doc map[string]any, prefix string, add func(string)) {
	for k, v := range doc {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if m, ok := v.(map[string]any); ok && len(m) > 0 && p != "keyBindings" && p != "gameplayFeatures" {
			collectPaths(m, p, add)
			continue
		}
		add(p)
	}
}

// Source markers and edit guards follow the selected content, including a mod
// awaiting Apply. Its locks must be approved before changes survive its reload.
func (s *nlScreen) sourceActive() bool {
	return s.src.mod != ""
}

// cardLocked reports whether the running mod locks something the card
// writes and the player has not overridden it.
func (s *nlScreen) cardLocked(c nlCard) bool {
	if !s.sourceActive() || s.src.overridden || s.draft.override || len(s.src.locks) == 0 {
		return false
	}
	return pathsLocked(s.src.paths[c.key], s.src.locks)
}

// keysLocked reports whether the keyboard is locked.
func (s *nlScreen) keysLocked() bool {
	if !s.sourceActive() || s.src.overridden || s.draft.override {
		return false
	}
	return pathsLocked([]string{"keyBindings"}, s.src.locks)
}

func pathsLocked(paths, locks []string) bool {
	for _, p := range paths {
		for _, l := range locks {
			if p == l || strings.HasPrefix(p, l+".") || strings.HasPrefix(l, p+".") {
				return true
			}
		}
	}
	return false
}

// guardLocked runs change now, or, when the card is locked, asks to
// override the mod's locks first and runs it after.
func (s *nlScreen) guardLocked(c nlCard, change func()) {
	if !s.cardLocked(c) {
		change()
		return
	}
	s.pendingAction, s.pendingWhat, s.dialog = change, c.label, "override"
}

// A profile or edit can precede selecting locked content. Check the final
// composed value too, before reload, so Apply cannot create a live override
// that persistence subsequently drops (DESIGN_MODS_MUTATORS §4.6).
func (s *nlScreen) guardApplyLocks() bool {
	if !s.sourceActive() || s.src.overridden || s.draft.override || len(s.src.locks) == 0 {
		return false
	}
	final, err := s.draftSettings()
	if err != nil {
		s.toast, s.toastLeft = err.Error(), 3
		return true
	}
	g, target := s.shell(), s.modAt(s.draft.mod)
	recommended, err := settings.Layer(g.baseSettings, modRecommendations(target))
	if err != nil {
		s.toast, s.toastLeft = err.Error(), 3
		return true
	}
	// Compare against what the target would bind: command-line selections,
	// the content's minimum rules and the canonical keyboard representation
	// already apply before a player edits anything on this screen.
	recommended.Presentation = startupPresentation(g.opts, recommended.Presentation)
	recommended.Gameplay = startupGameplay(g.opts, recommended.Gameplay)
	if minimum, ok := modMinimumGameplay(target); ok && gameplayBelow(recommended.Gameplay, minimum) {
		recommended.Gameplay = minimum
	}
	recommended.KeyBindings = keyBindingsSetting(keyMapFromSettings(recommended.KeyBindings))
	patch, err := settings.Diff(final, recommended, settings.ModScoped)
	if err != nil {
		s.toast, s.toastLeft = err.Error(), 3
		return true
	}
	var doc map[string]any
	_ = json.Unmarshal(patch, &doc)
	var paths []string
	collectPaths(doc, "", func(path string) { paths = append(paths, path) })
	if !pathsLocked(paths, s.src.locks) {
		return false
	}
	s.pendingAction, s.pendingWhat, s.dialog = s.apply, "the selected content's settings", "override"
	return true
}

// cardSource is the card's marker: "set" when the mod's recommendation sets
// it, "changed" when the player changed it for the mod, "" otherwise.
func (s *nlScreen) cardSource(c nlCard) string {
	if !s.sourceActive() || c.key == "content" || c.key == "resolution" {
		return ""
	}
	switch {
	case !nlCardEqual(c, &s.draft, &s.src.rec):
		return "changed"
	case !nlCardEqual(c, &s.src.rec, &s.src.base):
		return "set"
	}
	return ""
}

// drawSource is the hero's marker beside the page counter: who set the
// value, and a way back to the mod's. y is the marker's top.
func (s *nlScreen) drawSource(screen *ebiten.Image, c *nlCard, x, y, a float64) {
	u := s.u()
	df := s.fonts.Display
	chip := func(text string, col color.RGBA, lock bool) float64 {
		st := screenkit.Style{Size: 10.5 * u, Tracking: 0.14, Top: alphaC(col, a), Upper: true}
		tw := df.Measure(text, st) + 20*u
		if lock {
			tw += 16 * u
		}
		r := screenkit.Rect{X: x, Y: y, W: tw, H: 22 * u}
		screenkit.Fill(screen, r, color.RGBA{6, 12, 6, uint8(210 * a)})
		screenkit.Outline(screen, r, 1*u, alphaC(col, 0.7*a))
		tx := r.X + 10*u
		if lock {
			s.padlock(screen, tx, r.Y+4*u, 12*u, col, false)
			tx += 16 * u
		}
		df.Draw(screen, text, tx, r.Y+15.5*u, st)
		return tw
	}
	if s.cardLocked(*c) {
		chip("Locked by "+s.src.mod, nlAmber, true)
		return
	}
	switch s.cardSource(*c) {
	case "set":
		chip("Set by "+s.src.mod, nlGreenText, false)
	case "changed":
		w := chip("Changed for "+s.src.mod, nlAmber, false)
		id := s.ui.id("use-mod-", c.key, -1, -1)
		label := "Use " + s.src.mod + "'s"
		if c.kind != nlGroup {
			if rec := c.get(&s.src.rec); rec >= 0 && rec < len(c.steps) && c.steps[rec] != "" {
				label = "Use " + s.src.mod + "'s " + c.steps[rec]
			}
		}
		st := screenkit.Style{Size: 10.5 * u, Tracking: 0.12, Top: alphaC(lerpRGBA(nlKicker, nlCream, s.hits.HoverAmount(id)), a), Upper: true}
		disabled := s.cardUnavailable(*c) != ""
		if disabled {
			st.Top = alphaC(nlDim, a)
		}
		lx := x + w + 12*u
		lw := df.Draw(screen, label, lx, y+15.5*u, st)
		screenkit.Fill(screen, screenkit.Rect{X: lx, Y: y + 19*u, W: lw, H: 1 * u}, alphaC(nlKicker, a))
		s.hits.Add(screenkit.Region{ID: id, Rect: screenkit.Rect{X: lx - 4*u, Y: y, W: lw + 8*u, H: 22 * u}, Disable: disabled, Click: func() {
			s.setCardDraft(*c, s.src.rec)
		}})
	}
}

// drawCardSource keeps the marker over the card's picture. Compact cards
// put their name beside the picture, so the marker must fit within it
// (DESIGN_INTERFACE_HUD_INPUT §3.17).
func (s *nlScreen) drawCardSource(screen *ebiten.Image, c *nlCard, pic screenkit.Rect, a float64) {
	u := s.u()
	if s.cardLocked(*c) {
		s.padlock(screen, pic.X+pic.W-20*u, pic.Y+4*u, 14*u, nlAmber, false)
		return
	}
	tag := ""
	col := nlGreenText
	switch s.cardSource(*c) {
	case "set":
		tag = s.src.mod
	case "changed":
		tag, col = "Yours", nlAmber
	}
	if tag == "" {
		return
	}
	st := screenkit.Style{Size: 9.5 * u, Tracking: 0.12, Top: alphaC(col, a), Upper: true}
	tag, st = s.ui.fit(s.fonts.Display, tag, st, pic.W-20*u, 8.5*u)
	tw := s.fonts.Display.Measure(tag, st) + 12*u
	tr := screenkit.Rect{X: pic.X + 4*u, Y: pic.Y + 6*u, W: tw, H: 18 * u}
	screenkit.Fill(screen, tr, color.RGBA{6, 12, 6, uint8(210 * a)})
	screenkit.Outline(screen, tr, 1*u, alphaC(col, 0.6*a))
	s.fonts.Display.Draw(screen, tag, tr.X+6*u, tr.Y+13*u, st)
}
