package effects

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// TestEffectServiceResolvesPrimaryTimingBesideACalculatedFlash locks the
// per-player timing lookup [06 R-WFX-01 §2][03 §1].
//
// An impact record carries two players: the weapon's named art as the primary,
// and the procedurally generated flash table as the secondary, whose holds the
// producer publishes as the secondary durations. Admission used to consult the
// authored-timing resolver only when NEITHER player had timing, so the flash's
// presence suppressed the art's lookup and every impact reached the pool with
// no primary player at all — art that could only ever be shown as a static
// frame 0, for as long as the flash kept the record alive. Each player's timing
// is now resolved on its own.
func TestEffectServiceResolvesPrimaryTimingBesideACalculatedFlash(t *testing.T) {
	pool := &FixedEffectPool{}
	s := NewEffectServiceWithPool(EffectCapacity, pool, effectTimingFixture(t))
	// The production impact shape: named art, and the calculated table's holds
	// already published as the secondary timing.
	s.Advance(1, []Event{{
		Kind: KindExplosion, Tick: 1, Sequence: 1,
		Graphic: "art", AssetID: "fx",
		HasCalculatedFlash: true, CalculatedTable: 0,
		DurationsB: FlashFrameDurations(0),
	}})
	if pool.Len() != 1 {
		t.Fatalf("admission produced %d records", pool.Len())
	}
	rec := pool.Records()[0]
	if !rec.AnimA.Active || rec.AnimA.Frames != 4 {
		t.Fatalf("the named art owns no primary player: %+v", rec.AnimA)
	}
	if !rec.AnimB.Active || rec.AnimB.Frames != len(FlashFrameDurations(0)) {
		t.Fatalf("the calculated flash lost its secondary player: %+v", rec.AnimB)
	}
	views := pool.SnapshotViews()
	if len(views) != 1 || !views[0].ActiveA || !views[0].ActiveB {
		t.Fatalf("both players are running but liveness says otherwise: %+v", views)
	}

	// An entry the resolver cannot find stays unresolved: no player, no
	// invented lifetime, and the layer publishes dead [I9].
	miss := &FixedEffectPool{}
	m := NewEffectServiceWithPool(EffectCapacity, miss, effectTimingFixture(t))
	m.Advance(1, []Event{{
		Kind: KindExplosion, Tick: 1, Sequence: 1,
		Graphic: "no-such-entry", AssetID: "fx",
		HasCalculatedFlash: true, CalculatedTable: 0,
		DurationsB: FlashFrameDurations(0),
	}})
	if views = miss.SnapshotViews(); len(views) != 1 || views[0].ActiveA {
		t.Fatalf("an unresolvable entry published a live primary player: %+v", views)
	}
}

// Content timing controls retirement even with no client and never replaces
// producer-owned timing [03 §1][06 R-WFX-01 §1].
func TestEffectServiceContentTimingRetirement(t *testing.T) {
	pool := &FixedEffectPool{}
	s := NewEffectServiceWithPool(EffectCapacity, pool, effectTimingFixture(t))
	s.Admit(1, Event{Kind: KindExplosion, Tick: 1, Graphic: "art"})
	for tick := uint32(2); tick < 9; tick++ {
		s.Advance(tick, nil)
		if pool.Len() != 1 {
			t.Fatalf("retired at tick %d before eight authored advances", tick)
		}
	}
	s.Advance(9, nil)
	if pool.Len() != 0 {
		t.Fatal("authored eight-advance non-looping art survived")
	}
	s.Admit(10, Event{Kind: KindExplosion, Tick: 10, Graphic: "art", DurationsA: []int32{1}, LoopA: true})
	s.Advance(11, nil)
	if rec := pool.Records()[0]; rec.AnimA.Frames != 1 || !rec.AnimA.Loop {
		t.Fatalf("producer timing replaced: %+v", rec.AnimA)
	}
}

func effectTimingFixture(t *testing.T) *content.SimArt {
	t.Helper()
	data, err := formats.EncodeGAF([]formats.GAFWriteEntry{{Name: "art", Loop: true, Frames: []formats.GAFWriteFrame{
		{Width: 1, Height: 1, Duration: 2, Pixels: []byte{1}},
		{Width: 1, Height: 1, Duration: 2, Pixels: []byte{1}},
		{Width: 1, Height: 1, Duration: 2, Pixels: []byte{1}},
		{Width: 1, Height: 1, Duration: 2, Pixels: []byte{1}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "anims"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "anims/fx.gaf"), data, 0644); err != nil {
		t.Fatal(err)
	}
	fs := vfs.New()
	if err := fs.MountDirectory(root, 0); err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	return content.CompileSimArt(fs, nil)
}

// TestEffectServicePendingViewsCarryLiveness covers the fixture fallback that
// runs with no pool bound. The pool is the normal publisher of per-player
// liveness, so the fallback mirrors its admission rule; otherwise a pending
// view would be published with both players dead and silently draw nothing
// [03 §1].
func TestEffectServicePendingViewsCarryLiveness(t *testing.T) {
	s := newEffectService(0)
	s.Advance(1, []Event{
		{Kind: KindExplosion, Tick: 1, Sequence: 1, Graphic: "art", DurationsA: []int32{2, 2}},
		{Kind: KindExplosion, Tick: 1, Sequence: 2, Graphic: "art"},
	})
	views := s.Snapshot()
	if len(views) != 2 {
		t.Fatalf("pending admission produced %d views", len(views))
	}
	if !views[0].ActiveA {
		t.Fatal("a pending view with authored timing published a dead player")
	}
	if views[1].ActiveA {
		t.Fatal("a pending view with no authored timing published a live player")
	}
}

// A producer that admits straight into the service, as COB explosions and
// debris impacts do, is routed exactly as the event buffer routes one. Their
// explosions used to keep the zero strip and so drew at barrier 0, beneath
// the features and both unit passes, where every tree, metal patch, wreck
// and grounded unit painted over them. No retail producer writes strip 0;
// an unrouted producer belongs to the fixed pool, and an explicit strip is
// kept [03 §1][03 R-STRIP-01 §3].
func TestEffectServiceRoutesDirectAdmissions(t *testing.T) {
	pool := &FixedEffectPool{}
	s := NewEffectServiceWithPool(EffectCapacity, pool, effectTimingFixture(t))
	explosion := Event{Kind: KindExplosion, Tick: 1, Sequence: 1, Graphic: "art", AssetID: "fx",
		HasCalculatedFlash: true, CalculatedTable: 0, DurationsB: FlashFrameDurations(0)}
	beam := explosion
	beam.Sequence, beam.Strip = 2, int8(frame.StripBeam)
	for _, e := range []Event{explosion, beam} {
		if !s.Admit(1, e) {
			t.Fatalf("admission refused %+v", e)
		}
	}
	views := pool.SnapshotViews()
	if len(views) != 2 {
		t.Fatalf("admission published %d views", len(views))
	}
	if views[0].Strip != -1 {
		t.Fatalf("a directly admitted explosion is on strip %d, want the fixed pool (-1)", views[0].Strip)
	}
	if views[1].Strip != int8(frame.StripBeam) {
		t.Fatalf("an explicit strip became %d, want %d", views[1].Strip, frame.StripBeam)
	}
}
