//go:build retail

package session

import (
	"slices"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport/retailcat"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// builderSpeedTimings are the stock readiness intervals Builder speed
// targets (docs/DESIGN_MODS_MUTATORS.md "Builder speed"), in ticks.
type builderSpeedTimings struct {
	placeToReady int // mobile builder: first product placed to its INBUILDSTANCE
	queuedGap    int // mobile builder: first product complete to the queued one's INBUILDSTANCE
	labOpen      int // ARMLAB: product ordered to the yard's INBUILDSTANCE
}

// measureBuilderSpeed runs one Strict 3.1 skirmish under m on stock content:
// a fresh mobile builder (key) builds two queued solar collectors side by
// side, and a fresh ARMLAB is ordered one Peewee.
func measureBuilderSpeed(t *testing.T, m content.Mutators, key string) builderSpeedTimings {
	t.Helper()
	cat, fs := retailcat.Shared(t)
	cfg := DirectSkirmishConfig("ashap plateau")
	cfg.ApplyDefaults()
	cfg.Gameplay = gameplay.Strict31
	cfg.RNGSimSeed, cfg.RNGCrtSeed = aiE2ESeed, aiE2ESeed
	s, err := NewSkirmishWithEntryOptions(fs, cat, cfg, SkirmishEntryOptions{Mutators: m})
	if err != nil {
		t.Fatal(err)
	}
	driver := int32(1 << 20)
	step := func() {
		p := &s.Econ.Players[s.LocalOwner]
		p.Capacity, p.Stock = [2]float32{100000, 100000}, [2]float32{100000, 100000}
		s.Step(driver)
		driver++
	}
	for i := 0; i < 60 && s.State != StateBattle; i++ {
		step()
	}
	var commander *units.Unit
	for _, u := range s.Units.IterSliced() {
		if u != nil && u.Def != nil && u.Def.Commander && u.Owner == uint8(s.LocalOwner) {
			commander = u
			break
		}
	}
	if commander == nil {
		t.Fatal("no local commander")
	}
	create := func(key string, dx, dz int32) *units.Unit {
		def, ok := s.Catalog.Unit(key)
		if !ok {
			t.Skipf("%s absent from the reference install", key)
		}
		x, z := commander.X+world.CellToWorld(dx), commander.Z+world.CellToWorld(dz)
		h, err := s.Units.Create(def, uint8(s.LocalOwner), x, s.World.HeightAt(x, z), z)
		if err != nil {
			t.Fatal(err)
		}
		s.CompleteUnit(h)
		return s.Units.Unit(h)
	}
	builder := create(key, -4, 4)
	lab := create("armlab", 14, -14)
	if builder.COBBinding() == nil || lab.COBBinding() == nil {
		t.Fatal("stock builders have no production script binding")
	}
	for i, dx := range []int32{-12, -6} {
		x, z := commander.X+world.CellToWorld(dx), commander.Z+world.CellToWorld(18)
		if err := s.EnqueueHumanCommand(HumanCommand{Kind: HumanMobileBuild, MobileBuild: HumanMobileBuildCommand{
			Builder: builder.Handle, Product: "armsolar", WX: x, WY: s.World.HeightAt(x, z), WZ: z, Queued: i > 0,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnqueueHumanCommand(HumanCommand{Kind: HumanFactoryBuild, FactoryBuild: HumanFactoryBuildCommand{Builder: lab.Handle, Product: "armpw", Count: 1}}); err != nil {
		t.Fatal(err)
	}
	ordered := s.Clock.GlobalTick
	var products []pool.Handle
	var placed, ready, done, readyAgain, labReady uint32
	for i := 0; i < 6000 && (readyAgain == 0 || labReady == 0); i++ {
		step()
		tick := s.Clock.GlobalTick
		for _, u := range s.Units.IterSliced() {
			if u != nil && u.Def != nil && u.Def.UnitName == "ARMSOLAR" && u.Owner == builder.Owner && !slices.Contains(products, u.Handle) {
				products = append(products, u.Handle)
				if len(products) == 1 {
					placed = tick
				}
			}
		}
		if labReady == 0 && lab.InBuildStance {
			labReady = tick
		}
		switch {
		case placed != 0 && ready == 0 && builder.InBuildStance:
			ready = tick
		case ready != 0 && done == 0:
			if first := s.Units.Unit(products[0]); first != nil && first.Remaining == 0 {
				done = tick
			}
		case done != 0 && readyAgain == 0 && len(products) == 2 && builder.InBuildStance:
			readyAgain = tick
		}
	}
	if readyAgain == 0 || labReady == 0 {
		t.Fatalf("%s %q: incomplete trace: placed %d ready %d done %d ready again %d lab %d; messages %v", key, m.String(), placed, ready, done, readyAgain, labReady, s.Build.Messages())
	}
	return builderSpeedTimings{placeToReady: int(ready - placed), queuedGap: int(readyAgain - done), labOpen: int(labReady - ordered)}
}

// TestBuilderSpeedShortensStockReadinessRetail is Builder speed's stock
// relationship under Strict 3.1 (docs/DESIGN_MODS_MUTATORS.md "Builder
// speed"): at ×5 ARMCK and ARMCV reach their build stance sooner after
// placement, the fold and unfold between two queued jobs take less time, and
// ARMLAB opens its yard sooner than at ×1. It asserts relationships, not a
// census.
func TestBuilderSpeedShortensStockReadinessRetail(t *testing.T) {
	for _, key := range []string{"armck", "armcv"} {
		base := measureBuilderSpeed(t, content.Mutators{}, key)
		five := measureBuilderSpeed(t, content.Mutators{BuilderSpeed: content.Factor{Num: 5, Den: 1}}, key)
		twenty := measureBuilderSpeed(t, content.Mutators{BuilderSpeed: content.Factor{Num: 20, Den: 1}}, key)
		for _, row := range []struct {
			name string
			v    builderSpeedTimings
		}{{"×1", base}, {"×5", five}, {"×20", twenty}} {
			t.Logf("%s builder speed %s: placement to stance %d, queued-job gap %d; ARMLAB order to open yard %d", key, row.name, row.v.placeToReady, row.v.queuedGap, row.v.labOpen)
		}
		if five.placeToReady >= base.placeToReady || five.queuedGap >= base.queuedGap || five.labOpen >= base.labOpen {
			t.Fatalf("%s: ×5 did not shorten every interval: %+v against %+v", key, five, base)
		}
		if twenty.placeToReady > five.placeToReady || twenty.labOpen > five.labOpen {
			t.Fatalf("%s: ×20 was slower than ×5: %+v against %+v", key, twenty, five)
		}
	}
}
