package features

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/world"
	"github.com/nanolathe-gg/nanolathe/vfs"
)

// The cursor fixture uses authored GAF bytes frozen by the real content owner.
func featureCheckpointFixture(t *testing.T, extra ...*content.FeatureDef) (*Service, *CheckpointContext, *content.FeatureDef) {
	t.Helper()
	def := &content.FeatureDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: "tree"}, FootprintX: 1, FootprintZ: 1, Filename: "events", SeqNameBurn: "burn", SeqNameDie: "die", SeqNameReclamate: "reclaim"}
	cat := &content.Catalog{Features: map[string]*content.FeatureDef{"tree": def}}
	for _, v := range extra {
		cat.Features[v.CanonicalKey] = v
	}
	entries := []formats.GAFWriteEntry{}
	for _, name := range []string{"burn", "die", "reclaim"} {
		entries = append(entries, formats.GAFWriteEntry{Name: name, Frames: []formats.GAFWriteFrame{{Width: 1, Height: 1, Duration: 3, Pixels: []byte{1}}, {Width: 1, Height: 1, Duration: 0xffffffff, Pixels: []byte{2}}, {Width: 1, Height: 1, Duration: 0, Pixels: []byte{3}}}})
	}
	data, err := formats.EncodeGAF(entries)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "anims"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "anims", "events.gaf"), data, 0644); err != nil {
		t.Fatal(err)
	}
	fs := vfs.New()
	if err := fs.MountDirectory(root, 10); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	sources, err := content.CaptureSimulationSources(fs, "")
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := content.FreezeSimulationInputs(sources, content.SimulationInputRequest{Catalog: cat})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := inputs.CheckpointKeys()
	if err != nil {
		t.Fatal(err)
	}
	terrain := newEmptyTerrain(8, 8)
	s := NewService(terrain, nil, nil, nil)
	c := NewCheckpointContext(&world.CheckpointContext{Keys: keys, Terrain: terrain})
	return s, c, def
}

func featureCheckpointBytes(t *testing.T, s *Service, c *CheckpointContext) []byte {
	t.Helper()
	if added, err := s.CollectCheckpointReferences(c); err != nil || added != 0 {
		t.Fatalf("collect %d, %v", added, err)
	}
	var out bytes.Buffer
	if err := s.WriteCheckpoint(checkpoint.NewEncoder(&out), c); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// All wide fixed-point values exceed 32 bits. The absent definition/cursor
// edges retain their surrounding residual fields instead of zeroing a record.
func TestCheckpointFeatureVector(t *testing.T) {
	s, c, _ := featureCheckpointFixture(t)
	s.cursor = -2
	inst := &Instance{Terrain: s.Terrain, CX: 1, CZ: 1, AnimationSelector: 2, BurnCountdown: -3, DamageAccumulator: 0xabcd, FootprintX: 5, FootprintZ: 6, Orientation: Orientation{0x1234, 0x5678, 0x9abc}, RemoteSuppressed: true,
		Vy: numeric.Fixed(-(1 << 45) + 4), X: numeric.Fixed((1 << 42) + 1), Y: numeric.Fixed(-(1 << 43) + 2), Z: numeric.Fixed((1 << 44) + 3), cursor: eventCursor{delay: -4, frame: 7}}
	s.instances[9] = inst
	got := featureCheckpointBytes(t, s, c)
	want, err := hex.DecodeString("0000000000000000" + "010000" + "0000000000000000" + "feffffffffffffff" + "01000000" + "0900000000000000" +
		"02fdffffff01000000000000000100000000000000cdab00" + "05000000060000000000" + "34127856bc9a0101" +
		"0400000000e0ffff01000000000400000200000000f8ffff0300000000100000" + "00fcffffff00070000000000" + "00000000")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload\ngot  %x\nwant %x", got, want)
	}
}

