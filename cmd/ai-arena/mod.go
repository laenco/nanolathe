package main

import (
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/content/profiles"
	"github.com/nanolathe-gg/nanolathe/internal/install"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
)

// resolveArenaMod applies -mod as nanolathe-headless applies --mod
// (docs/DESIGN_MODS_MUTATORS.md §4.3): `none` or an empty selector selects
// no mod; otherwise the installed mod is looked up against the resolved base
// install and returned as the mount order, base roots then the mod, with the
// content section of its own config. A mod without a config is plain content
// on the base game's profile, and notice says so. The settings file's saved
// mod is never read and nothing is fetched, so a match reproduces from its
// command line. root is the -root flag, empty for the default install.
func resolveArenaMod(selector, root string) (roots []string, section profiles.Profile, notice string, err error) {
	if strings.TrimSpace(selector) == "" {
		return nil, profiles.Profile{}, "", nil
	}
	id, _, err := modlibrary.ParseSelector(selector)
	if err != nil || id == "" {
		return nil, profiles.Profile{}, "", err
	}
	var given []string
	if root != "" {
		given = []string{root}
	}
	base, err := install.Resolve(given)
	if err != nil {
		return nil, profiles.Profile{}, "", err
	}
	libraryRoot, err := modlibrary.DefaultRoot()
	if err != nil {
		return nil, profiles.Profile{}, "", err
	}
	lib, err := modlibrary.Open(libraryRoot)
	if err != nil {
		return nil, profiles.Profile{}, "", err
	}
	mod, ok, err := lib.Select(selector, base)
	if err != nil || !ok {
		return nil, profiles.Profile{}, "", err
	}
	roots = append(append([]string(nil), base...), mod.Dir)
	if !mod.HasConfig() {
		return roots, profiles.Profile{}, modlibrary.NoConfigNotice(mod.Name), nil
	}
	return roots, mod.Metadata.Content(), "", nil
}
