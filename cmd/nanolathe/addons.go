package main

// Official add-ons (docs/DESIGN_MODS_MUTATORS.md §5.7): Cavedog's bonus
// archives, hosted by Nanolathe and mounted with the base install at its
// *.UFO tier, apart from the mod slot, so a player can enable them and still
// select any mod. A fix add-on replaces the base patch archive and mounts
// over the content add-ons; model add-ons mount over the patch and faction
// add-ons over those (§5.8).

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/addonlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	contentprofiles "github.com/nanolathe-gg/nanolathe/internal/content/profiles"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
	"github.com/nanolathe-gg/nanolathe/internal/replay"
	"github.com/nanolathe-gg/nanolathe/internal/settings"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

func openAddonLibrary() (*modlibrary.Library, error) {
	root, err := addonlibrary.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return addonlibrary.Open(root)
}

// requestedAddons is the add-on selection a mount applies: --addon (or a
// remount's own) when given, else the saved setting, which captures,
// benchmarks and displayless runs never read.
func requestedAddons(opts Options) []settings.AddonSelection {
	if opts.AddonsSet {
		return opts.Addons
	}
	if opts.ignoresSavedSelection() {
		return nil
	}
	stored, _ := settings.Load()
	return stored.Addons
}

// addonMount is what one mount makes of the add-on selection.
type addonMount struct {
	// mounted are the add-ons this mount holds from the library.
	mounted []addonlibrary.Addon
	// selection is what the mount was asked for, each entry's version
	// resolved, kept so a remount asks for the same.
	selection []settings.AddonSelection
	// off marks a running config that leaves add-ons off.
	off   bool
	notes []string
	// factions are the archives of the mounted faction add-ons, presented
	// under the running content's layout (addonExtraLayout).
	factions map[string]bool
	// models are the archives whose audited payload contains models only.
	// They mount above the patch and below factions, under the same layout.
	models map[string]bool
}

// addonExtraLayout presents model and faction archives under the running
// content's layout, so a mod that renames retail directories reads those
// files beside its own (§5.7, §5.8 "With a mod").
func addonExtraLayout(layout vfs.Layout, factions, models map[string]bool) func(string) vfs.Layout {
	if len(layout.Names()) == 0 || len(factions)+len(models) == 0 {
		return nil
	}
	return func(full string) vfs.Layout {
		if factions[full] || models[full] {
			return layout
		}
		return vfs.Layout{}
	}
}

// addonExtraTier mounts model archives one tier above the base patch and
// factions one above that, while loose host files keep the last word
// (§5.7 "Layers and mounting", §5.8).
func addonExtraTier(factions, models map[string]bool) func(string) (vfs.MountTier, bool) {
	if len(factions)+len(models) == 0 {
		return nil
	}
	tier := vfs.DefaultRetailMountPlan().GP3 + 1
	return func(full string) (vfs.MountTier, bool) {
		if factions[full] {
			return tier + 1, true
		}
		return tier, models[full]
	}
}

// factionArchives are the archive paths of the faction add-ons in supplies.
func factionArchives(supplies []addonSupply) map[string]bool {
	out := map[string]bool{}
	for _, s := range supplies {
		if s.faction {
			for _, p := range s.addon.Paths {
				out[p] = true
			}
		}
	}
	return out
}

// modelArchives are the archive paths of the audited model add-ons.
func modelArchives(supplies []addonSupply) map[string]bool {
	out := map[string]bool{}
	for _, s := range supplies {
		if s.layer == layerModel {
			for _, p := range s.addon.Paths {
				out[p] = true
			}
		}
	}
	return out
}

// addonLayer orders the add-on kinds a mount stacks: content add-ons mount
// at their archives' tiers, a fix add-on replaces the base patch archive and
// so mounts over them, models mount over the patch, and factions above those
// (§5.7 "Layers and mounting", §5.8).
type addonLayer int

const (
	layerContent addonLayer = iota
	layerFix
	layerModel
	layerFaction
)

// addonLayerOf is the layer an add-on's archives give it: a faction
// declaration makes it a faction; otherwise a patch-tier revision archive
// makes it a fix; otherwise an audited model-only payload makes it a model
// add-on, and anything else is content (§5.7).
func addonLayerOf(addon addonlibrary.Addon, declared, modelsOnly bool) addonLayer {
	switch {
	case declared:
		return layerFaction
	case addonlibrary.ReplacesBase(addon):
		return layerFix
	case modelsOnly:
		return layerModel
	default:
		return layerContent
	}
}

