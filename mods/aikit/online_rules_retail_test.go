//go:build retail

package aikit_test

import (
	"testing"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/testsupport/retailcat"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// Online rooms under Strict 3.1 and Community 3.9 with real computer players
// (DESIGN_MULTIPLAYER §6.6, §16.6, §17 "Computer seats"). Each test runs two
// independent clients the way TestOnlineModernComputersAgreeAcrossClientsRetail
// does — the host composing from its resolved configuration, the joiner from
// the configuration's bytes with content it froze itself, each presenting its
// own seat, both stepping the same granted ticks and human command stream —
// and fails at the first 30-tick sample where their unit checksums or random
// streams part. Seat 0 hosts every computer. Each run goes on through seat
// 0's defeat, where the bound set's SeatRules.ComputersStopWithHost decides
// what its computers do: Strict 3.1 copies the host's countdown and ending
// bit onto their records at the host's own write, so their settlement is
// refused while it counts down, and a host whose machine still hosts a live
// computer then takes the watch branch rather than the ending, so they settle
// again [05 R-ECO-01 §1] [08 R-SKIR-01 §3]; Community 3.9 composes Modern's
// Nanolathe policy, under which they play on.

// hostedComputers follows the computers seat 0 hosts on one client, at every
// 30-tick sample from the first at which seat 0's countdown is armed or its
// game has ended. Settlement is observed through each record's production
// totals, which only a settlement pass adds to [05 "Stocks, counters, and
// waste"]; each 30-tick window between samples holds exactly one of the
// computer's settlement dues.
type hostedComputers struct {
	s     *session.Session
	rows  []int
	stops bool
	// armed is the first sample tick at which seat 0's defeat had armed its
	// countdown, and 0 before it; produced is each row's totals at that tick.
	armed    uint32
	produced map[int][2]float64
	// last is the last sample tick; live and settled are, per row, whether it
	// had a live unit at the last sample and whether it settled after armed.
	last    uint32
	live    map[int]bool
	settled map[int]bool
	// prev is each row's totals at the previous sample, and windows, per
	// row, whether each window from the arming sample on held a settlement.
	prev    map[int][2]float64
	windows map[int][]bool
	// watched says seat 0 took the watch branch: its countdown back at −1
	// after arming, its ending bit clear and its watch-mode bit set.
	watched bool
}

func newHostedComputers(s *session.Session, setup session.OnlineMatchSetup) *hostedComputers {
	w := &hostedComputers{
		s: s, stops: s.Rules.Seats.ComputersStopWithHost(),
		produced: map[int][2]float64{}, live: map[int]bool{}, settled: map[int]bool{},
		prev: map[int][2]float64{}, windows: map[int][]bool{},
	}
	for k := range setup.Computers {
		w.rows = append(w.rows, len(setup.Seats)+k)
	}
	return w
}

// sample checks, from the sample at which seat 0's defeat armed, that every
// computer it hosts follows the bound answer: under Strict 3.1 its countdown
// and ending bit are its host's, and the host's ending bit is never set while
// the computer and the other human still have live units — the watch branch
// takes the place of the ending, and only the no-human site, once the other
// human is gone, may end the host [08 R-SKIR-01 §3] [08 R-SESS-01 §1]; under
// Community 3.9 its own countdown stays unarmed and its ending bit clear.
func (w *hostedComputers) sample(t *testing.T, tick uint32) {
	t.Helper()
	host := &w.s.Econ.Players[0]
	settledWindow := map[int]bool{}
	for _, row := range w.rows {
		totals := w.s.Econ.Players[row].TotalProduced
		settledWindow[row] = totals != w.prev[row]
		w.prev[row] = totals
	}
	if w.armed == 0 {
		if host.EndGameCountdown < 0 && !host.GameEnded {
			return
		}
		w.armed = tick
		for _, row := range w.rows {
			w.produced[row] = w.s.Econ.Players[row].TotalProduced
		}
	}
	w.last = tick
	if host.EndGameCountdown < 0 && !host.GameEnded && host.Watcher {
		w.watched = true
	}
	for _, row := range w.rows {
		c := &w.s.Econ.Players[row]
		w.live[row] = w.s.Units.LiveCountForPlayer(row) > 0
		if c.TotalProduced != w.produced[row] {
			w.settled[row] = true
		}
		if w.stops {
			if c.EndGameCountdown != host.EndGameCountdown || c.GameEnded != host.GameEnded {
				t.Fatalf("tick %d: computer %d's countdown %d (ended %v) does not follow its host's %d (ended %v)",
					tick, row, c.EndGameCountdown, c.GameEnded, host.EndGameCountdown, host.GameEnded)
			}
			if host.GameEnded && w.live[row] && w.s.Units.LiveCountForPlayer(1) > 0 {
				t.Fatalf("tick %d: the host's ending bit is set while its computer %d and seat 1 live", tick, row)
			}
			w.windows[row] = append(w.windows[row], settledWindow[row])
		} else if c.EndGameCountdown != -1 || c.GameEnded {
			t.Fatalf("tick %d: computer %d stopped with its host (countdown %d, ended %v)", tick, row, c.EndGameCountdown, c.GameEnded)
		}
	}
}

// onlineRulesRun composes setup's room on two clients, checks that each
// binds the named set with every computer row running its marked controller
// and borrowing seat 0's perspective, and runs them in lockstep for at most
// ticks, following seat 0's computers on both. It fails unless the clients
// end alike and seat 0's defeat armed during the run.
func onlineRulesRun(t *testing.T, setup session.OnlineMatchSetup, ticks uint32, stops bool) ([]*session.Session, []onlineModernOutcome, []*hostedComputers) {
	t.Helper()
	inputs, config := onlineModernRoom(t, setup)
	joinerInputs, joinerConfig := onlineModernJoiner(t, config)
	replicas := []*session.Session{
		onlineModernReplica(t, inputs, config, 0),
		onlineModernReplica(t, joinerInputs, joinerConfig, 1),
	}
	watches := make([]*hostedComputers, len(replicas))
	for i, s := range replicas {
		if s.Rules.Name != setup.Rules {
			t.Fatalf("client %d composed under %q, want %q", i, s.Rules.Name, setup.Rules)
		}
		if got := s.Rules.Seats.ComputersStopWithHost(); got != stops {
			t.Fatalf("%q answers ComputersStopWithHost %v, want %v", setup.Rules, got, stops)
		}
		modernHosts(t, s, setup)
		for k := range setup.Computers {
			row := len(setup.Seats) + k
			if s.Vis.PerspectiveOf(visibility.PlayerID(row)) != 0 {
				t.Fatalf("computer %d does not borrow seat 0's perspective", row)
			}
		}
		watches[i] = newHostedComputers(s, setup)
	}
	begin := time.Now()
	out := runOnlineLockstepObserved(t, replicas, setup, ticks, func(tick uint32) {
		for _, w := range watches {
			w.sample(t, tick)
		}
	})
	elapsed := time.Since(begin)
	if out[0].digest != out[1].digest || out[0].fingerprint != out[1].fingerprint {
		t.Fatalf("the clients agreed at every sample but ended apart: digests %s and %s", out[0].digest, out[1].digest)
	}
	for row, st := range out[0].stats {
		if out[1].stats[row] != st || out[1].generators[row] != out[0].generators[row] {
			t.Fatalf("row %d's controller applied %+v (generator %d) on one client and %+v (generator %d) on the other",
				row, st, out[0].generators[row], out[1].stats[row], out[1].generators[row])
		}
		if st.Applied == 0 {
			t.Fatalf("Modern computer %d applied no command: the run no longer sees its decisions", row)
		}
	}
	w := watches[0]
	if w.armed == 0 {
		t.Fatalf("seat 0 was not defeated in %d ticks: the run never reached the seat policy", out[0].tick)
	}
	if r := replicas[0].ResultForSeat(0); r.Kind != "defeat" {
		t.Fatalf("seat 0's countdown armed at tick %d with result %+v, want a defeat", w.armed, r)
	}
	c := censusOf(replicas[0])
	rows := len(setup.Seats) + len(setup.Computers)
	t.Logf("%q: %d lockstep ticks of two clients in %v; seat 0's defeat armed at tick %d; battle ended %v; created %v, live %v; digest %s",
		setup.Rules, out[0].tick, elapsed, w.armed, replicas[0].OnlineBattleEnded(), c.created[:rows], c.live[:rows], out[0].digest)
	return replicas, out, watches
}

// onlineStrictComputer is the one computer a Strict 3.1 room's host may add:
// on a team of its own, so it is hostile to both humans, Arm, hard.
func onlineStrictComputer(kind ai.Controller) session.OnlineComputer {
	return session.OnlineComputer{Team: 3, Side: 0, Color: 2, Kind: kind, Difficulty: 2}
}

// A Strict 3.1 room holds the one computer its host may add, Modern or
// Classic, and two clients play it alike tick for tick through the host's
// defeat. From the sample at which the host's defeat arms its countdown, the
// computer's record carries the host's countdown: retail's one countdown per
// machine gates the human and the computers it hosts (DESIGN_MULTIPLAYER
// §6.6, SeatRules.ComputersStopWithHost true). A host whose computer still
// lives takes the watch branch rather than the ending, so its computer's
// settlement is refused on exactly five dues from the defeat and runs again
// on the sixth, and the host's ending bit stays clear [05 R-ECO-01 §1]
// [08 R-SKIR-01 §3] "A hosting machine's own defeat and its computer".
//
// The Modern computer defeats seat 0 a little under nine minutes in (tick
// 15720) and stands with dozens of units afterwards, so its run shows a live
// computer whose settlement pauses for the five dues and resumes while the
// battle goes on for seat 1; eleven minutes cover it. The Classic computer's
// commander meets seat 0's a little over five minutes in and both are
// destroyed in the exchange, so its run, which ends when seat 1's victory
// countdown does, locks the countdown copy rather than the settlement gate.
// Neither clock is retail data: they are what this composition does on The
// Pass with these seeds.
func TestOnlineStrictComputerStopsWithItsHostRetail(t *testing.T) {
	cat, _ := retailcat.Shared(t)
	for _, tc := range []struct {
		name  string
		kind  ai.Controller
		ticks uint32
		// outlives says the computer stands after its host's defeat, so the
		// run shows its settlement stopping rather than its elimination.
		outlives bool
	}{
		{"Modern", ai.ControllerModern, 19800, true},
		{"Classic", ai.ControllerClassic, onlineModernSpan, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup := onlineRulesSetup(cat, session.StrictRuleSetName, []session.OnlineComputer{onlineStrictComputer(tc.kind)})
			_, _, watches := onlineRulesRun(t, setup, tc.ticks, true)
			for i, w := range watches {
				host := &w.s.Econ.Players[0]
				t.Logf("client %d: armed at tick %d; watched %v, host ended %v, computer live %v; windows %v", i, w.armed, w.watched, host.GameEnded, w.live[2], w.windows[2])
				if !tc.outlives {
					if !w.watched && !host.GameEnded {
						t.Fatalf("client %d: the host's countdown neither took the watch branch nor ended", i)
					}
					continue
				}
				if w.last <= w.armed || !w.live[2] || !w.watched {
					t.Fatalf("client %d: the computer did not stand through its host's watch branch from the defeat at tick %d", i, w.armed)
				}
				// The arming due falls in the arming sample's window when the
				// computer's due follows it, and in the next one otherwise; five
				// refused windows follow from there, then a settlement.
				windows := w.windows[2]
				start := 0
				if len(windows) > 0 && windows[0] {
					start = 1
				}
				if len(windows) < start+6 {
					t.Fatalf("client %d: %d windows after the defeat, too few to show the gate reopen", i, len(windows))
				}
				for k := start; k < start+5; k++ {
					if windows[k] {
						t.Fatalf("client %d: the computer settled in window %d of its host's countdown: %v", i, k, windows)
					}
				}
				if !windows[start+5] {
					t.Fatalf("client %d: the computer did not settle on the sixth due: %v", i, windows)
				}
			}
		})
	}
}

