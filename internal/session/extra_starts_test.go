package session

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/mission"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
)

// Extra start positions (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern extra
// start positions"): a Nanolathe Modern policy for more players than a map
// has starts. Whatever retail can place is placed as retail places it; a
// slot whose start the map lacks takes an unused authored start, then a
// generated one. Strict 3.1 keeps retail's refusal.

// gapStarts are authored with a gap: stored 0, 1, 4 and 5.
func gapStarts() []mission.Special {
	var starts []mission.Special
	for _, id := range []int{0, 1, 4, 5} {
		starts = append(starts, mission.Special{Kind: 1, ID: int32(id), X: int16(500 + 1000*id), Z: 700})
	}
	return starts
}

func TestExtraStartsReuseUnusedStartsThenGenerate(t *testing.T) {
	for _, c := range []struct {
		players int
		want    map[int]int
		extra   []int
	}{
		// Retail places all four: nothing changes.
		{players: 2, want: map[int]int{0: 0, 1: 1}},
		// Slots 2 and 3 have no StartPos3/4: they take the unused 4 and 5.
		{players: 4, want: map[int]int{0: 0, 1: 1, 2: 4, 3: 5}},
		// Slots 4 and 5 hold their own 4 and 5, so 2 and 3 are generated.
		{players: 6, want: map[int]int{0: 0, 1: 1, 4: 4, 5: 5}, extra: []int{2, 3}},
		// Eight players on four starts: the four without one are generated.
		{players: 8, want: map[int]int{0: 0, 1: 1, 4: 4, 5: 5}, extra: []int{2, 3, 6, 7}},
	} {
		cfg := chosenStartConfig(1, make([]*[2]int32, c.players)...)
		crt := rng.NewCRT(cfg.RNGCrtSeed)
		got, _, extra, err := skirmishStartAssignment(cfg, eligibleSlots(c.players), gapStarts(), &crt, true, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(c.want) || len(extra) != len(c.extra) {
			t.Fatalf("%d players: assignment %v extra %v, want %v and %v", c.players, got, extra, c.want, c.extra)
		}
		for slot, id := range c.want {
			if got[slot] != id {
				t.Fatalf("%d players: slot %d took %d, want %d", c.players, slot, got[slot], id)
			}
		}
		for i, slot := range c.extra {
			if extra[i] != slot {
				t.Fatalf("%d players: extra %v, want %v", c.players, extra, c.extra)
			}
		}
		if crt.Draws() != 0 {
			t.Fatalf("%d players: fixed starts took %d CRT draws", c.players, crt.Draws())
		}
	}
	// Without the policy the assignment is retail's, missing numbers and all,
	// for battle entry's missing-start fatal.
	cfg := chosenStartConfig(1, make([]*[2]int32, 4)...)
	crt := rng.NewCRT(1)
	got, _, extra, err := skirmishStartAssignment(cfg, eligibleSlots(4), gapStarts(), &crt, true, false)
	if err != nil || len(extra) != 0 || got[2] != 2 || got[3] != 3 {
		t.Fatalf("retail assignment %v extra %v: %v", got, extra, err)
	}
}

// extraStartsSession is the synthetic 32×32-cell map with two authored
// starts in one corner and four players: two take the starts, two are
// generated.
func extraStartsSession(t *testing.T, seats SeatRules) (*Session, SkirmishConfig, *mission.Mission) {
	t.Helper()
	s := strictNewSessionWithUnits(t, 0, 7, 11)
	for i := 2; i < 4; i++ {
		s.Econ.Players[i] = economy.Player{Exists: true, ControllerState: 2, EndGameCountdown: -1}
	}
	s.Rules.Seats = seats
	m := wu19178Mission(
		mission.Special{Kind: 1, ID: 0, X: 40, Z: 40, Name: "StartPos1"},
		mission.Special{Kind: 1, ID: 1, X: 120, Z: 40, Name: "StartPos2"},
	)
	cfg := SkirmishConfig{NumPlayers: 4, Location: 1, RNGCrtSeed: 67890}
	for i := 0; i < 4; i++ {
		cfg.Players[i].Side = i & 1
	}
	return s, cfg, m
}

// Battle entry gives each extra player a generated start on its own cell,
// spread away from the authored starts, and the same seed gives the same
// starts. Strict 3.1 keeps retail's missing-start fatal.
func TestExtraPlayersBeginAtGeneratedStarts(t *testing.T) {
	place := func() map[uint8][2]int32 {
		s, cfg, m := extraStartsSession(t, ModernSeats{})
		if err := skirmishReconstructUnits(s, cfg, m); err != nil {
			t.Fatalf("Modern: %v", err)
		}
		at := map[uint8][2]int32{}
		for _, u := range s.Units.Iter() {
			at[u.Owner] = [2]int32{int32(u.X >> 16), int32(u.Z >> 16)}
		}
		return at
	}
	at := place()
	if len(at) != 4 || at[0] != [2]int32{40, 40} || at[1] != [2]int32{120, 40} {
		t.Fatalf("commanders %v: want four, players 0 and 1 on their authored starts", at)
	}
	for _, slot := range []uint8{2, 3} {
		p := at[slot]
		for other, q := range at {
			if other == slot {
				continue
			}
			dx, dz := int64(p[0]-q[0]), int64(p[1]-q[1])
			if dx*dx+dz*dz < 64*64 {
				t.Fatalf("generated start of player %d at %v is within 64 pixels of player %d at %v", slot, p, other, q)
			}
		}
	}
	if again := place(); again[2] != at[2] || again[3] != at[3] {
		t.Fatalf("the same seed generated %v then %v", at, again)
	}
	s, cfg, m := extraStartsSession(t, StrictSeats{})
	if err := skirmishReconstructUnits(s, cfg, m); err == nil || err.Error() != errStartPositionMissing(2).Error() {
		t.Fatalf("Strict 3.1: %v, want retail's %q", err, errStartPositionMissing(2))
	}
	if (StrictSeats{}).ExtraStarts() || !(ModernSeats{}).ExtraStarts() {
		t.Fatal("the reserved sets answer the extra-starts question wrongly")
	}
}

// The region's centre-nearest cell is a later Auto player's authored start,
// and every random candidate lies outside the region. Generated players must
// search past that reserved footprint, then remain disjoint when chosen and
// generated commanders are allocated together in slot order (Modern policy).
func TestGeneratedStartsAvoidLaterAuto(t *testing.T) {
	for _, chosen := range []bool{false, true} {
		s, cfg, _ := extraStartsSession(t, ModernSeats{})
		for z := int32(0); z < s.World.CellH; z++ {
			for x := int32(0); x < s.World.CellW; x++ {
				cell := s.World.PlotAt(x, z)
				cell.SetMinHeight(0)
				cell.SetMaxHeight(255)
				if x >= 26 && x <= 29 && z >= 20 && z <= 23 {
					cell.SetMinHeight(10)
					cell.SetMaxHeight(10)
				}
			}
		}
		if chosen {
			cfg.Players[0].Start = StartPoint{Set: true, X: 8, Z: 8}
		}
		m := wu19178Mission(mission.Special{Kind: 1, ID: 3, X: 26*16 + 8, Z: 20*16 + 8})
		beforeCRT := s.CrtRNG().Draws()
		if err := skirmishReconstructUnits(s, cfg, m); err != nil {
			t.Fatalf("chosen=%v: %v", chosen, err)
		}
		assertStartFootprintsDisjoint(t, s, cfg.NumPlayers)
		for _, u := range s.Units.Iter() {
			if u.Owner == 3 && (u.X != numeric.Fixed(m.Specials[0].X)<<16 || u.Z != numeric.Fixed(m.Specials[0].Z)<<16) {
				t.Fatalf("chosen=%v: Auto moved to (%d,%d)", chosen, u.X>>16, u.Z>>16)
			}
		}
		if s.CrtRNG().Draws() != beforeCRT {
			t.Fatalf("chosen=%v: placement read the retained CRT stream", chosen)
		}
	}
}
