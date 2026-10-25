package formats

import (
	"bytes"
	"testing"
)

// A written terrain file reads back cell for cell: heights, anchor features,
// tile numbers, tile pixels, feature names, sea level and minimap (the map
// generator).
func TestEncodeTNTRoundTrips(t *testing.T) {
	in := TNTWrite{Width: 4, Height: 2, SeaLevel: 40, TileIndices: []uint16{1, 0},
		Heights: []byte{10, 20, 30, 40, 50, 60, 70, 80}, Features: []uint16{0xFFFF, 1, 0xFFFF, 0xFFFF, 0, 0xFFFF, 0xFFFF, 0xFFFF},
		TileGraphics: make([]byte, 2*1024), FeatureNames: []string{"tree1", "ArchMetal1"},
		MinimapWidth: 2, MinimapHeight: 2, Minimap: []byte{1, 2, 3, 4}}
	in.TileGraphics[1024] = 99
	data, err := EncodeTNT(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadTNT(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 4 || got.Height != 2 || got.SeaLevel != 40 || got.Tiles != 2 || got.TileGraphics[1024] != 99 {
		t.Fatalf("header %dx%d sea %d tiles %d", got.Width, got.Height, got.SeaLevel, got.Tiles)
	}
	for i, a := range got.Attributes {
		if a.Height != in.Heights[i] || a.Feature != in.Features[i] {
			t.Fatalf("cell %d: %+v", i, a)
		}
	}
	if got.TileIndices[0] != 1 || got.FeatureTable[1].Name != "ArchMetal1" || !got.MiniMapPresent || !bytes.Equal(got.Minimap, in.Minimap) {
		t.Fatalf("tiles %v features %+v minimap %v", got.TileIndices, got.FeatureTable, got.Minimap)
	}
	bad := in
	bad.Width = 3
	if _, err := EncodeTNT(bad); err == nil {
		t.Fatal("odd width written")
	}
	bad = in
	bad.Features = append([]uint16{5}, in.Features[1:]...)
	if _, err := EncodeTNT(bad); err == nil {
		t.Fatal("feature index past the table written")
	}
}

// Written TDF parses back as an OTA with its schema, start positions and
// header keys; text TDF cannot carry is refused.
func TestEncodeTDFReadsBackAsOTA(t *testing.T) {
	head := &TDFWriteSection{Name: "GlobalHeader"}
	head.Set("missionname", "Generated").Set("numplayers", "2").Set("size", "8 x 8").Set("gravity", "112").Set("SCHEMACOUNT", "1")
	schema := head.Add("Schema 0")
	schema.Set("Type", "Network 1").Set("SurfaceMetal", "3")
	specials := schema.Add("specials")
	specials.Add("special0").Set("specialwhat", "StartPos1").Set("XPos", "512").Set("ZPos", "640")
	data, err := EncodeTDF(head)
	if err != nil {
		t.Fatal(err)
	}
	ota, err := LoadOTA(data)
	if err != nil {
		t.Fatal(err)
	}
	if ota.MissionName != "Generated" || ota.Size != "8 x 8" || !ota.HasNetworkSchema() || len(ota.Schemas) != 1 {
		t.Fatalf("ota %+v", ota)
	}
	sp := ota.Schemas[0].Section.Section("specials")
	if sp == nil || sp.Section("special0") == nil || sp.Section("special0").IntValue("XPos", 0) != 512 {
		t.Fatal("start position lost")
	}
	if _, err := EncodeTDF(&TDFWriteSection{Name: "x", Keys: []TDFWriteKey{{"k", "a;b"}}}); err == nil {
		t.Fatal("a semicolon in a value was written")
	}
	// A comment opener anywhere on a line would cut the value short.
	for _, v := range []string{"see http://example.org", "A /* B"} {
		if _, err := EncodeTDF(&TDFWriteSection{Name: "x", Keys: []TDFWriteKey{{"k", v}}}); err == nil {
			t.Fatalf("the comment opener in %q was written", v)
		}
	}
}
