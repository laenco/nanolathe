package features

import (
	"bytes"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

func bindFeatureCheckpoint(t *testing.T, s *Service, c *CheckpointContext, authority *checkpoint.BindingAuthority) {
	t.Helper()
	if err := c.SetBindings(s, s.Sim, s.Crt, s.Wind, authority); err != nil {
		t.Fatal(err)
	}
}

func featureBindingRefused(t *testing.T, s *Service, c *CheckpointContext, path string) {
	t.Helper()
	want := "nanolathe: checkpoint capture failed: logical path " + path + ","
	if n, err := s.CollectCheckpointReferences(c); n != 0 || err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("collect = %d, %v; want refusal at %s", n, err, path)
	}
	var out bytes.Buffer
	if err := s.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("write = %v; want refusal at %s", err, path)
	}
	if out.Len() != 0 {
		t.Fatal("refused bindings wrote partial payload")
	}
}

// Use only real installation/extraction APIs; panics forbid callback invocation
// while observing proof, owner identity or cached immutable sequence values.
var featureCallbackSlots = []struct {
	name      string
	offset    int
	bind      func(*Service, *checkpoint.BindingAuthority)
	copyTo    func(dst, src *Service)
	clear     func(*Service)
	clearWith func(*Service, *checkpoint.BindingAuthority)
}{
	{
		"BurnFrameGeometry", 0,
		func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetBurnFrameGeometryWithCheckpointBinding(func(*content.FeatureDef, int32) (int32, int32, int32, int32) {
				panic("capture called BurnFrameGeometry")
			}, a)
		},
		func(dst, src *Service) { dst.SetBurnFrameGeometry(src.BurnFrameGeometryHook()) },
		func(s *Service) { s.SetBurnFrameGeometry(nil) },
		func(s *Service, a *checkpoint.BindingAuthority) { s.SetBurnFrameGeometryWithCheckpointBinding(nil, a) },
	},
	{
		"BurnSmoke", 1,
		func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetBurnSmokeWithCheckpointBinding(func([3]numeric.Fixed) { panic("capture called BurnSmoke") }, a)
		},
		func(dst, src *Service) { dst.SetBurnSmoke(src.BurnSmokeHook()) },
		func(s *Service) { s.SetBurnSmoke(nil) },
		func(s *Service, a *checkpoint.BindingAuthority) { s.SetBurnSmokeWithCheckpointBinding(nil, a) },
	},
	{
		"BurnSound", 2,
		func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetBurnSoundWithCheckpointBinding(func([3]numeric.Fixed) { panic("capture called BurnSound") }, a)
		},
		func(dst, src *Service) { dst.SetBurnSound(src.BurnSoundHook()) },
		func(s *Service) { s.SetBurnSound(nil) },
		func(s *Service, a *checkpoint.BindingAuthority) { s.SetBurnSoundWithCheckpointBinding(nil, a) },
	},
	{
		"BurnWeapon", 3,
		func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetBurnWeaponWithCheckpointBinding(func(string, [3]numeric.Fixed) { panic("capture called BurnWeapon") }, a)
		},
		func(dst, src *Service) { dst.SetBurnWeapon(src.BurnWeaponHook()) },
		func(s *Service) { s.SetBurnWeapon(nil) },
		func(s *Service, a *checkpoint.BindingAuthority) { s.SetBurnWeaponWithCheckpointBinding(nil, a) },
	},
	{
		"GeothermalSteam", 5,
		func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetGeothermalSteamWithCheckpointBinding(func(numeric.Fixed, numeric.Fixed, numeric.Fixed) {
				panic("capture called GeothermalSteam")
			}, a)
		},
		func(dst, src *Service) { dst.SetGeothermalSteam(src.GeothermalSteamHook()) },
		func(s *Service) { s.SetGeothermalSteam(nil) },
		func(s *Service, a *checkpoint.BindingAuthority) { s.SetGeothermalSteamWithCheckpointBinding(nil, a) },
	},
	{
		"SequenceFrames", 6,
		func(s *Service, a *checkpoint.BindingAuthority) {
			s.SetSequenceFramesWithCheckpointBinding(func(*content.FeatureDef, uint8) []int32 {
				panic("capture called SequenceFrames")
			}, a)
		},
		func(dst, src *Service) { dst.SetSequenceFrames(src.SequenceFramesHook()) },
		func(s *Service) { s.SetSequenceFrames(nil) },
		func(s *Service, a *checkpoint.BindingAuthority) { s.SetSequenceFramesWithCheckpointBinding(nil, a) },
	},
}

