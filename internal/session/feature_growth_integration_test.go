package session

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/clock"
	"github.com/nanolathe-gg/nanolathe/internal/combat"
	"github.com/nanolathe-gg/nanolathe/internal/construction"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/features"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/save"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/world"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

func featureGrowthSession(t *testing.T, mode gameplay.Mode) (*Session, *content.FeatureDef) {
	t.Helper()
	cat := minimalCatalogForStrict()
	cat.Units["armcom"].WorkerTime = 30
	cat.Units["armcom"].Limit = -1
	product := *cat.Units["armcom"]
	product.CanonicalKey, product.UnitName = "growthteeth", "growthteeth"
	product.Commander, product.IsFeature, product.Corpse = false, true, "growthteeth_dead"
	product.BuildTime, product.BuildCostMetal, product.BuildCostEnergy = 1, 12, 44
	cat.Units[product.CanonicalKey] = &product
	wreck := &content.FeatureDef{
		DefinitionHeader: content.DefinitionHeader{CanonicalKey: "growthteeth_dead"},
		Object:           "growthteeth_dead", FootprintX: 1, FootprintZ: 1, Damage: 100, Blocking: true,
	}
	cat.Features[wreck.CanonicalKey] = wreck
	w, err := newSlicedWorld(cat)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		Gameplay: mode, Catalog: cat, World: completedOccupancySessionTerrain(64, 48),
		Mission: syntheticMission(), Units: w, Clock: &clock.State{}, Econ: economyForTest(),
	}
	s.SeedSessionRNG(41, 43)
	s.Econ.Players[0].Exists, s.Econ.Players[0].ControllerState = true, 1
	s.Econ.Players[0].Stock = [2]float32{500, 900}
	s.Econ.Players[0].Capacity = [2]float32{1000, 2000}
	if err := createAndBindServicesForTest(t, s); err != nil {
		t.Fatal(err)
	}
	s.RegisterAll()
	s.State = StateBattle
	return s, wreck
}

type featureGrowthBalance struct {
	sim      rng.Simulation
	crt      rng.CRT
	stock    [10][2]float32
	capacity [10][2]float32
	mirror   [10][2]economy.Bucket
}

func featureGrowthBalances(s *Session) featureGrowthBalance {
	b := featureGrowthBalance{sim: *s.SimRNG(), crt: *s.CrtRNG()}
	for i := range s.Econ.Players {
		p := &s.Econ.Players[i]
		b.stock[i], b.capacity[i], b.mirror[i] = p.Stock, p.Capacity, p.Mirror
	}
	return b
}

// A completed isfeature product still takes the ordinary cause-7 finalizer;
// only the feature allocator's admission changes (DESIGN_ECONOMY_CONSTRUCTION
// "Modern feature growth"). Construction is paid before either answer, and
// the conversion adds no draws or resource effects [05 R-WORK-01 §1][06 §12.1].
func TestFeatureGrowthCompletedProductAtRetailArenaLimit(t *testing.T) {
	for _, mode := range []gameplay.Mode{gameplay.Strict31, gameplay.Modern} {
		t.Run(string(mode), func(t *testing.T) {
			s, wreck := featureGrowthSession(t, mode)
			before := featureGrowthBalances(s)
			for i := range features.FeatureAnimSlots {
				if s.Features.PlaceAt(i%64, i/64, wreck) == nil {
					t.Fatalf("arena refused initial record %d", i)
				}
			}
			if got := featureGrowthBalances(s); got != before {
				t.Fatal("direct 3D feature allocation changed RNG or player balances")
			}
			builderHandle, err := s.Units.Create(s.Catalog.Units["armcom"], 0, world.CellToWorld(8), 0, world.CellToWorld(40))
			if err != nil {
				t.Fatal(err)
			}
			productHandle, err := s.Units.CreateNanoframe(s.Catalog.Units["growthteeth"], 0, world.CellToWorld(10), 0, world.CellToWorld(40))
			if err != nil {
				t.Fatal(err)
			}
			builder, product := s.Units.Unit(builderHandle), s.Units.Unit(productHandle)
			product.Remaining = .5
			product.Health = product.MaxHealth / 2
			before = featureGrowthBalances(s)
			if !s.Build.Assist(builder, product, 1) {
				t.Fatal("ordinary construction step did not complete the product")
			}
			if product.Remaining != 0 || product.Flags&construction.FlagCompleted == 0 || !product.Dying || combat.Cause(product.LastDamageCause) != combat.CauseFeatureConversion {
				t.Fatalf("completion missed the cause-7 posture: remaining=%v flags=%x dying=%t cause=%d", product.Remaining, product.Flags, product.Dying, product.LastDamageCause)
			}
			buckets := *s.Econ.UnitBuckets(builderHandle)
			if buckets[economy.Metal].Requested != 6 || buckets[economy.Metal].Accepted != 6 || buckets[economy.Energy].Requested != 22 || buckets[economy.Energy].Accepted != 22 {
				t.Fatalf("completion cost = %+v, want 6 metal and 22 energy requested and accepted", buckets)
			}
			anchor := world.Cell{X: int32(product.CachedOccupancyX), Z: int32(product.CachedOccupancyZ)}
			s.finalizePhase2Death(productHandle, 2)
			if s.Units.Unit(productHandle) != nil {
				t.Fatal("completed isfeature product survived as a unit")
			}
			inst := s.Features.InstanceAt(int(anchor.X), int(anchor.Z))
			if mode == gameplay.Modern {
				if inst == nil || inst.Def != wreck || !inst.RuntimeView().Live || !inst.Def.Blocking {
					t.Fatalf("Modern lost the completed feature at a retail-full arena: %+v", inst)
				}
			} else if inst != nil || s.World.PlotAt(anchor.X, anchor.Z).Feature() != world.PlotFeatureNone {
				t.Fatalf("Strict admitted an over-limit completed feature: %+v", inst)
			}
			if got := featureGrowthBalances(s); got != before || *s.Econ.UnitBuckets(builderHandle) != buckets {
				t.Fatal("completion/conversion changed RNG or balances beyond the ordinary construction request")
			}
		})
	}
}

