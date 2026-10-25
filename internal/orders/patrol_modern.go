package orders

import (
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// patrolOutcome is the semantic result of one borrowed patrol job.
type patrolOutcome uint8

const (
	patrolFailure patrolOutcome = iota
	patrolSuccess
	// patrolNeutral is a reclaim whose feature disappeared, was replaced by
	// its successor or began animating, or a unit someone else finished or
	// healed first, before the job could pay: no fault of the target.
	patrolNeutral
)

// Nanolathe Modern policy: DESIGN_UNITS_ORDERS_COB "Modern patrol work" and
// "Smarter patrol — 2026-10-22". A movement wake is not necessarily arrival,
// and a terminal work code is not necessarily success. Record the semantic
// outcome before normal cleanup.
//
// Every end of a borrowed job removes its saved-position return and resumes
// the exact pending leg from where the builder stands; no outcome sends it
// back to the acquisition point. A failure remembers its target until the
// next genuine waypoint arrival, and a successful assist on a factory's
// product remembers that factory, so a route passing a producing factory
// does not become a guard post. Success resumes the leg in the same visit;
// failure and the neutral outcome resume it on the next tick, so no failing
// selection can repeat inside one pump visit. The hook adds no RNG draw or
// resource transaction.
func (*ModernRules) AutomaticWorkResult(u *units.Unit, n *Node, satisfied uint32, code Code, tick uint32) Code {
	q := QueueOfUnit(u)
	if q == nil || n == nil {
		return code
	}
	if modernWorkPatrol(n) && code == 6 && satisfied&gateArrived != 0 && satisfied&gateNoRoute == 0 {
		q.patrolWork.forget() // a genuine patrol-waypoint arrival
	}
	assignment := n.workAssignment
	if assignment == nil || !modernWorkPatrol(assignment) {
		return code
	}
	if n.patrolReturn {
		// The saved position is only the job's anchor: every job end below
		// removes it, so a return reaches the head only across a rule switch.
		// It then ends quietly instead of cancelling or retrying the route.
		if code == 7 || code == 9 {
			return 8
		}
		return code
	}
	if !n.automaticWork || code != 5 && code != 7 && code != 8 && code != 9 {
		return code
	}
	outcome := modernPatrolOutcome(u, n, satisfied, code)
	switch outcome {
	case patrolFailure:
		q.patrolWork.remember(modernPatrolTarget(u, n))
	case patrolSuccess:
		// The completion transition detaches a finished product from its
		// factory [04 R-FAC-02 §3], so the job carries the factory it was
		// selected under rather than reading the carrier now.
		if f := n.workFactory; f != nil {
			q.patrolWork.remember(patrolMemoryEntry{kind: patrolMemoryFactory, unit: f, handle: f.Handle})
		}
	}
	// Remove only this job's proven return, never an ordinary queued move.
	if back := n.workReturn; back != nil && back.patrolReturn && back.workAssignment == assignment {
		q.unlinkPrimary(back)
	}
	if q.indexOfPrimary(assignment) >= 0 {
		assignment.Satisfied &^= gateMoveOutcomes
		if outcome == patrolSuccess {
			assignment.DynamicGate = 0
		} else {
			assignment.Satisfied &^= gateDeadline
			assignment.DynamicGate, assignment.Deadline = gateDeadline, int32(tick+1)
		}
	}
	if outcome != patrolSuccess {
		return 8
	}
	return code
}

// modernPatrolOutcome classifies an ending borrowed job. Ground repair can
// finish unsuccessfully with code 5, and a reclaim reports code 8 both for an
// unreachable feature and for one that is no longer there.
func modernPatrolOutcome(u *units.Unit, n *Node, satisfied uint32, code Code) patrolOutcome {
	reclaim := n.ID == rowReclaim || n.ID == rowVTOLReclaim
	if code == 5 {
		target := lookupTarget(u, n.Target)
		success := false
		switch n.ID {
		case rowHelpBuild, rowVTOLHelpBuild:
			// Both rows also report code 5 from their approach when the
			// target is already finished — ground phase 1, air phase 2,
			// "nothing left to assist" [04 R-ORD-01 §5][04 R-ORD-02 §2].
			// Only the work phase (ground 3–4, air 3) assisted; an earlier
			// finish was someone else's, which is neutral and must not
			// remember the factory.
			finished := target != nil && target.Alive && target.Remaining == 0 && satisfied&gateCancelCurrent == 0
			if finished && n.Phase < 3 {
				return patrolNeutral
			}
			success = finished
		case rowRepairUnit, rowVTOLRepairUnit:
			success = target != nil && target.Alive && target.Def != nil && moverMode(target) == 1 && !leashBroken(u, n)
			if success {
				if n.ID == rowVTOLRepairUnit {
					success = health16(target) >= uint32(target.Def.MaxDamage)
				} else {
					success = uint32(target.Health) >= uint32(target.Def.MaxDamage)
				}
			}
		default:
			success = n.ID == rowReclaim && n.Phase == 5 || n.ID == rowVTOLReclaim && n.Phase == 4
		}
		if success {
			return patrolSuccess
		}
	}
	if reclaim {
		// The executor resolves its feature on every visit [05 R-WORK-01 §5]: a
		// cell that no longer holds a reclaimable, resting feature was
		// reclaimed or destroyed by someone else, which is not this target's
		// fault and must not be remembered against it.
		feature, ok := bindingFor(u).LookupFeature(world.WorldToCell(n.GoalX), world.WorldToCell(n.GoalZ))
		if !ok || !feature.Reclaimable || feature.Animating {
			return patrolNeutral
		}
		return patrolFailure
	}
	// Likewise a unit someone else finished or healed before this job could
	// contribute: it no longer needs the work, so it is not remembered.
	if target := lookupTarget(u, n.Target); target != nil && target.Alive && target.Def != nil && target.Remaining == 0 && health16(target) >= uint32(target.Def.MaxDamage) {
		return patrolNeutral
	}
	return patrolFailure
}

// modernPatrolTarget is the memory entry for a failed job's target: the
// anchor cell of a reclaim's feature, or the unit a construction or repair
// job names. A unit already freed, whose reference the queue has unlinked
// [04 R-MOV-03 §7], leaves nothing to remember.
func modernPatrolTarget(u *units.Unit, n *Node) patrolMemoryEntry {
	if n.ID != rowReclaim && n.ID != rowVTOLReclaim {
		target := lookupTarget(u, n.Target)
		if target == nil {
			return patrolMemoryEntry{}
		}
		return patrolMemoryEntry{kind: patrolMemoryUnit, unit: target, handle: n.Target}
	}
	cx, cz := world.WorldToCell(n.GoalX), world.WorldToCell(n.GoalZ)
	if feature, ok := bindingFor(u).LookupFeature(cx, cz); ok {
		cx, cz = feature.CX, feature.CZ
	}
	return patrolMemoryEntry{kind: patrolMemoryFeature, cx: cx, cz: cz}
}

// modernPatrolFactoryOf is the factory carrying target, or nil when target is
// not a carried product. A factory is a builder that carries its product on
// its pad while building it [04 R-FAC-02 §1].
func modernPatrolFactoryOf(u, target *units.Unit) *units.Unit {
	if target == nil || target.Remaining == 0 || target.Attachment.Carrier == 0 {
		return nil
	}
	factory := lookupTarget(u, target.Attachment.Carrier)
	if factory == nil || factory.Def == nil || !factory.Def.Builder {
		return nil
	}
	return factory
}