func TestCheckpointFeatureBindingPresenceVector(t *testing.T) {
	_, c, _ := featureCheckpointFixture(t)
	c.World.Terrain = nil
	s := &Service{}
	a := checkpoint.NewBindingAuthority()
	// Independent empty payload: ten presence bytes, one policy byte, arenaHeld/cursor i64,
	// then zero instance and active counts. No owner state is inlined.
	want := make([]byte, 35)
	if got := featureCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
		t.Fatalf("unregistered absent payload = %x, want %x", got, want)
	}
	bindFeatureCheckpoint(t, s, c, a)
	if got := featureCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
		t.Fatal("registered absent payload changed")
	}
	for _, slot := range featureCallbackSlots {
		t.Run(slot.name, func(t *testing.T) {
			slot.bind(s, a)
			want[slot.offset] = 1
			if got := featureCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
				t.Fatalf("presence payload = %x, want %x", got, want)
			}
			slot.clear(s)
			want[slot.offset] = 0
		})
	}
	for _, owner := range []struct {
		name   string
		offset int
		bind   func(*Service)
	}{
		{"Crt", 4, func(s *Service) { crt := rng.NewCRT(73); s.Crt = &crt }},
		{"Sim", 7, func(s *Service) { sim := rng.NewSimulation(79); s.Sim = &sim }},
		{"Wind", 9, func(s *Service) { s.Wind = &world.Wind{Strength: 83} }},
	} {
		t.Run(owner.name, func(t *testing.T) {
			s := &Service{}
			owner.bind(s)
			c := NewCheckpointContext(c.World)
			bindFeatureCheckpoint(t, s, c, a)
			want := make([]byte, 35)
			want[owner.offset] = 1
			if got := featureCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
				t.Fatalf("owner presence payload = %x, want %x", got, want)
			}
		})
	}
	s.ShadowSequenceResolved = func(*content.FeatureDef, string) bool { panic("capture called shadow resolver") }
	if got := featureCheckpointBytes(t, s, c); !bytes.Equal(got, want) {
		t.Fatal("presentation-only shadow resolver entered payload")
	}
}

func TestCheckpointFeatureCallbackInstallationProof(t *testing.T) {
	for index, slot := range featureCallbackSlots {
		t.Run(slot.name, func(t *testing.T) {
			s, c, _ := featureCheckpointFixture(t)
			a := checkpoint.NewBindingAuthority()
			bindFeatureCheckpoint(t, s, c, a)
			path := "features.Service." + slot.name
			slot.bind(s, a)
			featureCheckpointBytes(t, s, c)
			featureBindingRefused(t, s, NewCheckpointContext(c.World), path)
			slot.copyTo(s, s)
			featureBindingRefused(t, s, c, path)
			slot.bind(s, nil)
			featureBindingRefused(t, s, c, path)
			slot.bind(s, checkpoint.NewBindingAuthority())
			featureBindingRefused(t, s, c, path)
			slot.bind(s, a)
			featureCheckpointBytes(t, s, c)

			copyService := *s
			copyContext := NewCheckpointContext(c.World)
			bindFeatureCheckpoint(t, &copyService, copyContext, a)
			featureBindingRefused(t, &copyService, copyContext, path)
			other := &Service{Terrain: s.Terrain}
			slot.copyTo(other, s)
			otherContext := NewCheckpointContext(c.World)
			bindFeatureCheckpoint(t, other, otherContext, a)
			featureBindingRefused(t, other, otherContext, path)
			slot.bind(&copyService, a)
			featureCheckpointBytes(t, &copyService, copyContext)

			for _, clear := range []func(){func() { slot.clear(s) }, func() { slot.clearWith(s, a) }} {
				slot.bind(s, a)
				clear()
				if s.checkpointCallbacks[index] != (checkpointCallbackProof{}) {
					t.Fatal("nil installation retained proof")
				}
				featureCheckpointBytes(t, s, c)
			}
		})
	}
}

