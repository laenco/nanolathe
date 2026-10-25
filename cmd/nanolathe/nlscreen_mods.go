package main

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/modfetch"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// Getting and removing mods on the Nanolathe screen
// (docs/DESIGN_MODS_MUTATORS.md §8.2): a popup lists the online catalogue
// with a Download button per mod, sharing the process's one download job
// with the Mods & Mutators window; the content list removes an installed
// mod after a confirmation. Installing never selects the mod.

const nlContentVisible = 5

// Scroll and selection have separate ownership: browsing the installed list
// does not change the draft, and selecting reveals just that row
// (docs/DESIGN_INTERFACE_HUD_INPUT.md §3.17).
func (s *nlScreen) scrollContent(top int) {
	s.contentTop = max(0, min(top, len(s.mods)+1-nlContentVisible))
}

func (s *nlScreen) revealContent(selected int) {
	if selected < s.contentTop {
		s.contentTop = selected
	} else if selected >= s.contentTop+nlContentVisible {
		s.contentTop = selected - nlContentVisible + 1
	}
	s.scrollContent(s.contentTop)
}

// nlCatalog is the popup's catalogue fetch, filled by a worker.
type nlCatalog struct {
	mu      sync.Mutex
	status  string
	entries []modfetch.Entry
	cancel  context.CancelFunc
}

// openCatalog shows the popup and starts the catalogue fetch.
func (s *nlScreen) openCatalog() {
	lib, err := openModLibrary()
	if err != nil {
		s.toast, s.toastLeft = err.Error(), 3
		return
	}
	s.dialog = "catalog"
	c := &nlCatalog{status: "Fetching the catalogue…"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c.cancel = cancel
	s.catalog = c
	client := &modfetch.Client{CatalogURL: modfetch.CatalogURL(), CacheDir: lib.Root}
	go func() {
		defer cancel()
		result, err := client.FetchManifest(ctx)
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			c.status = "The catalogue is unavailable: " + err.Error()
			return
		}
		c.entries = result.Manifest.Mods
		c.status = fmt.Sprintf("%d mods in the catalogue", len(c.entries))
		if result.FromCache {
			c.status = "Offline: the catalogue from " + result.FetchedAt.Local().Format("2 Jan 15:04")
		}
	}()
}

func (s *nlScreen) closeCatalog() {
	if s.catalog != nil && s.catalog.cancel != nil {
		s.catalog.cancel()
	}
	modDownload.acknowledge()
	s.dialog = ""
}

// downloadMod starts the one download job for entry: download, then install
// into the library, the same steps the Mods & Mutators window takes.
func (s *nlScreen) downloadMod(entry modfetch.Entry) {
	g := s.shell()
	lib, err := openModLibrary()
	if err != nil || g == nil {
		return
	}
	if err := g.startCatalogueDownload(lib, entry); err != nil {
		s.toast, s.toastLeft = noticeReason(err), 3
	}
}

// removeMod deletes an installed mod after the confirmation; the running
// mod cannot be removed.
func (s *nlScreen) removeMod(m modlibrary.Mod) {
	g := s.shell()
	if g == nil || sameMod(&m, g.cs.mod) {
		s.toast, s.toastLeft = "The running mod cannot be removed; switch content first", 3
		return
	}
	lib, err := openModLibrary()
	if err == nil {
		err = lib.Remove(m.ID, m.Version)
	}
	if err != nil {
		s.toast, s.toastLeft = err.Error(), 3.5
		return
	}
	selected := s.draft.mod
	chosen := s.modAt(selected)
	s.reloadMods(g)
	s.draft.mod = 0
	for i := range s.mods {
		if chosen != nil && sameMod(&s.mods[i], chosen) {
			s.draft.mod = i + 1
		}
	}
	if s.draft.mod != selected {
		s.revealContent(s.draft.mod)
	}
	s.toast, s.toastLeft = fmt.Sprintf("Removed %s %s", m.Name, m.Version), 2.5
}

