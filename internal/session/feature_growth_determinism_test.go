//go:build retail

package session

import (
	"bytes"
	"crypto/sha256"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/features"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/movement"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/checkpoint"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// Expanded feature state must survive the complete canonical capture, including
// active order, terrain, resources and both RNG streams (DESIGN_MULTIPLAYER
// §16.3.6; DESIGN_ECONOMY_CONSTRUCTION "Modern feature growth"). The existing
// busy fixture independently freezes and admits each battle and crosses the
// actual parallel publication threshold. NewPlaytestSkirmish does not install
// checkpoint admission, so this proves admitted single-seat execution, not
// cross-seat online checkpoint equivalence.
func TestFeatureGrowthCanonicalDeterminismRetail(t *testing.T) {
	originalWorkers := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(originalWorkers) })
	var peers [2]*Session
	var actors [2]*units.Unit
	var sites [2][]world.Cell
	var wrecks [2]*content.FeatureDef
	var identity checkpoint.Identity
	for peer := range peers {
		runtime.GOMAXPROCS([]int{1, 4}[peer])
		s := checkpointCostBusyBattle(t)
		t.Cleanup(s.closeAIControllers)
		peers[peer] = s
		if s.Gameplay != gameplay.Modern {
			t.Fatal("expanded fixture needs Modern rules")
		}
		s.publishWorkerLimit = []int{-1, 4}[peer]
		s.verifyFeatureRevisions = true
		if peer == 0 {
			identity = s.checkpointAdmission.identity
		} else if s.checkpointAdmission.identity != identity || s.World == peers[0].World {
			t.Fatalf("peers must independently admit the same content and configuration: identity=%v sharedWorld=%v", s.checkpointAdmission.identity == identity, s.World == peers[0].World)
		}
		for _, u := range s.Units.IterSliced() {
			if u != nil && u.Alive && u.Owner == s.LocalOwner && u.Def.Commander {
				actors[peer] = u
				break
			}
		}
		if actors[peer] == nil {
			t.Fatal("missing local commander")
		}
		unit, ok := s.Catalog.Unit("armpw")
		if !ok || unit == nil {
			t.Fatal("expanded fixture needs its existing Peewee definition")
		}
		wreck := s.Catalog.Features[content.CanonicalKey(unit.Corpse)]
		if wreck == nil || wreck.Filename != "" || !wreck.Reclaimable || wreck.Indestructible {
			t.Fatal("expanded fixture needs an ordinary reclaimable 3D wreck")
		}
		wrecks[peer] = wreck
		sites[peer] = featureGrowthStampWrecks(t, s, wreck, features.FeatureAnimSlots+8)
		if featureGrowth3DCount(s) <= features.FeatureAnimSlots {
			t.Fatal("fixture did not exceed retail live-record admission")
		}
		enableLifecycleBattle(t, s)
		if unitChannelLive(s) < publishParallelMinUnits || (peer == 1 && len(s.unitChannel.scratch) <= 1) {
			t.Fatal("expanded fixture did not exercise the publication worker path")
		}
	}
	if !reflect.DeepEqual(sites[0], sites[1]) || !reflect.DeepEqual(peers[0].CheckpointHistory(), peers[1].CheckpointHistory()) {
		t.Fatal("independently admitted expanded entries differ")
	}

	var outputs [2]bytes.Buffer
	var previous checkpoint.Digest
	startX, startZ := actors[0].X, actors[0].Z
	for pump, ticks := range []int{1, 2, 3, 1, 2, 3} {
		var payouts [2][2]float32
		for peer, s := range peers {
			runtime.GOMAXPROCS([]int{1, 4}[peer])
			at := sites[peer][pump]
			payouts[peer] = featureGrowthChurn(t, s, at, wrecks[peer], pump)
			command := "move"
			if pump&1 != 0 {
				command = "stop"
			}
			for _, command := range checkpointPortableCommands(command, actors[peer], startX, startZ) {
				if err := s.EnqueueHumanCommand(command); err != nil {
					t.Fatal(err)
				}
			}
			outputs[peer].Reset()
			if err := s.RequestCheckpointCapture(&outputs[peer]); err != nil {
				t.Fatal(err)
			}
			s.ExecuteStep(StepPlan{run: true, ticks: ticks})
			result := s.CheckpointCaptureResult()
			if result.Err != nil || result.Pending || result.Record.Digests.Full != sha256.Sum256(outputs[peer].Bytes()) {
				t.Fatalf("pump %d peer %d: incomplete canonical capture: %+v", pump, peer, result)
			}
			checkpointPortableHeader(t, outputs[peer].Bytes(), identity)
			if s.State != StateBattle || featureGrowth3DCount(s) <= features.FeatureAnimSlots {
				t.Fatal("script left the battle or stopped exercising the expanded arena")
			}
		}
		if payouts[0] != payouts[1] || *peers[0].SimRNG() != *peers[1].SimRNG() || *peers[0].CrtRNG() != *peers[1].CrtRNG() ||
			peers[0].CheckpointCaptureResult() != peers[1].CheckpointCaptureResult() || !bytes.Equal(outputs[0].Bytes(), outputs[1].Bytes()) ||
			!reflect.DeepEqual(peers[0].CheckpointHistory(), peers[1].CheckpointHistory()) {
			t.Fatalf("serial and parallel expanded worlds differ after pump %d", pump)
		}
		if !reflect.DeepEqual(peers[0].Snapshot.Current().Features, peers[1].Snapshot.Current().Features) {
			t.Fatal("serial and parallel expanded feature publications differ")
		}
		current := peers[0].CheckpointCaptureResult().Record.Digests.Full
		if current == previous {
			t.Fatal("command/tick/churn sequence did not change the canonical state")
		}
		previous = current
	}

	// A rule switch retains the expanded records. Full capture must accept
	// that valid overflow after admission returns to the retail cap.
	for peer, s := range peers {
		before := featureGrowth3DCount(s)
		sim, crt := *s.SimRNG(), *s.CrtRNG()
		if err := s.SetRules(StrictRuleSetName); err != nil {
			t.Fatal(err)
		}
		if featureGrowth3DCount(s) != before || *s.SimRNG() != sim || *s.CrtRNG() != crt {
			t.Fatal("Strict rebinding changed existing records or RNG")
		}
		s.publishPausedSnapshot(s.Clock.GlobalTick)
		outputs[peer].Reset()
		record, err := s.captureCheckpoint(s.checkpoints.keys, CheckpointPosition{Tick: s.Clock.GlobalTick, Boundary: CheckpointFinalPumpTick}, &outputs[peer])
		if err != nil || record.Digests.Full != sha256.Sum256(outputs[peer].Bytes()) {
			t.Fatalf("retained Strict overflow: %v", err)
		}
	}
	if !bytes.Equal(outputs[0].Bytes(), outputs[1].Bytes()) {
		t.Fatal("retained Strict overflow has different canonical state")
	}
}

