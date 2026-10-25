package tactics

import (
	"slices"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/aikit/core"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
)

// The standing guard must activate below Attention 3: Easy is 1 and Medium
// is 2, and those are the personas the long-game correction targets.
func TestStandingGuardAtLowAttention(t *testing.T) {
	a, _ := testArmy(10, 10)
	a.P.Defend = true
	if !a.defendSquadAllowed(1, 0, true) || !a.defendSquadAllowed(2, 0, true) {
		t.Error("standing guard did not activate at Attention 1/2")
	}
	if a.defendSquadAllowed(1, 1000, false) || a.defendSquadAllowed(2, 1000, false) {
		t.Error("an incident activated the guard below Attention 3")
	}
	if !a.defendSquadAllowed(3, 1000, false) {
		t.Error("an incident did not activate the guard at Attention 3")
	}
	a.P.Defend = false
	if a.defendSquadAllowed(3, 1000, true) {
		t.Error("defend=0 still activated the guard")
	}
}

// An active, under-strength guard recruits existing main and raid squad
// members, nearest home first; otherwise production can stop and the guard
// stays empty while the main squad keeps attacking.
func TestStandingGuardRecruitsMainSquad(t *testing.T) {
	a, _ := testArmy(10, 10)
	info := &aikit.UnitInfo{Value: 100, HP: 1000}
	own := make([]aikit.OwnUnit, 10)
	combat := make([]int32, 10)
	for i := range own {
		// Unit i stands (10-i)*100 wu from home: the last ones are nearest.
		own[i] = aikit.OwnUnit{H: pool.Handle(i + 1), Info: info, Built: true, Tag: sqMain, X: 1000 + int32(10-i)*100, Z: 1000}
		combat[i] = int32(i)
	}
	b := &core.Board{K: &aikit.Kit{}, O: &aikit.Obs{Own: own}, Combat: combat, HomeX: 1000, HomeZ: 1000}
	a.sq[sqDefend].active = true
	a.recruitHomeGuard(b, 350)
	var got []int32
	for i := range own {
		if own[i].Tag == sqDefend {
			got = append(got, int32(i))
		}
	}
	if want := []int32{6, 7, 8, 9}; !slices.Equal(got, want) {
		t.Fatalf("recruited %v, want the four nearest home %v", got, want)
	}
	a.recruitHomeGuard(b, 350)
	n := 0
	for i := range own {
		if own[i].Tag == sqDefend {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("a full guard recruited again: %d members, want 4", n)
	}
}
