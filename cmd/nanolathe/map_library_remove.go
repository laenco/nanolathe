package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
)

func (g *gameShell) mapRemovalAllowed() bool {
	return g != nil && g.cs != nil && g.frontend != nil && g.battle == nil && g.frontend.Mode != modeLoading && !g.cs.manualRoots && !mapDownload.view().running && !modDownload.view().running
}

// Both paired files must actually be supplied by the downloaded package.
// Merely sharing a catalogue basename never gives ownership of retail or mod
// content, nor does a local directory without a download receipt.
func (g *gameShell) removableLibraryMap(logical string) (*modlibrary.Library, *modlibrary.Mod) {
	if logical == "" || !g.mapRemovalAllowed() {
		return nil, nil
	}
	root, err := maplibrary.DefaultRoot()
	if err != nil {
		return nil, nil
	}
	lib := &modlibrary.Library{Root: root}
	installed, err := lib.Installed()
	if err != nil {
		return nil, nil
	}
	return lib, g.removableMapProvider(logical, installed)
}

func (g *gameShell) removableMapProvider(logical string, installed []modlibrary.Mod) *modlibrary.Mod {
	if _, err := g.cs.unmappedMount.Stat(logical); err != nil {
		return g.skippedMapProvider(logical, strings.TrimSuffix(logical, filepath.Ext(logical))+".tnt", installed)
	}
	return g.cs.mapPackage(logical, installed)
}

// mapPackage is the downloaded or saved library package that supplies both
// files of a mounted map, or nil.
func (c *contentSet) mapPackage(logical string, installed []modlibrary.Mod) *modlibrary.Mod {
	ota, err := c.unmappedMount.Stat(logical)
	if err != nil {
		return nil
	}
	tnt, err := c.unmappedMount.Stat(strings.TrimSuffix(logical, filepath.Ext(logical)) + ".tnt")
	if err != nil {
		return nil
	}
	for _, item := range installed {
		if !downloadReceipt(item) {
			continue
		}
		if c.mod != nil && modDirectoryMounted(item.Dir, []string{c.mod.Dir}) {
			continue
		}
		if pathInsideMapPackage(item.Dir, ota.Source.SourcePath) && pathInsideMapPackage(item.Dir, tnt.Source.SourcePath) {
			return &item
		}
	}
	return nil
}

func downloadReceipt(item modlibrary.Mod) bool {
	source, err := url.Parse(item.Receipt.Source)
	return err == nil && ((source.Scheme == "https" || source.Scheme == "http") && source.Host != "" || generatedReceipt(source)) && len(item.Receipt.SHA256) == 64 && item.Receipt.Size > 0
}

// generatedReceipt reports a package the map generator saved: its receipt
// names the map's code as a download's names its URL, and it is removed as
// a downloaded map is (docs/DESIGN_MAP_GENERATOR.md §10).
func generatedReceipt(source *url.URL) bool {
	return source.Scheme == "generated" && source.Opaque != ""
}

// A package the mount left out supplies nothing, so no mounted provider can
// prove ownership. It stays removable when no mounted content supplies the
// map at all and the package's own directory holds both loose files of the
// pair (DESIGN_CONTENT_VFS "Downloaded community maps").
func (g *gameShell) skippedMapProvider(logical, tntPath string, installed []modlibrary.Mod) *modlibrary.Mod {
	if _, err := g.cs.unmappedMount.Stat(tntPath); err == nil {
		return nil
	}
	for _, item := range installed {
		if !downloadReceipt(item) || g.cs.mapSkipped(item.Dir) == nil {
			continue
		}
		owned := true
		for _, name := range []string{logical, tntPath} {
			file := filepath.Join(item.Dir, filepath.FromSlash(name))
			info, err := os.Lstat(file)
			owned = owned && err == nil && info.Mode().IsRegular() && pathInsideMapPackage(item.Dir, file)
		}
		if owned {
			return &item
		}
	}
	return nil
}

func pathInsideMapPackage(root, filename string) bool {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	filename, err = filepath.EvalSymlinks(filename)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, filename)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// Eligibility belongs to the mounted content and list identity. Selection,
// scrolling and painting reuse it without scanning the filesystem. Confirmation
// and deletion still revalidate the winning providers (DESIGN_MODS_MUTATORS §5.6).
type mapRemovalRows struct {
	panel   *ui.Panel
	content *contentSet
	keys    []string
	logical []string
	targets []*modlibrary.Mod
	pressed int // row + 1; zero is idle, -1 cancels until release
}

