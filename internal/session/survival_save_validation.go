package session

import (
	"fmt"
	"math"
	"slices"

	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/survival"
)

func survivalRecordError(what, expected string) error {
	return fmt.Errorf("nanolathe: save sidecar survival record: %s: logical path save/sidecar/survival, providers searched [sidecar], expected %s", what, expected)
}

// validateSurvivalRecord checks the director's own invariants before either
// decoding or a direct restore can publish it or alter the shared vision team
// (DESIGN_SURVIVAL §4.1, §6.1, §6.5, §11). References to dead or missing unit
// slots remain valid: the director's ordinary sweeps discard them.
func validateSurvivalRecord(r *SurvivalRecord) error {
	bad := survivalRecordError
	if r.Version != SurvivalRecordVersion {
		return bad(fmt.Sprintf("version %d", r.Version), fmt.Sprintf("version %d", SurvivalRecordVersion))
	}
	if r.Attacker == 0 || int(r.Attacker) >= pool.PlayerCount || len(r.Team) != int(r.Attacker) {
		return bad(fmt.Sprintf("attacker %d with team %v", r.Attacker, r.Team), "one or more survivors followed by an attacker below slot 10")
	}
	if !r.Setup.Survival.Enabled || r.Setup.survivalAttacker() != int(r.Attacker) {
		return bad(fmt.Sprintf("setup with %d players and Survival enabled %t", r.Setup.NumPlayers, r.Setup.Survival.Enabled), "the saved attacker in the setup's last row")
	}
	for i, p := range r.Team {
		if int(p) != i {
			return bad(fmt.Sprintf("team[%d] is %d", i, p), "all survivor slots in ascending order before the attacker")
		}
	}
	if !r.Setup.Players[r.Attacker].IsComputer() {
		return bad("attacker setup controller", "a computer row")
	}
	if r.Setup.Survival.Pace > survival.PaceRelentless {
		return bad(fmt.Sprintf("pace %d", r.Setup.Survival.Pace), "normal, relaxed or relentless")
	}
	tuning := survival.DefaultTuning(r.Setup.Survival.Pace)
	if r.Wave < 0 || r.Plan.Number != r.Wave || r.Survived < 0 || r.Survived > r.Wave {
		return bad(fmt.Sprintf("wave %d, plan %d, survived %d", r.Wave, r.Plan.Number, r.Survived), "matching nonnegative wave numbers and a survived count no greater than the wave")
	}
	if r.Plan.Budget < 0 || r.Plan.Budget > tuning.Budget(math.MaxUint32, 1) || len(r.Plan.Groups) > tuning.MaxDirections {
		return bad(fmt.Sprintf("plan budget %d with %d groups", r.Plan.Budget, len(r.Plan.Groups)), "a budget and direction count within the director's limits")
	}
	for i, g := range r.Plan.Groups {
		if survival.Domain(g.Domain) >= survival.DomainCount {
			return bad(fmt.Sprintf("plan group %d domain %d", i, g.Domain), "a known movement domain")
		}
	}
	if r.NextGroup < 0 || r.NextPick < 0 {
		return bad(fmt.Sprintf("spawn cursor (%d,%d)", r.NextGroup, r.NextPick), "nonnegative group and pick indices")
	}
	switch survivalPhase(r.Phase) {
	case survivalGrace:
		if r.Wave != 0 || r.Plan.Budget != 0 || len(r.Plan.Groups) != 0 || r.NextGroup != 0 || r.NextPick != 0 {
			return bad("grace phase with a planned wave or spawn cursor", "the initial empty plan and cursor")
		}
	case survivalWarning:
		// Planning replaces the wave, but only the transition to Active
		// resets the previous wave's exhausted cursor. Its group count can
		// exceed the new plan's when entry eligibility changes.
		if r.Wave == 0 || r.Survived != r.Wave-1 || r.NextGroup > tuning.MaxDirections || r.NextPick != 0 {
			return bad("warning phase wave counts or spawn cursor", "one pending wave and an exhausted previous-wave cursor")
		}
	case survivalActive, survivalDowntime:
		if r.Wave == 0 || r.NextGroup > len(r.Plan.Groups) || (r.NextGroup == len(r.Plan.Groups) && r.NextPick != 0) {
			return bad(fmt.Sprintf("phase %d spawn cursor (%d,%d)", r.Phase, r.NextGroup, r.NextPick), "a cursor inside the current plan or at its end")
		}
		if r.NextGroup < len(r.Plan.Groups) && r.NextPick > len(r.Plan.Groups[r.NextGroup].Units) {
			return bad(fmt.Sprintf("spawn cursor (%d,%d)", r.NextGroup, r.NextPick), "a pick no later than the current group's end")
		}
		wantSurvived := r.Wave - 1
		if survivalPhase(r.Phase) == survivalDowntime {
			wantSurvived = r.Wave
			if r.NextGroup != len(r.Plan.Groups) {
				return bad("downtime with unspawned groups", "a completed wave cursor")
			}
		}
		if r.Survived != wantSurvived {
			return bad(fmt.Sprintf("phase %d with %d survived waves", r.Phase, r.Survived), fmt.Sprintf("%d survived waves", wantSurvived))
		}
	default:
		return bad(fmt.Sprintf("phase %d", r.Phase), "grace, warning, active or downtime")
	}
	if r.CentreX < 0 || r.CentreZ < 0 || r.Deposits < 0 || r.Deposits > depositMax || r.Deposits != len(r.DepositAt) || r.DepositWant < 0 || r.DepositHave < 0 {
		return bad("start site or deposit counts", "nonnegative coordinates and counts with one anchor per added deposit")
	}
	if info := r.Info; info != nil {
		if len(info.Team) != len(info.Computer) || len(info.Team) != len(info.Starts) {
			return bad("scenario team, computer and start lengths", "one computer flag and start per scenario team member")
		}
		for i, p := range info.Team {
			if !slices.Contains(r.Team, p) || (i > 0 && p <= info.Team[i-1]) {
				return bad(fmt.Sprintf("scenario team[%d] is %d", i, p), "distinct survivor slots in ascending order")
			}
		}
	}
	return nil
}