func TestFeatureGrowthRebindingRetainsExpandedRecordsAndBalances(t *testing.T) {
	s, wreck := featureGrowthSession(t, gameplay.Modern)
	const count = features.FeatureAnimSlots + 2
	records := make([]*features.Instance, count)
	before := featureGrowthBalances(s)
	for i := range records {
		records[i] = s.Features.PlaceAt(i%64, i/64, wreck)
		if records[i] == nil {
			t.Fatalf("Modern refused record %d", i)
		}
	}
	for _, mode := range []gameplay.Mode{gameplay.Modern, gameplay.Modern, gameplay.Strict31, gameplay.Strict31, gameplay.Community39, gameplay.Modern, gameplay.Strict31} {
		s.SetGameplay(mode)
		s.BindRules(s.Rules)
		for i, want := range records {
			if got := s.Features.InstanceAt(i%64, i/64); got != want {
				t.Fatalf("%s rebind replaced or lost record %d", mode, i)
			}
		}
		got := s.Features.PlaceAt(10, 40, wreck)
		if mode == gameplay.Modern {
			if got == nil {
				t.Fatal("Modern rebind did not restore growth")
			}
			s.Features.RemoveFeatureAt(10, 40, features.CauseReclaim)
		} else if got != nil {
			t.Fatalf("%s admitted a new record while retaining overflow", mode)
		}
		if got := featureGrowthBalances(s); got != before {
			t.Fatalf("%s allocation/rebind changed RNG or balances", mode)
		}
	}
	// Strict must refuse at exactly 2,048 as well as above it, then reuse a
	// released slot below the boundary [05 R-FEAT-01 §3].
	for i := 0; i < 2; i++ {
		s.Features.RemoveFeatureAt(i, 0, features.CauseReclaim)
	}
	if s.Features.PlaceAt(10, 40, wreck) != nil {
		t.Fatal("Strict admitted a new record at exactly the retail limit")
	}
	s.Features.RemoveFeatureAt(2, 0, features.CauseReclaim)
	if s.Features.PlaceAt(10, 40, wreck) == nil {
		t.Fatal("Strict did not admit a record after the charge fell below its limit")
	}
	if got := featureGrowthBalances(s); got != before {
		t.Fatal("Strict slot release/reuse changed RNG or balances")
	}
}

func featureGrowthSpriteSession(t *testing.T, mode gameplay.Mode, fs *vfs.FS, cat *content.Catalog) *Session {
	t.Helper()
	w, err := newSlicedWorldWithCOB(cat, fs)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		Gameplay: mode, Catalog: cat, World: completedOccupancySessionTerrain(64, 48),
		Mission: syntheticMission(), Units: w, Clock: &clock.State{}, Econ: economyForTest(),
	}
	// The map's authored type table has one fixed order on both sides of
	// the round trip [08 R-SAVE-FEATURE-01].
	for _, key := range []string{"growthwreck", "tree1", "tree1dead", "tree1reclaimed", "tree1burnt"} {
		s.World.FeatureDefs = append(s.World.FeatureDefs, cat.Features[key])
		s.World.FeatureNames = append(s.World.FeatureNames, key)
	}
	s.SeedSessionRNG(41, 43)
	if err := createAndBindServicesForTest(t, s); err != nil {
		t.Fatal(err)
	}
	return s
}