func TestCheckpointFeatureProofIsPerSlot(t *testing.T) {
	s, c, _ := featureCheckpointFixture(t)
	a := checkpoint.NewBindingAuthority()
	bindFeatureCheckpoint(t, s, c, a)
	for _, slot := range featureCallbackSlots {
		slot.bind(s, a)
	}
	for index, slot := range featureCallbackSlots {
		before := s.checkpointCallbacks
		slot.copyTo(s, s)
		for other := range before {
			if other != index && s.checkpointCallbacks[other] != before[other] {
				t.Fatal("ordinary installation cleared another slot's proof")
			}
		}
		featureCallbackSlots[(index+1)%len(featureCallbackSlots)].bind(s, a)
		featureBindingRefused(t, s, c, "features.Service."+slot.name)
		slot.bind(s, a)
		featureCheckpointBytes(t, s, c)
	}
}

func TestCheckpointFeatureContextRegistrationAtomicity(t *testing.T) {
	s, c, _ := featureCheckpointFixture(t)
	sim, crt := rng.NewSimulation(7), rng.NewCRT(11)
	s.Sim, s.Crt, s.Wind = &sim, &crt, &world.Wind{Strength: 13}
	a := checkpoint.NewBindingAuthority()
	bindFeatureCheckpoint(t, s, c, a)
	before := *c
	bindFeatureCheckpoint(t, s, c, a)
	if *c != before {
		t.Fatal("identical registration changed context")
	}
	for name, args := range map[string]struct {
		service   *Service
		sim       *rng.Simulation
		crt       *rng.CRT
		wind      *world.Wind
		authority *checkpoint.BindingAuthority
	}{
		"absent service":    {nil, s.Sim, s.Crt, s.Wind, a},
		"absent authority":  {s, s.Sim, s.Crt, s.Wind, nil},
		"other service":     {&Service{}, s.Sim, s.Crt, s.Wind, a},
		"other simulation":  {s, &rng.Simulation{}, s.Crt, s.Wind, a},
		"other CRT":         {s, s.Sim, &rng.CRT{}, s.Wind, a},
		"other wind":        {s, s.Sim, s.Crt, &world.Wind{}, a},
		"absent simulation": {s, nil, s.Crt, s.Wind, a},
		"absent CRT":        {s, s.Sim, nil, s.Wind, a},
		"absent wind":       {s, s.Sim, s.Crt, nil, a},
		"other authority":   {s, s.Sim, s.Crt, s.Wind, checkpoint.NewBindingAuthority()},
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.SetBindings(args.service, args.sim, args.crt, args.wind, args.authority); err == nil {
				t.Fatal("conflicting registration accepted")
			}
			if *c != before {
				t.Fatal("failed registration changed context")
			}
			featureCheckpointBytes(t, s, c)
		})
	}
	var absent *CheckpointContext
	if err := absent.SetBindings(s, s.Sim, s.Crt, s.Wind, a); err == nil {
		t.Fatal("nil context accepted")
	}
	registered, other := &Service{Terrain: s.Terrain}, &Service{Terrain: s.Terrain}
	c = NewCheckpointContext(c.World)
	bindFeatureCheckpoint(t, registered, c, a)
	featureBindingRefused(t, other, c, "features.bindings")
}

func TestCheckpointFeatureOwnerIdentities(t *testing.T) {
	s, c, _ := featureCheckpointFixture(t)
	sim, crt := rng.NewSimulation(7), rng.NewCRT(11)
	s.Sim, s.Crt, s.Wind = &sim, &crt, &world.Wind{Strength: 13}
	bindFeatureCheckpoint(t, s, c, checkpoint.NewBindingAuthority())
	for _, owner := range []struct {
		name  string
		copy  func()
		clear func()
		reset func()
	}{
		{"Sim", func() { copySim := sim; s.Sim = &copySim }, func() { s.Sim = nil }, func() { s.Sim = &sim }},
		{"Crt", func() { copyCRT := crt; s.Crt = &copyCRT }, func() { s.Crt = nil }, func() { s.Crt = &crt }},
		{"Wind", func() { copyWind := *c.bindingWind; s.Wind = &copyWind }, func() { s.Wind = nil }, func() { s.Wind = c.bindingWind }},
		{"Terrain", func() { s.Terrain = newEmptyTerrain(8, 8) }, func() { s.Terrain = nil }, func() { s.Terrain = c.World.Terrain }},
	} {
		t.Run(owner.name, func(t *testing.T) {
			owner.copy()
			featureBindingRefused(t, s, c, "features.Service."+owner.name)
			owner.clear()
			featureBindingRefused(t, s, c, "features.Service."+owner.name)
			owner.reset()
			featureCheckpointBytes(t, s, c)
		})
	}
	// Expected-absent owners cannot be installed later, registered or otherwise.
	for _, registered := range []bool{false, true} {
		for _, name := range []string{"Sim", "Crt", "Wind"} {
			s.Sim, s.Crt, s.Wind = nil, nil, nil
			c := NewCheckpointContext(c.World)
			if registered {
				bindFeatureCheckpoint(t, s, c, checkpoint.NewBindingAuthority())
			}
			switch name {
			case "Sim":
				s.Sim = &sim
			case "Crt":
				s.Crt = &crt
			case "Wind":
				s.Wind = &world.Wind{}
			}
			featureBindingRefused(t, s, c, "features.Service."+name)
		}
	}
}