// Stamp only unoccupied empty land footprints, in row/column order. This is
// setup through the ordinary service; frozen definitions and terrain inputs
// remain untouched [05 R-FEAT-01 §3].
func featureGrowthStampWrecks(t *testing.T, s *Session, wreck *content.FeatureDef, count int) []world.Cell {
	t.Helper()
	fx, fz := wreck.FootprintX, wreck.FootprintZ
	if fx <= 0 || fz <= 0 {
		t.Fatal("wreck has an empty footprint")
	}
	var sites []world.Cell
	for z := int32(8); z+fz < s.World.CellH-8 && len(sites) < count; z += fz {
		for x := int32(8); x+fx < s.World.CellW-8 && len(sites) < count; x += fx {
			at := world.Cell{X: x, Z: z}
			if s.Movement.Grid.FootprintOccupied(movement.Cell{X: x, Z: z}, int16(fx), int16(fz), -1) {
				continue
			}
			clear := true
			for dz := int32(0); dz < fz; dz++ {
				for dx := int32(0); dx < fx; dx++ {
					if s.World.PlotAt(x+dx, z+dz).Feature() != world.PlotFeatureNone {
						clear = false
					}
				}
			}
			if !clear || s.World.HeightAt(numeric.FixedFromInt(int64(x)*16), numeric.FixedFromInt(int64(z)*16)) <= numeric.FixedFromInt(int64(s.World.SeaLevel)) {
				continue
			}
			if s.Features.PlaceAt(int(x), int(z), wreck) == nil {
				t.Fatalf("expanded wreck admission failed at (%d,%d) after %d placements", x, z, len(sites))
			}
			sites = append(sites, at)
		}
	}
	if len(sites) != count {
		t.Fatalf("expanded fixture found only %d clear wreck footprints, need %d", len(sites), count)
	}
	return sites
}

func featureGrowth3DCount(s *Session) int {
	count := 0
	for _, inst := range s.Features.Instances() {
		if inst.Def != nil && inst.Def.Filename == "" {
			count++
		}
	}
	return count
}