func TestCheckpointFeatureRetainedMutations(t *testing.T) {
	s, c, def := featureCheckpointFixture(t)
	inst := s.spawnFeatureAt(1, 1, def)
	if inst == nil {
		t.Fatal("stamp failed")
	}
	baseline := featureCheckpointBytes(t, s, c)
	original := *inst
	for name, edit := range map[string]func(*Instance){
		"selector": func(v *Instance) { v.AnimationSelector = 2 }, "countdown": func(v *Instance) { v.BurnCountdown = 17 },
		"damage": func(v *Instance) { v.DamageAccumulator = 0xffff }, "footprint X": func(v *Instance) { v.FootprintX = 3 }, "footprint Z": func(v *Instance) { v.FootprintZ = 4 },
		"animating": func(v *Instance) { v.IsAnimating = true }, "burning": func(v *Instance) { v.IsBurning = true },
		"bank": func(v *Instance) { v.Bank = 7 }, "heading": func(v *Instance) { v.Heading = 8 }, "pitch": func(v *Instance) { v.Pitch = 9 },
		"remote": func(v *Instance) { v.RemoteSuppressed = true }, "velocity": func(v *Instance) { v.Vy = 1 << 40 },
		"X": func(v *Instance) { v.X += 1 << 40 }, "Y": func(v *Instance) { v.Y += 1 << 40 }, "Z": func(v *Instance) { v.Z += 1 << 40 },
		"runtime": func(v *Instance) { v.runtimeLive = true }, "cursor delay": func(v *Instance) { v.cursor.delay = -1 }, "cursor frame": func(v *Instance) { v.cursor.frame = 6 },
		"sequence presence": func(v *Instance) { v.cursor.delays = []int32{3, -1, 0} },
	} {
		t.Run(name, func(t *testing.T) {
			*inst = original
			edit(inst)
			if bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
				t.Fatal("retained mutation vanished")
			}
		})
	}
	*inst = original
	s.cursor = 1 << 40
	if bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
		t.Fatal("global cursor vanished")
	}
	s.cursor = 63
	s.attachEventRecord(inst)
	if bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
		t.Fatal("active/arena membership vanished")
	}
	// Dormant runtime records still retain the arena charge.
	s.unlinkActive(inst)
	if bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
		t.Fatal("dormant arena charge vanished")
	}
}

func TestCheckpointFeaturesSortInstancesPreserveActiveOrderAndPurity(t *testing.T) {
	s, c, def := featureCheckpointFixture(t)
	a := s.spawnFeatureAt(1, 1, def)
	b := s.spawnFeatureAt(4, 2, def)
	a.IsBurning = true
	b.IsAnimating = true
	b.AnimationSelector = 1
	a.cursor.start([]int32{3, -1, 0})
	b.cursor.start([]int32{3, -1, 0})
	s.attachEventRecord(a)
	s.attachEventRecord(b)
	s.instanceKeys = []int{777}
	s.instanceValues = []*Instance{nil}
	s.instanceKeysStale = true
	s.sequences = map[sequenceKey][]int32{{def, 0}: {3, -1, 0}}
	before := *s
	beforeA, beforeB := *a, *b
	first := featureCheckpointBytes(t, s, c)
	if !reflect.DeepEqual(before, *s) || !reflect.DeepEqual(beforeA, *a) || !reflect.DeepEqual(beforeB, *b) {
		t.Fatal("capture changed live service/cache/list")
	}
	if got := first[len(first)-20:]; binary.LittleEndian.Uint32(got) != 2 || binary.LittleEndian.Uint64(got[4:]) != 20 || binary.LittleEndian.Uint64(got[12:]) != 9 {
		t.Fatalf("active anchor order %x", got)
	}
	s.instances = map[int]*Instance{20: b, 9: a}
	if !bytes.Equal(first, featureCheckpointBytes(t, s, c)) {
		t.Fatal("map insertion order changed payload")
	}
	s.unlinkActive(b)
	s.unlinkActive(a)
	s.linkActive(b)
	s.linkActive(a)
	if bytes.Equal(first, featureCheckpointBytes(t, s, c)) {
		t.Fatal("active order lost")
	}
	s.unlinkActive(a)
	s.unlinkActive(b)
	s.linkActive(a)
	s.linkActive(b)
	a.cursor.delay--
	if bytes.Equal(first, featureCheckpointBytes(t, s, c)) {
		t.Fatal("live delay lost")
	}
	a.cursor.delay++
	a.AnimationSelector = 1
	if bytes.Equal(first, featureCheckpointBytes(t, s, c)) {
		t.Fatal("sequence identity lost")
	}
}

