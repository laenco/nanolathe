package construction

import (
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/model"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// The work loop retries an unfinished product one tick later [05 "Factory production lifecycle"], and a factory working on itself has no other wake source: that
// retry is the only thing that will ever run the record again. The retry is
// armed as a wait mask for the interrupts plus a deadline, and the shared
// deadline setter always ORs the ordinary deadline bit into the gate
// [04 R-ORD-01 §0, 0x1 row] — expiry is raised only as that bit, and the pump
// reaches a handler only through the intersection with the gate. Armed without
// it, the expiry clears the deadline, the intersection is empty, and only the
// handler re-arms a deadline: the record is never visited again and the
// building stands at its work posture spending nothing for the rest of the
// battle. The get-built rows already arm the bit and are unaffected;
// TestGetBuiltDeadlineRaisesOnlyOrdinaryBit keeps the case where an expiry
// really does only raise the bit and leave the record waiting.
func TestFactoryWorkLoopRunsItsOwnOneTickRetry(t *testing.T) {
	cat := &content.Catalog{Units: map[string]*content.UnitDef{}}
	facDef := newFactoryDef("armfac", 1, 1, 60)
	prodDef := newProductDef("armflash", 1, 1, 100, 100)
	cat.Units[facDef.CanonicalKey] = facDef
	cat.Units[prodDef.CanonicalKey] = prodDef
	w := newConstructionFixtureWorld(12, cat)
	h, _ := w.Create(facDef, 0, 0, 0, 0)
	factory := w.Unit(h)
	ph, _ := w.Create(prodDef, 0, 0, 0, 0)
	product := w.Unit(ph)
	bindConstructionFixture(factory, trivialModel(1, nil), false)
	product.Remaining, product.MaxHealth = 0.5, 100
	q := orders.QueueForUnit(factory)
	q.Push(orders.Lookup("BuildingBuild"), orders.Node{BuildDefKey: prodDef.CanonicalKey, Param2: 1, Phase: uint8(State3)})
	q.Primary()[0].BindTarget(ph) // stage the runtime product relink [04 R-ORD-01 §6]
	node := q.Primary()[0]
	svc := NewService(nil, cat, w, &economy.Service{})
	svc.SetModelForUnit(func(*units.Unit) *model.Model { return trivialModel(1, nil) })
	// No script or interrupt is standing: the record's only wake source is the
	// retry it arms for itself.
	factory.Pending = 0
	svc.Pump(factory, 7)
	if node.Phase != uint8(State3) || node.Deadline != 8 {
		t.Fatalf("work visit did not re-arm its retry: phase=%d deadline=%d", node.Phase, node.Deadline)
	}
	if node.DynamicGate != WakeBit1|WakeBit3|orders.GateDeadline {
		t.Fatalf("retry armed without the shared deadline setter's bit: gate=%#x", node.DynamicGate)
	}
	factory.Pending = 0
	svc.Pump(factory, 8)
	if node.Deadline != 9 {
		t.Fatalf("the work loop's one-tick retry never ran: gate=%#x deadline=%d satisfied=%#x pending=%#x remaining=%v",
			node.DynamicGate, node.Deadline, node.Satisfied, factory.Pending, product.Remaining)
	}
}