func TestFeatureGrowthCheckpointPolicyRefusalsRetail(t *testing.T) {
	s := checkpointLifecycleBattle(t)
	t.Cleanup(s.closeAIControllers)
	enableLifecycleBattle(t, s)
	position := CheckpointPosition{Boundary: CheckpointEntry}
	baseline, err := s.captureCheckpoint(s.checkpoints.keys, position, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"session.Rules.FeaturePool", "session.Features.Rules"} {
		for _, policy := range []features.Rules{
			nil, features.StrictRules{}, features.CommunityRules{},
			(*features.StrictRules)(nil), (*features.CommunityRules)(nil), (*features.ModernRules)(nil),
			struct{ features.ModernRules }{},
			struct {
				features.Rules
				values []int
			}{},
		} {
			oldSet, oldService := s.Rules.FeaturePool, s.Features.Rules
			sim, crt := *s.SimRNG(), *s.CrtRNG()
			if target == "session.Rules.FeaturePool" {
				s.Rules.FeaturePool = policy
			} else {
				s.Features.Rules = policy
			}
			record, err := s.captureCheckpoint(s.checkpoints.keys, position, nil)
			if err == nil || !strings.Contains(err.Error(), target) || record != (CheckpointRecord{}) {
				t.Fatalf("%s accepted %T or returned usable state: record=%+v err=%v", target, policy, record, err)
			}
			if *s.SimRNG() != sim || *s.CrtRNG() != crt {
				t.Fatal("policy refusal consumed RNG")
			}
			s.Rules.FeaturePool, s.Features.Rules = oldSet, oldService
		}
	}
	restored, err := s.captureCheckpoint(s.checkpoints.keys, position, nil)
	if err != nil || restored != baseline {
		t.Fatalf("policy validation changed the admitted state: %v", err)
	}
}

func featureGrowthChurn(t *testing.T, s *Session, at world.Cell, wreck *content.FeatureDef, cycle int) [2]float32 {
	t.Helper()
	old := s.Features.InstanceAt(int(at.X), int(at.Z))
	if old == nil {
		t.Fatal("churn target vanished before reclaim")
	}
	metal, energy, ok := s.Features.ReclaimAt(int(at.X), int(at.Z))
	if !ok || metal != float32(wreck.Metal) || energy != float32(wreck.Energy) {
		t.Fatal("expanded reclaim lost the definition's payout")
	}
	if s.Features.InstanceAt(int(at.X), int(at.Z)) == old {
		t.Fatal("reclaim did not replace the old wreck")
	}
	pos := [3]numeric.Fixed{old.X, s.World.HeightAt(old.X, old.Z), old.Z}
	orient := features.Orientation{Bank: uint16(cycle), Heading: uint16(1000 + cycle), Pitch: uint16(2 * cycle)}
	if inst := s.Features.PlaceCorpse(at, pos, orient, wreck, false, uint8(cycle&1)); inst == nil || inst == old || inst.Orientation != orient {
		t.Fatal("new wreck did not replace the reclaimed feature above the retail limit")
	}
	return [2]float32{metal, energy}
}

