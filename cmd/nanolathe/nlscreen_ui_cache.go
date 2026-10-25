package main

import (
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// Values the Nanolathe screen's draw path would otherwise build afresh every
// frame (DESIGN_INTERFACE_HUD_INPUT §3.17): hit-region names, wrapped
// paragraphs and the key map's static catalogue. A frame names and wraps what
// the last one did, so each is built once and looked up after; none of these
// changes anything drawn.

type nlUICache struct {
	// language is the interface language the kept capitals and lines were
	// translated into; another starts them over.
	language string
	ids      map[nlIDKey]string
	texts    map[nlTextKey]string
	wraps    map[nlWrapKey][]string
	fits     map[nlFitKey]nlFittedText
	upper    map[string]string
	mutators map[content.Mutators]string
}

type nlIDKey struct {
	prefix, key string
	i, j        int
}

// id is prefix+key, then "-i" and "-j" for each that is not negative.
func (c *nlUICache) id(prefix, key string, i, j int) string {
	k := nlIDKey{prefix, key, i, j}
	if id, ok := c.ids[k]; ok {
		return id
	}
	id := prefix + key
	if i >= 0 {
		id += "-" + strconv.Itoa(i)
	}
	if j >= 0 {
		id += "-" + strconv.Itoa(j)
	}
	if c.ids == nil {
		c.ids = map[nlIDKey]string{}
	}
	c.ids[k] = id
	return id
}

// nlTextKey names a formatted text by its kind and what it is formatted from.
type nlTextKey struct {
	kind string
	a, b string
	i, j int
}

// text is a formatted text kept by its key: build runs once per key. Counts
// can take many values, so the cache starts over past a few hundred texts.
func (c *nlUICache) text(k nlTextKey, build func() string) string {
	if t, ok := c.texts[k]; ok {
		return t
	}
	if c.texts == nil || len(c.texts) >= 512 {
		c.texts = map[nlTextKey]string{}
	}
	t := build()
	c.texts[k] = t
	return t
}

// mutatorKey is m.String(), the spelling a scene key carries, kept.
func (c *nlUICache) mutatorKey(m content.Mutators) string {
	if k, ok := c.mutators[m]; ok {
		return k
	}
	if c.mutators == nil || len(c.mutators) >= 64 {
		c.mutators = map[content.Mutators]string{}
	}
	k := m.String()
	c.mutators[m] = k
	return k
}

// translated starts the kept capitals and lines over when the interface
// language changed since they were kept.
func (c *nlUICache) translated() {
	if language := i18n.Language(); language != c.language {
		c.language, c.upper, c.wraps, c.fits = language, nil, nil, nil
	}
}

// upperCase is a label in the interface language and in capitals, kept. It
// is translated already: draw it Verbatim.
func (c *nlUICache) upperCase(label string) string {
	c.translated()
	if up, ok := c.upper[label]; ok {
		return up
	}
	if c.upper == nil {
		c.upper = map[string]string{}
	}
	up := strings.ToUpper(i18n.T(label))
	c.upper[label] = up
	return up
}

// nlWrapKey is everything Font.Wrap's measuring reads.
type nlWrapKey struct {
	font                  *screenkit.Font
	text                  string
	size, tracking, width float64
	upper, verbatim       bool
}

// wrap is Font.Wrap, kept: it measures every word of a paragraph and builds
// each candidate line. A window resize makes new widths, so the cache starts
// over once it holds a few hundred paragraphs.
func (c *nlUICache) wrap(f *screenkit.Font, text string, st screenkit.Style, width float64) []string {
	c.translated()
	k := nlWrapKey{f, text, st.Size, st.Tracking, width, st.Upper, st.Verbatim}
	if lines, ok := c.wraps[k]; ok {
		return lines
	}
	if c.wraps == nil || len(c.wraps) >= 256 {
		c.wraps = map[nlWrapKey][]string{}
	}
	lines := f.Wrap(text, st, width)
	c.wraps[k] = lines
	return lines
}

type nlFitKey struct {
	nlWrapKey
	minSize float64
}

type nlFittedText struct {
	text string
	size float64
}

// fit keeps a single line inside its column: shrink to minSize, then elide
// whole runes. Keep the measured result, including the font and scale, just
// as wrap does (DESIGN_INTERFACE_HUD_INPUT §3.17).
func (c *nlUICache) fit(f *screenkit.Font, text string, st screenkit.Style, width, minSize float64) (string, screenkit.Style) {
	c.translated()
	// Fit what the screen shows: text still to be translated is translated
	// here and returned Verbatim, so eliding it never leaves an English
	// fragment that no catalog entry matches.
	if !st.Verbatim {
		text, st.Verbatim = i18n.T(text), true
	}
	k := nlFitKey{nlWrapKey{f, text, st.Size, st.Tracking, width, st.Upper, st.Verbatim}, minSize}
	if fit, ok := c.fits[k]; ok {
		st.Size = fit.size
		return fit.text, st
	}
	if w := f.Measure(text, st); w > width {
		st.Size = max(minSize, st.Size*max(0, width)/w)
		// Measuring sums glyph advances, so rounding the scaled size can
		// leave a line a fraction of a pixel over its exact-fit width.
		for st.Size > minSize && f.Measure(text, st) > width {
			st.Size = max(minSize, math.Nextafter(st.Size, 0))
		}
		if f.Measure(text, st) > width {
			runes := []rune(text)
			for len(runes) > 0 && f.Measure(strings.TrimSpace(string(runes))+"…", st) > width {
				runes = runes[:len(runes)-1]
			}
			text = strings.TrimSpace(string(runes)) + "…"
			if f.Measure(text, st) > width {
				text = ""
			}
		}
	}
	if c.fits == nil || len(c.fits) >= 512 {
		c.fits = map[nlFitKey]nlFittedText{}
	}
	c.fits[k] = nlFittedText{text, st.Size}
	return text, st
}

// drawWrapped is Font.DrawWrapped over the kept lines: the same baselines,
// leading and height.
func (s *nlScreen) drawWrapped(screen *ebiten.Image, f *screenkit.Font, text string, x, top, width, leading float64, st screenkit.Style) float64 {
	lines := s.ui.wrap(f, text, st, width)
	st.Verbatim = true
	y := top + st.Size
	for _, l := range lines {
		f.Draw(screen, l, x, y, st)
		y += st.Size * leading
	}
	return float64(len(lines)) * st.Size * leading
}

// nlActions is the key map's action catalogue: input.Actions copies it on
// every call, and the Controls page and the change count read it every frame.
// Nothing here modifies it.
var nlActions = sync.OnceValue(input.Actions)

// nlGroupActions lists each Controls tab's actions in catalogue order.
var nlGroupActions = sync.OnceValue(func() map[string][]input.Action {
	out := map[string][]input.Action{}
	for _, a := range nlActions() {
		out[a.Group] = append(out[a.Group], a)
	}
	return out
})

// nlProfileDefaults is input.ProfileDefaults, kept per profile: each call
// builds the whole table, and the key table reads it for every row it draws.
var nlProfileDefaults = sync.OnceValue(func() map[string]map[string][]input.Chord {
	out := map[string]map[string][]input.Chord{}
	for _, p := range input.Profiles() {
		out[p] = input.ProfileDefaults(p)
	}
	return out
})

// nlActionNames gives each action's group and label by its identifier.
var nlActionNames = sync.OnceValue(func() (names struct{ group, label map[string]string }) {
	names.group, names.label = map[string]string{}, map[string]string{}
	for _, a := range nlActions() {
		names.group[a.ID], names.label[a.ID] = a.Group, a.Label
	}
	return names
})

func nlProfileDefault(profile, id string) []input.Chord {
	if table, ok := nlProfileDefaults()[profile]; ok {
		return table[id]
	}
	return input.ProfileDefaults(profile)[id]
}
