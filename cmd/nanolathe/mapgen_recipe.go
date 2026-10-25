package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/mapgen"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// Generated maps as content (docs/DESIGN_MAP_GENERATOR.md §10): a recipe
// says what made a map, and a generated map the shell holds is mounted from
// memory as the archive generated:<code>.

// mapgenRecipeFormat is the recipe's own layout version; mapgen.Version is
// the generator's.
const mapgenRecipeFormat = 1

// mapgenRecipe is what makes a generated map again: the generator version,
// the code (world, parameters and seed), the name and description its map
// file carries, the source maps in the order the generator read them, and
// the SHA-256 of the files it made.
type mapgenRecipe struct {
	Format      int              `json:"format"`
	Generator   int              `json:"generator"`
	Code        string           `json:"code"`
	World       string           `json:"world"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Sources     []mapgenSourceID `json:"sources"`
	TNT         string           `json:"tnt"`
	OTA         string           `json:"ota"`
}

// generatedMap is a generated map's files with the recipe that made them.
type generatedMap struct {
	recipe   mapgenRecipe
	tnt, ota []byte
}

// mapgenSum is a file's SHA-256 as a recipe writes it.
func mapgenSum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// newGeneratedMap writes a generated map's files under a name and
// description and records how they were made.
func newGeneratedMap(m *mapgen.Map, world string, sources []mapgenSourceID, name, description string) (*generatedMap, error) {
	ota, err := m.OTA(name, description)
	if err != nil {
		return nil, err
	}
	return &generatedMap{
		recipe: mapgenRecipe{
			Format: mapgenRecipeFormat, Generator: mapgen.Version, Code: m.Code, World: world,
			Name: name, Description: description, Sources: slices.Clone(sources),
			TNT: mapgenSum(m.TNT), OTA: mapgenSum(ota),
		},
		tnt: m.TNT, ota: ota,
	}, nil
}

// provider is the name the map's archive is mounted under.
func (g *generatedMap) provider() string { return "generated:" + g.recipe.Code }

// archive is the map's pair as an HPI archive, for mounting from memory.
func (g *generatedMap) archive() ([]byte, error) {
	var b bytes.Buffer
	err := vfs.WriteArchive(&b, []vfs.ArchiveFile{
		{Path: "maps/" + g.recipe.Name + ".ota", Data: g.ota},
		{Path: "maps/" + g.recipe.Name + ".tnt", Data: g.tnt},
	}, vfs.ArchiveWriteOptions{})
	return b.Bytes(), err
}

// mountGeneratedMaps mounts each map below everything else, so a generated
// map adds its own two files and can override nothing.
func mountGeneratedMaps(fs *vfs.FS, maps []*generatedMap) error {
	for _, g := range maps {
		data, err := g.archive()
		if err != nil {
			return err
		}
		if _, err := fs.MountArchiveReader(g.provider(), bytes.NewReader(data), int64(len(data)), -1, vfs.ArchiveOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// mapgenTag is a short name for a code: six base-36 digits of its hash.
func mapgenTag(code string) string {
	h := fnv.New64a()
	h.Write([]byte(code))
	tag := strconv.FormatUint(h.Sum64()%2176782336, 36) // 36^6
	return strings.Repeat("0", 6-len(tag)) + tag
}

// mapgenName is a generated map's default name, which is also its file
// name: the world, the player count and the code's tag, in characters any
// file system and the TDF syntax accept.
func mapgenName(world string, m *mapgen.Map) string {
	var title strings.Builder
	for _, r := range world {
		switch {
		case r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			title.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			title.WriteByte(' ')
		}
	}
	world = strings.Join(strings.Fields(title.String()), " ")
	if len(world) > 24 {
		// "Gen ", " 10p " and the tag leave 24 of a name's 40 characters.
		world = strings.TrimSpace(world[:24])
	}
	return fmt.Sprintf("Gen %s %dp %s", world, m.Params.Players, mapgenTag(m.Code))
}

// mapgenWorkers bounds the generator's candidates at once, leaving a core
// for the window while a preview generates (DESIGN_MAP_GENERATOR §8).
func mapgenWorkers() int {
	return max(1, min(mapgen.Candidates, runtime.GOMAXPROCS(0)-1))
}

// regenerateMap makes a recipe's map again from the mounted content, whose
// map library packages are mapRoots: the recipe's own source maps, from the
// archives it names, each with the bytes it had, the same generator version,
// and files with the recipe's hashes. Any difference is an error that names
// it; nothing is made from other sources. A recipe comes from a save or a
// replay file, so one with more sources than the generator reads, or with a
// source twice, is refused before anything is read.
func regenerateMap(ctx context.Context, fs vfs.FSOps, mapRoots []string, maxTNTBytes int64, r mapgenRecipe) (*generatedMap, error) {
	if r.Format != mapgenRecipeFormat {
		return nil, fmt.Errorf("nanolathe: generated map %q: recipe format %d, this build reads %d", r.Name, r.Format, mapgenRecipeFormat)
	}
	if r.Generator != mapgen.Version {
		return nil, fmt.Errorf("nanolathe: generated map %q: made by map generator v%d, this build has v%d", r.Name, r.Generator, mapgen.Version)
	}
	slug, p, err := mapgen.ParseCode(r.Code)
	if err != nil {
		return nil, err
	}
	if slug != mapgen.Slug(r.World) || len(r.Sources) == 0 {
		return nil, fmt.Errorf("nanolathe: generated map %q: recipe code %s does not match its world %q and sources", r.Name, r.Code, r.World)
	}
	if len(r.Sources) > mapgenMaxSources {
		return nil, fmt.Errorf("nanolathe: generated map %q: recipe names %d source maps, expected at most %d", r.Name, len(r.Sources), mapgenMaxSources)
	}
	for i, id := range r.Sources {
		if slices.ContainsFunc(r.Sources[:i], func(o mapgenSourceID) bool { return strings.EqualFold(o.Map, id.Map) }) {
			return nil, fmt.Errorf("nanolathe: generated map %q: recipe names source map %s twice", r.Name, id.Map)
		}
	}
	worlds, err := mapgenWorlds(fs, mapRoots)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(worlds, func(w mapgenWorldInfo) bool { return content.CanonicalKey(w.Planet) == content.CanonicalKey(r.World) })
	if i < 0 {
		return nil, fmt.Errorf("nanolathe: generated map %q: the content has no %s maps, expected %s from %s", r.Name, r.World, r.Sources[0].Map, r.Sources[0].Archive)
	}
	world, _, err := mapgenWorld(ctx, fs, worlds[i], maxTNTBytes, r.Sources)
	if err != nil {
		return nil, err
	}
	gen, err := mapgen.NewGenerator(world)
	if err != nil {
		return nil, err
	}
	gen.Workers = mapgenWorkers()
	m, err := gen.Generate(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	made, err := newGeneratedMap(m, r.World, r.Sources, r.Name, r.Description)
	if err != nil {
		return nil, err
	}
	if made.recipe.TNT != r.TNT || made.recipe.OTA != r.OTA {
		return nil, fmt.Errorf("nanolathe: generated map %q: made again from its recipe, its files differ from the ones it recorded", r.Name)
	}
	return made, nil
}
