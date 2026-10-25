package orders

import (
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Nanolathe Modern policy: DESIGN_UNITS_ORDERS_COB "Smarter patrol —
// 2026-10-22". These are approved policy values, not retail findings.
const (
	// modernPatrolMemorySize is how many failed targets one patrol remembers.
	// A full memory overwrites its oldest entry.
	modernPatrolMemorySize = 8
	// modernPatrolScanTicks is the selection cadence between maintenance
	// visits, on the ground and in the air.
	modernPatrolScanTicks uint32 = 15
)

// patrolMemoryKind names what a remembered entry refers to.
type patrolMemoryKind uint8

const (
	patrolMemoryEmpty   patrolMemoryKind = iota
	patrolMemoryUnit                     // a construction or repair target whose job failed
	patrolMemoryFeature                  // a feature whose reclaim failed, by anchor cell
	patrolMemoryFactory                  // a factory whose product this patrol just helped finish
)

// patrolMemoryEntry is one remembered target. A unit entry keeps the unit's
// identity as well as its handle: a reused pool slot is a new unit, exactly
// as the danger contacts keep it. A feature entry keeps its anchor cell.
type patrolMemoryEntry struct {
	kind   patrolMemoryKind
	unit   *units.Unit
	handle pool.Handle
	cx, cz int32
}

// patrolWorkState is the queue-owned Modern patrol state. Selection skips
// every remembered target; a genuine waypoint arrival forgets them all, and
// a replacement command resets the whole state. scanDue is the earliest tick
// a selection-only wake may run (PatrolWorkWake); every selection moves it
// one cadence on. Slots are written in a fixed ring order, so capture and
// selection never depend on map or pointer order [I1].
type patrolWorkState struct {
	entries [modernPatrolMemorySize]patrolMemoryEntry
	next    uint8
	scanDue uint32
}

// forget clears the memory at a genuine waypoint arrival. The cadence stays:
// the next leg's maintenance visit rearms it.
func (s *patrolWorkState) forget() {
	s.entries = [modernPatrolMemorySize]patrolMemoryEntry{}
	s.next = 0
}

// remember records e unless the same target is already remembered; a full
// memory overwrites its oldest entry.
func (s *patrolWorkState) remember(e patrolMemoryEntry) {
	if e.kind == patrolMemoryEmpty {
		return
	}
	for _, have := range s.entries {
		if have == e {
			return
		}
	}
	s.entries[s.next] = e
	s.next = (s.next + 1) % modernPatrolMemorySize
}

func (s *patrolWorkState) remembers(kind patrolMemoryKind, u *units.Unit) bool {
	if u == nil {
		return false
	}
	for _, have := range s.entries {
		if have.kind == kind && have.unit == u && have.handle == u.Handle {
			return true
		}
	}
	return false
}

func (s *patrolWorkState) remembersFeature(cx, cz int32) bool {
	for _, have := range s.entries {
		if have.kind == patrolMemoryFeature && have.cx == cx && have.cz == cz {
			return true
		}
	}
	return false
}

func (s *patrolWorkState) remembersAnyFactory() bool {
	for _, have := range s.entries {
		if have.kind == patrolMemoryFactory {
			return true
		}
	}
	return false
}

// These read-only projections publish the Modern patrol receipts for parity
// traces. Canonical capture follows the same links as object references;
// neither projection serializes a host pointer. DESIGN_UNITS_ORDERS_COB
// "Modern patrol work"; retail-format saved orders have no producer receipt.
func (q *Queue) WorkReturnOrdinal(n *Node) int {
	if n == nil || n.workReturn == nil {
		return 0
	}
	if q == nil {
		return -1
	}
	if index := q.indexOfPrimary(n.workReturn); index >= 0 {
		return index + 1
	}
	return -1
}

func (n *Node) IsPatrolReturn() bool { return n != nil && n.patrolReturn }

// WorkFactory is the handle of the factory whose product a Modern patrol job
// was selected to assist, or zero.
func (n *Node) WorkFactory() pool.Handle {
	if n == nil || n.workFactory == nil {
		return 0
	}
	return n.workFactory.Handle
}

// PatrolMemoryEntry is the parity projection of one memory slot. Kind is
// zero for an empty slot, 1 for a unit, 2 for a feature cell and 3 for a
// factory.
type PatrolMemoryEntry struct {
	Kind   uint8
	Handle pool.Handle
	CX, CZ int32
}

// PatrolWorkTrace is the parity projection of the queue's Modern patrol
// state: every memory slot in slot order, the slot the next entry
// overwrites, and the next selection-only wake tick.
type PatrolWorkTrace struct {
	Entries [modernPatrolMemorySize]PatrolMemoryEntry
	Next    uint8
	ScanDue uint32
}

func (q *Queue) PatrolWorkTrace() PatrolWorkTrace {
	var t PatrolWorkTrace
	if q == nil {
		return t
	}
	for i, e := range q.patrolWork.entries {
		t.Entries[i] = PatrolMemoryEntry{Kind: uint8(e.kind), Handle: e.handle, CX: e.cx, CZ: e.cz}
	}
	t.Next, t.ScanDue = q.patrolWork.next, q.patrolWork.scanDue
	return t
}