func (g *gameShell) refreshMapRemovalRows(p *ui.Panel) {
	if p == nil || p.Index("MAPNAMES") < 0 || g.cs == nil {
		return
	}
	var logical, keys []string
	var installed []modlibrary.Mod
	catalogue := g.mapsFetchActive()
	if catalogue {
		state := mapsFetchUI
		state.mu.Lock()
		for _, entry := range state.entries {
			logical = append(logical, entry.Map)
			keys = append(keys, entry.Map+"\x00"+entry.ID+"@"+entry.Version+"\x00"+entry.Archive.SHA256)
		}
		installed = append(installed, state.installed...)
		state.mu.Unlock()
	} else {
		for _, name := range g.maps {
			logical = append(logical, "maps/"+name+".ota")
		}
		keys = logical
	}
	cache := &g.mapRemovals
	if cache.panel == p && cache.content == g.cs && slices.Equal(cache.keys, keys) {
		return
	}
	*cache = mapRemovalRows{panel: p, content: g.cs, keys: keys, logical: logical, targets: make([]*modlibrary.Mod, len(logical))}
	if g.cs.manualRoots {
		return
	}
	if !catalogue {
		root, err := maplibrary.DefaultRoot()
		if err != nil {
			return
		}
		installed, err = (&modlibrary.Library{Root: root}).Installed()
		if err != nil {
			return
		}
	}
	for i, path := range logical {
		target := g.removableMapProvider(path, installed)
		if catalogue && target != nil {
			state := mapsFetchUI
			state.mu.Lock()
			entry := state.entries[i]
			if entry.ID != target.ID || entry.Version != target.Version || !modInstalled(installed, entry) {
				target = nil
			}
			state.mu.Unlock()
		}
		cache.targets[i] = target
	}
}

// The cap uses the painter's row admission, then requires a complete cap
// inside the list interior. The same rectangle controls drawing and clicks.
func (g *gameShell) mapRemoveRowRect(p *ui.Panel, index, row int) (gui.Rect, bool) {
	cache := &g.mapRemovals
	if p == nil || cache.panel != p || cache.content != g.cs || index != p.Index("MAPNAMES") || row < 0 || row >= len(cache.targets) || cache.targets[row] == nil || !g.mapRemovalAllowed() {
		return gui.Rect{}, false
	}
	_, _, top, ok := p.ListValuesAt(index)
	if !ok || row < top {
		return gui.Rect{}, false
	}
	gad := p.Window.Gadgets[index]
	r := p.Window.PlacedRect(index)
	height := int(gad.ItemHeight)
	if height == 0 {
		height = g.retailTextHeight() + 1
	}
	offset := (row - top) * height
	if row != top && int(r.H)-offset < g.retailTextHeight() {
		return gui.Rect{}, false
	}
	cap := gui.Rect{X: r.X + r.W - 18, Y: r.Y + 2 + int32(offset), W: 16, H: int32(height - 1)}
	if cap.H < 3 || cap.X < r.X+2 || cap.Y+cap.H > r.Y+r.H-1 {
		return gui.Rect{}, false
	}
	return cap, true
}

func (g *gameShell) drawMapRemoveCap(c *client.Client, p *ui.Panel, row int, r gui.Rect) {
	down := 0
	if g.mapRemovals.pressed == row+1 {
		down = 1
	}
	v := retailButtonVerdict(gui.Gadget{}, 0, 0, down, 0, false)
	drawGUIBevel(c, r, g.guiColor(v.top), g.guiColor(v.bot), g.guiColor(v.fill))
	font := g.windowGadgetFont(p, p.Window.Gadgets[p.Index("MAPNAMES")])
	measure, metric := g.retailTextMetrics(font)
	g.drawRetailStringSelected(c, "X", int(r.X)+(int(r.W)-measure("X"))/2, int(r.Y)+(int(r.H)-metric)/2, int(r.W), g.guiColor(15), 0, font)
}

// Capture the entire pointer gesture before the list can select or activate.
// Releasing over another row or scrolling while held cancels the action.
func (g *gameShell) serviceMapRemovePointer(p *ui.Panel, frame ui.WidgetFrame) bool {
	cache := &g.mapRemovals
	if cache.panel != p {
		return false
	}
	hit := func(x, y int32) int {
		index := p.Index("MAPNAMES")
		for row := range cache.targets {
			if r, ok := g.mapRemoveRowRect(p, index, row); ok && pointInRect(x, y, r) {
				return row + 1
			}
		}
		return 0
	}
	consumed := cache.pressed != 0
	for _, event := range frame.PointerEvents {
		switch event.Kind {
		case input.LeftDown:
			if row := hit(event.X, event.Y); row != 0 {
				cache.pressed = row
				consumed = true
			}
		case input.LeftUp:
			if cache.pressed != 0 {
				row := cache.pressed
				cache.pressed = 0
				consumed = true
				if row > 0 && hit(event.X, event.Y) == row {
					g.confirmMapRemoval(cache.logical[row-1], cache.targets[row-1])
				}
			}
		}
	}
	return consumed
}

type mapRemovalConfirmation struct {
	panel   *ui.Panel
	shell   *gameShell
	logical string
	target  modlibrary.Mod
}

var pendingMapRemoval *mapRemovalConfirmation

