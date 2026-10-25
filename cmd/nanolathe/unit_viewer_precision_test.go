package main

import (
	"math"
	"reflect"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/model"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

func TestUnitViewerPivotFreezesRootLocalCreationPose(t *testing.T) {
	f := func(n int64) numeric.Fixed { return numeric.Fixed(n << 16) }
	mdl := &model.Model{Root: 0, Pieces: []model.Piece{
		{Name: "root", Parent: -1, Translate: [3]numeric.Fixed{f(10), f(20), f(30)}},
		{Name: "body", Parent: 0, Translate: [3]numeric.Fixed{f(1), f(1), f(1)},
			Vertices:   [][3]numeric.Fixed{{}, {f(2), f(4), f(6)}},
			Primitives: []model.Primitive{{IsColored: 1, VertexIndices: []uint16{0, 1, 0}}}},
		{Name: "hidden parent", Parent: 0},
		{Name: "hidden child", Parent: 2, Vertices: [][3]numeric.Fixed{{f(1000), 0, 0}},
			Primitives: []model.Primitive{{IsColored: 1, VertexIndices: []uint16{0, 0, 0}}}},
	}}
	states := []model.PieceState{{RotZ: 16384, Trans: [3]numeric.Fixed{f(5), f(6), f(7)}}, {}, {Hidden: true}, {Hidden: true}}
	before := append([]model.PieceState(nil), states...)
	m := unitViewerModel{geometry: mdl}
	m.fitCreatePose(states)
	if !reflect.DeepEqual(states, before) || m.pivot != [3]numeric.Fixed{f(2), f(3), f(4)} || m.radius != math.Sqrt(56)/2 {
		t.Fatalf("reference fit changed its source or lost child transforms: pivot=%v radius=%v", m.pivot, m.radius)
	}
	if got := m.orientedPivot(0, 0, 0); got != [3]numeric.Fixed{f(12), f(28), f(41)} {
		t.Fatalf("frozen root translation/rotation was lost: %v", got)
	}
	for _, yaw := range []uint16{0, 1, 8192, 16384, 32768, 49152, 65535} {
		for _, pitch := range []uint16{0, 8192, 16384, 32768, 49152} {
			h, p, b := unitViewerOrientation(yaw, pitch)
			pivot := m.orientedPivot(h, p, b)
			// An animated root can translate, rotate or hide while the fixed
			// creation reference remains the camera target.
			m.poses = []frame.PieceView{{Index: 0, Name: "root", RotX: yaw, RotZ: pitch, Ty: f(100), Hidden: true}}
			if got := m.orientedPivot(h, p, b); got != pivot || m.radius != math.Sqrt(56)/2 {
				t.Fatal("animation changed the frozen pivot or fit")
			}
		}
	}
}

func TestUnitViewerFitIncludesVisibleChildOfHiddenPivot(t *testing.T) {
	f := func(n int64) numeric.Fixed { return numeric.Fixed(n << 16) }
	mdl := &model.Model{Root: 0, Pieces: []model.Piece{
		{Name: "root", Parent: -1},
		{Name: "pivot", Parent: 0, Translate: [3]numeric.Fixed{f(10), 0, 0}, Vertices: [][3]numeric.Fixed{{}}},
		{Name: "body", Parent: 1, Translate: [3]numeric.Fixed{0, f(5), 0},
			Vertices:   [][3]numeric.Fixed{{}, {f(2), f(4), f(6)}},
			Primitives: []model.Primitive{{IsColored: 1, VertexIndices: []uint16{0, 1, 0}}}},
		{Name: "hidden tip", Parent: 2, Vertices: [][3]numeric.Fixed{{f(1000), 0, 0}},
			Primitives: []model.Primitive{{IsColored: 1, VertexIndices: []uint16{0, 0, 0}}}},
	}}
	states := []model.PieceState{{}, {Hidden: true, Trans: [3]numeric.Fixed{f(2), 0, 0}}, {}, {Hidden: true}}
	pivot, _, radius := unitViewerFit(mdl, states)
	// Authored and script translations still pass through the invisible
	// transform parent; only each piece's own visibility gates its fit.
	if pivot != [3]numeric.Fixed{f(13), f(7), f(3)} || radius != math.Sqrt(56)/2 {
		t.Fatalf("visible child fit: pivot=%v radius=%v", pivot, radius)
	}
}

func TestUnitViewerProjectedFitAndAtlasRetail(t *testing.T) {
	cs, err := openContent(Options{Root: testsupport.RetailRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	cat, err := cs.nlPreviewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := client.NewModelPreviewRenderer(cs.unmappedMount, cs.presentation.TeamLogos)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"armcom", "armlab", "armfig"} {
		t.Run(name, func(t *testing.T) {
			def, ok := cat.Unit(name)
			if !ok {
				t.Fatal("missing reference unit")
			}
			mdl, err := model.Load(cs.unmappedMount, vfs.ResourcePath("objects3d", def.ObjectName, "3do"))
			if err != nil {
				t.Fatal(err)
			}
			m := unitViewerModel{preview: preview}
			m.loadAnimation(def, mdl)
			pivot, radius := m.pivot, m.radius
			for _, yaw := range []uint16{0, 16384, 32768, 40960, 49152, 65535} {
				h, p, b := unitViewerOrientation(yaw, 8192)
				for _, size := range [][3]int{{984, 628, 1}, {4096, 3072, 3}} {
					record, rw, rh, err := m.record(def, h, p, b, float64(size[2]), size[0], size[1])
					if err != nil {
						t.Fatal(err)
					}
					commands := record.List.ModelCommands()
					if len(commands) != 1 || commands[0].Geometry == nil {
						t.Fatal("preview did not record one complete model")
					}
					g := commands[0].Geometry
					if g.AnchorX != int32(rw/2) || g.AnchorY != int32(rh/2) || g.KeyPlane != def.ZBuffer || g.Cache.Reusable() {
						t.Fatal("changing geometry bounds moved the pivot anchor or depth class")
					}
					if g.Width*2+4 > 4096 || g.Height*2+4 > 4096 || g.Supersample == nil || g.Supersample.Width+4 > 4096 || g.Supersample.Height+4 > 4096 {
						t.Fatal("projected model exceeded the composition atlas")
					}
					if size[2] == 1 && (rw != size[0] || rh != size[1]) {
						t.Fatal("normal fit unnecessarily reduced raster resolution")
					}
					if size[2] == 3 && (rw >= size[0] || rh >= size[1]) {
						t.Fatal("large zoom did not reduce and re-project its raster")
					}
				}
				m.updateAnimation(1.0 / 30)
			}
			if m.pivot != pivot || m.radius != radius {
				t.Fatal("pose playback changed the creation fit")
			}
		})
	}
}