// addonCandidate is an enabled add-on a mount holds, with the layer its
// archives put it in and its faction declaration.
type addonCandidate struct {
	addon    addonlibrary.Addon
	layer    addonLayer
	factions []addonlibrary.Faction
}

// resolveAddons decides which enabled add-ons a mount holds: none when the
// running config sets content.addons false; each installed one otherwise,
// except one already in the base folder, which the install supplies itself
// (a model pack still needs its model tier),
// one that defines a unit or feature name the base or the mod defines at
// another path (except a fix add-on, which repairs that content), and one
// that supplies a logical path or defines a unit or feature name an add-on
// mounted before it also does (except that a fix may replace a content
// add-on). A saved add-on that has
// gone is left out with a notice; one named by --addon fails the start, as
// --mod does (§5.7, §5.8).
func resolveAddons(opts Options, profile contentprofiles.Profile, baseRoots []string, mod *modlibrary.Mod) (addonMount, error) {
	requested := requestedAddons(opts)
	var out addonMount
	if len(requested) == 0 || runtime.GOOS == "js" || len(baseRoots) == 0 {
		return out, nil
	}
	if !profile.AddonsAllowed() {
		out.off = true
		out.selection = append([]settings.AddonSelection{}, requested...)
		out.notes = append(out.notes, "nanolathe: the running mod leaves the official add-ons off")
		return out, nil
	}
	root, err := addonlibrary.DefaultRoot()
	if err != nil {
		return out, err
	}
	installed, skipped, err := addonlibrary.Installed(root)
	if err != nil {
		if opts.AddonsSet {
			return out, err
		}
		out.notes = append(out.notes, "nanolathe: official add-ons not mounted: "+noticeReason(err))
		return out, nil
	}
	for _, s := range skipped {
		out.notes = append(out.notes, fmt.Sprintf("nanolathe: official add-on not mounted: %s; delete %s", noticeReason(s.Err), s.Dir))
	}
	var candidates []addonCandidate
	for _, want := range requested {
		addon, ok := installedAddon(installed, want.ID, want.Version)
		if !ok {
			selector := modSelectorOf(want.ID, want.Version)
			if opts.AddonsSet {
				return out, &missingProductError{what: "official add-on is not installed", logical: selector, providers: []string{root}, expected: "an add-on downloaded from the Nanolathe screen"}
			}
			out.notes = append(out.notes, fmt.Sprintf("nanolathe: the enabled official add-on %s is not installed", selector))
			continue
		}
		out.selection = append(out.selection, settings.AddonSelection{ID: addon.ID, Version: addon.Version})
		factions, err := addonlibrary.Factions(addon)
		if err != nil {
			out.notes = append(out.notes, fmt.Sprintf("nanolathe: official add-on %s not mounted: %s", addon.Name, noticeReason(err)))
			continue
		}
		modelsOnly, err := addonlibrary.ModelsOnly(addon)
		if err != nil {
			out.notes = append(out.notes, fmt.Sprintf("nanolathe: official add-on %s not mounted: %s", addon.Name, noticeReason(err)))
			continue
		}
		layer := addonLayerOf(addon, len(factions) > 0, modelsOnly)
		// A selected model pack needs its model tier even when the base
		// folder holds identical bytes: the ordinary UFO tier would leave
		// those models below the patch. Extra replaces the folder copy at
		// mount time (§5.7 "Layers and mounting").
		if _, in := addonlibrary.InFolder(baseRoots[0], addon.Archives); in && layer != layerModel {
			continue
		}
		candidates = append(candidates, addonCandidate{addon: addon, layer: layer, factions: factions})
	}
	if len(candidates) == 0 {
		return out, nil
	}
	// The layer decides the mount order whatever the selection's order says:
	// content, fix, models, factions (§5.7 "Layers and mounting").
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].layer < candidates[j].layer })
	addonsToCheck := make([]addonlibrary.Addon, 0, len(candidates))
	for _, c := range candidates {
		addonsToCheck = append(addonsToCheck, c.addon)
	}
	others, err := addonOthers(profile, baseRoots, mod, addonsToCheck)
	if err != nil {
		return out, err
	}
	defer others.close()
	// accepted are the add-ons this mount holds so far, with what each
	// supplies, read only when there is another add-on to compare it with.
	var accepted []addonSupply
	// composedSides counts the sides the mount holds so far, read from the
	// base only once a faction add-on needs it.
	composedSides := -1
	for _, candidate := range candidates {
		addon, layer := candidate.addon, candidate.layer
		names, err := addonlibrary.DefinedNames(addon)
		// A fix add-on repairs the base content wholesale: it may move a
		// definition between files, so §5.7's name check does not refuse it
		// (§5.7 "Layers and mounting").
		if err == nil && layer != layerFix {
			var found string
			if found, err = addonlibrary.Conflict(others.fs, names); err == nil && found != "" {
				err = fmt.Errorf("the installed content already defines %s at another path", found)
			}
		}
		var paths map[string]string
		if err == nil && len(candidates) > 1 {
			if paths, err = addonlibrary.Paths(addon); err == nil {
				err = addonCollision(accepted, layer, paths, names)
			}
		}
		sides := 0
		if err == nil {
			paths, sides, err = factionBaseCollision(others.fs, addon, candidate.factions, paths)
		}
		if err == nil && sides > 0 {
			if composedSides < 0 {
				composedSides = baseSideCount(others.fs)
			}
			if composedSides+sides > content.MaxComposedSides {
				err = fmt.Errorf("its %d sides would take the battle past %d sides", sides, content.MaxComposedSides)
			}
		}
		if err != nil {
			out.notes = append(out.notes, fmt.Sprintf("nanolathe: official add-on %s not mounted: %s", addon.Name, noticeReason(err)))
			continue
		}
		accepted = append(accepted, addonSupply{addon: addon, paths: paths, names: names, faction: len(candidate.factions) > 0, layer: layer})
		composedSides += sides
	}
	out.mounted = compilingAddons(profile, baseRoots, mod, accepted, &out.notes)
	out.factions = factionArchives(accepted)
	out.models = modelArchives(accepted)
	return out, nil
}