func TestCheckpointFeatureBoundCapturePurity(t *testing.T) {
	s, c, def := featureCheckpointFixture(t)
	inst := s.spawnFeatureAt(1, 1, def)
	if inst == nil {
		t.Fatal("fixture stamp failed")
	}
	startBurning(s, inst, []int32{3, -1, 0}, 2)
	s.sequences = map[sequenceKey][]int32{{def, 0}: {3, -1, 0}}
	s.instanceKeys, s.instanceKeysStale = []int{777}, true
	s.instanceValues = []*Instance{nil}
	sim, crt := rng.NewSimulation(17), rng.NewCRT(19)
	s.Sim, s.Crt, s.Wind = &sim, &crt, &world.Wind{Strength: 23}
	a := checkpoint.NewBindingAuthority()
	for _, slot := range featureCallbackSlots {
		slot.bind(s, a)
	}
	s.ShadowSequenceResolved = func(*content.FeatureDef, string) bool { panic("capture called shadow resolver") }
	bindFeatureCheckpoint(t, s, c, a)
	state := func() Service {
		copyService := *s
		copyService.instances = maps.Clone(s.instances)
		copyService.instanceKeys = slices.Clone(s.instanceKeys)
		copyService.instanceValues = slices.Clone(s.instanceValues)
		copyService.sequences = maps.Clone(s.sequences)
		for key, delays := range copyService.sequences {
			copyService.sequences[key] = slices.Clone(delays)
		}
		copyService.burnFrameGeometry, copyService.burnSmoke, copyService.burnSound = nil, nil, nil
		copyService.burnWeapon, copyService.geothermalSteam, copyService.sequenceFrames = nil, nil, nil
		copyService.ShadowSequenceResolved = nil
		return copyService
	}
	before, beforeContext, beforeWind := state(), *c, *s.Wind
	beforeSim, beforeCRT, beforeInst := sim, crt, *inst
	beforeInst.cursor.delays = slices.Clone(inst.cursor.delays)
	first := featureCheckpointBytes(t, s, c)
	if again := featureCheckpointBytes(t, s, c); !bytes.Equal(first, again) {
		t.Fatal("repeated capture changed payload")
	}
	if !reflect.DeepEqual(state(), before) || !reflect.DeepEqual(*inst, beforeInst) || *c != beforeContext || *s.Wind != beforeWind || sim != beforeSim || crt != beforeCRT {
		t.Fatal("capture mutated service, instance, cache, context or RNG/wind")
	}
	// Valid callback proof does not bypass the original traversal and immutable
	// timing checks, and failed capture must neither invoke nor repair a producer.
	s.activeWalking = true
	featureBindingRefused(t, s, c, "features.Service.activeWalking")
	s.activeWalking = false
	s.pendingBurnReplacement = &burnReplacement{cx: 1, cz: 1}
	featureBindingRefused(t, s, c, "features.Service.pendingBurnReplacement")
	s.pendingBurnReplacement = nil
	inst.cursor.delays[0] = 4
	featureBindingRefused(t, s, c, "features.Service.instances[9].cursor.delays")
	inst.cursor.delays[0] = 3
	if !reflect.DeepEqual(state(), before) || !reflect.DeepEqual(*inst, beforeInst) || *c != beforeContext || *s.Wind != beforeWind || sim != beforeSim || crt != beforeCRT {
		t.Fatal("refused capture mutated source or context")
	}
}