func TestCheckpointFeatureExclusions(t *testing.T) {
	s, c, def := featureCheckpointFixture(t)
	inst := s.spawnFeatureAt(1, 1, def)
	baseline := featureCheckpointBytes(t, s, c)
	inst.ReclaimProgress = 98
	inst.Status = 0xff
	inst.IsSinking = true
	inst.Settled = true
	inst.runtimeShadowEnabled = true
	inst.SavedAnchorWord = 65535
	s.LastReproIdx = 72
	s.instanceKeys = []int{999}
	s.instanceValues = []*Instance{nil}
	s.instanceKeysStale = true
	s.definitionRestoreStart = 9
	s.definitionRestoreStarted = true
	s.definitionAdmissionObserver = func(*content.FeatureDef) { t.Error("observer invoked") }
	s.ShadowSequenceResolved = func(*content.FeatureDef, string) bool { t.Error("shadow resolver invoked"); return false }
	s.sequences = map[sequenceKey][]int32{{def, 0}: {3, -1, 0}}
	if !bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
		t.Fatal("excluded data changed payload")
	}
	// An unretained, same-name definition in the sequence cache needs only its
	// frozen sequence values; the cache carries no definition edge in the stream.
	copy := *def
	s.sequences[sequenceKey{&copy, 0}] = []int32{3, -1, 0}
	if !bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
		t.Fatal("cache object identity leaked")
	}
}

func TestCheckpointFeatureNormalizationAtExistingStamp(t *testing.T) {
	bad := &content.FeatureDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: "bad"}, FootprintX: 0, FootprintZ: -1, Damage: -2, Metal: -3, Energy: -4, Filename: "events", SeqNameBurn: "burn"}
	s, c, _ := featureCheckpointFixture(t, bad)
	s.Terrain.FeatureDefs = []*content.FeatureDef{bad}
	inst := s.spawnFeatureAt(1, 1, bad)
	if inst == nil || inst.Def == bad || inst.checkpointBase != bad {
		t.Fatal("stamp did not retain normalization base")
	}
	if len(s.Terrain.FeatureDefs) != 1 || s.Terrain.FeatureDefs[0] != bad {
		t.Fatal("checkpoint changed same-name admission")
	}
	baseline := featureCheckpointBytes(t, s, c)
	inst.checkpointBase = nil
	if _, err := s.CollectCheckpointReferences(c); err == nil {
		t.Fatal("name-only instance provenance accepted")
	}
	inst.checkpointBase = bad
	inst.Def.Metal = 1
	if _, err := s.CollectCheckpointReferences(c); err == nil {
		t.Fatal("changed normalized instance accepted")
	}
	inst.Def.Metal = 0
	if !bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
		t.Fatal("validation changed normalized instance")
	}
	// With no same-name terrain row, the normalized object itself is appended,
	// so the terrain must retain exactly this base relation even if placement
	// later fails on an authored void cell.
	s.Terrain.FeatureDefs = nil
	if s.spawnFeatureAt(-1, 0, bad) != nil || len(s.Terrain.FeatureDefs) != 0 {
		t.Fatal("out-of-bounds attempt retained a definition")
	}
	s.Terrain.Plot[0].SetFeature(world.PlotFeatureVoid)
	if s.spawnFeatureAt(0, 0, bad) != nil {
		t.Fatal("void placement succeeded")
	}
	if len(s.Terrain.FeatureDefs) != 1 {
		t.Fatal("existing append-before-stamp behavior changed")
	}
	normalized := s.Terrain.FeatureDefs[0]
	if ref, err := s.Terrain.CheckpointFeature(c.World.Keys, normalized, nil); err != nil || ref.Variant != 2 {
		t.Fatalf("retained failed-placement definition = %+v, %v", ref, err)
	}
}

