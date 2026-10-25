package main

// Official add-ons in online rooms (docs/DESIGN_MULTIPLAYER.md §16.8,
// DESIGN_MODS_MUTATORS §5.7). A room lists each add-on its battle mounts by
// id, version and content identity — one the host mounts from its library,
// or one whose archives sit byte-identical in the host's folder — and every
// seat mounts the listed add-ons in their audited local layers: its own
// copy, the library's, or one the lobby downloads from the add-on catalogue
// and enables.

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/addonlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/modfetch"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
)

// addonCatalogueTimeout bounds the add-on catalogue lookup an online job
// makes; a slow catalogue only leaves an add-on unnamed or unfetched.
const addonCatalogueTimeout = 10 * time.Second

// knownAddon is an official add-on whose archives this seat can name: one
// the add-on library holds, or one the add-on catalogue offers, or both.
type knownAddon struct {
	id, version, name string
	archives          []addonlibrary.Archive
	installed         *addonlibrary.Addon
	entry             *modfetch.Entry
}

// addonRoomEntry is an add-on's field-16 entry: its id and version, its
// archives' total size and its content identity (addonlibrary.Digest).
func addonRoomEntry(id, version string, archives []addonlibrary.Archive) session.MatchContentEntry {
	return session.MatchContentEntry{Kind: session.MatchContentAddon, Name: id, Version: version, Size: addonlibrary.Size(archives), Digest: addonlibrary.Digest(archives)}
}

func (k knownAddon) is(e session.MatchContentEntry) bool {
	return e.Kind == session.MatchContentAddon && addonRoomEntry(k.id, k.version, k.archives) == e
}

// catalogueAddonArchives are the archives a catalogue add-on lists.
func catalogueAddonArchives(e modfetch.Entry) ([]addonlibrary.Archive, error) {
	out := make([]addonlibrary.Archive, len(e.Contents))
	for i, c := range e.Contents {
		b, err := hex.DecodeString(c.SHA256)
		if err != nil || len(b) != len(out[i].SHA256) {
			return nil, fmt.Errorf("nanolathe: add-on %s lists an unreadable digest for %s", e.ID, c.Name)
		}
		out[i] = addonlibrary.Archive{Name: c.Name, Size: c.Size}
		copy(out[i].SHA256[:], b)
	}
	return out, nil
}

// knownAddons lists the add-ons this seat can name: the library's, then the
// catalogue's — fetched live when network is set, else the cached copy. An
// unreadable library or catalogue only names fewer.
func knownAddons(ctx context.Context, network bool) []knownAddon {
	if runtime.GOOS == "js" {
		return nil
	}
	root, err := addonlibrary.DefaultRoot()
	if err != nil {
		return nil
	}
	var out []knownAddon
	if installed, _, err := addonlibrary.Installed(root); err == nil {
		for i := range installed {
			a := installed[i]
			out = append(out, knownAddon{id: a.ID, version: a.Version, name: a.Name, archives: a.Archives, installed: &a})
		}
	}
	client := &modfetch.Client{CatalogURL: modfetch.AddonCatalogURL(), CacheDir: root}
	var manifest modfetch.Manifest
	ok := false
	if network {
		ctx, cancel := context.WithTimeout(ctx, addonCatalogueTimeout)
		defer cancel()
		if result, err := client.FetchManifest(ctx); err == nil {
			manifest, ok = result.Manifest, true
		}
	} else {
		manifest, _, ok = client.CachedManifest()
	}
	if !ok {
		return out
	}
	for i := range manifest.Addons {
		e := manifest.Addons[i]
		archives, err := catalogueAddonArchives(e)
		if err != nil {
			continue
		}
		listed := false
		for j := range out {
			if out[j].id == e.ID && out[j].version == e.Version && addonlibrary.Digest(out[j].archives) == addonlibrary.Digest(archives) {
				out[j].entry, listed = &e, true
			}
		}
		if !listed {
			out = append(out, knownAddon{id: e.ID, version: e.Version, name: e.Name, archives: archives, entry: &e})
		}
	}
	return out
}