// pollInstalls re-reads the library when the download job installed a mod.
func (s *nlScreen) pollInstalls() {
	v := modDownload.view()
	if v.installs == s.installsSeen {
		return
	}
	s.installsSeen = v.installs
	if g := s.shell(); g != nil {
		selected := s.draft.mod
		chosen := s.modAt(selected)
		s.reloadMods(g)
		s.draft.mod = 0
		for i := range s.mods {
			if chosen != nil && sameMod(&s.mods[i], chosen) {
				s.draft.mod = i + 1
			}
		}
		if s.draft.mod != selected {
			s.revealContent(s.draft.mod)
		}
	}
}

// drawCatalog is the popup: the catalogue, one row per mod version.
func (s *nlScreen) drawCatalog(screen *ebiten.Image) {
	u := s.u()
	screenkit.Fill(screen, screenkit.Rect{W: s.w(), H: s.h()}, color.RGBA{0, 0, 0, 160})
	s.hits.Add(screenkit.Region{ID: "catalog-block", Rect: screenkit.Rect{W: s.w(), H: s.h()}})
	w, h := 860*u, 700*u
	r := screenkit.Rect{X: s.w()/2 - w/2, Y: s.h()/2 - h/2, W: w, H: h}
	s.well(screen, r, 1)
	df, bf := s.fonts.Display, s.fonts.Body
	df.Draw(screen, "Get more mods", r.X+32*u, r.Y+52*u, screenkit.Style{Size: 30 * u, Tracking: 0.04, Top: nlGoldTop, Bottom: nlGoldBottom})
	c := s.catalog
	c.mu.Lock()
	status, entries := c.status, append([]modfetch.Entry(nil), c.entries...)
	c.mu.Unlock()
	bf.Draw(screen, status, r.X+32*u, r.Y+80*u, screenkit.Style{Size: 12 * u, Top: nlBody})
	job := modDownload.view()
	// Rows fill the space above Close; more mods than fit scroll, with the
	// scrollbar beside the rows.
	rowH := 84 * u
	list := screenkit.Rect{X: r.X + 28*u, Y: r.Y + 100*u, W: w - 56*u, H: h - 180*u}
	shown := min(len(entries), max(1, int(list.H/rowH)))
	if shown < len(entries) {
		list.W -= 50 * u
	}
	list.H = float64(shown) * rowH
	s.catalogScroll.layout(len(entries), shown, list)
	y := list.Y
	for i := s.catalogScroll.top; i < s.catalogScroll.top+shown; i++ {
		e := entries[i]
		rr := screenkit.Rect{X: list.X, Y: y, W: list.W, H: rowH - 8*u}
		screenkit.Fill(screen, rr, color.RGBA{8, 12, 8, 220})
		screenkit.Outline(screen, rr, 1*u, color.RGBA{50, 60, 46, 255})
		tw := df.Draw(screen, e.Name, rr.X+16*u, rr.Y+28*u, screenkit.Style{Size: 18 * u, Tracking: 0.03, Top: nlCream})
		bf.Draw(screen, fmt.Sprintf("%s  ·  %.1f MB", e.Version, float64(e.Archive.Size)/1e6), rr.X+26*u+tw, rr.Y+27*u, screenkit.Style{Size: 11 * u, Top: nlDim})
		// Right side: installed, the running download, or a button. The
		// summary wraps in the room left of it, cut on its second line.
		bx := rr.X + rr.W - 170*u
		st := screenkit.Style{Size: 11 * u, Top: nlBody}
		room := bx - 16*u - (rr.X + 16*u)
		lines := s.ui.wrap(bf, e.Summary, st, room)
		st.Verbatim = true // the wrapped lines are translated already
		for li, line := range lines[:min(2, len(lines))] {
			if li == 1 && len(lines) > 2 {
				line = s.ui.text(nlTextKey{kind: "catalog summary cut", a: e.Summary, i: int(room)}, func() string {
					runes := []rune(line)
					for len(runes) > 1 && bf.Measure(string(runes)+"…", st) > room {
						runes = runes[:len(runes)-1]
					}
					return strings.TrimSpace(string(runes)) + "…"
				})
			}
			bf.Draw(screen, line, rr.X+16*u, rr.Y+52*u+float64(li)*16*u, st)
		}
		installed := modInstalled(s.mods, e)
		switch {
		case installed:
			df.Draw(screen, "Installed", rr.X+rr.W-24*u, rr.Y+rr.H/2+5*u, screenkit.Style{Size: 12 * u, Tracking: 0.16, Top: nlGreenText, Upper: true, Align: 2})
		case modUpdateMounted(e, s.shell().cs.mod):
			bf.Draw(screen, modUpdateSwitchNotice, rr.X+rr.W-24*u, rr.Y+rr.H/2+5*u, screenkit.Style{Size: 10 * u, Top: nlAmber, Align: 2})
		case job.running && job.entry.ID == e.ID && job.entry.Version == e.Version:
			label := "Installing…"
			frac := 1.0
			if !job.installing && job.total > 0 {
				frac = float64(job.done) / float64(job.total)
				label = fmt.Sprintf("%d%%", int(frac*100))
			}
			bar := screenkit.Rect{X: bx, Y: rr.Y + rr.H/2 - 8*u, W: 150 * u, H: 16 * u}
			screenkit.Fill(screen, bar, color.RGBA{20, 30, 20, 255})
			screenkit.Fill(screen, screenkit.Rect{X: bar.X, Y: bar.Y, W: bar.W * frac, H: bar.H}, color.RGBA{61, 200, 92, 255})
			df.Draw(screen, label, bar.X+bar.W/2, bar.Y+12*u, screenkit.Style{Size: 10 * u, Top: nlCream, Align: 1})
		default:
			enabled := !job.running
			id := s.ui.id("catalog-get", "", i, -1)
			if enabled {
				s.button(screen, id, screenkit.Rect{X: bx, Y: rr.Y + rr.H/2 - 18*u, W: 150 * u, H: 36 * u}, "Download", true, false, func() { s.downloadMod(e) })
			} else {
				df.Draw(screen, "Waiting", rr.X+rr.W-24*u, rr.Y+rr.H/2+5*u, screenkit.Style{Size: 12 * u, Tracking: 0.16, Top: nlDim, Upper: true, Align: 2})
			}
		}
		y += rowH
	}
	s.drawScrollbar(screen, "catalog", &s.catalogScroll, list)
	if job.outcome != "" {
		bf.Draw(screen, job.outcome, r.X+32*u, r.Y+h-44*u, screenkit.Style{Size: 12 * u, Top: nlAmber})
	}
	s.button(screen, "catalog-close", screenkit.Rect{X: r.X + w - 32*u - 130*u, Y: r.Y + h - 66*u, W: 130 * u, H: 40 * u}, "Close", false, false, s.closeCatalog)
}