// This complements the complete admitted capture above with actual online
// admission and distinct local seats. Online constructors do not currently
// install canonical-checkpoint admission; no receipt is synthesized here.
// The bounded diagnostic fingerprint, RNGs and explicit feature projection
// are therefore complementary evidence, not a full online checkpoint claim.
func TestFeatureGrowthOnlineSeatsAgreeRetail(t *testing.T) {
	originalWorkers := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(originalWorkers) })
	var peers [2]*Session
	var sites [2][]world.Cell
	var wrecks [2]*content.FeatureDef
	var actors [2]*units.Unit
	for peer := range peers {
		inputs, config := onlineRetailMatch(t, onlineRetailSetup(false, 0, 1, 2))
		s := onlineRetailSession(t, inputs, config, uint8(peer))
		peers[peer] = s
		unit, ok := s.Catalog.Unit("armpw")
		if !ok || unit == nil {
			t.Fatal("missing ordinary wreck source")
		}
		wrecks[peer] = s.Catalog.Features[content.CanonicalKey(unit.Corpse)]
		if wrecks[peer] == nil || wrecks[peer].Filename != "" || !wrecks[peer].Reclaimable {
			t.Fatal("online fixture requires a reclaimable 3D wreck")
		}
		sites[peer] = featureGrowthStampWrecks(t, s, wrecks[peer], features.FeatureAnimSlots+8)
		if s.checkpointAdmission != nil || s.LocalOwner != uint8(peer) {
			t.Fatal("online fixture changed its admission or perspective contract")
		}
		if peer == 0 {
			for _, u := range s.Units.IterSliced() {
				if u != nil && u.Alive && u.Def.Commander && int(u.Owner) < len(actors) {
					actors[u.Owner] = u
				}
			}
		}
	}
	if !reflect.DeepEqual(sites[0], sites[1]) || actors[0] == nil || actors[1] == nil {
		t.Fatal("online peers have different sites or missing seat actors")
	}
	compare := func(tick uint32) {
		t.Helper()
		left, err := peers[0].PartialStateFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		right, err := peers[1].PartialStateFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if left != right || *peers[0].SimRNG() != *peers[1].SimRNG() || *peers[0].CrtRNG() != *peers[1].CrtRNG() ||
			!reflect.DeepEqual(peers[0].World.Plot, peers[1].World.Plot) ||
			!reflect.DeepEqual(featureGrowthProjection(peers[0]), featureGrowthProjection(peers[1])) {
			t.Fatalf("expanded online seats differ at tick %d", tick)
		}
		for _, s := range peers {
			if featureGrowth3DCount(s) <= features.FeatureAnimSlots {
				t.Fatal("online fixture no longer exceeds retail admission")
			}
		}
	}
	compare(0)
	var position uint64
	for tick := uint32(1); tick <= 6; tick++ {
		for seat, actor := range actors {
			ref := pool.UnitRef{Handle: actor.Handle, Serial: actor.AllocationSerial}
			command := SeatCommand{Kind: SeatStop, Stop: StopPayload{Actors: []pool.UnitRef{ref}}}
			if tick&1 != 0 {
				command = SeatCommand{Kind: SeatOrder, Order: OrderPayload{Actors: []pool.UnitRef{ref}, Code: 2,
					Position: CommandPosition{X: actor.X + 64<<16, Y: actor.Y, Z: actor.Z}}}
			}
			position++
			for _, s := range peers {
				if err := s.EnqueueSeatCommand(CommandStamp{Seat: uint8(seat), Tick: tick, Position: position}, command); err != nil {
					t.Fatal(err)
				}
			}
		}
		var payouts [2][2]float32
		for peer, s := range peers {
			runtime.GOMAXPROCS([]int{1, 4}[peer])
			payouts[peer] = featureGrowthChurn(t, s, sites[peer][tick-1], wrecks[peer], int(tick))
			if err := s.StepGranted(tick); err != nil {
				t.Fatal(err)
			}
			for _, receipt := range s.DrainCommandReceipts() {
				if receipt.Outcome == CommandRejected {
					t.Fatal(receipt.Diagnostic)
				}
			}
		}
		if payouts[0] != payouts[1] {
			t.Fatal("online peers reclaimed different resources")
		}
		compare(tick)
	}
}

// All exported mutable feature values, with definition identity reduced to its
// admitted key and no pointer identity. Private active links and billed-count
// state are covered by the canonical single-seat comparison above.
type featureGrowthState struct {
	Definition                 string
	Cell                       world.Cell
	Position                   [3]numeric.Fixed
	Velocity                   numeric.Fixed
	Orientation                features.Orientation
	Footprint                  [2]int32
	ReclaimProgress, Countdown int32
	Damage, SavedWord          uint16
	Status, Selector           uint8
	Burning, Animating, Remote bool
	Sinking, Settled           bool
	Runtime                    features.RuntimeView
	Cursor                     [2]int32
	Sequence, Shadow           string
	EventVisit                 int32
	Event                      bool
}

func featureGrowthProjection(s *Session) []featureGrowthState {
	instances := s.Features.Instances()
	out := make([]featureGrowthState, 0, len(instances))
	for _, inst := range instances {
		key := ""
		if inst.Def != nil {
			key = inst.Def.CanonicalKey
		}
		sequence, shadow, visit, event := inst.EventSequence()
		out = append(out, featureGrowthState{
			Definition: key, Cell: world.Cell{X: int32(inst.CX), Z: int32(inst.CZ)},
			Position: [3]numeric.Fixed{inst.X, inst.Y, inst.Z}, Velocity: inst.Vy,
			Orientation: inst.Orientation, Footprint: [2]int32{inst.FootprintX, inst.FootprintZ},
			ReclaimProgress: inst.ReclaimProgress, Countdown: inst.BurnCountdown,
			Damage: inst.DamageAccumulator, SavedWord: inst.SavedAnchorWord,
			Status: inst.Status, Selector: inst.AnimationSelector,
			Burning: inst.IsBurning, Animating: inst.IsAnimating, Remote: inst.RemoteSuppressed,
			Sinking: inst.IsSinking, Settled: inst.Settled, Runtime: inst.RuntimeView(),
			Cursor:   [2]int32{inst.CursorFrame(), inst.CursorDelay()},
			Sequence: sequence, Shadow: shadow, EventVisit: visit, Event: event,
		})
	}
	return out
}
