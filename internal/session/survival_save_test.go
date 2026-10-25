package session

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/save"
	"github.com/nanolathe-gg/nanolathe/internal/survival"
)

// Contracts for Survival saves (DESIGN_SURVIVAL §11).

const survivalSaveSeed = 7

// Every field of the director is either in SurvivalRecord or rebuilt at the
// load. A field the director gains fails this test until the record or the
// list below accounts for it, so no change silently drops director state.
func TestSurvivalRecordAccountsForEveryDirectorField(t *testing.T) {
	recorded := []string{"attacker", "team", "settled", "centreX", "centreZ", "deposits", "depositAt", "depositWant", "depositHave",
		"phase", "phaseEnd", "wave", "plan", "nextG", "nextP", "lastSpawn", "waveUnits", "units", "nextRetarget",
		"survived", "wavePoints", "cleanLost", "stats", "removed", "history", "info"}
	rebuilt := []string{
		"tuning", "opts", "pool", // from the setup's switches and the catalog
		"startClass", "startRegion", "classes", // terrain region labels
		"accts", "walk", // per-call scratch
	}
	typ := reflect.TypeOf(survivalState{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !slices.Contains(recorded, name) && !slices.Contains(rebuilt, name) {
			t.Errorf("survivalState.%s is neither recorded nor rebuilt by restoreSurvival", name)
		}
	}
	unit := reflect.TypeOf(survivalUnit{})
	for i := range unit.NumField() {
		switch name := unit.Field(i).Name; name {
		case "h", "wave", "target", "infecting":
		case "shun", "shunSerial", "shunCellX", "shunCellZ", "shunUntil":
			// A shunned victim is skipped for at most survivalShunTicks; a
			// load forgets it, as allocation serials are not saved.
		default:
			t.Errorf("survivalUnit.%s is neither recorded nor deliberately dropped", name)
		}
	}
}

// A Survival battle saved during an active wave loads as a Survival battle
// whose director stands where it stood, with the same attackers on the same
// targets, and plays on. Skipped without retail assets.
func TestSurvivalDirectorSurvivesASaveRoundTrip(t *testing.T) {
	f := loadRetailFixture(t)
	cfg := SurvivalSkirmishConfig(retailMap, 1, SurvivalOptions{Pace: survival.PaceRelentless})
	cfg.RNGSimSeed, cfg.RNGCrtSeed = survivalSaveSeed, survivalSaveSeed
	src, err := NewSkirmishWithProgress(f.fs, f.cat, cfg, nil)
	if err != nil {
		t.Fatalf("survival battle: %v", err)
	}
	if !src.IsSurvival() {
		t.Fatal("not a Survival battle")
	}
	step := func(s *Session, n int) {
		for range n {
			s.Step(s.Clock.ScaledAnchor + 1)
		}
	}
	step(src, 30)
	st := src.Survival
	st.phaseEnd = src.Clock.GlobalTick + 1 // bring the first warning forward
	for i := 0; i < 2400 && !(st.phase == survivalActive && st.nextG >= len(st.plan.Groups) && len(st.units) > 0); i++ {
		step(src, 1)
	}
	if st.phase != survivalActive || len(st.units) == 0 {
		t.Fatalf("the first wave did not arrive: phase %d, %d attackers", st.phase, len(st.units))
	}
	step(src, 90) // let the retarget sweep and combat run
	if len(st.info.Warnings(src.Clock.GlobalTick, nil)) == 0 {
		t.Fatal("no warning was published to the survivors")
	}

	// Make sure the running damage totals are exercised: one attacker has
	// lost health to the survivors, whatever the fight did so far.
	if st.removed == nil {
		st.removed = map[pool.Handle]int32{}
	}
	st.removed[st.units[0].h] += 5
	raw, err := json.Marshal(RecordSurvival(src))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := SidecarSurvival(raw)
	if err != nil {
		t.Fatal(err)
	}
	summary := RetailBattleSummary(src, "survival", "0", src.Skirmish.UnitLimit)
	in, err := src.RetailBattleSaveInputs(summary, save.Camera{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "SURVIVAL.SAV")
	if err := src.WriteRetailSave(path, in); err != nil {
		t.Fatalf("write survival save: %v", err)
	}
	result, err := LoadRetailSavePath(path, RetailLoadDeps{
		FS: f.fs, Catalog: f.cat, SimSeed: survivalSaveSeed, CRTSeed: survivalSaveSeed,
		UnitLimit: src.Skirmish.UnitLimit, Survival: rec,
	})
	if err != nil {
		t.Fatalf("load survival save: %v", err)
	}
	dst := result.Battle.Session
	if !dst.IsSurvival() {
		t.Fatal("the save loaded as an ordinary skirmish")
	}
	got := dst.Survival
	if mgr := dst.AI[got.attacker]; mgr == nil || !mgr.Passive {
		t.Fatal("the restored attacker lost its passive manager")
	} else if mgr.Deadlines != [ai.TaskKindCount]uint32{} {
		t.Fatalf("the attacker's planner ran during the load-entry prime: deadlines %v", mgr.Deadlines)
	}
	if got.attacker != st.attacker || !slices.Equal(got.team, st.team) || dst.EnemyOwner != src.EnemyOwner {
		t.Fatalf("attacker %d team %v enemy %d, want %d %v %d", got.attacker, got.team, dst.EnemyOwner, st.attacker, st.team, src.EnemyOwner)
	}
	if got.phase != st.phase || got.phaseEnd != st.phaseEnd || got.wave != st.wave || got.nextG != st.nextG || got.nextP != st.nextP ||
		got.lastSpawn != st.lastSpawn || got.nextRetarget != st.nextRetarget || got.survived != st.survived ||
		got.wavePoints != st.wavePoints || got.cleanLost != st.cleanLost || got.stats != st.stats {
		t.Fatalf("director restored as %+v, want %+v", got.status(), st.status())
	}
	if !reflect.DeepEqual(got.plan, st.plan) || !slices.Equal(got.waveUnits, st.waveUnits) || !reflect.DeepEqual(got.history, st.history) {
		t.Fatal("the planned wave, its units or the wave history changed across the save")
	}
	if len(got.units) != len(st.units) {
		t.Fatalf("%d attackers under the director, want %d", len(got.units), len(st.units))
	}
	for i, su := range st.units {
		g := got.units[i]
		if g.h != su.h || g.wave != su.wave || g.target != su.target || g.infecting != su.infecting {
			t.Fatalf("attacker %d restored as %+v, want %+v", i, g, su)
		}
		if u := dst.Units.Unit(g.h); u != nil && (!u.Alive || u.Owner != got.attacker) {
			t.Fatalf("attacker %d's slot holds unit owner %d alive %t", i, u.Owner, u.Alive)
		}
	}
	if len(st.removed) == 0 || !reflect.DeepEqual(got.removed, st.removed) {
		t.Fatal("the attackers' running damage totals changed across the save")
	}
	// Shared sight: each survivor's vision-team word (SetVisionTeam) is the
	// saved battle's. The word is unexported; read it by reflection, so a
	// rename upstream fails here rather than skipping the check.
	teamWord := func(s *Session, p uint8) uint64 {
		f := reflect.ValueOf(s.Vis).Elem().FieldByName("team")
		if !f.IsValid() || f.Kind() != reflect.Array {
			t.Fatal("visibility.Service has no team array; update this check")
		}
		return f.Index(int(p)).Uint()
	}
	for _, p := range st.team {
		if want := teamWord(src, p); want == 0 || teamWord(dst, p) != want {
			t.Fatalf("survivor %d's vision team word %#x after the load, want %#x", p, teamWord(dst, p), want)
		}
	}
	tick := src.Clock.GlobalTick
	if w, want := got.info.Warnings(tick, nil), st.info.Warnings(tick, nil); !reflect.DeepEqual(w, want) {
		t.Fatalf("survivors' warnings %v, want %v", w, want)
	}
	for _, p := range got.info.Team {
		if m := dst.AI[p]; m != nil && m.Survival != got.info {
			t.Fatalf("survivor %d's manager lost the scenario record", p)
		}
	}
	if !dst.Skirmish.Survival.Enabled || dst.Skirmish.Survival.Pace != survival.PaceRelentless || dst.Skirmish.Players != src.Skirmish.Players {
		t.Fatal("the restored battle would not restart as the saved Survival battle")
	}

	// It plays on: the restored director keeps stepping, through to the next
	// wave's warning.
	before := got.wave
	step(dst, 1)
	if dst.Clock.GlobalTick <= tick {
		t.Fatalf("restored battle did not tick: %d", dst.Clock.GlobalTick)
	}
	got.phaseEnd = min(got.phaseEnd, dst.Clock.GlobalTick+1)
	for i := 0; i < 4000 && got.wave == before; i++ {
		if got.phase == survivalActive && got.nextG >= len(got.plan.Groups) {
			// Clear the arrived wave so the downtime and next warning follow.
			for _, h := range got.waveUnits {
				if u := dst.Units.Unit(h); u != nil && u.Alive {
					dst.Units.FreeImmediate(h)
				}
			}
		}
		step(dst, 1)
	}
	if got.wave != before+1 || got.survived != st.survived+1 {
		t.Fatalf("the restored director did not reach the next wave: wave %d survived %d", got.wave, got.survived)
	}
}

// status is a compact view of the director's scalars for failure messages.
func (st *survivalState) status() map[string]any {
	return map[string]any{"phase": st.phase, "phaseEnd": st.phaseEnd, "wave": st.wave, "next": [2]int{st.nextG, st.nextP},
		"lastSpawn": st.lastSpawn, "retarget": st.nextRetarget, "survived": st.survived, "points": st.wavePoints}
}
