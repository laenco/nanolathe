package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
	"github.com/nanolathe-gg/nanolathe/internal/modlibrary"
)

// Generated maps in the shell (docs/DESIGN_MAP_GENERATOR.md §10): the run's
// own, mounted from memory and gone when the game closes, and saved ones,
// kept in the map library as a package of the two files whose receipt
// records the recipe.

// mapgenKeptMaps bounds the run's generated maps held in memory; the oldest
// one the setup does not use goes first. A save or replay of a battle on a
// map that has gone makes it again from its recipe.
const mapgenKeptMaps = 6

// mountGeneratedMap adds a map to the run's generated maps, replacing one
// of the same name, and remounts the content with it as a library refresh
// does: the shell, its panels and the setup stay.
func (g *gameShell) mountGeneratedMap(m *generatedMap) error {
	old := g.keepGeneratedMap(m)
	if err := g.refreshMapLibrary(); err != nil {
		g.opts.generatedMaps = old
		return err
	}
	return nil
}

// keepGeneratedMap adds a map to the ones every later mount holds, replacing
// one of the same name and forgetting the oldest the setup does not use past
// mapgenKeptMaps; it returns the list as it was.
func (g *gameShell) keepGeneratedMap(m *generatedMap) []*generatedMap {
	old := g.opts.generatedMaps
	kept := slices.DeleteFunc(slices.Clone(old), func(o *generatedMap) bool { return strings.EqualFold(o.recipe.Name, m.recipe.Name) })
	for len(kept) >= mapgenKeptMaps {
		i := slices.IndexFunc(kept, func(o *generatedMap) bool { return !strings.EqualFold(o.recipe.Name, g.setup.MapName) })
		if i < 0 {
			break
		}
		kept = slices.Delete(kept, i, i+1)
	}
	g.opts.generatedMaps = append(kept, m)
	return old
}

// mapgenPackageID is a saved map's package id: tags of its code and name.
func mapgenPackageID(r mapgenRecipe) string {
	return "gen-" + mapgenTag(r.Code) + "-" + mapgenTag(strings.ToLower(r.Name))
}

// saveGeneratedMap keeps a generated map in the map library and remounts
// the content with it. The package holds the map's two files, so the map
// stays playable whatever later generator versions make of its code; its
// receipt names the code as a download's names its URL and records the
// recipe, and the picker removes it as it removes a downloaded map.
func (g *gameShell) saveGeneratedMap(m *generatedMap) error {
	root, err := maplibrary.DefaultRoot()
	if err != nil {
		return err
	}
	lib, err := maplibrary.Open(root)
	if err != nil {
		return err
	}
	recipe, err := json.Marshal(m.recipe)
	if err != nil {
		return err
	}
	id, name := mapgenPackageID(m.recipe), m.recipe.Name
	meta, err := json.Marshal(modlibrary.Metadata{Schema: 1, ID: id, Name: name, Version: "1"})
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	archive := filepath.Join(dir, id+".zip")
	if err := writeMapPackage(archive, meta, name, m.ota, m.tnt); err != nil {
		return err
	}
	defer os.Remove(archive)
	if _, err := lib.InstallArchive(archive, modlibrary.InstallOptions{
		Source:   "generated:" + m.recipe.Code,
		Recipe:   string(recipe),
		Validate: maplibrary.Validator(g.cs.baseAndModRoots(), "maps/"+name+".ota"),
	}); err != nil {
		return err
	}
	return g.refreshMapLibrary()
}

// writeMapPackage writes a map package: its identity and the map's pair.
func writeMapPackage(path string, meta []byte, name string, ota, tnt []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	for _, file := range []struct {
		name string
		data []byte
	}{{modlibrary.MetadataFile, meta}, {"maps/" + name + ".ota", ota}, {"maps/" + name + ".tnt", tnt}} {
		w, err := zw.Create(file.name)
		if err == nil {
			_, err = w.Write(file.data)
		}
		if err != nil {
			zw.Close()
			f.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// generatedRecipe is the recipe of a map the generator made, by name: the
// map whose terrain the content serves under that name, when that is one of
// the maps the mount holds from memory or a saved one whose package's
// receipt records it. Any other map has none.
func (c *contentSet) generatedRecipe(mapName string) *mapgenRecipe {
	if c == nil || c.unmappedMount == nil || mapName == "" {
		return nil
	}
	info, err := c.unmappedMount.Stat("maps/" + mapName + ".tnt")
	if err != nil {
		return nil
	}
	if id := info.Source.ProviderID(); strings.HasPrefix(id, "generated:") {
		for _, m := range c.generated {
			if m.provider() == id && strings.EqualFold(m.recipe.Name, mapName) {
				r := m.recipe
				return &r
			}
		}
		return nil
	}
	// Only a map library package can be a saved one; any other map is
	// answered without reading the library.
	if !slices.ContainsFunc(c.mapRoots, func(root string) bool { return pathInsideMapPackage(root, info.Source.SourcePath) }) {
		return nil
	}
	root, err := maplibrary.DefaultRoot()
	if err != nil {
		return nil
	}
	installed, err := (&modlibrary.Library{Root: root}).Installed()
	if err != nil {
		return nil
	}
	item := c.mapPackage("maps/"+mapName+".ota", installed)
	if item == nil || item.Receipt.Recipe == "" || !strings.HasPrefix(item.Receipt.Source, "generated:") {
		return nil
	}
	var r mapgenRecipe
	if json.Unmarshal([]byte(item.Receipt.Recipe), &r) != nil || !strings.EqualFold(r.Name, mapName) {
		return nil
	}
	return &r
}

// generatedRecipeRecord is generatedRecipe as a save or a replay records it;
// nil for any other map.
func (c *contentSet) generatedRecipeRecord(mapName string) []byte {
	r := c.generatedRecipe(mapName)
	if r == nil {
		return nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		return nil
	}
	return data
}