// compilingAddons is the accepted add-ons less any faction add-on the
// catalog compiler refuses with the base and the mod: a declaration whose
// names collide in a table the mount does not read, a side missing its font,
// or a reference nothing supplies would otherwise stop every start, the main
// menu's included (§5.8). With no faction add-on accepted nothing is compiled; with all of
// them compiling, one compile is the cost. When the content without the
// faction add-ons does not compile either, the faction add-ons are not to
// blame and all stay, so the start reports the failure itself.
func compilingAddons(profile contentprofiles.Profile, baseRoots []string, mod *modlibrary.Mod, accepted []addonSupply, notes *[]string) []addonlibrary.Addon {
	all := make([]addonlibrary.Addon, 0, len(accepted))
	var kept []addonlibrary.Addon
	factions := false
	for _, s := range accepted {
		all = append(all, s.addon)
		if s.faction {
			factions = true
		} else {
			kept = append(kept, s.addon)
		}
	}
	rehome, models := factionArchives(accepted), modelArchives(accepted)
	if !factions || composeAddons(profile, baseRoots, mod, all, rehome, models) == nil || composeAddons(profile, baseRoots, mod, kept, rehome, models) != nil {
		return all
	}
	for _, s := range accepted {
		if !s.faction {
			continue
		}
		trial := append(append([]addonlibrary.Addon(nil), kept...), s.addon)
		if err := composeAddons(profile, baseRoots, mod, trial, rehome, models); err != nil {
			*notes = append(*notes, fmt.Sprintf("nanolathe: official add-on %s not mounted: the game data does not compile with it: %s", s.addon.Name, noticeReason(err)))
			continue
		}
		kept = trial
	}
	// The selection's order, which the mount keeps (addonExtras).
	ordered := kept[:0:0]
	for _, a := range all {
		for _, k := range kept {
			if k.ID == a.ID && k.Version == a.Version {
				ordered = append(ordered, a)
				break
			}
		}
	}
	return ordered
}

// composeAddons compiles the catalog of the base roots and the mod with
// addons mounted as the start mounts them, model and faction archives under
// the layout.
func composeAddons(profile contentprofiles.Profile, baseRoots []string, mod *modlibrary.Mod, addons []addonlibrary.Addon, factions, models map[string]bool) error {
	replaced := map[string]bool{}
	for _, a := range addons {
		for _, archive := range a.Archives {
			replaced[strings.ToLower(archive.Name)] = true
		}
	}
	roots := make([]vfs.GameRoot, 0, len(baseRoots)+1)
	for i, dir := range baseRoots {
		root := vfs.GameRoot{Dir: dir}
		if i == 0 {
			root.Archives = func(name string) bool { return !replaced[strings.ToLower(name)] }
			root.Extra = addonExtras(addons)
			root.ExtraLayout = addonExtraLayout(profile.Layout(), factions, models)
			root.ExtraTier = addonExtraTier(factions, models)
		}
		roots = append(roots, root)
	}
	if mod != nil {
		roots = append(roots, vfs.GameRoot{Dir: mod.Dir})
	}
	mounted := vfs.New()
	defer mounted.Close()
	if err := mounted.MountGameRoots(roots); err != nil {
		return err
	}
	_, err := content.CompileWithOptions(profile.Layout().Apply(mounted), content.Options{Limits: content.LimitsFromProfile(profile.Limits)})
	return err
}

