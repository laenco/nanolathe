package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/mapgen"
	"github.com/nanolathe-gg/nanolathe/internal/palette"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// The map generator's view of the mounted content
// (docs/DESIGN_MAP_GENERATOR.md §2 and §10): everything it knows of a world
// is read from that world's own maps in the content's archives, so a
// generated map wears the world's tiles and features and keeps its climate.

// mapgenMaxSources caps the maps a world's tiles come from; their tile art
// is held in memory while the generator panel is open.
const mapgenMaxSources = 16

// mapgenGroundClasses are the movement classes a route between bases must
// exist for, the most restrictive common land class first: a 3x3 tank with
// retail's tank slopes. The generator measures with the first one present.
var mapgenGroundClasses = []string{"tanksh3", "tanksh2", "kbotsh2"}

// mapgenWorldInfo is one world the generator can make maps for.
type mapgenWorldInfo struct {
	Planet string // as the maps' OTA planet key spells it
	Maps   []*content.MapHeader
}

// mapgenSourceID identifies one source map: the logical path of its terrain
// file, the archive that supplied it, and the SHA-256 of the terrain and map
// files the generator read. A recipe lists them in the order the generator
// used them, which is part of the result.
type mapgenSourceID struct {
	Map     string `json:"map"`
	Archive string `json:"archive"`
	TNT     string `json:"tnt"`
	OTA     string `json:"ota"`
}

// mapgenSourceArchive reports whether a map comes from an archive rather
// than a loose folder or memory. Saved and generated maps are loose files or
// memory, so this alone keeps generated maps from feeding the generator.
func mapgenSourceArchive(provider string) bool {
	switch strings.ToLower(path.Ext(provider)) {
	case ".hpi", ".ufo", ".ccx", ".gp3", ".gpf", ".gp4", ".kbm":
		return true
	}
	return false
}

// mapgenInMapLibrary reports whether a map's terrain comes from one of the
// map library's packages, mapRoots: a downloaded map may be an archive too,
// and what one machine has downloaded must not change what a code makes.
func mapgenInMapLibrary(fs vfs.FSOps, logicalTNT string, mapRoots []string) bool {
	if len(mapRoots) == 0 {
		return false
	}
	info, err := fs.Stat(logicalTNT)
	if err != nil {
		return true
	}
	return slices.ContainsFunc(mapRoots, func(root string) bool { return pathInsideMapPackage(root, info.Source.SourcePath) })
}

// mapgenWorlds lists the worlds with archive maps to take tiles from, most
// maps first: the install's and the mod's archives, never a package of the
// map library, mapRoots. A world's maps are in the order the generator
// reads them: by the mount order of their archives, which retail's archive
// precedence fixes from the archives' names, then by name.
func mapgenWorlds(fs vfs.FSOps, mapRoots []string) ([]mapgenWorldInfo, error) {
	maps, err := content.CompileMaps(fs)
	if err != nil {
		return nil, err
	}
	byPlanet := map[string]*mapgenWorldInfo{}
	var order []string
	for _, m := range maps {
		if strings.TrimSpace(m.Planet) == "" || !mapgenSourceArchive(m.TNTProvenance.ProviderID) || m.TNTWidth%2 != 0 || m.TNTHeight%2 != 0 {
			continue
		}
		if mapgenInMapLibrary(fs, m.LogicalTNT, mapRoots) {
			continue
		}
		key := content.CanonicalKey(m.Planet)
		w := byPlanet[key]
		if w == nil {
			w = &mapgenWorldInfo{Planet: strings.TrimSpace(m.Planet)}
			byPlanet[key] = w
			order = append(order, key)
		}
		w.Maps = append(w.Maps, m)
	}
	out := make([]mapgenWorldInfo, 0, len(order))
	for _, key := range order {
		w := byPlanet[key]
		slices.SortFunc(w.Maps, func(a, b *content.MapHeader) int {
			if a.TNTProvenance.MountOrder != b.TNTProvenance.MountOrder {
				return a.TNTProvenance.MountOrder - b.TNTProvenance.MountOrder
			}
			return strings.Compare(a.CanonicalKey, b.CanonicalKey)
		})
		out = append(out, *w)
	}
	slices.SortFunc(out, func(a, b mapgenWorldInfo) int {
		if len(a.Maps) != len(b.Maps) {
			return len(b.Maps) - len(a.Maps)
		}
		return strings.Compare(content.CanonicalKey(a.Planet), content.CanonicalKey(b.Planet))
	})
	return out, nil
}

