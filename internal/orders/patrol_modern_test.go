package orders

import (
	"fmt"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/save"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// Nanolathe Modern policy, user-approved 2026-10-09: acquisition follows
// current sight, and every borrowed patrol job retains a saved-position return.
func TestModernPatrolCurrentSightAcquisition(t *testing.T) {
	for _, air := range []bool{false, true} {
		for stance := uint32(0); stance < 3; stance++ {
			for _, outside := range []bool{false, true} {
				t.Run(fmt.Sprintf("air%v/stance%d/outside%v", air, stance, outside), func(t *testing.T) {
					f := newModernWorkFixture(air)
					f.u.X, f.u.Z = numeric.FixedFromInt(100), numeric.FixedFromInt(160)
					f.u.Def.SightDistance = 280
					f.u.Flags = f.u.Flags&^(stanceFieldMask<<stanceMoveShift) | stance<<stanceMoveShift
					target := modernPatient(2, 100, 440, true)
					if outside {
						target.Z++
					}
					f.units = []*units.Unit{target}
					before := f.sim
					f.visit(100)
					selected := f.q.Head() != f.patrol
					if selected == outside {
						t.Fatalf("selected=%v outside=%v: current sight-circle boundary", selected, outside)
					}
					if selected && f.q.Head().Target != target.Handle {
						t.Fatal("selected wrong frame")
					}
					if f.sim != before {
						t.Fatal("Modern selection drew RNG")
					}
				})
			}
		}
	}
}

func TestModernPatrolBorrowsWithSavedReturn(t *testing.T) {
	for _, air := range []bool{false, true} {
		for stance := uint32(0); stance < 3; stance++ {
			t.Run(fmt.Sprintf("air%v/stance%d", air, stance), func(t *testing.T) {
				f := newModernWorkFixture(air)
				f.u.X = numeric.FixedFromInt(100)
				f.u.Flags = f.u.Flags&^(stanceFieldMask<<stanceMoveShift) | stance<<stanceMoveShift
				f.units = []*units.Unit{modernPatient(2, 132, 0, true)}
				f.visit(100)
				rows := f.q.primary
				move := rowMoveGround
				if air {
					move = Lookup("VTOL_Move")
				}
				if len(rows) != 5 || rows[1].ID != move {
					t.Fatalf("borrowed queue has %d rows, want work/return/original three rows", len(rows))
				}
				back := rows[1]
				if back.GoalX != f.u.X || back.GoalY != f.u.Y || back.GoalZ != f.u.Z || rows[2] != f.patrol || rows[3] != f.next || rows[4] != f.successor {
					t.Fatal("borrow lost acquisition position, patrol identity or successor")
				}
			})
		}
	}
}

// Nanolathe Modern policy, user-authorized 2026-10-22 ("Smarter patrol"): a
// failed job drops its saved-position return, remembers its target and
// resumes the retained leg from where the builder stands on the next tick.
func TestModernPatrolFailedJobDropsReturnAndRemembersTarget(t *testing.T) {
	for _, air := range []bool{false, true} {
		t.Run(fmt.Sprintf("air%v", air), func(t *testing.T) {
			f := newModernWorkFixture(air)
			target := modernPatient(2, 32, 0, true)
			f.units = []*units.Unit{target}
			workID := rowHelpBuild
			if air {
				workID = rowVTOLHelpBuild
			}
			attempts := 0
			f.q.SetOwnedHandler(workID, func(*units.Unit, *Node, uint32, uint32) (Code, bool) { attempts++; return 8, true })
			random, resources := f.sim, f.resources
			f.q.Pump(f.u, 100)
			if attempts != 1 || f.q.Head() != f.patrol || len(f.q.primary) != 3 || f.q.primary[1] != f.next || f.q.primary[2] != f.successor {
				t.Fatalf("failed job attempts=%d remaining=%d: want the retained route with no return", attempts, len(f.q.primary))
			}
			if !f.q.patrolWork.remembers(patrolMemoryUnit, target) {
				t.Fatal("failed target was not remembered")
			}
			if f.patrol.DynamicGate != gateDeadline || f.patrol.Deadline != 101 {
				t.Fatalf("failed job resumed the leg with gate %#x deadline %d, want the next tick", f.patrol.DynamicGate, f.patrol.Deadline)
			}
			f.q.Pump(f.u, 101)
			if attempts != 1 || f.q.Head() != f.patrol || f.sim != random || f.resources != resources {
				t.Fatal("remembered target was selected again, or recovery changed RNG/resources")
			}
		})
	}
}

// The target must stay within or on the sight circle around the saved
// acquisition point, and the builder within or on twice that radius of it
// (P6). Leaving either abandons the job as a failure.
func TestModernPatrolPursuitAndStrayBounds(t *testing.T) {
	for _, air := range []bool{false, true} {
		for _, escape := range []string{"target", "builder"} {
			t.Run(fmt.Sprintf("air%v/%s", air, escape), func(t *testing.T) {
				f := newModernWorkFixture(air)
				f.u.X, f.u.Z = numeric.FixedFromInt(100), numeric.FixedFromInt(160)
				f.u.Def.SightDistance = 280
				target := modernPatient(2, 132, 160, true)
				f.units = []*units.Unit{target}
				f.visit(100)
				work := f.q.Head()
				f.u.X = numeric.FixedFromInt(100 + 560)
				target.X, target.Z = numeric.FixedFromInt(100), numeric.FixedFromInt(440)
				if !f.q.binding.Rules.AutomaticWorkValid(f.u, work) {
					t.Fatal("inclusive saved sight circle or stray bound refused")
				}
				if escape == "target" {
					target.Z++
				} else {
					f.u.X++
				}
				if f.q.binding.Rules.AutomaticWorkValid(f.u, work) {
					t.Fatalf("%s escaped its bound", escape)
				}
				f.q.Pump(f.u, 101)
				if f.q.Head() != f.patrol || len(f.q.primary) != 3 || !f.q.patrolWork.remembers(patrolMemoryUnit, target) {
					t.Fatal("abandoned pursuit kept its return or forgot the target")
				}
			})
		}
	}
}

// Only a genuine patrol-waypoint arrival forgets the memory: maintenance,
// rotation without arrival, payload release and no-route do not.
func TestModernPatrolMemoryForgottenAtWaypointArrival(t *testing.T) {
	for _, air := range []bool{false, true} {
		t.Run(fmt.Sprintf("air%v", air), func(t *testing.T) {
			f := newModernWorkFixture(air)
			target := modernPatient(2, 32, 0, true)
			f.units = []*units.Unit{target}
			id := rowHelpBuild
			if air {
				id = rowVTOLHelpBuild
			}
			f.q.SetOwnedHandler(id, func(*units.Unit, *Node, uint32, uint32) (Code, bool) { return 8, true })
			f.q.Pump(f.u, 100)
			f.q.primary = []*Node{f.patrol, f.next, f.successor}
			f.visit(1000)
			if f.q.Head() != f.patrol || !f.q.patrolWork.remembers(patrolMemoryUnit, target) {
				t.Fatal("maintenance selected or forgot a remembered target")
			}
			r := f.q.binding.Rules
			for _, tc := range []struct {
				satisfied uint32
				code      Code
			}{{0, 6}, {gateNoRoute, 6}, {0x80, 6}, {gateArrived | gateNoRoute, 6}, {gateArrived, 2}, {gateArrived, 5}} {
				r.AutomaticWorkResult(f.u, f.patrol, tc.satisfied, tc.code, 1001)
				if !f.q.patrolWork.remembers(patrolMemoryUnit, target) {
					t.Fatalf("satisfied %#x code %d falsely counted as arrival", tc.satisfied, tc.code)
				}
			}
			r.AutomaticWorkResult(f.u, f.next, gateArrived, 6, 1002)
			if f.q.patrolWork != (patrolWorkState{scanDue: f.q.patrolWork.scanDue}) {
				t.Fatal("genuine patrol arrival did not forget the memory")
			}
			f.visit(1003)
			if f.q.Head().Target != target.Handle {
				t.Fatal("forgotten target was not selectable again")
			}
		})
	}
}

// Ground repair can report code 5 for an unsuccessful ending. A repair that
// left its patient damaged, airborne or dead is a failure and remembers it; a
// patient at full health either succeeded or, when the leash broke the job,
// no longer needs the work, which is neutral.
func TestModernPatrolSemanticRepairFailure(t *testing.T) {
	for _, air := range []bool{false, true} {
		for _, tc := range []struct {
			outcome            string
			remembered, failed bool
		}{{"dead", true, true}, {"airborne", true, true}, {"damaged", true, true}, {"leash", false, true}, {"healthy", false, false}} {
			t.Run(fmt.Sprintf("air%v/%s", air, tc.outcome), func(t *testing.T) {
				f := newModernWorkFixture(air)
				target := modernPatient(2, 32, 0, false)
				f.units = []*units.Unit{target}
				f.visit(100)
				work := f.q.Head()
				switch tc.outcome {
				case "dead":
					target.Alive = false
				case "airborne":
					target.Move.ModeMirror = 2
				case "leash":
					target.Health = target.Def.MaxDamage
					f.u.X = numeric.FixedFromInt(1)
					work.Param3 = 1
				case "healthy":
					target.Health = target.Def.MaxDamage
				}
				code := f.q.binding.Rules.AutomaticWorkResult(f.u, work, 0, 5, 101)
				if got := f.q.patrolWork.remembers(patrolMemoryUnit, target); got != tc.remembered || (code == 8) != tc.failed {
					t.Fatalf("remembered=%v code=%d for semantic repair outcome %s", got, code, tc.outcome)
				}
			})
		}
	}
}

func TestModernPatrolMemoryReplacementAndStrictBypass(t *testing.T) {
	f := newModernWorkFixture(false)
	entry := patrolMemoryEntry{kind: patrolMemoryFeature, cx: 1, cz: 2}
	f.q.patrolWork.remember(entry)
	f.q.patrolWork.scanDue = 300
	held := f.q.patrolWork
	f.q.Push(rowMoveGround, Node{QueuedIssue: true})
	if f.q.patrolWork != held {
		t.Fatal("queued successor reset the patrol memory")
	}
	f.q.Push(Lookup("Standing_MoveOrder"), Node{})
	if f.q.patrolWork != held {
		t.Fatal("stance issue reset the patrol memory")
	}
	f.q.PurgeUnprotected()
	f.q.Push(rowRepairPatrol, Node{})
	if f.q.patrolWork != (patrolWorkState{}) {
		t.Fatal("replacement patrol retained the old assignment's memory or cadence")
	}
	for _, r := range []Rules{StrictRules{}, &CommunityRules{}} {
		f.q.patrolWork = held
		state, resources := f.sim, f.resources
		if code := r.AutomaticWorkResult(f.u, f.patrol, gateArrived, 6, 200); code != 6 || f.q.patrolWork != held || f.sim != state || f.resources != resources {
			t.Fatalf("%T changed bypass state", r)
		}
		if r.PatrolWorkWake(f.u, f.patrol, 400) || f.q.patrolWork != held || f.sim != state {
			t.Fatalf("%T woke a patrol between maintenance visits", r)
		}
	}
}

func TestModernPatrolSuccessfulWorkContinuesPendingLeg(t *testing.T) {
	for _, air := range []bool{false, true} {
		t.Run(fmt.Sprintf("air%v", air), func(t *testing.T) {
			f := newModernWorkFixture(air)
			target := modernPatient(2, 32, 0, true)
			f.units = []*units.Unit{target}
			id := rowHelpBuild
			if air {
				id = rowVTOLHelpBuild
			}
			f.q.SetOwnedHandler(id, func(_ *units.Unit, n *Node, _ uint32, _ uint32) (Code, bool) {
				// A real executor completes from its work phase (ground 4, air 3).
				n.Phase = 3
				target.Remaining, target.Health = 0, target.Def.MaxDamage
				return 5, true
			})
			f.q.Pump(f.u, 100)
			if f.q.Head() != f.patrol || f.q.patrolWork.entries != [modernPatrolMemorySize]patrolMemoryEntry{} || len(f.q.primary) != 3 {
				t.Fatal("successful work did not resume the pending patrol leg without returning")
			}
			if f.q.primary[1] != f.next || f.q.primary[2] != f.successor {
				t.Fatal("successful work changed the remaining route")
			}
		})
	}
}

// Issue #122: three nearby jobs must not install a backward return between
// contributions, and finishing them must not rotate a three-waypoint route.
func TestModernPatrolConsecutiveJobsKeepWaypointOrder(t *testing.T) {
	for _, air := range []bool{false, true} {
		for stance := uint32(0); stance < 3; stance++ {
			t.Run(fmt.Sprintf("air%v/stance%d", air, stance), func(t *testing.T) {
				f := newModernWorkFixture(air)
				f.u.Flags = f.u.Flags&^(stanceFieldMask<<stanceMoveShift) | stance<<stanceMoveShift
				f.next.GoalX = numeric.FixedFromInt(1000)
				f.successor.ID, f.successor.Phase = f.patrol.ID, 1
				f.successor.GoalX, f.successor.GoalZ = 0, 0
				for i := 0; i < 3; i++ {
					f.units = append(f.units, modernPatient(pool.Handle(i+2), int32(200+i*100), 0, true))
				}
				var goals []*Node
				f.q.binding.Movement = NewMovementGoalAdapter(MovementGoalAdapterConfig{
					InstallPoint: func(req PointGoalRequest) bool { goals = append(goals, req.Node); return true },
					InstallAir:   func(req AirGoalRequest) bool { goals = append(goals, req.Node); return true },
				})
				id := rowHelpBuild
				if air {
					id = rowVTOLHelpBuild
				}
				completed := 0
				f.q.SetOwnedHandler(id, func(_ *units.Unit, n *Node, _ uint32, _ uint32) (Code, bool) {
					target := f.units[completed]
					if n.Target != target.Handle || n.workReturn.GoalX != f.u.X {
						t.Fatal("next job lost distance ordering or its new acquisition anchor")
					}
					// Stand in for paid work and its approach; stock session fixtures
					// below exercise those executors and actual onward movement.
					f.u.X = target.X
					n.Phase = 3 // the work phase a real executor completes from
					target.Remaining, target.Health = 0, target.Def.MaxDamage
					completed++
					return 5, true
				})
				random, resources := f.sim, f.resources
				f.q.Pump(f.u, 100)
				if completed != 3 || f.q.Head() != f.patrol || len(f.q.primary) != 3 || f.sim != random || f.resources != resources {
					t.Fatal("successful chain failed to retain pending waypoint without extra RNG/resources")
				}
				for _, n := range goals {
					if n != f.patrol {
						t.Fatal("job chain installed a return or later waypoint before pending waypoint arrival")
					}
				}
				for i, want := range []*Node{f.next, f.successor, f.patrol} {
					head := f.q.Head()
					f.u.X, f.u.Z = head.GoalX, head.GoalZ
					head.Satisfied = gateArrived
					f.q.Pump(f.u, uint32(101+i))
					if f.q.Head() != want || goals[len(goals)-1] != want {
						t.Fatal("job chain changed subsequent waypoint rotation")
					}
				}
			})
		}
	}
}

func TestPatrolSuccessfulWorkRemovalRequiresModernProvenance(t *testing.T) {
	for _, air := range []bool{false, true} {
		for _, kind := range []string{"build", "repair", "reclaim"} {
			for _, rules := range []Rules{StrictRules{}, &CommunityRules{}, &ModernRules{}} {
				for _, proven := range []bool{false, true} {
					t.Run(fmt.Sprintf("air%v/%s/%T/proven%v", air, kind, rules, proven), func(t *testing.T) {
						f := newModernWorkFixture(air)
						target := modernPatient(2, 32, 0, true)
						f.units = []*units.Unit{target}
						f.visit(100)
						work, back := f.q.Head(), f.q.primary[1]
						target.Remaining, target.Health = 0, target.Def.MaxDamage
						if kind == "build" {
							// HelpBuild completes from its work phase: ground 4, air 3.
							work.Phase = 4
							if air {
								work.Phase = 3
							}
						} else if kind == "repair" {
							work.ID = rowRepairUnit
							if air {
								work.ID = rowVTOLRepairUnit
							}
						} else if kind == "reclaim" {
							work.ID, work.Phase = Lookup("Reclaim"), 5
							if air {
								work.ID, work.Phase = Lookup("VTOL_Reclaim"), 4
							}
						}
						if !proven {
							work.workAssignment = nil
						}
						f.q.binding.Rules = rules
						random, resources := f.sim, f.resources
						if code := rules.AutomaticWorkResult(f.u, work, 0, 5, 101); code != 5 {
							t.Fatalf("successful work changed result to %d", code)
						}
						_, modern := rules.(*ModernRules)
						if removed := f.q.indexOfPrimary(back) < 0; removed != (modern && proven) {
							t.Fatal("return removal crossed the mode/provenance boundary")
						}
						if f.sim != random || f.resources != resources || f.q.patrolWork.entries != [modernPatrolMemorySize]patrolMemoryEntry{} {
							t.Fatal("successful outcome changed RNG, resources or the failure memory")
						}
					})
				}
			}
		}
	}
}

// Danger abandons automatic repair like any other failed job: the return
// goes, the patient is remembered and the danger response resumes the patrol.
func TestModernPatrolDangerInterruptionDropsReturnAndRemembers(t *testing.T) {
	f := newModernWorkFixture(false)
	patient := modernPatient(2, 32, 0, false)
	f.units = []*units.Unit{patient}
	f.visit(100)
	f.q.suspendDangerAssignment(f.u)
	if f.q.Head() != f.patrol || f.q.danger.resume != f.patrol || !f.q.patrolWork.remembers(patrolMemoryUnit, patient) || len(f.q.primary) != 3 {
		t.Fatal("danger kept the saved return or forgot the abandoned patient")
	}
	held := f.q.patrolWork
	f.q.binding.Rules = StrictRules{}
	f.q.SetBinding(f.q.binding)
	f.q.binding.Rules = &ModernRules{}
	f.q.SetBinding(f.q.binding)
	if f.q.Head() != f.patrol || f.q.patrolWork != held {
		t.Fatal("rebinding or mode switch discarded staged assignment state")
	}
	// A danger response must keep construction already in progress protected.
	f = newModernWorkFixture(false)
	f.units = []*units.Unit{modernPatient(2, 32, 0, true)}
	f.visit(100)
	if !protectedDangerWork(f.q) {
		t.Fatal("borrowed construction lost danger protection")
	}
}

func TestModernPatrolFeatureCurrentSightBoundary(t *testing.T) {
	for _, air := range []bool{false, true} {
		for _, outside := range []bool{false, true} {
			t.Run(fmt.Sprintf("air%v/outside%v", air, outside), func(t *testing.T) {
				f := newModernWorkFixture(air)
				f.u.Z = numeric.FixedFromInt(160)
				f.u.Def.SightDistance = 280
				f.resources.Stock[0] = 0
				feature := modernFeature(1, 0, 440, 10, 0)
				if outside {
					feature.Z++
				}
				f.features = []FeatureView{feature}
				before := f.sim
				f.visit(100)
				if selected := f.q.Head() != f.patrol; selected == outside || f.sim != before {
					t.Fatal("feature acquisition violated sight boundary or selection RNG")
				}
			})
		}
	}
}

func TestModernPatrolFeatureCompletionUsesExecutorPhase(t *testing.T) {
	for _, air := range []bool{false, true} {
		t.Run(fmt.Sprintf("air%v", air), func(t *testing.T) {
			f := newModernWorkFixture(air)
			f.resources.Stock[0] = 0
			f.features = []FeatureView{modernFeature(1, 32, 0, 10, 0)}
			f.visit(100)
			work := f.q.Head()
			work.Phase = 5
			if air {
				work.Phase = 4
			}
			if code := f.q.binding.Rules.AutomaticWorkResult(f.u, work, 0, 5, 101); code != 5 || f.q.patrolWork.entries != [modernPatrolMemorySize]patrolMemoryEntry{} {
				t.Fatal("completed feature payout was classified as failed work")
			}
		})
	}
}

// A saved-position return reaches the head only across a rule switch, since
// every Modern job end removes it. It then ends quietly: a failed move is
// dropped instead of cancelling or retrying the route, and nothing is
// remembered.
func TestModernPatrolReturnAcrossRuleSwitchEndsQuietly(t *testing.T) {
	for _, air := range []bool{false, true} {
		t.Run(fmt.Sprintf("air%v", air), func(t *testing.T) {
			f := newModernWorkFixture(air)
			f.units = []*units.Unit{modernPatient(2, 32, 0, true)}
			f.visit(100)
			back := f.q.primary[1]
			r := f.q.binding.Rules
			for _, tc := range []struct{ code, want Code }{{5, 5}, {7, 8}, {8, 8}, {9, 8}, {2, 2}} {
				if got := r.AutomaticWorkResult(f.u, back, gateNoRoute, tc.code, 101); got != tc.want || f.q.patrolWork.entries != [modernPatrolMemorySize]patrolMemoryEntry{} {
					t.Fatalf("return code %d became %d (want %d) or was remembered", tc.code, got, tc.want)
				}
			}
		})
	}
}

func TestModernPatrolArrivalRetainsNormalPayloadRelease(t *testing.T) {
	for _, air := range []bool{false, true} {
		t.Run(fmt.Sprintf("air%v", air), func(t *testing.T) {
			f := newModernWorkFixture(air)
			f.q.patrolWork.remember(patrolMemoryEntry{kind: patrolMemoryFeature, cx: 5})
			// The movement producer raises arrival before normal payload detach, so
			// both bits survive to the executor [04 R-PATH-01 §8][04 R-AIR-01 §1].
			f.q.binding.Rules.AutomaticWorkResult(f.u, f.patrol, gateArrived|0x80, 6, 102)
			if f.q.patrolWork.remembersFeature(5, 0) {
				t.Fatal("normal waypoint arrival did not forget the memory")
			}
		})
	}
}

func TestModernPatrolRetailSaveKeepsReturnButLosesReceipts(t *testing.T) {
	f := newModernWorkFixture(false)
	f.u.X, f.u.Y, f.u.Z = numeric.FixedFromInt(100), numeric.FixedFromInt(17), numeric.FixedFromInt(160)
	f.units = []*units.Unit{modernPatient(2, 132, 160, true)}
	f.visit(100)
	f.q.patrolWork.remember(patrolMemoryEntry{kind: patrolMemoryFeature, cx: 1, cz: 2})
	ids := map[pool.Handle]uint16{f.u.Handle: 1, f.units[0].Handle: 2}
	images, err := RetailOrderImagesWithPayload(f.u, func(h pool.Handle) (uint16, bool) { id, ok := ids[h]; return id, ok }, func(pool.Handle) bool { return true }, nil)
	if err != nil {
		t.Fatal(err)
	}
	records := make([]save.OrderRecord, len(images))
	for i, image := range images {
		records[i] = save.OrderRecord{ParentStableID: 1, Sequence: uint32(i), Main: image.Main, DescriptorName: image.DescriptorName, SubtypeCode: image.SubtypeCode, Subtype: image.Subtype}
	}
	if err := RetailRestoreOrdersAtTick(f.u, records, map[uint16]pool.Handle{1: f.u.Handle, 2: f.units[0].Handle}, f.q.binding, 101); err != nil {
		t.Fatal(err)
	}
	q := QueueOfUnit(f.u)
	back := q.primary[1]
	if back.ID != rowMoveGround || back.GoalX != f.u.X || back.GoalY != f.u.Y || back.GoalZ != f.u.Z || back.IsPatrolReturn() || q.patrolWork != (patrolWorkState{}) || q.Head().workAssignment != nil || q.Head().workReturn != nil || !q.binding.Rules.AutomaticWorkValid(f.u, q.Head()) {
		t.Fatal("retail save boundary changed ordinary return or inferred producer receipts")
	}
}