func (g *gameShell) confirmMapRemoval(logical string, expected *modlibrary.Mod) {
	_, target := g.removableLibraryMap(logical)
	if expected == nil || target == nil || target.ID != expected.ID || target.Version != expected.Version || target.Receipt != expected.Receipt {
		return
	}
	name := strings.TrimSuffix(filepath.Base(logical), filepath.Ext(logical))
	if err := g.showRetailMessage("Delete downloaded map '" + name + "'?\nShared feature packs will be kept."); err != nil {
		reportRetailMessageError(err)
		return
	}
	old := g.frontend.Panels.Modal()
	w := gui.CloneWindow(old.Window)
	index := w.GadgetIndex("OK")
	if index < 0 {
		return
	}
	cancel := &w.Gadgets[index]
	cancel.Text = "Cancel"
	width := max(w.Rect.W, 2*cancel.Rect.W+45)
	w.Rect.W, w.Rect.X = width, (retailScreenW-width)/2
	w.OriginX = w.Rect.X
	w.Gadgets[0].Rect = w.Rect
	for i := range w.Gadgets {
		if w.Gadgets[i].Kind == gui.KindLabel {
			w.Gadgets[i].Rect.W = width
		}
	}
	cancel.Rect.X = width - cancel.Rect.W - 15
	remove := *cancel
	remove.Name, remove.SourceName, remove.Text, remove.QuickKey = "DELETEMAP", "DELETEMAP", "Delete", 0
	remove.Rect.X = 15
	w.Gadgets = append(w.Gadgets, remove)
	// Both Enter and Escape retain Cancel; deletion requires its explicit action.
	w.Header.CrDefault, w.Header.EscDefault = "OK", "OK"
	p := ui.NewPanel(w)
	g.frontend.Panels.CloseModal()
	g.frontend.Panels.PushModal(p)
	pendingMapRemoval = &mapRemovalConfirmation{panel: p, shell: g, logical: logical, target: *target}
}

func (g *gameShell) finishMapRemovalConfirmation(p *ui.Panel, action string) {
	confirmation := pendingMapRemoval
	if confirmation == nil || confirmation.panel != p || confirmation.shell != g {
		return
	}
	pendingMapRemoval = nil
	if action != "DELETEMAP" {
		return
	}
	if err := g.removeLibraryMap(confirmation.logical, confirmation.target); err != nil {
		reportRetailMessageError(g.showRetailMessage(err.Error()))
	}
}

func (g *gameShell) removeLibraryMap(logical string, target modlibrary.Mod) error {
	lib, current := g.removableLibraryMap(logical)
	if current == nil || current.ID != target.ID || current.Version != target.Version || current.Receipt != target.Receipt {
		return &missingProductError{what: "map cannot be deleted", logical: logical, expected: "the same downloaded map in an idle front end"}
	}
	selected := ""
	if g.mapIdx >= 0 && g.mapIdx < len(g.maps) {
		selected = g.maps[g.mapIdx]
	}
	// Prepare and validate the replacement mount first, then publish it and
	// close every old reader before Remove atomically moves the package away.
	old := g.cs
	if err := g.refreshMapLibraryExcluding(target.Dir); err != nil {
		if g.cs != old {
			return g.restoreMapRemovalMount(selected, err)
		}
		return err
	}
	if err := lib.Remove(target.ID, target.Version); err != nil {
		return g.restoreMapRemovalMount(selected, err)
	}

	if slices.IndexFunc(g.maps, func(s string) bool { return strings.EqualFold(s, g.setup.MapName) }) < 0 {
		// The fallback is another map, so the removed map's chosen start
		// points go with it.
		replacement := ""
		if len(g.maps) > 0 {
			replacement = g.maps[g.mapIdx]
		}
		g.setSetupMap(replacement)
	}
	mapDownload.mu.Lock()
	mapDownload.outcome, mapDownload.unseen = "", false
	mapDownload.mu.Unlock()
	if g.mapsFetchActive() {
		state := mapsFetchUI
		installed, err := lib.Installed()
		if err != nil {
			return err
		}
		state.mu.Lock()
		state.installed, state.status = installed, "Deleted "+target.Name
		state.mu.Unlock()
		g.refreshMapsFetch()
	} else {
		g.refreshMapPanel()
	}
	return nil
}

// A failed directory removal keeps the receipt/package intact. Republish its
// mount and restore the chooser, including its preview and delete visibility.
func (g *gameShell) restoreMapRemovalMount(selected string, cause error) error {
	if err := g.refreshMapLibrary(); err != nil {
		return fmt.Errorf("%w; restoring map mount failed: %v", cause, err)
	}
	if i := slices.Index(g.maps, selected); i >= 0 {
		g.mapIdx = i
	}
	if g.mapsFetchActive() {
		g.refreshMapsFetch()
	} else {
		g.refreshMapPanel()
	}
	return cause
}
