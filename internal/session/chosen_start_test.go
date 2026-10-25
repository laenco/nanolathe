package session

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/community"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/mission"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// Chosen start points (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern chosen
// start positions"): a lobby choice per player, a Nanolathe Modern policy
// that Strict 3.1 ignores. A setup with no choice must place exactly as
// retail does; the others lock the policy's claiming and ordering rules.

// chosenStarts are stored numbers 0..5 a thousand pixels apart on one line,
// authored out of order so the assignment's own ordering is what puts them
// in ascending order.
func chosenStarts() []mission.Special {
	var starts []mission.Special
	for _, id := range []int{3, 0, 5, 1, 4, 2} {
		starts = append(starts, mission.Special{Kind: 1, ID: int32(id), X: int16(500 + 1000*id), Z: 700})
	}
	return starts
}

// chosenStartConfig is a setup of len(points) players; a nil point leaves
// the player to Location.
func chosenStartConfig(location int, points ...*[2]int32) SkirmishConfig {
	cfg := SkirmishConfig{NumPlayers: len(points), Location: location, RNGCrtSeed: 67890, MapName: "fixture"}
	for i, p := range points {
		if p != nil {
			cfg.Players[i].Start = StartPoint{Set: true, X: p[0], Z: p[1]}
		}
	}
	return cfg
}

func eligibleSlots(n int) []int {
	slots := make([]int, n)
	for i := range slots {
		slots[i] = i
	}
	return slots
}

// With no choice the assignment is retail's: the identity under fixed starts
// with no CRT draw, and the loading walk's cyclic permutation under random
// starts with exactly its draws.
func TestUnchosenStartsKeepRetailPlacement(t *testing.T) {
	for _, location := range []int{1, 0} {
		cfg := chosenStartConfig(location, nil, nil, nil, nil)
		crt := rng.NewCRT(cfg.RNGCrtSeed)
		got, points, _, err := skirmishStartAssignment(cfg, eligibleSlots(4), chosenStarts(), &crt, true, false)
		if err != nil || len(points) != 0 {
			t.Fatalf("location %d: %v, points %v", location, err, points)
		}
		want := eligibleSlots(4)
		control := rng.NewCRT(cfg.RNGCrtSeed)
		if location == 0 {
			shuffleEligibleStarts(&control, want)
		}
		for slot, start := range want {
			if got[slot] != start {
				t.Fatalf("location %d: slot %d took %d, want retail's %d [P0-04]", location, slot, got[slot], start)
			}
		}
		if crt.Draws() != control.Draws() {
			t.Fatalf("location %d: %d CRT draws, want retail's %d", location, crt.Draws(), control.Draws())
		}
	}
}