// onlineCommunitySpan bounds the Community run at 13 minutes 20 seconds.
// Seat 0 is defeated about twelve minutes in, by the hard Modern computer on
// seat 1's team; seat 1 then wins with it, so the battle ends five seconds
// later.
const onlineCommunitySpan = 24000

// A Community 3.9 room with TestOnlineModernComputersAgreeAcrossClientsRetail's
// computers — a Modern computer on each human's team, medium and hard, and a
// Classic one on a third team, all hosted by seat 0 — plays alike on two
// clients tick for tick through seat 0's defeat. Community composes the
// Nanolathe Modern seat policy (DESIGN_MULTIPLAYER §6.6,
// SeatRules.ComputersStopWithHost false): after the host's defeat arms, no
// computer it hosts takes its countdown or ending bit, and every one still
// standing goes on settling until the battle ends.
func TestOnlineCommunityComputersOutliveTheirHostRetail(t *testing.T) {
	cat, _ := retailcat.Shared(t)
	setup := onlineRulesSetup(cat, session.CommunityRuleSetName, onlineModernComputers(0, true))
	replicas, out, watches := onlineRulesRun(t, setup, onlineCommunitySpan, false)
	if hosts := modernHosts(t, replicas[0], setup); len(hosts) != 2 {
		t.Fatalf("%d Modern controllers, want 2", len(hosts))
	}
	for _, row := range []uint8{2, 3} {
		t.Logf("Modern computer %d: %+v", row, out[0].stats[row])
	}
	for i, w := range watches {
		standing := 0
		for _, row := range w.rows {
			if !w.live[row] {
				continue
			}
			standing++
			if !w.settled[row] {
				t.Fatalf("client %d: computer %d stood from its host's defeat at tick %d to tick %d without settling", i, row, w.armed, w.last)
			}
		}
		if standing == 0 || w.last <= w.armed {
			t.Fatalf("client %d: no computer stood after its host's defeat at tick %d, so the run does not show one playing on", i, w.armed)
		}
	}
}