func TestCheckpointFeatureRefusals(t *testing.T) {
	s, c, def := featureCheckpointFixture(t)
	baseline := *s
	for name, edit := range map[string]func(){
		"walking": func() { s.activeWalking = true }, "handoff": func() { s.pendingBurnReplacement = &burnReplacement{} },
		"sim": func() { s.Sim = &rng.Simulation{} }, "crt": func() { s.Crt = &rng.CRT{} }, "wind": func() { s.Wind = &world.Wind{} },
		"frames": func() {
			s.SetSequenceFrames(func(*content.FeatureDef, uint8) []int32 { t.Error("invoked"); return nil })
		},
		"geometry": func() {
			s.SetBurnFrameGeometry(func(*content.FeatureDef, int32) (int32, int32, int32, int32) { t.Error("invoked"); return 0, 0, 0, 0 })
		},
		"smoke": func() { s.SetBurnSmoke(func([3]numeric.Fixed) { t.Error("invoked") }) }, "sound": func() { s.SetBurnSound(func([3]numeric.Fixed) { t.Error("invoked") }) },
		"weapon": func() { s.SetBurnWeapon(func(string, [3]numeric.Fixed) { t.Error("invoked") }) }, "steam": func() { s.SetGeothermalSteam(func(numeric.Fixed, numeric.Fixed, numeric.Fixed) { t.Error("invoked") }) },
		"terrain": func() { s.Terrain = &world.Terrain{} },
		"cache":   func() { s.sequences = map[sequenceKey][]int32{{def, 0}: {4, -1, 0}} },
	} {
		t.Run(name, func(t *testing.T) { *s = baseline; edit(); checkFeatureCheckpointRefused(t, s, c) })
	}
	*s = baseline
}

func checkFeatureCheckpointRefused(t *testing.T, s *Service, c *CheckpointContext) {
	t.Helper()
	if _, err := s.CollectCheckpointReferences(c); err == nil || !strings.HasPrefix(err.Error(), "nanolathe: checkpoint capture failed: logical path ") {
		t.Fatalf("collector error %v", err)
	}
	var out bytes.Buffer
	e := checkpoint.NewEncoder(&out)
	if err := s.WriteCheckpoint(e, c); err == nil || !strings.HasPrefix(err.Error(), "nanolathe: checkpoint owner stream: logical path ") || e.Err() == nil {
		t.Fatalf("writer error %v", err)
	}
	if out.Len() != 0 {
		t.Fatal("failed preflight wrote bytes")
	}
}

func TestCheckpointFeatureRejectsBrokenOwnershipAndLinks(t *testing.T) {
	s, c, def := featureCheckpointFixture(t)
	a := s.spawnFeatureAt(1, 1, def)
	b := s.spawnFeatureAt(2, 1, def)
	a.IsBurning = true
	b.IsBurning = true
	s.attachEventRecord(a)
	s.attachEventRecord(b)
	before, aa, bb := *s, *a, *b
	for name, edit := range map[string]func(){
		"cycle": func() { a.nextActive = b }, "backlink": func() { a.prevActive = nil }, "flag": func() { a.onActive = false },
		"unlisted": func() { s.activeHead = nil }, "foreign head": func() { s.activeHead = &Instance{onActive: true} },
		"charge": func() { s.arenaHeld++ }, "unbilled": func() { a.arenaBilled = false; s.arenaHeld-- },
		"instance terrain": func() { a.Terrain = &world.Terrain{} }, "anchor": func() { a.CX++ },
		"cursor": func() { a.cursor.delays = []int32{3, -1, 1} }, "selector": func() { a.cursor.delays = []int32{3, -1, 0}; a.AnimationSelector = 3 },
		"foreign definition": func() { copy := *def; a.Def = &copy },
	} {
		t.Run(name, func(t *testing.T) { *s = before; *a = aa; *b = bb; edit(); checkFeatureCheckpointRefused(t, s, c) })
	}
	*s = before
	*a = aa
	*b = bb
	s.instances[12] = a
	checkFeatureCheckpointRefused(t, s, c)
	delete(s.instances, 12)
	s.instances[12] = nil
	checkFeatureCheckpointRefused(t, s, c)
}

func TestCheckpointFeatureActiveWalkMarker(t *testing.T) {
	s, c, def := featureCheckpointFixture(t)
	inst := s.spawnFeatureAt(1, 1, def)
	inst.IsBurning = true
	inst.cursor.start([]int32{3, -1, 0})
	s.attachEventRecord(inst)
	called := false
	s.SetBurnSmoke(func([3]numeric.Fixed) {
		called = true
		s.SetBurnSmoke(nil)
		if _, err := s.CollectCheckpointReferences(c); err == nil || !strings.Contains(err.Error(), "activeWalking") {
			t.Fatalf("interior capture = %v", err)
		}
	})
	s.activeWalk(0)
	if !called || s.activeWalking {
		t.Fatal("walk marker did not follow callback boundary")
	}
	featureCheckpointBytes(t, s, c)
}