// hostAskedAddonCatalogue records that a room's creation asked the live
// add-on catalogue in this run, so an install whose folder holds *.UFO
// archives no add-on claims — every install's Example.ufo among them — waits
// on an unreachable catalogue at most once.
var hostAskedAddonCatalogue atomic.Bool

// hostNeedsAddonCatalogue reports whether the host's folder holds a *.UFO
// archive that is no known add-on's and could be an official add-on's copy,
// when no catalogue, cached or live, has been read to say and this run has
// not asked yet.
func hostNeedsAddonCatalogue(archives []localArchive, known []knownAddon) bool {
	for _, k := range known {
		if k.entry != nil {
			return false
		}
	}
	if hostAskedAddonCatalogue.Load() {
		return false
	}
	for _, a := range archives {
		if a.mapOnly || !addonlibrary.ArchiveName(a.entry.Name) {
			continue
		}
		claimed := false
		for _, k := range known {
			for _, archive := range k.archives {
				claimed = claimed || strings.EqualFold(archive.Name, a.entry.Name)
			}
		}
		if !claimed {
			return !hostAskedAddonCatalogue.Swap(true)
		}
	}
	return false
}

// refreshKnownAddons is known after a remount: the library and cached
// catalogue as they are now, keeping the catalogue entries known held, for
// a room that lists add-ons; a room without any keeps known as it is.
func refreshKnownAddons(known []knownAddon, entries []session.MatchContentEntry) []knownAddon {
	listed := false
	for _, e := range entries {
		listed = listed || e.Kind == session.MatchContentAddon
	}
	if !listed {
		return known
	}
	fresh := knownAddons(context.Background(), false)
	for _, old := range known {
		if old.entry == nil {
			continue
		}
		found := false
		for i := range fresh {
			if fresh[i].id == old.id && fresh[i].version == old.version && addonlibrary.Digest(fresh[i].archives) == addonlibrary.Digest(old.archives) {
				found = true
				if fresh[i].entry == nil {
					fresh[i].entry = old.entry
				}
			}
		}
		if !found {
			fresh = append(fresh, knownAddon{id: old.id, version: old.version, name: old.name, archives: old.archives, entry: old.entry})
		}
	}
	return fresh
}

// roomAddonsUnknown reports whether a room lists an add-on that cs does not
// mount and known cannot name, which only the live catalogue may offer.
func roomAddonsUnknown(cs *contentSet, known []knownAddon, entries []session.MatchContentEntry) bool {
	for _, e := range entries {
		if e.Kind != session.MatchContentAddon || mountedAddon(cs, e) != nil {
			continue
		}
		named := false
		for _, k := range known {
			named = named || k.is(e) && (k.installed != nil || k.entry != nil)
		}
		if !named {
			return true
		}
	}
	return false
}

// onlineKnownAddons is what an online job knows of add-ons: the library's
// and the cached catalogue's, and the live catalogue's when need says the
// cached knowledge may not be enough.
func onlineKnownAddons(ctx context.Context, need func([]knownAddon) bool) []knownAddon {
	known := knownAddons(ctx, false)
	if need(known) {
		known = knownAddons(ctx, true)
	}
	return known
}

// mountedAddon is the add-on cs mounts with e's identity, or nil.
func mountedAddon(cs *contentSet, e session.MatchContentEntry) *addonlibrary.Addon {
	if cs == nil {
		return nil
	}
	for i := range cs.addons {
		a := &cs.addons[i]
		if addonRoomEntry(a.ID, a.Version, a.Archives) == e {
			return a
		}
	}
	return nil
}

// folderAddonPaths are the paths of local archives holding byte-identical
// copies of every one of archives, matched by file name without regard to
// case.
func folderAddonPaths(local map[string]localArchive, archives []addonlibrary.Archive) ([]string, bool) {
	paths := make([]string, 0, len(archives))
	for _, want := range archives {
		a, ok := local[strings.ToLower(want.Name)]
		if !ok || a.entry.Size != uint64(want.Size) || a.entry.Digest != want.SHA256 {
			return nil, false
		}
		paths = append(paths, a.path)
	}
	return paths, len(paths) > 0
}

