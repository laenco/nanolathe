package features

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// CheckpointContext shares the collected terrain identity and frozen content
// keys. Features has no object table (DESIGN_MULTIPLAYER §16.3.11).
type CheckpointContext struct {
	World *world.CheckpointContext

	bindingService   *Service
	bindingSim       *rng.Simulation
	bindingCrt       *rng.CRT
	bindingWind      *world.Wind
	bindingAuthority *checkpoint.BindingAuthority
}

// NewCheckpointContext reuses the terrain owner's capture-local context.
func NewCheckpointContext(w *world.CheckpointContext) *CheckpointContext {
	return &CheckpointContext{World: w}
}

// CollectCheckpointReferences validates the service without adding graph IDs.
func (s *Service) CollectCheckpointReferences(c *CheckpointContext) (int, error) {
	_, _, err := s.checkpointState(c)
	return 0, err
}

// WriteCheckpoint writes only the feature-service payload. Service fields:
// BurnFrameGeometry, BurnSmoke, BurnSound, BurnWeapon, Crt, GeothermalSteam,
// SequenceFrames, Sim, Terrain, Wind, Rules, arenaHeld, cursor; then count/key/instance
// rows in numeric anchor order and count/key active order. Ports and Terrain
// use presence tags after validation against the capture context.
// Instance fields are AnimationSelector, BurnCountdown, CX, CZ,
// DamageAccumulator, Def, FootprintX, FootprintZ, IsAnimating, IsBurning,
// Orientation (Bank, Heading, Pitch), RemoteSuppressed, Terrain, Vy, X, Y, Z,
// arenaBilled, cursor (delay, delays presence/key, frame), onActive, runtimeLive.
// Def uses presence/variant/base/five normalization words as the terrain does.
// No caches, presentation state, provenance, or link pointers are emitted
// (DESIGN_MULTIPLAYER §16.3.5–§16.3.6, §16.3.11).
func (s *Service) WriteCheckpoint(e *checkpoint.Encoder, c *CheckpointContext) error {
	e.Field("features.Service")
	keys, active, err := s.checkpointState(c)
	if err != nil {
		e.Fail(err)
		return e.Err()
	}
	for _, port := range []struct {
		name    string
		present bool
	}{
		{"BurnFrameGeometry", s.burnFrameGeometry != nil}, {"BurnSmoke", s.burnSmoke != nil},
		{"BurnSound", s.burnSound != nil}, {"BurnWeapon", s.burnWeapon != nil},
		{"Crt", s.Crt != nil}, {"GeothermalSteam", s.geothermalSteam != nil},
		{"SequenceFrames", s.sequenceFrames != nil}, {"Sim", s.Sim != nil},
	} {
		e.Field("features.Service." + port.name)
		e.Bool(port.present)
	}
	e.Field("features.Service.Terrain")
	e.Bool(s.Terrain != nil)
	e.Field("features.Service.Wind")
	e.Bool(s.Wind != nil)
	kind, _ := CheckpointRulesKind(s.Rules) // validated by checkpointState
	e.Field("features.Service.Rules")
	e.U8(kind)
	e.Field("features.Service.arenaHeld")
	e.I64(int64(s.arenaHeld))
	e.Field("features.Service.cursor")
	e.I64(int64(s.cursor))
	e.Field("features.Service.instances")
	e.Count(len(keys))
	for _, key := range keys {
		path := fmt.Sprintf("features.Service.instances[%d]", key)
		e.Field(path)
		e.I64(int64(key))
		s.writeCheckpointInstance(e, c, s.instances[key], path)
	}
	e.Field("features.Service.activeHead")
	e.Count(len(active))
	for _, key := range active {
		e.I64(int64(key))
	}
	return e.Err()
}