// Installation proof must not alter the established stamp, ignition, smoke,
// burn event, completion or reproduction order [05 R-ECO-02 §3]
// [05 R-FEAT-01 §9–§12]. Capture during this sequence must add no calls or draws.
func TestCheckpointFeatureAttestedLifecycleEquivalence(t *testing.T) {
	type result struct {
		trace   []string
		payload []byte
		plot    []world.PlotCell
		sim     rng.Simulation
		crt     rng.CRT
	}
	run := func(admitted bool) result {
		def := featureDef("binding-tree", 0, 0, 10)
		def.Filename, def.SeqNameBurn, def.BurnWeapon = "events", "burn", "spark"
		def.SparkTime, def.Geothermal = 4, true
		s, c, _ := featureCheckpointFixture(t, def)
		sim, crt := rng.SimulationFromState(73), rng.CRTFromState(79)
		s.Sim, s.Crt, s.Wind = &sim, &crt, &world.Wind{}
		a := checkpoint.NewBindingAuthority()
		bindFeatureCheckpoint(t, s, c, a)
		var trace []string
		s.SetSequenceFrames(func(d *content.FeatureDef, selector uint8) []int32 {
			trace = append(trace, fmt.Sprintf("sequence:%s:%d", d.CanonicalKey, selector))
			return []int32{3, -1, 0}
		})
		s.SetGeothermalSteam(func(x, y, z numeric.Fixed) {
			trace = append(trace, fmt.Sprintf("steam:%v", [3]numeric.Fixed{x, y, z}))
		})
		s.SetBurnSound(func(pos [3]numeric.Fixed) { trace = append(trace, fmt.Sprintf("sound:%v", pos)) })
		s.SetBurnWeapon(func(name string, pos [3]numeric.Fixed) {
			trace = append(trace, fmt.Sprintf("weapon:%s:%v", name, pos))
		})
		s.SetBurnFrameGeometry(func(d *content.FeatureDef, visit int32) (int32, int32, int32, int32) {
			trace = append(trace, fmt.Sprintf("geometry:%s:%d", d.CanonicalKey, visit))
			return 10, 14, 2, 3
		})
		s.SetBurnSmoke(func(pos [3]numeric.Fixed) {
			trace = append(trace, fmt.Sprintf("smoke:%v", pos))
			crt.Rand() // Existing strip producer's third draw [05 R-FEAT-01 §16].
		})
		attest := func() {
			s.SetSequenceFramesWithCheckpointBinding(s.SequenceFramesHook(), a)
			s.SetGeothermalSteamWithCheckpointBinding(s.GeothermalSteamHook(), a)
			s.SetBurnSoundWithCheckpointBinding(s.BurnSoundHook(), a)
			s.SetBurnWeaponWithCheckpointBinding(s.BurnWeaponHook(), a)
			s.SetBurnFrameGeometryWithCheckpointBinding(s.BurnFrameGeometryHook(), a)
			s.SetBurnSmokeWithCheckpointBinding(s.BurnSmokeHook(), a)
		}
		if admitted {
			attest()
		}
		if s.PlaceAt(1, 1, def) == nil || s.PlaceAt(6, 6, def) == nil || !s.igniteAt(1, 1, def) {
			t.Fatal("fixture placement or ignition failed")
		}
		for tick := uint32(0); tick < 10; tick++ {
			s.TickLifecycle(tick)
			if admitted {
				beforeCalls, beforeSim, beforeCRT := len(trace), sim, crt
				featureCheckpointBytes(t, s, c)
				if len(trace) != beforeCalls || sim != beforeSim || crt != beforeCRT {
					t.Fatal("capture added lifecycle calls or draws")
				}
			}
		}
		if s.InstanceAt(1, 1) != nil || s.InstanceAt(6, 6) == nil || sim.Draws() != 2 || crt.Draws() != 6 {
			t.Fatalf("lifecycle result: burnt=%v resting=%v sim=%d crt=%d", s.InstanceAt(1, 1), s.InstanceAt(6, 6), sim.Draws(), crt.Draws())
		}
		// The ordinary run acquires proof only after gameplay, allowing both
		// completed payloads to be compared without changing callback presence.
		attest()
		return result{trace, featureCheckpointBytes(t, s, c), slices.Clone(s.Terrain.Plot), sim, crt}
	}
	ordinary, admitted := run(false), run(true)
	if !reflect.DeepEqual(ordinary, admitted) {
		t.Fatalf("attestation changed lifecycle state or trace\nordinary %#v\nadmitted %#v", ordinary, admitted)
	}
}