// roomAddons are the add-ons a host lists (§16.8): each the content mounts
// from the library, and each known add-on whose archives sit in the host's
// folder, by file name in lower case, which then are not listed as base
// archives.
func roomAddons(cs *contentSet, archives []localArchive, known []knownAddon) ([]session.MatchContentEntry, map[string]bool) {
	var entries []session.MatchContentEntry
	inFolder := make(map[string]bool)
	listed := make(map[string]bool)
	for _, a := range cs.addons {
		entries = append(entries, addonRoomEntry(a.ID, a.Version, a.Archives))
		listed[a.ID] = true
	}
	local := make(map[string]localArchive, len(archives))
	for _, a := range archives {
		local[strings.ToLower(a.entry.Name)] = a
	}
	for _, k := range known {
		if listed[k.id] {
			continue
		}
		if _, ok := folderAddonPaths(local, k.archives); !ok {
			continue
		}
		entries = append(entries, addonRoomEntry(k.id, k.version, k.archives))
		listed[k.id] = true
		for _, archive := range k.archives {
			inFolder[strings.ToLower(archive.Name)] = true
		}
	}
	return entries, inFolder
}

// resolveRoomAddon sorts one listed add-on (§16.8): present when cs mounts
// it or this seat's folder holds its archives; present for the battle, and
// to be enabled, when the library holds it unmounted; fetched when the
// catalogue offers it; missing otherwise.
func resolveRoomAddon(cs *contentSet, local map[string]localArchive, known []knownAddon, e session.MatchContentEntry, plan *roomContentPlan) {
	if a := mountedAddon(cs, e); a != nil {
		plan.addons = append(plan.addons, *a)
		return
	}
	for _, k := range known {
		if !k.is(e) {
			continue
		}
		if paths, ok := folderAddonPaths(local, k.archives); ok {
			plan.addons = append(plan.addons, addonlibrary.Addon{ID: k.id, Version: k.version, Name: k.name, Archives: k.archives, Paths: paths})
			return
		}
	}
	for _, k := range known {
		if k.is(e) && k.installed != nil {
			plan.addons = append(plan.addons, *k.installed)
			plan.enable = append(plan.enable, settings.AddonSelection{ID: k.id, Version: k.version})
			return
		}
	}
	for _, k := range known {
		if k.is(e) && k.entry != nil {
			plan.fetch = append(plan.fetch, *k.entry)
			break
		}
	}
	plan.missing = append(plan.missing, e)
}

// addonDownload is the process's one add-on download and install, shared
// by the Nanolathe screen and the lobby.
var addonDownload modDownloadJob

// startAddonDownload downloads a catalogue add-on and installs it into the
// library, checking every archive against the catalogue's contents. It
// returns false when another add-on download holds the job.
func startAddonDownload(entry modfetch.Entry) (bool, error) {
	expect, err := catalogueAddonArchives(entry)
	if err != nil {
		return false, err
	}
	lib, err := openAddonLibrary()
	if err != nil {
		return false, err
	}
	client := &modfetch.Client{CatalogURL: modfetch.AddonCatalogURL(), CacheDir: lib.Root}
	dst := filepath.Join(lib.Root, ".downloads", entry.ArchiveName())
	started := addonDownload.start(entry, func(ctx context.Context, progress func(done, total int64)) error {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return client.Download(ctx, entry, dst, progress)
	}, func() error {
		options := entry.InstallOptions()
		options.Validate = addonlibrary.Validator(expect)
		_, err := lib.InstallArchive(dst, options)
		if err == nil {
			_ = os.Remove(dst)
		}
		return err
	})
	return started, nil
}

// enableAddons adds add-ons to the running selection and to the saved one,
// saves it and remounts the content, keeping the shell, as a More maps
// install remounts (§5.7: installing an add-on enables it). The saved
// setting gains only add; an --addon choice for this run stays this run's.
func (g *gameShell) enableAddons(add ...settings.AddonSelection) error {
	g.opts.Addons = settings.NormalizeAddons(append(append([]settings.AddonSelection{}, g.cs.addonSelection...), add...))
	g.opts.AddonsSet = true
	g.addonSetting = settings.NormalizeAddons(append(append([]settings.AddonSelection{}, g.addonSetting...), add...))
	g.saveSettings()
	return g.refreshMapLibrary()
}