func (s *Service) writeCheckpointInstance(e *checkpoint.Encoder, c *CheckpointContext, inst *Instance, path string) {
	e.Field(path + ".AnimationSelector")
	e.U8(inst.AnimationSelector)
	e.Field(path + ".BurnCountdown")
	e.I32(inst.BurnCountdown)
	e.Field(path + ".CX")
	e.I64(int64(inst.CX))
	e.Field(path + ".CZ")
	e.I64(int64(inst.CZ))
	e.Field(path + ".DamageAccumulator")
	e.U16(inst.DamageAccumulator)
	e.Field(path + ".Def")
	e.Bool(inst.Def != nil)
	if inst.Def != nil {
		ref, err := s.Terrain.CheckpointFeature(c.World.Keys, inst.Def, inst.checkpointBase)
		if err != nil {
			e.Fail(err)
			return
		}
		e.U8(ref.Variant)
		e.Definition(ref.Base)
		if ref.Variant == 2 {
			e.I32(ref.FootprintX)
			e.I32(ref.FootprintZ)
			e.I32(ref.Damage)
			e.I32(ref.Metal)
			e.I32(ref.Energy)
		}
	}
	e.Field(path + ".FootprintX")
	e.I32(inst.FootprintX)
	e.Field(path + ".FootprintZ")
	e.I32(inst.FootprintZ)
	e.Field(path + ".IsAnimating")
	e.Bool(inst.IsAnimating)
	e.Field(path + ".IsBurning")
	e.Bool(inst.IsBurning)
	e.Field(path + ".Orientation.Bank")
	e.U16(inst.Bank)
	e.Field(path + ".Orientation.Heading")
	e.U16(inst.Heading)
	e.Field(path + ".Orientation.Pitch")
	e.U16(inst.Pitch)
	e.Field(path + ".RemoteSuppressed")
	e.Bool(inst.RemoteSuppressed)
	e.Field(path + ".Terrain")
	e.Bool(inst.Terrain != nil)
	e.Field(path + ".Vy")
	e.I64(int64(inst.Vy))
	e.Field(path + ".X")
	e.I64(int64(inst.X))
	e.Field(path + ".Y")
	e.I64(int64(inst.Y))
	e.Field(path + ".Z")
	e.I64(int64(inst.Z))
	e.Field(path + ".arenaBilled")
	e.Bool(inst.arenaBilled)
	e.Field(path + ".cursor.delay")
	e.I32(inst.cursor.delay)
	e.Field(path + ".cursor.delays")
	e.Bool(inst.cursor.delays != nil)
	if inst.cursor.delays != nil {
		ref, err := c.World.Keys.FeatureSequence(inst.Def.Filename, EventSequenceName(inst.Def, inst.AnimationSelector), inst.cursor.delays)
		if err != nil {
			e.Fail(err)
			return
		}
		e.Definition(ref)
	}
	e.Field(path + ".cursor.frame")
	e.I32(inst.cursor.frame)
	e.Field(path + ".onActive")
	e.Bool(inst.onActive)
	e.Field(path + ".runtimeLive")
	e.Bool(inst.runtimeLive)
}