// addonSupply is an add-on a mount has accepted and what it supplies: its
// logical files and its unit and feature names.
type addonSupply struct {
	addon addonlibrary.Addon
	paths map[string]string
	names addonlibrary.Names
	// faction marks an add-on with a faction declaration, a unit pack's
	// included (§5.8).
	faction bool
	// layer is the mount layer the add-on's archives put it in (§5.7).
	layer addonLayer
}

// addonCollision says why an add-on cannot mount beside the add-ons already
// accepted: one of them supplies a logical path it also supplies, or defines
// a unit or feature name it also defines, so one would replace the other's
// (§5.8 isolation rules 1 and 2). Within a layer the add-on earlier in the
// selection keeps its own, so the outcome follows the selection's order
// alone. A fix add-on repairs the content add-ons mounted before it, so it
// may replace theirs. A model add-on may replace content and fix models;
// its audit ensures it has no other paths or definitions (§5.7).
func addonCollision(accepted []addonSupply, layer addonLayer, paths map[string]string, names addonlibrary.Names) error {
	for _, prior := range accepted {
		if layer == layerFix && prior.layer == layerContent {
			continue
		}
		if layer == layerModel && prior.layer < layerModel {
			continue
		}
		if shared := addonlibrary.SharedPath(paths, prior.paths); shared != "" {
			return fmt.Errorf("the enabled add-on %s also supplies %s", prior.addon.Name, shared)
		}
		if shared := addonlibrary.SharedName(names, prior.names); shared != "" {
			return fmt.Errorf("the enabled add-on %s already defines %s", prior.addon.Name, shared)
		}
	}
	return nil
}

// factionBaseCollision refuses a faction add-on that supplies a logical path
// the base install or the mod also supplies: a faction never replaces a base
// file (§5.8 isolation rule 1). An add-on without a declaration keeps §5.7's
// name check alone. factions is the add-on's declaration, read when it
// became a candidate; paths is its path set when already read; the result is
// it, read here if a faction needed it, and the number of sides the add-on
// declares.
func factionBaseCollision(others vfs.FSOps, addon addonlibrary.Addon, factions []addonlibrary.Faction, paths map[string]string) (map[string]string, int, error) {
	if len(factions) == 0 {
		return paths, 0, nil
	}
	sides := 0
	for _, f := range factions {
		sides += len(f.Sides)
	}
	if paths == nil {
		var err error
		if paths, err = addonlibrary.Paths(addon); err != nil {
			return nil, 0, err
		}
	}
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if info, err := others.Stat(key); err == nil && !info.IsDir {
			return paths, 0, fmt.Errorf("the installed content already supplies %s", paths[key])
		}
	}
	return paths, sides, nil
}

// baseSideCount is how many sides the content without add-ons defines;
// zero when they do not compile, which the start reports itself.
func baseSideCount(others vfs.FSOps) int {
	sides, err := content.CompileSides(others)
	if err != nil {
		return 0
	}
	return len(sides)
}

// installedAddon finds an installed add-on by id and version; an empty
// version selects the last one the library lists.
func installedAddon(installed []addonlibrary.Addon, id, version string) (addonlibrary.Addon, bool) {
	var found addonlibrary.Addon
	ok := false
	for _, a := range installed {
		if a.ID == id && (version == "" || a.Version == version) {
			found, ok = a, true
		}
	}
	return found, ok
}

// addonOthersFS is the content an add-on is checked against: the base roots
// and the mod, without the add-ons' archives or any of the base folder's of
// the same names, which they would replace.
type addonOthersFS struct {
	fs      vfs.FSOps
	mounted *vfs.FS
}

func (o addonOthersFS) close() { o.mounted.Close() }