// mapgenWorld reads one world for the generator: the movement class, the
// palette, the features its maps use by how often they use them, the median
// climate, and its source maps with their identities. With want nil the
// sources are the first readable maps in the world's order; otherwise they
// are exactly those want names, from the archives it names, each with the
// bytes it had, or an error naming the first one that is missing or
// differs. A cancelled ctx stops it between source maps.
func mapgenWorld(ctx context.Context, fs vfs.FSOps, info mapgenWorldInfo, maxTNTBytes int64, want []mapgenSourceID) (mapgen.World, []mapgenSourceID, error) {
	w := mapgen.World{Name: info.Planet}
	features, err := content.CompileFeatures(fs)
	if err != nil {
		return w, nil, err
	}
	moves, err := content.CompileMovement(fs)
	if err != nil {
		return w, nil, err
	}
	for _, name := range mapgenGroundClasses {
		if mc := moves[content.CanonicalKey(name)]; mc != nil {
			w.Classes = append(w.Classes, mapgen.MoveClass{
				Name: name, FootX: int(mc.FootprintX), FootZ: int(mc.FootprintZ),
				MaxSlope: int(mc.MaxSlope), BadSlope: int(mc.BadSlope),
				MaxWaterSlope: int(mc.MaxWaterSlope), BadWaterSlope: int(mc.BadWaterSlope),
				MaxWaterDepth: int(mc.MaxWaterDepth), MinWaterDepth: int(mc.MinWaterDepth),
			})
			break
		}
	}
	if len(w.Classes) == 0 {
		return w, nil, fmt.Errorf("nanolathe: map generator: logical path gamedata/moveinfo.tdf, expected one of the ground classes %v", mapgenGroundClasses)
	}
	pal, err := palette.Load(fs)
	if err != nil {
		return w, nil, err
	}
	var rgb [256][3]byte
	for i := range rgb {
		rgb[i] = [3]byte{pal.Base[i][0], pal.Base[i][1], pal.Base[i][2]}
	}
	w.Palette = &rgb

	type read struct {
		header *content.MapHeader
		id     mapgenSourceID
		tnt    *formats.TNT
		ota    []byte
	}
	readMap := func(m *content.MapHeader) (read, error) {
		data, err := fs.ReadFileLimit(m.LogicalTNT, maxTNTBytes)
		if err != nil {
			return read{}, err
		}
		ota, err := fs.ReadFileLimit(m.LogicalOTA, int64(formats.DefaultTDFLimits().MaxBytes))
		if err != nil {
			return read{}, err
		}
		tntSum, otaSum := sha256.Sum256(data), sha256.Sum256(ota)
		id := mapgenSourceID{Map: m.LogicalTNT, Archive: m.TNTProvenance.ProviderID, TNT: hex.EncodeToString(tntSum[:]), OTA: hex.EncodeToString(otaSum[:])}
		tnt, err := formats.LoadTNT(data)
		if err == nil && len(tnt.TileGraphics) == 0 {
			err = fmt.Errorf("nanolathe: map generator: logical path %s, expected tile art", m.LogicalTNT)
		}
		return read{header: m, id: id, tnt: tnt, ota: ota}, err
	}
	var reads []read
	if want == nil {
		for _, m := range info.Maps {
			if len(reads) == mapgenMaxSources {
				break
			}
			if err := ctx.Err(); err != nil {
				return w, nil, err
			}
			if r, err := readMap(m); err == nil {
				reads = append(reads, r)
			}
		}
	} else {
		for _, id := range want {
			if err := ctx.Err(); err != nil {
				return w, nil, err
			}
			i := slices.IndexFunc(info.Maps, func(m *content.MapHeader) bool { return strings.EqualFold(m.LogicalTNT, id.Map) })
			if i < 0 {
				return w, nil, fmt.Errorf("nanolathe: map generator: source map %s from %s is not among the content's %s maps", id.Map, id.Archive, info.Planet)
			}
			if from := info.Maps[i].TNTProvenance.ProviderID; !strings.EqualFold(from, id.Archive) {
				return w, nil, fmt.Errorf("nanolathe: map generator: source map %s comes from %s, not from %s the recipe was made from", id.Map, from, id.Archive)
			}
			r, err := readMap(info.Maps[i])
			if r.id.TNT != "" && (r.id.TNT != id.TNT || r.id.OTA != id.OTA) {
				return w, nil, fmt.Errorf("nanolathe: map generator: source map %s differs from the one in %s the recipe was made from", id.Map, id.Archive)
			}
			if err != nil {
				return w, nil, err
			}
			reads = append(reads, r)
		}
	}

	uses, spelled := map[string]int{}, map[string]string{}
	var ids []mapgenSourceID
	var minWind, maxWind, tidal, gravity, solar, surface, moho, lava []int
	for _, r := range reads {
		m, tnt := r.header, r.tnt
		src, counted := mapgenSource(m.Name, tnt, features)
		w.Sources = append(w.Sources, src)
		ids = append(ids, r.id)
		for k, n := range counted {
			uses[k] += n
		}
		for _, rec := range tnt.FeatureTable {
			if k := content.CanonicalKey(rec.Name); spelled[k] == "" {
				spelled[k] = strings.TrimSpace(rec.Name)
			}
		}
		minWind, maxWind = append(minWind, int(m.MinWindSpeed)), append(maxWind, int(m.MaxWindSpeed))
		tidal, gravity = append(tidal, int(m.TidalStrength)), append(gravity, int(m.Gravity))
		lava = append(lava, int(m.LavaWorld))
		if len(m.Schemas) > 0 {
			surface = append(surface, int(m.Schemas[0].SurfaceMetal))
		}
		if ota, err := formats.LoadOTA(r.ota); err == nil && ota.Global != nil {
			solar = append(solar, int(ota.Global.IntValue("solarstrength", 0)))
			if len(ota.Schemas) > 0 && ota.Schemas[0].Section != nil {
				moho = append(moho, int(ota.Schemas[0].Section.IntValue("mohometal", 0)))
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return w, nil, err
	}
	if len(w.Sources) == 0 {
		return w, nil, fmt.Errorf("nanolathe: map generator: world %q: no readable terrain files, expected archive maps of that planet", info.Planet)
	}
	w.MinWind, w.MaxWind = mapgenMedian(minWind), mapgenMedian(maxWind)
	w.Tidal, w.Gravity, w.Solar = mapgenMedian(tidal), mapgenMedian(gravity), mapgenMedian(solar)
	lavaMaps := 0
	for _, v := range lava {
		lavaMaps += min(1, max(0, v))
	}
	w.SurfaceMetal, w.MohoMetal, w.Lava = mapgenMedian(surface), mapgenMedian(moho), 2*lavaMaps > len(lava)
	mapgenFeatures(&w, uses, spelled, features, func(d *content.FeatureDef) bool { return mapgenDrawn(fs, d) })
	return w, ids, nil
}

// mapgenDrawn reports whether a feature's sprite shows anything: the first
// frame of its sequence has an opaque pixel. Third-party "metal zones" are
// fully transparent sprites over metal painted into the tiles, which a
// generated map's tiles do not have.
func mapgenDrawn(fs vfs.FSOps, d *content.FeatureDef) bool {
	bank, err := formats.LoadGAFFile(fs, "anims/"+d.Filename+".gaf")
	if err != nil {
		return false
	}
	e, ok := bank.Find(d.SeqName)
	if !ok || len(e.Frames) == 0 || e.Frames[0].Frame == nil {
		return false
	}
	return slices.Contains(e.Frames[0].Frame.Transparent, false)
}

// mapgenSource converts one terrain file, marking the cells whose art shows
// metal and the void, and counts the features it places.
func mapgenSource(name string, tnt *formats.TNT, features map[string]*content.FeatureDef) (mapgen.Source, map[string]int) {
	cw, ch := int(tnt.Width), int(tnt.Height)
	src := mapgen.Source{
		Name: name, Width: cw, Height: ch, SeaLevel: byte(min(tnt.SeaLevel, 255)),
		Heights: make([]byte, cw*ch), TileIndices: tnt.TileIndices, TileGraphics: tnt.TileGraphics,
		Avoid: make([]bool, cw*ch),
	}
	counted := map[string]int{}
	avoid := func(x, y, fx, fz int) {
		for yy := max(0, y); yy < min(ch, y+fz); yy++ {
			for xx := max(0, x); xx < min(cw, x+fx); xx++ {
				src.Avoid[yy*cw+xx] = true
			}
		}
	}
	for i, a := range tnt.Attributes {
		src.Heights[i] = a.Height
		x, y := i%cw, i/cw
		switch {
		case a.Feature == 0xFFFC || a.Metal > 0 || x >= cw-2 || y >= ch-8:
			// The authored void, legacy painted metal, and the strips
			// the engine leaves outside the play area [fmt tnt].
			src.Avoid[i] = true
		case int(a.Feature) < len(tnt.FeatureTable):
			key := content.CanonicalKey(tnt.FeatureTable[a.Feature].Name)
			counted[key]++
			if d := features[key]; d != nil && d.Indestructible && d.Metal > 0 {
				// Metal art under a deposit, drawn or not, stays where it is.
				avoid(x-1, y-1, int(d.FootprintX)+2, int(d.FootprintZ)+2)
			}
		}
	}
	return src, counted
}

// mapgenFeatures sorts the features a world's maps use into the generator's
// kinds, each kind keeping its most used members: the deposit is the most
// used drawn, indestructible metal patch; trees block and hold energy;
// foliage neither blocks nor holds metal; rocks are reclaimable metal.
// Models (corpses, dragon's teeth) and decals are left out. A feature keeps
// the spelling the maps give it. A world whose maps draw no deposit — the
// Metal world's ground is its metal — gets none.
func mapgenFeatures(w *mapgen.World, uses map[string]int, spelled map[string]string, features map[string]*content.FeatureDef, drawn func(*content.FeatureDef) bool) {
	keys := make([]string, 0, len(uses))
	for k := range uses {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if uses[a] != uses[b] {
			return uses[b] - uses[a]
		}
		return strings.Compare(a, b)
	})
	const keep = 8
	for _, k := range keys {
		d := features[k]
		if d == nil || d.Filename == "" || d.SeqName == "" || d.Object != "" || d.FootprintX < 1 || d.FootprintZ < 1 {
			continue
		}
		f := mapgen.Feature{Name: spelled[k], FootX: int(d.FootprintX), FootZ: int(d.FootprintZ), Metal: int(d.Metal), Blocking: d.Blocking, Weight: uses[k]}
		small := d.FootprintX <= 3 && d.FootprintZ <= 3
		switch {
		case d.Indestructible && d.Metal > 0 && d.FootprintX >= 2 && d.FootprintZ >= 2 && !d.Blocking:
			if w.Deposit.Name == "" && drawn(d) {
				w.Deposit = f
			}
		case d.Indestructible || !d.Reclaimable || !small:
		case d.Blocking && d.Metal == 0 && d.Energy > 0:
			if len(w.Trees) < keep {
				w.Trees = append(w.Trees, f)
			}
		case !d.Blocking && d.Metal == 0 && d.Energy > 0:
			if len(w.Foliage) < keep {
				w.Foliage = append(w.Foliage, f)
			}
		case d.Metal > 0 && d.Energy == 0:
			if len(w.Rocks) < keep {
				w.Rocks = append(w.Rocks, f)
			}
		}
	}
}

func mapgenMedian(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}
