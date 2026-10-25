package orders

// Nanolathe Modern policy: DESIGN_UNITS_ORDERS_COB "Modern guard assistance"
// and "Modern patrol work". Selection borrows existing work rows, never their
// resource arithmetic, worker state or simulation random stream.

import (
	"math/bits"

	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

const modernWorkRadius int32 = 128

func (*ModernRules) DefaultBuilderOptions() BuilderOptions {
	options := DefaultBuilderOptions()
	options.Patrol = [3]PatrolWorkOption{PatrolBoth, PatrolBoth, PatrolBoth}
	return options
}

func (r *ModernRules) PatrolWork(req PatrolWorkRequest) PatrolWorkOption {
	u := req.Builder
	mode := standingMoveMode(u)
	if mode >= 3 {
		return PatrolBoth
	}
	options := r.DefaultBuilderOptions()
	if b := bindingFor(u); b != nil && b.BuilderOptionsHook() != nil {
		options = b.BuilderOptionsHook()(u.Owner)
	}
	if option := options.Patrol[mode]; option <= PatrolAssistOnly {
		return option
	}
	return PatrolBoth
}

func modernBorrowWork(u *units.Unit, assignment *Node, work Node) {
	releaseGoalPayload(u, assignment)
	q := QueueOfUnit(u)
	work.automaticWork, work.workAssignment = true, assignment
	if modernWorkPatrol(assignment) {
		id := rowMoveGround
		if u.Def.CanFly {
			id = Lookup("VTOL_Move")
		}
		back := NewNodeForOrder(id, 0, u.X, u.Y, u.Z, work.CreationTick, u.Handle, false)
		back.workAssignment, back.patrolReturn = assignment, true
		q.PushHead(id, back)
		work.workReturn = q.Head()
	}
	q.PushHead(work.ID, work)
	// Releasing the leg can raise a movement outcome. Retain only its existing
	// maintenance deadline so an immediate failure cannot cascade into a retry.
	assignment.DynamicGate = gateDeadline
	assignment.Satisfied &^= gateMoveOutcomes
}

func (*ModernRules) AutomaticWorkValid(u *units.Unit, n *Node) bool {
	if n == nil || n.workAssignment == nil {
		// Direct orders and restored records have no proven borrowed producer.
		// Their presence before a patrol/guard is insufficient evidence: a human
		// can queue those records explicitly. Leave the retail save unchanged.
		// TODO(question): restored work has no producer receipt; an authored
		// extension save field would be needed to distinguish borrowed jobs from
		// explicit work. Preserve unknown heads until their ordinary work ends.
		return true
	}
	q := QueueOfUnit(u)
	assignment := n.workAssignment
	if q == nil || q.indexOfPrimary(assignment) < 0 {
		return false
	}
	if n.patrolReturn {
		return true
	}
	x, z := n.GoalX, n.GoalZ
	if n.Target != 0 {
		target := lookupTarget(u, n.Target)
		if target == nil || !target.Alive {
			return false
		}
		x, z = target.X, target.Z
	}
	if modernWorkPatrol(assignment) {
		// The target must stay within or on the sight circle around the saved
		// acquisition point, and the builder within or on twice that radius
		// of it: a pursuit or a route detour beyond either abandons the job as
		// a failure (DESIGN_UNITS_ORDERS_COB "Smarter patrol — 2026-10-22").
		back := n.workReturn
		return u != nil && u.Def != nil && back != nil && q.indexOfPrimary(back) >= 0 &&
			modernWithinPoint(back.GoalX, back.GoalZ, x, z, u.Def.SightDistance) &&
			modernWithinPoint(back.GoalX, back.GoalZ, u.X, u.Z, modernPatrolReach(u.Def.SightDistance))
	}
	ward := getLookupForWard(assignment, u)
	return ward != nil && ward.Alive && modernWithinPoint(ward.X, ward.Z, x, z, modernWorkRadius)
}

// WorkAssignmentOrdinal is a read-only diagnostic/fingerprint projection of
// proven borrowed-work provenance. Zero is absent; positive values are the
// retained assignment's one-based primary position; -1 means that identity is
// no longer retained. It creates no state and never serializes a host pointer.
func (q *Queue) WorkAssignmentOrdinal(n *Node) int {
	if n == nil || n.workAssignment == nil {
		return 0
	}
	if q == nil {
		return -1
	}
	if index := q.indexOfPrimary(n.workAssignment); index >= 0 {
		return index + 1
	}
	return -1
}

func modernWorkPatrol(n *Node) bool {
	return n != nil && (n.ID == rowRepairPatrol || n.ID == rowVTOLRepairPatrol)
}

func modernPatrolCandidate(u *units.Unit, x, z numeric.Fixed) bool {
	return modernWithinPoint(u.X, u.Z, x, z, u.Def.SightDistance)
}

// Signed double-word intermediates preserve the full 16.16 boundary without
// floating point or allocation. Each coordinate is a world word; the difference
// can need 33 bits and the sum of its squares can need 65 [I2].
type modernWide struct {
	hi int64
	lo uint64
}

func modernProduct(a, b int64) modernWide {
	negative := (a < 0) != (b < 0)
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	w := modernWide{int64(hi), lo}
	if negative {
		return w.negate()
	}
	return w
}

func (w modernWide) add(v modernWide) modernWide {
	lo, carry := bits.Add64(w.lo, v.lo, 0)
	return modernWide{w.hi + v.hi + int64(carry), lo}
}

func (w modernWide) negate() modernWide {
	lo, carry := bits.Add64(^w.lo, 1, 0)
	return modernWide{int64(^uint64(w.hi) + carry), lo}
}

func (w modernWide) less(v modernWide) bool {
	return w.hi < v.hi || w.hi == v.hi && w.lo < v.lo
}

func modernDelta(a, b numeric.Fixed) int64 { return int64(int32(a.Raw())) - int64(int32(b.Raw())) }

func modernDistanceSquared(ax, az, bx, bz numeric.Fixed) modernWide {
	dx, dz := modernDelta(ax, bx), modernDelta(az, bz)
	return modernProduct(dx, dx).add(modernProduct(dz, dz))
}

func modernWithinPoint(ax, az, x, z numeric.Fixed, radius int32) bool {
	if radius < 0 {
		return false
	}
	r := int64(radius) << 16
	return !modernProduct(r, r).less(modernDistanceSquared(ax, az, x, z))
}