func addonOthers(profile contentprofiles.Profile, baseRoots []string, mod *modlibrary.Mod, addons []addonlibrary.Addon) (addonOthersFS, error) {
	replaced := map[string]bool{}
	for _, a := range addons {
		for _, archive := range a.Archives {
			replaced[strings.ToLower(archive.Name)] = true
		}
	}
	roots := make([]vfs.GameRoot, 0, len(baseRoots)+1)
	for i, dir := range baseRoots {
		root := vfs.GameRoot{Dir: dir}
		if i == 0 {
			root.Archives = func(name string) bool { return !replaced[strings.ToLower(name)] }
		}
		roots = append(roots, root)
	}
	if mod != nil {
		roots = append(roots, vfs.GameRoot{Dir: mod.Dir})
	}
	mounted := vfs.New()
	if err := mounted.MountGameRoots(roots); err != nil {
		mounted.Close()
		return addonOthersFS{}, err
	}
	return addonOthersFS{fs: profile.Layout().Apply(mounted), mounted: mounted}, nil
}

// addonExtras are the archive files the mount adds to the base install's
// folder.
func addonExtras(addons []addonlibrary.Addon) []string {
	var paths []string
	for _, a := range addons {
		paths = append(paths, a.Paths...)
	}
	return paths
}

// remountAddons sets a remount's options to the add-ons the running content
// was asked for, so the fresh set holds what the battle ran on.
func (c *contentSet) remountAddons(opts *Options) {
	if c == nil {
		return
	}
	opts.Addons, opts.AddonsSet = append([]settings.AddonSelection{}, c.addonSelection...), true
}

func printNotes(notes []string) {
	for _, note := range notes {
		fmt.Fprintln(os.Stderr, note)
	}
}

// addonIdentity is an add-on a save or a replay recorded: its id, version
// and content identity (addonlibrary.Digest).
type addonIdentity struct {
	id, version string
	digest      [32]byte
}

// addonsSatisfied reports whether cs plays exactly the recorded add-ons:
// each mounted from the library or held by the base folder, and no other
// add-on mounted. known name the archives a folder copy is checked against.
func addonsSatisfied(cs *contentSet, want []addonIdentity, known []knownAddon) bool {
	wanted := make(map[addonIdentity]bool, len(want))
	for _, w := range want {
		wanted[w] = true
	}
	mounted := make(map[addonIdentity]bool, len(cs.addons))
	for _, a := range cs.addons {
		id := addonIdentity{a.ID, a.Version, addonlibrary.Digest(a.Archives)}
		if !wanted[id] {
			return false
		}
		mounted[id] = true
	}
	for _, w := range want {
		if !mounted[w] && !addonInFolder(cs, w, known) {
			return false
		}
	}
	return true
}

func addonInFolder(cs *contentSet, w addonIdentity, known []knownAddon) bool {
	for _, k := range known {
		if k.id == w.id && k.version == w.version && addonlibrary.Digest(k.archives) == w.digest {
			if paths, in := addonlibrary.InFolder(cs.root, k.archives); in {
				// A model UFO in the folder alone is below the patch. A
				// restore must select its installed copy to regain the model
				// tier; a revision archive already has its fix tier (§5.7).
				addon := addonlibrary.Addon{Archives: k.archives, Paths: paths}
				if addonlibrary.ReplacesBase(addon) {
					return true
				}
				modelsOnly, err := addonlibrary.ModelsOnly(addon)
				return err == nil && !modelsOnly
			}
		}
	}
	return false
}

// addonSelectionFor is the add-on selection that plays exactly want on this
// install: each recorded add-on from the library, except one the base
// folder holds. missing is the first it can find nowhere.
func addonSelectionFor(cs *contentSet, want []addonIdentity, known []knownAddon) (selection []settings.AddonSelection, missing *addonIdentity) {
	selection = []settings.AddonSelection{}
	for i, w := range want {
		if addonInFolder(cs, w, known) {
			continue
		}
		found := false
		for _, k := range known {
			if k.installed != nil && k.id == w.id && k.version == w.version && addonlibrary.Digest(k.archives) == w.digest {
				selection, found = append(selection, settings.AddonSelection{ID: k.id, Version: k.version}), true
				break
			}
		}
		if !found {
			return nil, &want[i]
		}
	}
	return selection, nil
}

// replayAddonsOf is a replay header's list of the add-ons cs mounts, in id
// order (§5.7).
func replayAddonsOf(cs *contentSet) []replay.Addon {
	var out []replay.Addon
	for _, a := range cs.addons {
		out = append(out, replay.Addon{ID: a.ID, Version: a.Version, Size: addonlibrary.Size(a.Archives), Digest: addonlibrary.Digest(a.Archives)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func replayAddonIdentities(addons []replay.Addon) []addonIdentity {
	out := make([]addonIdentity, len(addons))
	for i, a := range addons {
		out[i] = addonIdentity{a.ID, a.Version, a.Digest}
	}
	return out
}
