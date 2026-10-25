package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/addonlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/i18n"
	"github.com/nanolathe-gg/nanolathe/internal/modfetch"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// The Add-ons card follows the Content card on the Nanolathe screen's Game
// page (docs/DESIGN_MODS_MUTATORS.md §5.7, §8.2): one row per official
// add-on the library holds or the add-on catalogue offers, with its switch,
// Get while it is not installed and then its download's progress, or
// "Already in your install" when the base folder holds its archives. The
// switches edit the draft and Apply reloads the content with the drafted
// selection. The catalogue is asked for only once the card is shown (D5), and
// a Get that installs switches its add-on on in the draft.

// nlAddonRow is one add-on the card lists.
type nlAddonRow struct {
	known knownAddon
	// inFolder marks an add-on whose archives sit byte-identical in the base
	// install's folder: the install supplies it, so it has no switch. A
	// model pack still needs an installed selection to apply its model tier.
	inFolder bool
	// units are the units its archives define, read from the archives
	// themselves when they are at hand (installed or in the folder), so they
	// show whether or not the add-on is mounted; names is their names,
	// comma-separated.
	units []nlAddonUnit
	names string
	// faction is the row's faction line when its archives hold a faction
	// declaration (docs/DESIGN_MODS_MUTATORS.md §5.8): its sides and how
	// many units they have, shown in place of the unit names.
	faction string
	// models marks an audited model-only payload, which replaces models
	// rather than adding units (§5.7).
	models bool
}

// nlAddonUnit is one unit an add-on's archives define: its unit name, its
// name in play and its build picture, nil when the archives hold none.
type nlAddonUnit struct {
	key, name string
	pic       *image.RGBA
}

// nlAddonList is the card's listing, filled by a worker, and the screen's
// Get.
type nlAddonList struct {
	mu      sync.Mutex
	rows    []nlAddonRow
	loading bool
	gen     int // the listing whose result is wanted
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// The game goroutine's own: whether this opening has asked the live
	// catalogue, the Get in flight and the install count it started from,
	// why the last Get failed, and the unit pictures uploaded.
	fetched bool
	get     *modfetch.Entry
	getFrom int
	failure string
	images  map[*image.RGBA]*ebiten.Image
}

// start lists the add-ons: the library's and the cached catalogue's at once,
// then, with network, the live catalogue's. root is the base install's
// folder, where an add-on's archives may already sit. A later start or stop
// discards what an earlier listing finds.
func (l *nlAddonList) start(root string, network bool) {
	l.mu.Lock()
	if l.cancel != nil {
		l.cancel()
	}
	l.gen++
	gen := l.gen
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel, l.loading = cancel, true
	l.mu.Unlock()
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		defer cancel()
		publish := func(rows []nlAddonRow, done bool) {
			l.mu.Lock()
			defer l.mu.Unlock()
			if gen == l.gen {
				l.rows = rows
				l.loading = !done
			}
		}
		publish(nlAddonRowsOf(knownAddons(ctx, false), root), !network)
		if network {
			rows := nlAddonRowsOf(knownAddons(ctx, true), root)
			if ctx.Err() == nil {
				publish(rows, true)
			}
		}
	}()
}

// stop abandons a listing in progress: closing the screen stops the
// catalogue fetch, as closing the mod catalogue does.
func (l *nlAddonList) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	l.gen++
	l.loading = false
}

// view is the listing as it stands, and whether one is still running. The
// worker replaces rows whole, so the slice is never written after this.
func (l *nlAddonList) view() ([]nlAddonRow, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rows, l.loading
}

// image is a unit picture uploaded once, on the game goroutine.
func (l *nlAddonList) image(px *image.RGBA) *ebiten.Image {
	if px == nil {
		return nil
	}
	if img, ok := l.images[px]; ok {
		return img
	}
	if l.images == nil || len(l.images) >= 64 {
		l.images = map[*image.RGBA]*ebiten.Image{}
	}
	img := ebiten.NewImageFromImage(px)
	l.images[px] = img
	return img
}