func TestCheckpointFeatureCachedMissesAndStableRefusal(t *testing.T) {
	missing := &content.FeatureDef{DefinitionHeader: content.DefinitionHeader{CanonicalKey: "missing"}, FootprintX: 1, FootprintZ: 1, Filename: "events", SeqNameBurn: "missing"}
	s, c, def := featureCheckpointFixture(t, missing)
	baseline := featureCheckpointBytes(t, s, c)
	s.sequences = map[sequenceKey][]int32{{missing, 0}: nil}
	if !bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
		t.Fatal("admitted miss changed payload")
	}
	s.sequences[sequenceKey{def, 0}] = nil
	checkFeatureCheckpointRefused(t, s, c)
	delete(s.sequences, sequenceKey{def, 0})
	foreign := *missing
	foreign.SeqNameBurn = "not requested"
	s.sequences[sequenceKey{&foreign, 0}] = nil
	checkFeatureCheckpointRefused(t, s, c)
	// Equal authored feature names do not order this pointer-keyed cache. The
	// logical sequence key and its actual nil/delay words choose a stable error.
	first, second := *def, *def
	first.SeqNameBurn = "z-missing"
	second.SeqNameBurn = "a-missing"
	s.sequences = map[sequenceKey][]int32{{&first, 0}: {1}, {&second, 0}: nil, {def, 0}: {2}}
	_, _, err := s.checkpointState(c)
	if err == nil || !strings.Contains(err.Error(), "a-missing") {
		t.Fatalf("first refusal %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, _, again := s.checkpointState(c); again == nil || again.Error() != err.Error() {
			t.Fatalf("unstable refusal %v", again)
		}
	}
}

func TestCheckpointFeatureRefusesIntermediateNormalizationBase(t *testing.T) {
	base := &content.FeatureDef{FootprintX: 0, FootprintZ: 0, Filename: "events"}
	s, c, _ := featureCheckpointFixture(t, base)
	first := s.spawnFeatureAt(1, 1, base)
	if first == nil {
		t.Fatal("first stamp refused")
	}
	featureCheckpointBytes(t, s, c)
	second := s.spawnFeatureAt(3, 1, first.Def)
	if second == nil || second.checkpointBase != first.Def {
		t.Fatal("repeat stamp changed normalization semantics")
	}
	checkFeatureCheckpointRefused(t, s, c)
}

func TestCheckpointFeatureValidLookupCacheValidation(t *testing.T) {
	s, c, def := featureCheckpointFixture(t)
	a := s.spawnFeatureAt(1, 1, def)
	baseline := featureCheckpointBytes(t, s, c)
	s.sortedInstanceKeys()
	before := *s
	if !bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) || !reflect.DeepEqual(before, *s) {
		t.Fatal("valid cache affected capture")
	}
	s.instanceKeys[0] = 12
	checkFeatureCheckpointRefused(t, s, c)
	if s.instanceKeys[0] != 12 {
		t.Fatal("capture repaired invalid cached key")
	}
	s.instanceKeys[0] = 9
	s.instanceValues[0] = &Instance{}
	wrong := s.instanceValues[0]
	checkFeatureCheckpointRefused(t, s, c)
	if s.instanceValues[0] != wrong {
		t.Fatal("capture repaired invalid cached value")
	}
	s.instanceValues = nil // a length mismatch makes the consumer use the map
	if !bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
		t.Fatal("unused value cache affected capture")
	}
	s.instanceValues = []*Instance{a}
	s.instanceKeysStale = true
	s.instanceKeys[0] = 12
	s.instanceValues[0] = nil
	if !bytes.Equal(baseline, featureCheckpointBytes(t, s, c)) {
		t.Fatal("stale cache affected capture")
	}
	if s.instanceKeys[0] != 12 || s.instanceValues[0] != nil || !s.instanceKeysStale {
		t.Fatal("capture rebuilt stale cache")
	}
}