// checkpointState walks detached sorted keys, never sortedInstanceKeys: capture
// must not refresh a live lookup cache or rewrite active links. Dormant records
// may keep stale next links by unlinkActive's traversal contract; only active
// links are a retained order (DESIGN_MULTIPLAYER §16.3.11).
func (s *Service) checkpointState(c *CheckpointContext) ([]int, []int, error) {
	fail := func(path, expected string) ([]int, []int, error) {
		return nil, nil, featureCheckpointError(path, expected)
	}
	if s == nil {
		return fail("features.Service", "a present service")
	}
	if c == nil || c.World == nil || c.World.Keys == nil {
		return fail("features.context", "a world context with admitted content keys")
	}
	if s.Terrain != c.World.Terrain {
		return fail("features.Service.Terrain", "the collected terrain identity")
	}
	if s.activeWalking {
		return fail("features.Service.activeWalking", "a completed active traversal")
	}
	if s.pendingBurnReplacement != nil {
		return fail("features.Service.pendingBurnReplacement", "a completed burn handoff")
	}
	if err := s.validateCheckpointBindings(c); err != nil {
		return nil, nil, err
	}
	if _, err := CheckpointRulesKind(s.Rules); err != nil {
		return nil, nil, err
	}
	keys := slices.Sorted(maps.Keys(s.instances))
	// A stale or absent key row rebuilds before use. A row advertised as
	// valid is read directly, so exclusion requires checking its derivation.
	if !s.instanceKeysStale && s.instanceKeys != nil {
		if !slices.Equal(keys, s.instanceKeys) {
			return fail("features.Service.instanceKeys", "valid cached keys matching the instance map")
		}
		if len(s.instanceValues) == len(keys) {
			for i, key := range keys {
				if s.instanceValues[i] != s.instances[key] {
					return fail("features.Service.instanceValues", "valid cached values matching the instance map")
				}
			}
		}
	}
	identities := make(map[*Instance]int, len(keys))
	charged := 0
	for _, key := range keys {
		path := fmt.Sprintf("features.Service.instances[%d]", key)
		inst := s.instances[key]
		if inst == nil {
			return fail(path, "a live lookup record")
		}
		if _, duplicate := identities[inst]; duplicate {
			return fail(path, "one anchor per instance identity")
		}
		identities[inst] = key
		if inst.Terrain != s.Terrain || s.Terrain == nil {
			return fail(path+".Terrain", "the service terrain identity")
		}
		if inst.CX < 0 || int64(inst.CX) >= int64(s.Terrain.CellW) || inst.CZ < 0 || int64(inst.CZ) >= int64(s.Terrain.CellH) || int64(key) != int64(inst.CZ)*int64(s.Terrain.CellW)+int64(inst.CX) {
			return fail(path, "the instance's row-major anchor key")
		}
		if inst.Def != nil {
			if _, err := s.Terrain.CheckpointFeature(c.World.Keys, inst.Def, inst.checkpointBase); err != nil {
				return nil, nil, fmt.Errorf("%w: %w", featureCheckpointError(path+".Def", "an admitted feature definition or validated normalization"), err)
			}
		} else if inst.checkpointBase != nil {
			return fail(path+".Def", "a definition for the recorded normalization")
		}
		if inst.cursor.delays != nil {
			if inst.Def == nil {
				return fail(path+".cursor.delays", "the cursor's definition")
			}
			if _, err := c.World.Keys.FeatureSequence(inst.Def.Filename, EventSequenceName(inst.Def, inst.AnimationSelector), inst.cursor.delays); err != nil {
				return nil, nil, fmt.Errorf("%w: %w", featureCheckpointError(path+".cursor.delays", "the admitted sequence and exact delay words"), err)
			}
		}
		if inst.arenaBilled {
			charged++
		}
	}
	// An in-battle switch to Strict retains existing Modern records. Admission
	// limits future allocations; checkpoint validity checks the actual records.
	if charged != s.arenaHeld || s.arenaHeld < 0 {
		return fail("features.Service.arenaHeld", "the exact charged live-arena count")
	}
	active := make([]int, 0)
	seen := make(map[*Instance]bool)
	var previous *Instance
	for inst := s.activeHead; inst != nil; inst = inst.nextActive {
		key, exists := identities[inst]
		if !exists || seen[inst] || !inst.onActive || inst.prevActive != previous || !inst.arenaBilled {
			return fail("features.Service.activeHead", "a coherent acyclic active list of charged owned instances")
		}
		active = append(active, key)
		seen[inst] = true
		previous = inst
	}
	for _, key := range keys {
		inst := s.instances[key]
		if inst.onActive != seen[inst] || (!inst.onActive && inst.prevActive != nil) {
			return fail(fmt.Sprintf("features.Service.instances[%d].onActive", key), "membership matching the active list")
		}
	}
	// This excluded cache is safe only when its existing words bind the frozen
	// sequence. Validation reads the cache directly and never calls its producer.
	// Sort logical keys before validation; pointer values never enter the stream.
	cached := slices.Collect(maps.Keys(s.sequences))
	slices.SortFunc(cached, func(a, b sequenceKey) int {
		if a.def == nil && b.def != nil {
			return -1
		}
		if a.def != nil && b.def == nil {
			return 1
		}
		name := func(k sequenceKey) string {
			if k.def == nil {
				return ""
			}
			return content.CanonicalKey(k.def.Filename) + "|" + content.CanonicalKey(EventSequenceName(k.def, k.selector))
		}
		if n := cmp.Compare(name(a), name(b)); n != 0 {
			return n
		}
		ad, bd := s.sequences[a], s.sequences[b]
		if ad == nil && bd != nil {
			return -1
		}
		if ad != nil && bd == nil {
			return 1
		}
		return slices.Compare(ad, bd)
	})
	for _, key := range cached {
		if key.def == nil {
			return fail("features.Service.sequences", "a definition for each cached sequence")
		}
		var err error
		if delays := s.sequences[key]; delays == nil {
			err = c.World.Keys.FeatureSequenceAbsent(key.def.Filename, EventSequenceName(key.def, key.selector))
		} else {
			_, err = c.World.Keys.FeatureSequence(key.def.Filename, EventSequenceName(key.def, key.selector), delays)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", featureCheckpointError("features.Service.sequences", "admitted immutable sequence metadata"), err)
		}
	}
	return keys, active, nil
}

func featureCheckpointError(path, expected string) error {
	return fmt.Errorf("nanolathe: checkpoint capture failed: logical path %s, providers searched [features], expected %s", path, expected)
}