// An expanded save can exhaust the shared arena with sprite events alone.
// A successful load must retain the events, rather than silently restore them
// as resting sprites. Burn restoration still spends its ordinary ignition
// draw before copying the saved state back [08 R-SAVE-FEATURE-01].
func TestFeatureGrowthExpandedSpriteSaveRestore(t *testing.T) {
	for _, tc := range []struct {
		name, family string
		wrecks       int
	}{
		{name: "burn", family: "burn"},
		{name: "death", family: "death"},
		{name: "reclaim", family: "reclaim"},
		{name: "wrecks-and-death-events", family: "death", wrecks: features.FeatureAnimSlots / 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, cat := featureLifecycleFS(t), featureLifecycleCatalog()
			wreck := &content.FeatureDef{
				DefinitionHeader: content.DefinitionHeader{CanonicalKey: "growthwreck"},
				Object:           "growthwreck", FootprintX: 1, FootprintZ: 1, Damage: 100,
			}
			cat.Features[wreck.CanonicalKey] = wreck
			src := featureGrowthSpriteSession(t, gameplay.Modern, fs, cat)
			tree := cat.Features["tree1"]
			const count = features.FeatureAnimSlots + 1
			events := count - tc.wrecks
			for i := range count {
				x, z := i%64, i/64
				if i < tc.wrecks {
					if src.Features.PlaceAt(x, z, wreck) == nil {
						t.Fatalf("3D placement %d refused", i)
					}
					continue
				}
				if src.Features.PlaceAt(x, z, tree) == nil {
					t.Fatalf("sprite placement %d refused", i)
				}
				switch tc.family {
				case "burn":
					if !src.Features.Ignite(x, z, 1, 0) {
						t.Fatalf("Modern ignition %d refused", i)
					}
				case "death":
					src.Features.RemoveFeatureAt(x, z, features.CauseDead)
				case "reclaim":
					src.Features.RemoveFeatureAt(x, z, features.CauseReclaim)
				}
			}
			projection, err := ProjectRetailSession(src, RetailSaveInputs{
				Summary: save.Summary{Gametype: GametypeMultiplayer, Players: 1, IsBattle: true},
				Mapping: make([]byte, len(src.World.Plot)/2),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(projection.Features.Animating) != events || len(projection.Features.ThreeD) != tc.wrecks {
				t.Fatalf("saved %d events and %d wrecks, want %d and %d", len(projection.Features.Animating), len(projection.Features.ThreeD), events, tc.wrecks)
			}
			want, err := src.Features.RetailFeatureImage()
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []gameplay.Mode{gameplay.Strict31, gameplay.Community39, gameplay.Modern} {
				t.Run(string(mode), func(t *testing.T) {
					dst := featureGrowthSpriteSession(t, mode, fs, cat)
					before := featureGrowthBalances(dst)
					err := restoreRetailFeatures(dst.Features, dst.Catalog, projection.Features)
					if mode != gameplay.Modern {
						if err == nil {
							t.Fatal("expanded event restore succeeded under a retail arena policy")
						}
						if len(dst.Features.Instances()) != 0 || featureGrowthBalances(dst) != before {
							t.Fatal("over-limit restore changed records, RNG or balances before refusal")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					got, err := dst.Features.RetailFeatureImage()
					if err != nil || !reflect.DeepEqual(got.Animating, want.Animating) || !reflect.DeepEqual(got.ThreeD, want.ThreeD) {
						t.Fatalf("Modern changed expanded records: got %d events and %d wrecks, error %v", len(got.Animating), len(got.ThreeD), err)
					}
					if tc.family == "burn" {
						for range events {
							before.sim.Uint32n(uint32(tree.SparkTime / 2))
						}
					}
					if featureGrowthBalances(dst) != before {
						t.Fatal("Modern event restore changed RNG or balances beyond the ordinary ignition draws")
					}
				})
			}
		})
	}
}

// Exercise the battle writer and complete detached loader, including map
// entry, instead of calling RestoreAt directly. The count and every 3D wire
// record must survive both generations [08 R-SAVE-FEATURE-01].
func TestFeatureGrowthRetailSaveLoadResave(t *testing.T) {
	f := loadRetailFixture(t)
	s := f.session(t)
	s.SetGameplay(gameplay.Modern)
	def, ok := s.Catalog.Unit("ARMDRAG")
	if !ok || def == nil {
		t.Skip("retail fixture has no ARMDRAG")
	}
	wreck := s.Catalog.Features[content.CanonicalKey(def.Corpse)]
	if wreck == nil || wreck.Object == "" || wreck.Filename != "" {
		t.Fatalf("ARMDRAG corpse %q is not an object-backed feature", def.Corpse)
	}
	image, err := s.Features.RetailFeatureImage()
	if err != nil {
		t.Fatal(err)
	}
	const count = features.FeatureAnimSlots + 9
	charged := len(image.ThreeD)
	before := featureGrowthBalances(s)
	for z := 0; z+int(wreck.FootprintZ) <= int(s.World.CellH) && charged < count; z++ {
		for x := 0; x+int(wreck.FootprintX) <= int(s.World.CellW) && charged < count; x++ {
			if !featureGrowthEmptyRect(s.World, x, z, wreck) {
				continue
			}
			inst := s.Features.PlaceAt(x, z, wreck)
			if inst == nil {
				t.Fatalf("Modern refused a vacant feature footprint at (%d,%d)", x, z)
			}
			i := charged
			inst.DamageAccumulator = uint16(i + 17)
			inst.Orientation = features.Orientation{Bank: uint16(i + 31), Heading: uint16(i + 71), Pitch: uint16(i + 113)}
			inst.Y += numeric.FixedFromInt(int64(i % 3))
			charged++
		}
	}
	if charged != count {
		t.Fatalf("fixture did not provide space for an expanded arena: %d 3D records", charged)
	}
	if got := featureGrowthBalances(s); got != before {
		t.Fatal("expanded direct placement changed RNG or player balances")
	}
	want, err := s.Features.RetailFeatureImage()
	if err != nil {
		t.Fatal(err)
	}
	summary := RetailBattleSummary(s, "expanded feature arena", "1", s.Skirmish.UnitLimit)
	path := filepath.Join(t.TempDir(), "features.sav")
	deps := RetailLoadDeps{FS: f.fs, Catalog: f.cat, Gameplay: gameplay.Modern, SimSeed: 41, CRTSeed: 43, UnitLimit: s.Skirmish.UnitLimit}
	for generation := 1; generation <= 2; generation++ {
		inputs, err := s.RetailBattleSaveInputs(summary, save.Camera{})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.WriteRetailSave(path, inputs); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadRetailSavePath(path, deps)
		if err != nil {
			t.Fatalf("Modern load generation %d: %v", generation, err)
		}
		s = loaded.Battle.Session
		got, err := s.Features.RetailFeatureImage()
		if err != nil {
			t.Fatal(err)
		}
		if len(got.ThreeD) != count || !reflect.DeepEqual(got.ThreeD, want.ThreeD) {
			t.Fatalf("generation %d changed expanded 3D records: got %d, want %d", generation, len(got.ThreeD), count)
		}
		if len(got.TypeNames) != len(want.TypeNames) {
			t.Fatalf("generation %d changed the feature type-name table", generation)
		}
		if generation == 1 {
			for _, mode := range []gameplay.Mode{gameplay.Strict31, gameplay.Community39} {
				restricted := deps
				restricted.Gameplay = mode
				result, err := LoadRetailSavePath(path, restricted)
				if err == nil || result.Battle != nil || !strings.Contains(err.Error(), "feature restore capacity exceeded") {
					t.Fatalf("%s reported a successful or unrelated truncated restore: battle=%v error=%v", mode, result.Battle != nil, err)
				}
			}
		}
	}
	wreck = s.Catalog.Features[content.CanonicalKey(def.Corpse)]
	before = featureGrowthBalances(s)
	for z := 0; z+int(wreck.FootprintZ) <= int(s.World.CellH); z++ {
		for x := 0; x+int(wreck.FootprintX) <= int(s.World.CellW); x++ {
			if !featureGrowthEmptyRect(s.World, x, z, wreck) {
				continue
			}
			if s.Features.PlaceAt(x, z, wreck) == nil {
				t.Fatal("loaded Modern arena refused further growth")
			}
			got, err := s.Features.RetailFeatureImage()
			if err != nil || len(got.ThreeD) != count+1 {
				t.Fatalf("post-load allocation: 3D count %d, error %v", len(got.ThreeD), err)
			}
			if featureGrowthBalances(s) != before {
				t.Fatal("post-load direct allocation changed RNG or balances")
			}
			return
		}
	}
	t.Fatal("fixture has no vacant footprint for post-load growth")
}

func featureGrowthEmptyRect(terrain *world.Terrain, x, z int, def *content.FeatureDef) bool {
	for dz := range int(def.FootprintZ) {
		for dx := range int(def.FootprintX) {
			cell := terrain.PlotAt(int32(x+dx), int32(z+dz))
			if cell.Feature() != world.PlotFeatureNone || cell.OccupantA() != 0 || cell.OccupantB() != 0 {
				return false
			}
		}
	}
	return true
}
