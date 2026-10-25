package formats

import (
	"encoding/binary"
	"fmt"
)

// TNTWrite is a canonical (0x2000) terrain file to encode (the map
// generator, docs/DESIGN_MAP_GENERATOR.md). Its layout is [fmt tnt]:
// a 64-byte header, then the tile index map, the attribute map, the tile
// graphics, the feature records and the minimap, each located by its header
// pointer.
type TNTWrite struct {
	// Width and Height are the attribute map's size in 16-pixel cells; both
	// are even, since a tile covers 2x2 cells.
	Width, Height int
	SeaLevel      byte
	// TileIndices are the (Width/2)*(Height/2) tile numbers, row-major.
	TileIndices []uint16
	// Heights and Features are the Width*Height cells, row-major. A feature
	// word below 0xFFFB is an index into FeatureNames, written in the anchor
	// cell of its footprint only; 0xFFFF is an empty cell.
	Heights  []byte
	Features []uint16
	// TileGraphics are the tiles, 32x32 palette indices each.
	TileGraphics []byte
	FeatureNames []string
	// Minimap is MinimapWidth*MinimapHeight palette indices; empty leaves
	// the file without one.
	MinimapWidth, MinimapHeight int
	Minimap                     []byte
}

// tntFeatureNameBytes is a feature record's name field: 128 bytes after its
// four-byte index [fmt tnt].
const tntFeatureNameBytes = 128

// EncodeTNT writes a canonical terrain file that LoadTNT reads back.
func EncodeTNT(t TNTWrite) ([]byte, error) {
	switch {
	case t.Width <= 0 || t.Height <= 0 || t.Width%2 != 0 || t.Height%2 != 0:
		return nil, fmt.Errorf("tnt write: %dx%d cells, expected positive even dimensions", t.Width, t.Height)
	case len(t.TileIndices) != (t.Width/2)*(t.Height/2):
		return nil, fmt.Errorf("tnt write: %d tile indices, expected %d", len(t.TileIndices), (t.Width/2)*(t.Height/2))
	case len(t.Heights) != t.Width*t.Height || len(t.Features) != t.Width*t.Height:
		return nil, fmt.Errorf("tnt write: %d heights and %d feature words, expected %d", len(t.Heights), len(t.Features), t.Width*t.Height)
	case len(t.TileGraphics)%1024 != 0 || len(t.TileGraphics) == 0:
		return nil, fmt.Errorf("tnt write: %d tile graphics bytes, expected whole 32x32 tiles", len(t.TileGraphics))
	case len(t.Minimap) != t.MinimapWidth*t.MinimapHeight:
		return nil, fmt.Errorf("tnt write: %d minimap bytes for %dx%d", len(t.Minimap), t.MinimapWidth, t.MinimapHeight)
	}
	tiles := len(t.TileGraphics) / 1024
	for _, v := range t.TileIndices {
		if int(v) >= tiles {
			return nil, fmt.Errorf("tnt write: tile index %d of %d tiles", v, tiles)
		}
	}
	for _, f := range t.Features {
		if f < 0xFFFB && int(f) >= len(t.FeatureNames) {
			return nil, fmt.Errorf("tnt write: feature index %d of %d records", f, len(t.FeatureNames))
		}
	}
	for _, name := range t.FeatureNames {
		if len(name) == 0 || len(name) >= tntFeatureNameBytes {
			return nil, fmt.Errorf("tnt write: feature name %q, expected 1..%d bytes", name, tntFeatureNameBytes-1)
		}
	}
	const header = 0x40
	mapData := header
	mapAttr := mapData + 2*len(t.TileIndices)
	tileGfx := mapAttr + 4*len(t.Heights)
	tileAnims := tileGfx + len(t.TileGraphics)
	miniMap := tileAnims + (4+tntFeatureNameBytes)*len(t.FeatureNames)
	size := miniMap
	if len(t.Minimap) > 0 {
		size += 8 + len(t.Minimap)
	}
	out := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint32(out[0x00:], 0x2000)
	le.PutUint32(out[0x04:], uint32(t.Width))
	le.PutUint32(out[0x08:], uint32(t.Height))
	le.PutUint32(out[0x0C:], uint32(mapData))
	le.PutUint32(out[0x10:], uint32(mapAttr))
	le.PutUint32(out[0x14:], uint32(tileGfx))
	le.PutUint32(out[0x18:], uint32(tiles))
	le.PutUint32(out[0x1C:], uint32(len(t.FeatureNames)))
	le.PutUint32(out[0x20:], uint32(tileAnims))
	le.PutUint32(out[0x24:], uint32(t.SeaLevel))
	if len(t.Minimap) > 0 {
		le.PutUint32(out[0x28:], uint32(miniMap))
		le.PutUint32(out[0x2C:], 1)
	}
	for i, v := range t.TileIndices {
		le.PutUint16(out[mapData+2*i:], v)
	}
	for i, h := range t.Heights {
		at := mapAttr + 4*i
		out[at] = h
		le.PutUint16(out[at+1:], t.Features[i]) // bytes 1..2, unaligned
	}
	copy(out[tileGfx:], t.TileGraphics)
	for i, name := range t.FeatureNames {
		at := tileAnims + (4+tntFeatureNameBytes)*i
		le.PutUint32(out[at:], uint32(i))
		copy(out[at+4:at+4+tntFeatureNameBytes], name)
	}
	if len(t.Minimap) > 0 {
		le.PutUint32(out[miniMap:], uint32(t.MinimapWidth))
		le.PutUint32(out[miniMap+4:], uint32(t.MinimapHeight))
		copy(out[miniMap+8:], t.Minimap)
	}
	return out, nil
}