// Under fixed starts a chosen point claims every authored start within
// chosenStartClaim of it; every other player keeps its own number when that
// start is free, and the rest take the lowest free starts in slot order.
// Here slot 2 chooses a point beside stored start 0, so slots 1 and 3 keep
// their own, and slot 0, whose own was claimed, takes the lowest free start,
// stored 2. No CRT draw is taken.
func TestChosenPointsUnderFixedLocation(t *testing.T) {
	cfg := chosenStartConfig(1, nil, nil, &[2]int32{520, 760}, nil)
	crt := rng.NewCRT(cfg.RNGCrtSeed)
	got, points, _, err := skirmishStartAssignment(cfg, eligibleSlots(4), chosenStarts(), &crt, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := points[2]; !ok || p != [2]int32{520, 760} || len(points) != 1 {
		t.Fatalf("points %v, want slot 2's own", points)
	}
	if _, ok := got[2]; ok {
		t.Fatalf("slot 2 also took an authored start: %v", got)
	}
	for slot, want := range map[int]int{0: 2, 1: 1, 3: 3} {
		if got[slot] != want {
			t.Fatalf("slot %d took %d, want %d (assignment %v)", slot, got[slot], want, got)
		}
	}
	if crt.Draws() != 0 {
		t.Fatalf("fixed starts took %d CRT draws, want none", crt.Draws())
	}
}

// Under random starts the players who chose nothing are dealt the lowest
// free starts, one each, by a Fisher–Yates shuffle on the loading CRT. The
// expectation replays the shuffle from the stream definition [01 §7.2].
// Slot 0's point is far from every start, so it claims none.
func TestChosenPointsUnderRandomLocation(t *testing.T) {
	cfg := chosenStartConfig(0, &[2]int32{9000, 9000}, nil, nil, nil)
	crt := rng.NewCRT(cfg.RNGCrtSeed)
	got, _, _, err := skirmishStartAssignment(cfg, eligibleSlots(4), chosenStarts(), &crt, true, false)
	if err != nil {
		t.Fatal(err)
	}
	pool := []int{0, 1, 2}
	state := cfg.RNGCrtSeed
	for k := len(pool) - 1; k > 0; k-- {
		state = state*214013 + 2531011
		r := int((state>>16)&0x7FFF) % (k + 1)
		pool[k], pool[r] = pool[r], pool[k]
	}
	for i, slot := range []int{1, 2, 3} {
		if got[slot] != pool[i] {
			t.Fatalf("slot %d took %d, want %d (assignment %v, dealt %v)", slot, got[slot], pool[i], got, pool)
		}
	}
	if crt.Draws() != 2 {
		t.Fatalf("dealing three starts took %d CRT draws, want 2", crt.Draws())
	}
}

// Too few starts left after the chosen points is this build's error, and a
// point outside the coordinate bound is refused by Validate.
func TestChosenPointsRefuseWhatTheMapCannotSeat(t *testing.T) {
	starts := chosenStarts()[:4] // stored 3, 0, 5, 1
	crt := rng.NewCRT(1)
	cfg := chosenStartConfig(1, &[2]int32{500, 700}, nil, nil, nil, nil)
	if _, _, _, err := skirmishStartAssignment(cfg, eligibleSlots(5), starts, &crt, true, false); err == nil || !strings.Contains(err.Error(), "starts left after the chosen points") {
		t.Fatalf("four players on three free starts: %v", err)
	}
	if err := validateChosenStarts(chosenStartConfig(1, &[2]int32{StartPointLimit, 0}, nil)); err == nil {
		t.Fatal("Validate accepted a point beyond the coordinate bound")
	}
}

// Battle entry places a chosen commander on the nearest cell it can stand
// on, here the chosen cell itself on flat open ground, and gives the other
// player its own authored start, which the far point does not claim.
func TestChosenPointPlacesTheCommander(t *testing.T) {
	s := strictNewSessionWithUnits(t, 0, 7, 11)
	m := wu19178Mission(
		mission.Special{Kind: 1, ID: 0, X: 12, Z: 20, Name: "StartPos1"},
		mission.Special{Kind: 1, ID: 1, X: 24, Z: 28, Name: "StartPos2"},
	)
	cfg := wu19178Config()
	cfg.Players[0].Start = StartPoint{Set: true, X: 400, Z: 410}
	s.Rules.Seats = ModernSeats{}
	if err := skirmishReconstructUnits(s, cfg, m); err != nil {
		t.Fatalf("skirmishReconstructUnits: %v", err)
	}
	want := map[uint8][2]int32{0: {25*16 + 8, 25*16 + 8}, 1: {24, 28}}
	for _, u := range s.Units.Iter() {
		w := want[u.Owner]
		if u.X != numeric.Fixed(w[0])<<16 || u.Z != numeric.Fixed(w[1])<<16 {
			t.Fatalf("player %d commander at (%d,%d), want (%d,%d)", u.Owner, u.X>>16, u.Z>>16, w[0], w[1])
		}
	}
}

// Only a distant island is standable. The original click claims no authored
// start, but its nearest standable cell is Auto's start. Auto must keep its
// exact point, and chosen commanders must avoid its entire footprint even
// though it will be allocated later (Modern policy).
func TestChosenRelocationAvoidsLaterAuto(t *testing.T) {
	for _, footprint := range [][2]int32{{1, 1}, {3, 2}} {
		t.Run(fmt.Sprintf("%dx%d", footprint[0], footprint[1]), func(t *testing.T) {
			for _, chosenPlayers := range []int{1, 2} {
				s := strictNewSessionWithUnits(t, 0, 7, 11)
				s.Rules.Seats = ModernSeats{}
				s.Catalog.Movement["testmove"].FootprintX = footprint[0]
				s.Catalog.Movement["testmove"].FootprintZ = footprint[1]
				for z := int32(0); z < s.World.CellH; z++ {
					for x := int32(0); x < s.World.CellW; x++ {
						cell := s.World.PlotAt(x, z)
						cell.SetMinHeight(0)
						cell.SetMaxHeight(255)
						if x >= 24 && x <= 30 && z >= 18 && z <= 23 {
							cell.SetMinHeight(10)
							cell.SetMaxHeight(10)
						}
					}
				}
				cfg := wu19178Config()
				cfg.NumPlayers = chosenPlayers + 1
				for slot := 0; slot < cfg.NumPlayers; slot++ {
					s.Econ.Players[slot] = economy.Player{Exists: true, ControllerState: 2, EndGameCountdown: -1}
					s.Econ.Players[slot].Stock = [2]float32{100, 200}
					if slot < chosenPlayers {
						cfg.Players[slot].Start = StartPoint{Set: true, X: 8, Z: 8}
					}
				}
				def, _ := s.Catalog.Unit("armcom")
				px, _, pz, ok := s.chosenStartPosition(def, [2]int32{8, 8})
				if !ok {
					t.Fatal("fixture has no standable cell")
				}
				auto := mission.Special{Kind: 1, ID: int32(chosenPlayers), X: int16(px >> 16), Z: int16(pz >> 16)}
				if startClaimed(auto, []int{0}, map[int][2]int32{0: {8, 8}}) {
					t.Fatal("fixture must put Auto outside the original click's claim")
				}
				beforeCRT := s.CrtRNG().Draws()
				beforeSim := s.SimRNG().Draws()
				if err := skirmishReconstructUnits(s, cfg, wu19178Mission(auto)); err != nil {
					t.Fatal(err)
				}
				assertStartFootprintsDisjoint(t, s, cfg.NumPlayers)
				for slot := 0; slot < cfg.NumPlayers; slot++ {
					if got := s.Econ.Players[slot].Stock; got != [2]float32{100, 200} {
						t.Fatalf("placement changed player %d's resources to %v", slot, got)
					}
				}
				for _, u := range s.Units.Iter() {
					if int(u.Owner) == chosenPlayers && (u.X != px || u.Z != pz) {
						t.Fatalf("Auto moved from (%d,%d) to (%d,%d)", px>>16, pz>>16, u.X>>16, u.Z>>16)
					}
				}
				control := strictNewSessionWithUnits(t, 0, 7, 11)
				controlBefore := control.SimRNG().Draws()
				for slot := 0; slot < cfg.NumPlayers; slot++ {
					def, err := skirmishCommander(control.Catalog, cfg.Players[slot].Side, slot)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := control.Units.Create(def, uint8(slot), 0, 0, 0); err != nil {
						t.Fatal(err)
					}
				}
				if got, want := s.SimRNG().Draws()-beforeSim, control.SimRNG().Draws()-controlBefore; got != want {
					t.Fatalf("placement consumed %d simulation draws, want allocator's %d", got, want)
				}
				if s.CrtRNG().Draws() != beforeCRT {
					t.Fatal("placement read the retained CRT stream")
				}
			}
		})
	}
}

// Reservations belong only to chosen/generated placement. An ordinary Modern
// setup and Strict's bypass retain even overlapping authored footprints and
// their exact authored coordinates [08 R-ENTRY-01 §5].
func TestStartReservationsLeaveRetailPlacementUnchanged(t *testing.T) {
	for _, strict := range []bool{false, true} {
		s := strictNewSessionWithUnits(t, 0, 7, 11)
		s.Catalog.Movement["testmove"].FootprintX = 3
		s.Catalog.Movement["testmove"].FootprintZ = 2
		cfg := wu19178Config()
		s.Rules.Seats = ModernSeats{}
		if strict {
			s.Rules.Seats = StrictSeats{}
			cfg.Players[0].Start = StartPoint{Set: true, X: 8, Z: 8}
		}
		m := wu19178Mission(
			mission.Special{Kind: 1, ID: 0, X: 392, Z: 296},
			mission.Special{Kind: 1, ID: 1, X: 408, Z: 296},
		)
		if err := skirmishReconstructUnits(s, cfg, m); err != nil {
			t.Fatal(err)
		}
		for _, u := range s.Units.Iter() {
			sp := m.Specials[u.Owner]
			if u.X != numeric.Fixed(sp.X)<<16 || u.Z != numeric.Fixed(sp.Z)<<16 {
				t.Fatalf("strict=%v: player %d moved from authored (%d,%d) to (%d,%d)", strict, u.Owner, sp.X, sp.Z, u.X>>16, u.Z>>16)
			}
		}
	}
}

func assertStartFootprintsDisjoint(t *testing.T, s *Session, players int) {
	t.Helper()
	occupied := map[[2]int32]uint8{}
	if got := len(s.Units.Iter()); got != players {
		t.Fatalf("created %d commanders, want %d", got, players)
	}
	for _, u := range s.Units.Iter() {
		fx, fz := world.FootprintForUnit(s.Catalog, u.Def)
		x, z := world.PlacementAnchor(u.X, u.Z, fx, fz)
		for dz := int32(0); dz < fz; dz++ {
			for dx := int32(0); dx < fx; dx++ {
				cell := [2]int32{x + dx, z + dz}
				if owner, found := occupied[cell]; found {
					t.Fatalf("players %d and %d overlap at cell %v", owner, u.Owner, cell)
				}
				occupied[cell] = u.Owner
			}
		}
	}
}

// Strict 3.1 keeps retail's placement: a chosen point is ignored, with no
// CRT draw beyond retail's, and the configuration refuses one
// (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern chosen start positions").
func TestStrictIgnoresChosenStarts(t *testing.T) {
	if (StrictSeats{}).ChosenStarts() || !(ModernSeats{}).ChosenStarts() || !CommunityRuleSet().Seats.ChosenStarts() {
		t.Fatal("the reserved sets answer the chosen-start question wrongly")
	}
	for _, location := range []int{1, 0} {
		cfg := chosenStartConfig(location, &[2]int32{520, 700}, nil, nil, nil)
		crt := rng.NewCRT(cfg.RNGCrtSeed)
		got, points, _, err := skirmishStartAssignment(cfg, eligibleSlots(4), chosenStarts(), &crt, false, false)
		if err != nil || len(points) != 0 {
			t.Fatalf("location %d: %v, points %v", location, err, points)
		}
		control := rng.NewCRT(cfg.RNGCrtSeed)
		want := eligibleSlots(4)
		if location == 0 {
			shuffleEligibleStarts(&control, want)
		}
		for slot, start := range want {
			if got[slot] != start || crt.Draws() != control.Draws() {
				t.Fatalf("location %d: slot %d took %d after %d draws, want retail's %d after %d", location, slot, got[slot], crt.Draws(), start, control.Draws())
			}
		}
	}
	r := validMatchRequest(t)
	r.RuleName, r.RuleBase = StrictRuleSetName, MatchBaseStrict31
	r.Community = community.Features{}
	r.Mod, r.ContentProfile = MatchMod{}, MatchContentProfile{Name: "retail", Units: 512, Weapons: 256, TNTBytes: 16 << 20, LOSBytes: 1 << 20}
	r.Seats[2].ComputerKind, r.Seats[2].AIParams = 0, nil
	if _, err := ResolveMatchConfig(r); err != nil {
		t.Fatalf("the fixture is not a Strict configuration: %v", err)
	}
	strictBase := encodeMatchForTest(t, resolveMatch(t, r))
	for _, record := range [][]byte{
		extensionRecordForTest(17, 1, []byte{1, 3, 4, 0, 0}),
		extensionRecordForTest(18, 1, []byte{2}),
	} {
		if _, err := DecodeMatchConfig(withMatchTailForTest(strictBase, append(record, 0))); err == nil {
			t.Fatal("a Strict 3.1 configuration decoded a gameplay-choice extension")
		}
	}
	r.Seats[0].Start = StartPoint{Set: true, X: 100, Z: 100}
	if _, err := ResolveMatchConfig(r); err == nil {
		t.Fatal("a Strict 3.1 configuration carried a chosen start")
	}
}

// Chosen starts are a required record after the frozen version-2 fields,
// including nonempty room content. The default keeps main's current bytes;
// historical v1 configurations are tested by the replay compatibility suite.
func TestChosenStartsEncodeAsConfigurationVersion2(t *testing.T) {
	r := validMatchRequest(t)
	r.RoomContent = []MatchContentEntry{{Kind: MatchContentBaseArchive, Name: "totala1.hpi", Size: 42}}
	v1 := encodeMatchForTest(t, resolveMatch(t, r))
	r.Seats[0].Start = StartPoint{Set: true, X: 3, Z: 4}
	r.Seats[2].Start = StartPoint{Set: true, X: 1, Z: 2}
	c := resolveMatch(t, r)
	v2 := encodeMatchForTest(t, c)
	if tail := append(extensionRecordForTest(17, 1, []byte{1, 3, 4, 0, 1, 1, 2}), 0); !bytes.Equal(v2[:len(v1)], v1) || !bytes.Equal(v2[len(v1):], tail) {
		t.Fatalf("chosen starts did not follow frozen fields as required extension 17:\nv1 %x\nv2 %x", v1, v2)
	}
	h := sha256.New()
	h.Write([]byte(matchConfigDomain))
	h.Write(v2)
	if digest := c.Digest(); !bytes.Equal(digest[:], h.Sum(nil)) {
		t.Fatalf("version 2 identity %x does not hash the version-2 domain", digest)
	}
	if !checkMatchDecode(t, v2) {
		t.Fatal("a version-2 payload did not decode")
	}
	decoded, _ := DecodeMatchConfig(v2)
	if got := decoded.Request().Seats; got[0].Start != r.Seats[0].Start || got[1].Start.Set || got[2].Start != r.Seats[2].Start {
		t.Fatalf("decoded starts %+v %+v %+v", got[0].Start, got[1].Start, got[2].Start)
	}
	if checkMatchDecode(t, append(append([]byte(nil), v1...), 0, 0, 0, 0)) {
		t.Fatal("a version-2 tail of defaults decoded")
	}
	s := validSurvivalRequest(t)
	s.Seats[0].Start = StartPoint{Set: true, X: 1, Z: 1}
	if _, err := ResolveMatchConfig(s); err == nil {
		t.Fatal("a Survival seat with a chosen start resolved")
	}
}

// The local adapter and the admitted setup carry each row's choice, so a
// replay composes the battle its recording played.
func TestChosenStartsRoundTripTheLocalAdapter(t *testing.T) {
	cfg := DirectSkirmishConfig("fixture")
	cfg.Players[1].Start = StartPoint{Set: true, X: 640, Z: 320}
	r, err := NewMatchConfigRequest(cfg, SkirmishEntryOptions{}, MatchRoomInputs{Policies: MatchPolicies{Revision: 1, Scheduling: 1, Pacing: 1, Drop: 1, Audience: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Seats[0].Start.Set || r.Seats[1].Start != cfg.Players[1].Start {
		t.Fatalf("request starts %+v %+v", r.Seats[0].Start, r.Seats[1].Start)
	}
	back, _ := matchSkirmishSetup(&r)
	if back.Players[0].Start.Set || back.Players[1].Start != cfg.Players[1].Start {
		t.Fatalf("admitted setup starts %+v %+v", back.Players[0].Start, back.Players[1].Start)
	}
}