// roomAddonFetch is a lobby seat's download or enabling of the add-ons the
// room lists and its content does not mount.
type roomAddonFetch struct {
	// want is the catalogue entry downloading, nil when none is.
	want     *modfetch.Entry
	installs int
	// enabled are the add-ons already enabled for this room, so one the
	// mount still leaves out is not enabled again; failed records why an
	// add-on could not be fetched, by id and version.
	enabled map[string]bool
	failed  map[string]string
}

func addonKey(id, version string) string { return id + "@" + version }

// pollRoomAddonFetch advances the room's add-ons: it enables installed ones
// the content does not mount, starts a download for one the catalogue
// offers, and once that installs, enables it. Each remount resolves the
// room again (refreshOnlineRoomContent). It reports whether the lobby's
// status may have changed.
func (g *gameShell) pollRoomAddonFetch() bool {
	r := &g.online.room
	f := &r.addonFetch
	if f.want != nil {
		v := addonDownload.view()
		switch {
		case v.running:
			return true
		case v.installs == f.installs:
			reason := strings.TrimPrefix(v.outcome, "Failed: ")
			if reason == "" {
				reason = "the download stopped"
			}
			f.fail(addonKey(f.want.ID, f.want.Version), reason)
			f.want = nil
			return true
		}
		want := *f.want
		f.want = nil
		g.enableRoomAddon(settings.AddonSelection{ID: want.ID, Version: want.Version})
		return true
	}
	var enable []settings.AddonSelection
	for _, a := range r.content.enable {
		if key := addonKey(a.ID, a.Version); !f.enabled[key] {
			enable = append(enable, a)
		}
	}
	if len(enable) > 0 {
		g.enableRoomAddon(enable...)
		return true
	}
	for i := range r.content.fetch {
		entry := r.content.fetch[i]
		key := addonKey(entry.ID, entry.Version)
		if _, failed := f.failed[key]; failed || f.enabled[key] {
			continue
		}
		if addonDownload.view().running {
			return false // another add-on download holds the job
		}
		installs := addonDownload.view().installs
		started, err := startAddonDownload(entry)
		if err != nil {
			f.fail(key, noticeReason(err))
			return true
		}
		if started {
			f.want, f.installs = &entry, installs
		}
		return true
	}
	return false
}

// enableRoomAddon enables add-ons for the room and remounts; a remount that
// fails is named as the add-on's reason.
func (g *gameShell) enableRoomAddon(add ...settings.AddonSelection) {
	f := &g.online.room.addonFetch
	if f.enabled == nil {
		f.enabled = make(map[string]bool)
	}
	for _, a := range add {
		f.enabled[addonKey(a.ID, a.Version)] = true
	}
	if err := g.enableAddons(add...); err != nil {
		for _, a := range add {
			f.fail(addonKey(a.ID, a.Version), noticeReason(err))
		}
	}
}

func (f *roomAddonFetch) fail(key, reason string) {
	if f.failed == nil {
		f.failed = make(map[string]string)
	}
	f.failed[key] = reason
}

// roomAddonFetchText is the lobby's line while an add-on downloads.
func (g *gameShell) roomAddonFetchText() string {
	f := &g.online.room.addonFetch
	if f.want == nil {
		return ""
	}
	v := addonDownload.view()
	if !v.running || v.total <= 0 {
		return "Getting " + f.want.Name + " for this game..."
	}
	return fmt.Sprintf("Downloading %s for this game... %d%%", f.want.Name, v.done*100/v.total)
}

// roomAddonFailures names why listed add-ons could not be fetched.
func (g *gameShell) roomAddonFailures() string {
	r := &g.online.room
	var reasons []string
	for _, e := range r.content.missing {
		if e.Kind != session.MatchContentAddon {
			continue
		}
		if reason, ok := r.addonFetch.failed[addonKey(e.Name, e.Version)]; ok {
			reasons = append(reasons, fmt.Sprintf("%s could not be downloaded: %s.", joinNames(roomContentNames([]session.MatchContentEntry{e})), reason))
		}
	}
	return strings.Join(reasons, " ")
}

// libraryAddonsOf lists the library's add-ons as known add-ons, with no
// network, for a replay's room content.
func libraryAddonsOf() []knownAddon { return knownAddons(context.Background(), false) }