func nlAddonRowsOf(known []knownAddon, root string) []nlAddonRow {
	rows := make([]nlAddonRow, 0, len(known))
	for _, k := range known {
		row := nlAddonRow{known: k}
		var paths []string
		if root != "" {
			paths, row.inFolder = addonlibrary.InFolder(root, k.archives)
		}
		if !row.inFolder && k.installed != nil {
			paths = k.installed.Paths
		}
		if row.inFolder || k.installed != nil {
			row.units = nlAddonUnitsOf(paths)
			names := make([]string, 0, len(row.units))
			for _, u := range row.units {
				names = append(names, u.name)
			}
			row.names = strings.Join(names, ", ")
			if factions, err := addonlibrary.Factions(addonlibrary.Addon{Paths: paths}); err == nil {
				row.faction = nlFactionText(factions)
			}
			row.models, _ = addonlibrary.ModelsOnly(addonlibrary.Addon{Paths: paths})
			if row.models && !addonlibrary.ReplacesBase(addonlibrary.Addon{Archives: k.archives}) {
				// A folder copy alone has the UFO tier, below the patch;
				// keep the installed switch or catalogue Get available (§5.7).
				row.inFolder = false
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// nlFactionText is a faction add-on's line, its declarations' sides and
// their units, e.g. "Faction: Mynn · 78 units", or a unit pack's, e.g.
// "Unit pack · 52 units"; "" for an add-on without a declaration.
func nlFactionText(factions []addonlibrary.Faction) string {
	var sides []string
	units, packUnits, packs := 0, 0, 0
	for _, f := range factions {
		if f.UnitPack() {
			packUnits += f.Units
			packs++
			continue
		}
		named := false
		for _, side := range f.Sides {
			if side != "" {
				sides, named = append(sides, side), true
			}
		}
		if !named {
			sides = append(sides, f.ID)
		}
		units += f.Units
	}
	if len(sides) == 0 {
		if packs == 0 {
			return ""
		}
		return "Unit pack · " + unitCountText(packUnits)
	}
	label := "Faction: "
	if len(sides) > 1 {
		label = "Factions: "
	}
	return label + strings.Join(sides, ", ") + " · " + unitCountText(units)
}

// nlAddonUnitsOf reads the units an add-on's archives define, in unit-name
// order: each unit file's UNITINFO name and name in play, and its build
// picture. Archives that do not read give none.
func nlAddonUnitsOf(paths []string) []nlAddonUnit {
	fs := vfs.New()
	defer fs.Close()
	for _, p := range paths {
		if _, err := fs.MountArchive(p, 0); err != nil {
			return nil
		}
	}
	entries, err := fs.ReadDir("units")
	if err != nil {
		return nil
	}
	var units []nlAddonUnit
	for _, e := range entries {
		if e.IsDir || !strings.EqualFold(path.Ext(e.Path), ".fbi") {
			continue
		}
		data, err := fs.ReadFileLimit(e.Path, 1<<20)
		if err != nil {
			continue
		}
		doc, err := formats.ParseTDF(data)
		if err != nil {
			continue
		}
		info := doc.Root.Section("UNITINFO")
		if info == nil {
			continue
		}
		key, _ := info.StringValue("UnitName", "")
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		name, _ := info.StringValue("Name", "")
		if name = strings.TrimSpace(name); name == "" {
			name = strings.ToUpper(key)
		}
		units = append(units, nlAddonUnit{key: key, name: name, pic: decodeNLPic(fs, key)})
	}
	sort.Slice(units, func(i, j int) bool { return units[i].key < units[j].key })
	return units
}

// nlAddonsText spells a selection for the draft in NormalizeAddons's order,
// each entry id@version, comma-separated, so equal selections compare equal.
// The id and version alphabets (§4.2) hold neither separator.
func nlAddonsText(sel []settings.AddonSelection) string {
	var b strings.Builder
	for i, a := range settings.NormalizeAddons(sel) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(a.ID)
		b.WriteByte('@')
		b.WriteString(a.Version)
	}
	return b.String()
}

// nlAddonsOf reads a draft's spelling back into a selection.
func nlAddonsOf(text string) []settings.AddonSelection {
	if text == "" {
		return nil
	}
	var sel []settings.AddonSelection
	for _, entry := range strings.Split(text, ",") {
		id, version, _ := strings.Cut(entry, "@")
		sel = append(sel, settings.AddonSelection{ID: id, Version: version})
	}
	return sel
}

// addonsCard is the Add-ons card. Its value is the drafted selection, which
// no controls profile, preset or mod recommendation carries, so it copies
// nothing between drafts and the change count compares the selection itself
// (nlCardChanged). The arrows and the wheel step nothing: each row has its
// own switch.
func (s *nlScreen) addonsCard() nlCard {
	return nlCard{
		key: "addons", label: "Add-ons", pics: []string{"armflea", "armfark"}, kind: nlAddons,
		get: func(d *nlDraft) int {
			if d.addons == "" {
				return 0
			}
			return strings.Count(d.addons, ",") + 1
		},
		set:  func(*nlDraft, int) {},
		copy: func(*nlDraft, *nlDraft) {},
		valueText: func(d *nlDraft) string {
			n := 0
			if d.addons != "" {
				n = strings.Count(d.addons, ",") + 1
			}
			return s.ui.text(nlTextKey{kind: "addons on", i: n}, func() string {
				if n == 0 {
					return "None on"
				}
				return fmt.Sprintf("%d on", n)
			})
		},
		desc: func(*nlDraft, int) string {
			return "Bonus units, community fixes, model replacements and extra factions, enabled beside the selected mod."
		},
		scene: func(*nlDraft, int) string { return "armor" },
	}
}

// nlCardChanged reports whether a card's value differs between two drafts:
// nlCardEqual, except for the Add-ons card, whose selection no card copy
// carries (addonsCard).
func nlCardChanged(c nlCard, a, b *nlDraft) bool {
	if c.kind == nlAddons {
		return a.addons != b.addons
	}
	return !nlCardEqual(c, a, b)
}

// wantAddons starts the card's listing the first time an opening shows it,
// which is when the live catalogue is asked for (D5).
func (s *nlScreen) wantAddons() {
	l := &s.addonList
	g := s.shell()
	if l.fetched || g == nil || g.cs == nil {
		return
	}
	l.fetched = true
	l.start(g.cs.root, true)
}

// addonOn reports whether the draft enables this add-on version. Draw asks
// for every row, so it reads the spelling in place.
func (s *nlScreen) addonOn(id, version string) bool {
	for rest := s.draft.addons; rest != ""; {
		var entry string
		entry, rest, _ = strings.Cut(rest, ",")
		if i, v, _ := strings.Cut(entry, "@"); i == id && v == version {
			return true
		}
	}
	return false
}

// setAddon switches an add-on in the draft. The setting keeps one version
// per add-on, so switching a version on replaces another.
func (s *nlScreen) setAddon(id, version string, on bool) {
	if s.addonsUnavailable() != "" {
		return
	}
	sel := slices.DeleteFunc(nlAddonsOf(s.draft.addons), func(a settings.AddonSelection) bool { return a.ID == id })
	if on {
		sel = append(sel, settings.AddonSelection{ID: id, Version: version})
	}
	text := nlAddonsText(sel)
	if text == s.draft.addons {
		return
	}
	s.draft.addons = text
	s.touched["addons"] = true
	if g := s.shell(); g != nil {
		g.playMenuCue("SmallButton")
	}
}

// addonsUnavailable says why the switches cannot change: a command-line
// asset stack fixes the content, which Apply then never reloads.
func (s *nlScreen) addonsUnavailable() string {
	if g := s.shell(); g != nil && g.cs != nil && g.cs.manualRoots {
		return "Add-ons are fixed by the command-line asset stack."
	}
	return ""
}

// addonsLeftOffBy names the drafted mod when its config leaves the official
// add-ons off (§5.7 "With a mod"), "" otherwise.
func (s *nlScreen) addonsLeftOffBy() string {
	if m := s.modAt(s.draft.mod); m != nil && !m.Content().AddonsAllowed() {
		return m.Name
	}
	return ""
}

// draftAddons is the drafted selection and whether it differs from the one
// the running content was mounted with.
func (s *nlScreen) draftAddons(g *gameShell) ([]settings.AddonSelection, bool) {
	return nlAddonsOf(s.draft.addons), s.draft.addons != nlAddonsText(g.cs.addonSelection)
}

// getAddon starts the process's one add-on download for entry; pollAddons
// follows it.
func (s *nlScreen) getAddon(entry modfetch.Entry) {
	l := &s.addonList
	installs := addonDownload.view().installs
	started, err := startAddonDownload(entry)
	switch {
	case err != nil:
		s.toast, s.toastLeft = noticeReason(err), 3
	case !started:
		s.toast, s.toastLeft = "Another add-on download is running", 2.5
	default:
		l.get, l.getFrom, l.failure = &entry, installs, ""
		if g := s.shell(); g != nil {
			g.playMenuCue("SmallButton")
		}
	}
}

// pollAddons follows the screen's Get. Once it installs, the add-on is
// switched on in the draft — installing an add-on enables it (§5.7), and
// Apply mounts it — and the library is listed again; a failure is kept for
// the card to show. A Get still running when the screen closes is followed
// when it opens again.
func (s *nlScreen) pollAddons() {
	l := &s.addonList
	if l.get == nil {
		return
	}
	v := addonDownload.view()
	if v.running {
		return
	}
	entry := *l.get
	l.get = nil
	addonDownload.acknowledge()
	if v.installs == l.getFrom {
		l.failure = "the download stopped"
		if reason := strings.TrimPrefix(v.outcome, "Failed: "); reason != "" {
			l.failure = noticeReason(errors.New(reason))
		}
		return
	}
	s.setAddon(entry.ID, entry.Version, true)
	s.toast, s.toastLeft = entry.Name+" installed: Apply to mount it", 2.5
	if g := s.shell(); g != nil && g.cs != nil {
		l.start(g.cs.root, false)
	}
}

// addonSubText is a row's second line: a faction add-on's sides and unit
// count, saying so while the drafted mod leaves it off; else the units its
// archives define, by name, else the catalogue's summary or how many
// archives the add-on holds.
func (s *nlScreen) addonSubText(row nlAddonRow) string {
	if row.faction != "" {
		return row.faction
	}
	if row.models {
		return "Model replacements"
	}
	if row.names != "" {
		return row.names
	}
	k := row.known
	if k.entry != nil && strings.TrimSpace(k.entry.Summary) != "" {
		return strings.TrimSpace(k.entry.Summary)
	}
	return s.ui.text(nlTextKey{kind: "addon archives", i: len(k.archives)}, func() string {
		if len(k.archives) == 1 {
			return "1 archive"
		}
		return fmt.Sprintf("%d archives", len(k.archives))
	})
}

// addonVersionText is the version beside a row's name, with the download's
// size while the add-on is not installed.
func (s *nlScreen) addonVersionText(row nlAddonRow) string {
	k := row.known
	if k.installed != nil || row.inFolder || k.entry == nil {
		return k.version
	}
	return s.ui.text(nlTextKey{kind: "addon download", a: k.version, i: int(k.entry.Archive.Size)}, func() string {
		return k.version + " · " + onlineBytes(uint64(k.entry.Archive.Size))
	})
}

// heroAddons is the Add-ons card's control: a row per add-on with its lamp,
// name and version and what it holds, and at the right its switch, Get or
// its download's progress, or "Already in your install". Beneath an add-on
// whose archives are at hand its units' build pictures show, read from those
// archives, so they show before the add-on is mounted.
// The list ends at bottom: more rows than fit there scroll, as the content
// list does, so the cards beneath never cover a row.
func (s *nlScreen) heroAddons(screen *ebiten.Image, x, y, bottom, a float64) float64 {
	u := s.u()
	s.wantAddons()
	l := &s.addonList
	rows, loading := l.view()
	df, bf := s.fonts.Display, s.fonts.Body
	disabled := s.addonsUnavailable() != ""
	leftOff := s.addonsLeftOffBy() != ""
	box := screenkit.Rect{X: x, Y: y, W: 640 * u, H: 66 * u}
	if len(rows) == 0 {
		s.well(screen, box, a)
		text := "The add-on catalogue is unavailable."
		if loading {
			text = "Checking the add-on catalogue…"
		}
		bf.Draw(screen, text, box.X+24*u, box.Y+38*u, screenkit.Style{Size: 13 * u, Top: alphaC(nlBody, a), Shadow: 0.1})
		return box.Y + box.H
	}
	// An add-on's units show beneath it while there is room for two rows of
	// pictures.
	rowH, stripH := 58*u, 0.0
	if len(rows) <= 2 {
		stripH = 50 * u
	}
	strip := func(row nlAddonRow) bool { return stripH > 0 && len(row.units) > 0 }
	shown := len(rows)
	if stripH == 0 {
		notes := 0.0
		if loading {
			notes += 24 * u
		}
		if l.failure != "" {
			notes += 24 * u
		}
		shown = max(1, min(shown, int((bottom-notes-box.Y-12*u)/rowH)))
	}
	box.H = 12 * u
	for _, row := range rows[:shown] {
		box.H += rowH
		if strip(row) {
			box.H += stripH
		}
	}
	s.addonScroll.layout(len(rows), shown, box)
	s.well(screen, box, a)
	job := addonDownload.view()
	ry := box.Y + 6*u
	for i := s.addonScroll.top; i < s.addonScroll.top+shown; i++ {
		row := rows[i]
		k := row.known
		h := rowH
		if strip(row) {
			h += stripH
		}
		r := screenkit.Rect{X: box.X + 8*u, Y: ry, W: box.W - 16*u, H: h - 4*u}
		ry += h
		on := row.inFolder || s.addonOn(k.id, k.version)
		if on {
			screenkit.HGradient(screen, r, color.RGBA{40, 100, 44, uint8(170 * a)}, color.RGBA{16, 30, 16, uint8(90 * a)})
		}
		s.lamp(screen, r.X+18*u, r.Y+24*u, 6*u, nlGreen, row.inFolder || on && !leftOff)
		tc := color.RGBA{200, 192, 160, 255}
		if on {
			tc = nlCream
		}
		tw := df.Draw(screen, k.name, r.X+36*u, r.Y+30*u, screenkit.Style{Size: 18 * u, Tracking: 0.03, Top: alphaC(tc, a)})
		bf.Draw(screen, s.addonVersionText(row), r.X+44*u+tw, r.Y+29*u, screenkit.Style{Size: 11 * u, Top: alphaC(nlDim, a)})
		// The control, right-aligned and centred on the name's line.
		right, cy := r.X+r.W-14*u, r.Y+27*u
		// controlW is the width of a control tall enough to reach the second
		// line; a tag or a progress bar stays on the first.
		controlW := 0.0
		tag := func(text string, c color.RGBA) {
			df.Draw(screen, text, right, cy+5*u, screenkit.Style{Size: 11 * u, Tracking: 0.12, Top: alphaC(c, a), Upper: true, Align: 2})
		}
		switch {
		case row.inFolder:
			tag("Already in your install", nlGreenText)
		case k.installed != nil:
			controlW = 110 * u
			id := s.ui.id("addon-sw-", k.id, i, -1)
			v := onOff(s.addonOn(k.id, k.version))
			s.partSwitch(screen, id, screenkit.Rect{X: right - controlW, Y: cy - 17*u, W: controlW, H: 34 * u}, v, disabled, a, func() {
				s.setAddon(k.id, k.version, v == 0)
			})
		case job.running && k.entry != nil && job.entry.ID == k.id && job.entry.Version == k.version:
			label, frac := "Installing…", 1.0
			if !job.installing && job.total > 0 {
				frac = float64(job.done) / float64(job.total)
				label = s.ui.text(nlTextKey{kind: "addon percent", i: int(frac * 100)}, func() string { return fmt.Sprintf("%d%%", int(frac*100)) })
			}
			bar := screenkit.Rect{X: right - 150*u, Y: cy - 8*u, W: 150 * u, H: 16 * u}
			screenkit.Fill(screen, bar, color.RGBA{20, 30, 20, uint8(255 * a)})
			screenkit.Fill(screen, screenkit.Rect{X: bar.X, Y: bar.Y, W: bar.W * frac, H: bar.H}, alphaC(color.RGBA{61, 200, 92, 255}, a))
			screenkit.Outline(screen, bar, 1*u, alphaC(color.RGBA{50, 60, 46, 255}, a))
			df.Draw(screen, label, bar.X+bar.W/2, bar.Y+12*u, screenkit.Style{Size: 10 * u, Top: alphaC(nlCream, a), Align: 1})
		case on:
			// Just installed: the library is being listed again.
			tag("Installed", nlGreenText)
		case k.entry != nil && job.running:
			tag("Waiting", nlDim)
		case k.entry != nil:
			controlW = 110 * u
			entry := *k.entry
			s.button(screen, s.ui.id("addon-get-", k.id, i, -1), screenkit.Rect{X: right - controlW, Y: cy - 18*u, W: controlW, H: 36 * u}, "Get", true, false, func() { s.getAddon(entry) })
		}
		// The second line stops short of the control; a long one shrinks a
		// little before it is cut.
		// It is translated first and cut as shown, so the cut never leaves an
		// English fragment.
		sub := i18n.T(s.addonSubText(row))
		st := screenkit.Style{Size: 11 * u, Top: alphaC(color.RGBA{169, 162, 131, 255}, a), Verbatim: true}
		room := right - controlW - 16*u - (r.X + 36*u)
		for st.Size > 9.5*u && bf.Measure(sub, st) > room {
			st.Size -= 0.5 * u
		}
		if bf.Measure(sub, st) > room {
			sub = s.ui.text(nlTextKey{kind: "addon sub cut", a: sub, i: int(room)}, func() string {
				runes := []rune(sub)
				for len(runes) > 1 && bf.Measure(string(runes)+"…", st) > room {
					runes = runes[:len(runes)-1]
				}
				return strings.TrimSpace(string(runes)) + "…"
			})
		}
		bf.Draw(screen, sub, r.X+36*u, r.Y+48*u, st)
		if strip(row) {
			px := r.X + 36*u
			for _, unit := range row.units {
				if px+44*u > r.X+r.W {
					break
				}
				pr := screenkit.Rect{X: px, Y: r.Y + 58*u, W: 40 * u, H: 40 * u}
				screenkit.Fill(screen, pr, color.RGBA{6, 10, 6, uint8(220 * a)})
				if img := l.image(unit.pic); img != nil {
					screenkit.Image(screen, img, pr, a, false)
				}
				screenkit.Outline(screen, pr, 1*u, alphaC(color.RGBA{58, 58, 51, 255}, a))
				px += 46 * u
			}
		}
	}
	s.drawScrollbar(screen, "addon", &s.addonScroll, box)
	y = box.Y + box.H
	if loading {
		bf.Draw(screen, "Checking the add-on catalogue…", x+4*u, y+18*u, screenkit.Style{Size: 11 * u, Top: alphaC(nlDim, a), Shadow: 0.1})
		y += 24 * u
	}
	if l.failure != "" {
		failed := s.ui.text(nlTextKey{kind: "addon failed", a: l.failure}, func() string { return "Get failed: " + l.failure })
		bf.Draw(screen, failed, x+4*u, y+18*u, screenkit.Style{Size: 11 * u, Top: alphaC(nlAmber, a), Shadow: 0.1})
		y += 24 * u
	}
	return y
}