// drawRemoveConfirm asks before a mod is deleted.
func (s *nlScreen) drawRemoveConfirm(screen *ebiten.Image) {
	u := s.u()
	m := s.removing
	screenkit.Fill(screen, screenkit.Rect{W: s.w(), H: s.h()}, color.RGBA{0, 0, 0, 150})
	s.hits.Add(screenkit.Region{ID: "remove-block", Rect: screenkit.Rect{W: s.w(), H: s.h()}})
	w, h := 600*u, 220*u
	r := screenkit.Rect{X: s.w()/2 - w/2, Y: s.h()/2 - h/2, W: w, H: h}
	s.well(screen, r, 1)
	s.fonts.Display.Draw(screen, fmt.Sprintf("Remove %s %s?", m.Name, m.Version), r.X+32*u, r.Y+56*u, screenkit.Style{Size: 24 * u, Tracking: 0.04, Top: nlAmber, Upper: true})
	s.drawWrapped(screen, s.fonts.Body, "Its files are deleted from the mod library. A saved game that uses it will offer to download it again.", r.X+32*u, r.Y+84*u, w-64*u, 2.1, screenkit.Style{Size: 12.5 * u, Top: nlBody})
	bw := 150 * u
	s.button(screen, "remove-yes", screenkit.Rect{X: r.X + w - 32*u - bw, Y: r.Y + h - 66*u, W: bw, H: 42 * u}, "Remove", true, false, func() {
		s.dialog = ""
		s.removeMod(m)
	})
	s.button(screen, "remove-no", screenkit.Rect{X: r.X + w - 46*u - 2*bw, Y: r.Y + h - 66*u, W: bw, H: 42 * u}, "Keep", false, false, func() { s.dialog = "" })
}
