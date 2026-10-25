package headless

import (
	"fmt"
	"hash/fnv"
	"io"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/aikit"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/content/profiles"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/movement"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/sim/rng"
	"github.com/nanolathe-gg/nanolathe/internal/units"
)

// ArenaPlayer is one contestant. A nil Brain plays the bound retail step.
type ArenaPlayer struct {
	Label   string
	Brain   aikit.Brain
	Persona aikit.Persona
	Side    int // side ordinal: 0 ARM, 1 CORE, then any faction add-on's sides
}

// ArenaRequest describes one displayless AI-versus-AI match.
type ArenaRequest struct {
	// Jam adds ArenaResult.Jam, a picture of the ground units when the match
	// ended, for the pathfinding laboratory. JamOf names the kind of unit
	// whose reading of the ground the picture carries; without it, or with
	// no such unit alive, it is the first held unit's that has no route.
	Jam   bool
	JamOf string
	// Watch names units by handle whose every tick from WatchFrom on is
	// added to ArenaResult.Watch: a match plays the same again, so the units
	// a picture showed held can be followed through the ticks before it.
	Watch     []int
	WatchFrom uint32
	Root      string
	Roots     []string
	// Content is the content section the catalog compiles under: a mod's
	// own config's, when the roots end in that mod. The zero value is the
	// base game's profile (profiles.Retail). Only the content applies; the
	// match keeps the arena's rule set, so a config's Community table does
	// not (docs/DESIGN_CONTENT_VFS.md §5 "Content profiles").
	Content profiles.Profile
	// Addons are add-on archives mounted at the first root's *.UFO tier,
	// as enabled add-ons are (docs/DESIGN_MODS_MUTATORS.md §5.8).
	Addons   []string
	Map      string
	Gameplay gameplay.Mode // a registered set whose planner is aikit.HostPlanner
	Seed     uint32
	// MapSeed plays the battle on ArenaMapSeed(Seed, Map) instead of Seed.
	// A Modern brain draws its style and jitter from the battle seed and its
	// slot, so a tournament that reuses one seed list on every map would
	// otherwise sample the same few draws on every map. False plays Seed
	// itself, which reproduces runs made before the derivation existed.
	MapSeed   bool
	TickLimit uint32
	Players   []ArenaPlayer
	// Starts selects which of the map's start positions each slot takes, so
	// start asymmetry can be separated from slot order.
	Starts ArenaStarts
	// Score selects the score a timeout is adjudicated on (default
	// ScoreDefault). Both scores are recorded either way.
	Score ArenaScore
	// SampleEvery is the metric sample interval in ticks (default 150).
	SampleEvery uint32
	// TraceEvery > 0 records unit positions every that many ticks and each
	// brain's Explain every ExplainEvery ticks, for the replay viewer.
	TraceEvery   uint32
	ExplainEvery uint32
	// Level is the battle's difficulty word (default hard). The retail
	// planner's plan gates read it, and under a set with the retail income
	// discount (-income retail) so does every computer player's income; a
	// Modern brain's persona is set by its player spec, not by the level.
	Level       ArenaLevel
	StartMetal  int
	StartEnergy int
	// Mutators are the battle's global multipliers, applied to the battle's
	// catalog clone at entry as in any skirmish
	// (docs/DESIGN_MODS_MUTATORS.md §6).
	Mutators content.Mutators
	// Values, when set, prices units for every score and value count by
	// canonical unit key instead of the battle catalog's costs. A balance
	// sweep scores every cost variant on one ruler: priced at its own costs,
	// a discounted army would score less for the same strength. Units it does
	// not name keep their
	// catalog value.
	Values map[string]int32
	Log    io.Writer
	// PaceTPS > 0 paces the match to that many ticks per wall second (30 is
	// real time), so asynchronous thinking is measured with real reaction
	// windows. Host-side sleep only; the simulation never reads a clock.
	PaceTPS int
	// MeasureAllocs turns on per-think allocation accounting. The runtime's
	// counters are process-wide, so the figures mean something only for
	// synchronous personas and one match per process.
	MeasureAllocs bool
	// Publish keeps the session's committed-frame publication. The arena has
	// no presentation consumer, and nothing authoritative reads a published
	// frame [I6], so by default a match drops the frame buffer after
	// composition and the session skips publication: the same game in a
	// little over half the simulation time (docs/MODERN_AI_RESEARCH.md §5.3).
	// Publish restores it, to check that the two still agree.
	Publish bool
	// Adjudicate ends a match early once its winner is clear. The zero value
	// plays every match to a commander kill or the tick limit.
	Adjudicate ArenaAdjudication
}

// ArenaAdjudication ends a match early, as a win for the leader (reason
// "adjudicated"), once one live player's score has been at least RatioPct/100
// times every other live player's at every metric sample of the last Window
// ticks, at a tick no earlier than From. Scores are the request's score kind
// read from the samples; a score below zero counts as zero and the leader's
// must be positive. The rule and its validation against full-length games
// are docs/MODERN_AI_RESEARCH.md §5.3.
type ArenaAdjudication struct {
	RatioPct uint32 `json:"ratio_pct"` // 200: twice the runner-up
	Window   uint32 `json:"window"`    // ticks the lead must hold
	From     uint32 `json:"from"`      // earliest tick
}

// adjudicator follows one match's lead from sample to sample.
type adjudicator struct {
	rule   ArenaAdjudication
	leader int    // the player holding the lead, -1 for none
	since  uint32 // the first sample of the current lead
}

// observe reads one sample tick's scores (index by player; dead players are
// ignored) and returns the adjudicated winner, or -1 to play on.
func (a *adjudicator) observe(tick uint32, scores []int64, alive []bool) int {
	lead, best, second := -1, int64(0), int64(0)
	for i, s := range scores {
		if !alive[i] {
			continue
		}
		s = max(s, 0)
		switch {
		case lead < 0 || s > best:
			second = max(second, best)
			lead, best = i, s
		case s > second:
			second = s
		}
	}
	// A lone survivor also counts as leading; the decisive check ends such a
	// match first.
	if lead < 0 || best <= 0 || best*100 < second*int64(a.rule.RatioPct) {
		a.leader = -1
		return -1
	}
	if lead != a.leader {
		a.leader, a.since = lead, tick
	}
	if tick >= a.rule.From && tick-a.since >= a.rule.Window {
		return lead
	}
	return -1
}

// sampleScore is a sample's score of the given kind, as the tick limit scores
// a survivor.
func sampleScore(s ArenaSample, kind ArenaScore) int64 {
	v := int64(s.ArmyValue) + int64(s.EcoValue) + s.ValueKilled - s.ValueLost/2
	if kind == ScoreInvested {
		v += int64(s.BuilderValue) + int64(s.FrameValue)
	}
	return v
}

// ArenaStarts selects how the arena's slots take the map's start positions.
// Slot order is not neutral on its own (think stagger, the order players are
// stepped, the per-slot random stream), so a tournament that always puts slot
// i at start i cannot tell a start advantage from a slot advantage.
type ArenaStarts string

const (
	// StartsSlot is identity placement: slot i takes the start position
	// whose stored number is i (the lobby's fixed-location setting).
	StartsSlot ArenaStarts = "slot"
	// StartsRandom is the retail randomized assignment, drawn from the
	// battle's CRT seed [08 "Randomization for skirmish starts"]: with two
	// players, a coin decides whether they exchange starts, so across seeds
	// the start is independent of the slot. The result records who got which.
	StartsRandom ArenaStarts = "random"
	// StartsSwap (two players only) puts slot 0 at start 1 and slot 1 at
	// start 0. The session offers no explicit assignment, so the arena plays
	// the randomized assignment with the first CRT seed, counting up from the
	// battle seed, whose coin exchanges the two starts, and checks the
	// placement before the first tick. The simulation seed is unchanged.
	StartsSwap ArenaStarts = "swap"
)

// ArenaLevel is the battle's difficulty word by name.
type ArenaLevel string

const (
	LevelEasy   ArenaLevel = "easy"
	LevelMedium ArenaLevel = "medium"
	LevelHard   ArenaLevel = "hard"
)

// word is the difficulty word the level names: 0 easy, 1 medium, 2 hard
// (the default).
func (l ArenaLevel) word() (int, error) {
	switch l {
	case "", LevelHard:
		return 2, nil
	case LevelMedium:
		return 1, nil
	case LevelEasy:
		return 0, nil
	}
	return 0, fmt.Errorf("arena: unknown level %q (have easy, medium, hard)", l)
}

// startSwapSeed returns the first CRT seed from seed upward whose retail
// randomized start assignment exchanges two players' starts. With fewer
// than three eligible slots that assignment first draws a CRT gate,
// (rand·2)/0x8000, and runs its single exchange only when the gate is 1
// [08 "Randomization for skirmish starts"].
func startSwapSeed(seed uint32) (uint32, error) {
	for k := uint32(0); k < 1<<20; k++ {
		c := rng.NewCRT(seed + k)
		if int(c.Rand())*2/0x8000 != 0 {
			return seed + k, nil
		}
	}
	return 0, fmt.Errorf("arena: no CRT seed within 2^20 of %d exchanges the starts", seed)
}

// ArenaScore names the score a game that reaches its tick limit is
// adjudicated on: the higher score wins on points when it is at least 1.3
// times the other, otherwise the game is a draw. Formulas are in
// docs/MODERN_AI_RESEARCH.md §5.
type ArenaScore string

const (
	// ScoreDefault is army value + economy value + value killed − value
	// lost / 2, from the last sample. Builders, the commander, nanoframes and
	// stock count nothing.
	ScoreDefault ArenaScore = "default"
	// ScoreInvested adds the value of finished builders other than the
	// commander and the built fraction of every nanoframe to ScoreDefault:
	// value invested, not only value fielded, for tests (such as a growth
	// switch) whose treatment spends on constructors and unfinished work.
	// Stock and the commander still count nothing.
	ScoreInvested ArenaScore = "invested"
)

// ArenaSample is one row of a player's time series.
type ArenaSample struct {
	Tick         uint32 `json:"t"`
	MetalIncome  int32  `json:"mi"`
	EnergyIncome int32  `json:"ei"`
	MetalStock   int32  `json:"ms"`
	EnergyStock  int32  `json:"es"`
	MetalWaste   int32  `json:"mw"` // cumulative overflow
	// MetalUse and EnergyUse are what the player's consumers asked for this
	// pass: above income with an empty store, the economy is stalled.
	MetalUse     int32 `json:"mu"`
	EnergyUse    int32 `json:"eu"`
	ArmyValue    int32 `json:"av"`
	EcoValue     int32 `json:"ev"`
	Units        int32 `json:"u"`
	Builders     int32 `json:"b"`
	IdleBuilders int32 `json:"ib"`
	Factories    int32 `json:"f"`
	IdleFactory  int32 `json:"if"`
	ValueLost    int64 `json:"vl"`
	ValueKilled  int64 `json:"vk"`
	Extractors   int32 `json:"mx"`
	// BuilderValue is the value of finished builders other than the
	// commander; FrameValue is the built fraction of every nanoframe's value.
	// Only ScoreInvested counts them.
	BuilderValue int32 `json:"bv"`
	FrameValue   int32 `json:"nv"`
	// Trapped counts ground combat units that have stayed within
	// trapRadius of where they were first seen for at least trapAge ticks
	// while their current order's goal lies beyond trapRadius: a unit told
	// to go somewhere that never leaves is boxed in by buildings, features
	// or wrecks. Units the army holds at home (no far goal) do not count.
	Trapped int32 `json:"tr"`
	// Held counts ground units of any role that have stood within
	// holdRadius of one point for holdAge ticks while the goal of their
	// current order lies beyond trapRadius: a unit with somewhere to go
	// that is not going. HeldMoving counts those of them whose order is a
	// plain ground move, which no weapon range or work site explains.
	Held       int32 `json:"hd"`
	HeldMoving int32 `json:"hm"`
	// HeldOpen and HeldMovingOpen count those of the two whose goal the
	// ground connects with where they stand, for their kind and owner and
	// with every mobile unit absent: the others are kept from a goal they
	// cannot reach however they move.
	HeldOpen       int32 `json:"ho"`
	HeldMovingOpen int32 `json:"hmo"`
	// Circling counts the held units that have gone circleWalk world units
	// since their hold began: on the move and going nowhere.
	Circling int32 `json:"ci"`
	// Ground is the ground units the two counts were taken over.
	Ground int32 `json:"gu"`
}

// ArenaCost is the measured AI cost for one player.
type ArenaCost struct {
	Steps          int     `json:"steps"`
	StepMeanUS     float64 `json:"step_mean_us"`
	StepP99US      float64 `json:"step_p99_us"`
	StepMaxUS      float64 `json:"step_max_us"`
	Thinks         int     `json:"thinks"`
	ThinkMeanUS    float64 `json:"think_mean_us"`
	ThinkP99US     float64 `json:"think_p99_us"`
	ThinkMaxUS     float64 `json:"think_max_us"`
	ThinkAllocB    float64 `json:"think_alloc_bytes_mean"`
	ThinkAllocObjs float64 `json:"think_alloc_objs_mean"`
	// StepAllocB and StepAllocObjs are the mean allocations of a whole host
	// step on the simulation thread (the observation, the command apply, a
	// synchronous think and the engine upkeep), measured like the think's.
	StepAllocB    float64 `json:"step_alloc_bytes_mean,omitempty"`
	StepAllocObjs float64 `json:"step_alloc_objs_mean,omitempty"`
	// Parts attributes the step's simulation-thread time (aikit.PartProbe),
	// one entry per part that ran.
	Parts []ArenaPartCost `json:"parts,omitempty"`
	// JoinWaits counts the joins that found the worker still busy and the
	// simulation thread waited for it (its time is the "join" part). An
	// unpaced match (PaceTPS 0) reaches a deadline far sooner than real time,
	// so only a paced match measures what a player would wait.
	JoinWaits int `json:"join_waits"`
	// PrepUS is the preparation's time on its worker (the map analysis and
	// the brain's Init), which must finish before the first batch is due.
	PrepUS float64 `json:"prep_us,omitempty"`
}

// ArenaPartCost is one part of the host step (aikit.StepPart): the steps
// that ran it and its time per occurrence.
type ArenaPartCost struct {
	Part   string  `json:"part"`
	Count  int     `json:"n"`
	MeanUS float64 `json:"mean_us"`
	P99US  float64 `json:"p99_us"`
	MaxUS  float64 `json:"max_us"`
}

// ArenaHostTiming is the match's host-side cost: whole ticks, the AI's share
// of each tick and the collector. Every field is a measurement of the host,
// never of the game, so two runs of one game differ here and nowhere else.
type ArenaHostTiming struct {
	TickMaxUS float64 `json:"tick_max_us"`
	// AITick* summarize, per tick, the sum of every player's host step: the
	// simulation-thread time the computer players take in that tick.
	AITickMeanUS float64 `json:"ai_tick_mean_us"`
	AITickP99US  float64 `json:"ai_tick_p99_us"`
	AITickMaxUS  float64 `json:"ai_tick_max_us"`
	// AITicksOver1ms and AITicksOver4ms count the ticks whose AI sum exceeded
	// 1 ms and 4 ms.
	AITicksOver1ms int     `json:"ai_ticks_over_1ms"`
	AITicksOver4ms int     `json:"ai_ticks_over_4ms"`
	GC             ArenaGC `json:"gc"`
	// ProcessCPUSeconds is the match process's user and system CPU time,
	// content loading included, when the host fills it in (ai-arena does):
	// what the game would take on a core of its own.
	ProcessCPUSeconds float64 `json:"process_cpu_seconds,omitempty"`
	// Ticks and AITicks are the per-tick series (µs×10) the figures above
	// summarize, for host tools that compare distributions (a per-tick
	// minimum over repeated runs of one deterministic match removes most of
	// what a loaded host adds). Not written to the result file.
	Ticks   []int32 `json:"-"`
	AITicks []int32 `json:"-"`
	// PartTicks is AITicks split by the part of the step (aikit.StepPart),
	// summed over the players; empty for steps without a part probe.
	PartTicks [aikit.NumStepParts][]int32 `json:"-"`
}

// ArenaGC is the process's collector activity over the match: deltas between
// two reads taken as the match starts and ends. The counters are
// process-wide, so they describe one match only with one match per process.
type ArenaGC struct {
	Cycles       uint32  `json:"cycles"`
	PauseTotalUS float64 `json:"pause_total_us"`
	PauseMaxUS   float64 `json:"pause_max_us"`
	AllocMB      float64 `json:"alloc_mb"`
	AllocObjects uint64  `json:"alloc_objects"`
	CPUSeconds   float64 `json:"cpu_seconds"`
}

// ArenaPlayerResult is one contestant's outcome.
type ArenaPlayerResult struct {
	Slot    int    `json:"slot"`
	Label   string `json:"label"`
	Brain   string `json:"brain"`
	Persona string `json:"persona"`
	Side    int    `json:"side"`
	// Start is the stored number of the start position the slot took (0 is
	// the map's StartPos1), or -1 when the commander stood on none.
	Start    int    `json:"start"`
	Alive    bool   `json:"alive"`
	DiedTick uint32 `json:"died_tick,omitempty"`
	// Score is the score the game was adjudicated on (ArenaResult.ScoreKind);
	// ScoreDefault and ScoreInvested are both variants. All three are -1 for
	// a player that did not survive.
	Score         int64            `json:"score"`
	ScoreDefault  int64            `json:"score_default"`
	ScoreInvested int64            `json:"score_invested"`
	Kills         int32            `json:"kills"`
	Losses        int32            `json:"losses"`
	ValueKilled   int64            `json:"value_killed"`
	ValueLost     int64            `json:"value_lost"`
	Commands      aikit.ApplyStats `json:"commands"`
	Cost          ArenaCost        `json:"cost"`
	Series        []ArenaSample    `json:"series"`
	// FirstAttack is the tick this player first destroyed a finished unit
	// of another player (credited as in ValueKilled, to the side that
	// damaged it last): the human benchmark's "first kill" milestone, for
	// every brain alike. Absent when it never did.
	FirstAttack uint32             `json:"first_attack_tick,omitempty"`
	Built       map[string]int     `json:"built,omitempty"`
	RoleBuilt   map[string]int     `json:"role_built,omitempty"`
	Extra       map[string]float64 `json:"extra,omitempty"`
	// LostByClass is the finished value lost by unit class (the replay
	// trace's role classes).
	LostByClass map[string]int64 `json:"lost_by_class,omitempty"`
	// TrappedMax and TrappedMean summarize ArenaSample.Trapped.
	TrappedMax  int32   `json:"trapped_max"`
	TrappedMean float64 `json:"trapped_mean"`
	// HeldMean and HeldMovingMean are the means of ArenaSample.Held and
	// HeldMoving over the series, HeldMovingMax the greatest HeldMoving,
	// and GroundMean the mean of the ground units they were taken over.
	HeldMean       float64 `json:"held_mean"`
	HeldMovingMean float64 `json:"held_moving_mean"`
	HeldMovingMax  int32   `json:"held_moving_max"`
	GroundMean     float64 `json:"ground_mean"`
	// HeldOpenMean, HeldOpenMax and HeldMovingOpenMean summarize
	// ArenaSample.HeldOpen and HeldMovingOpen.
	HeldOpenMean       float64 `json:"held_open_mean"`
	HeldOpenMax        int32   `json:"held_open_max"`
	HeldMovingOpenMean float64 `json:"held_moving_open_mean"`
	// CirclingMean and CirclingMax summarize ArenaSample.Circling.
	CirclingMean float64 `json:"circling_mean"`
	CirclingMax  int32   `json:"circling_max"`
	// HeldByOrder is the held units counted over the series by the name of
	// the order they stood on, and HeldRefused those of them whose last
	// step a friend had refused.
	HeldByOrder map[string]int `json:"held_by_order,omitempty"`
	HeldRefused int            `json:"held_refused"`
	// HeldLast lists the units held when the match ended, for looking into
	// a jam: where each stands and wants to go, on what order, for how
	// long, and what refused its last step.
	HeldLast []ArenaHeld `json:"held_last,omitempty"`
}

// ArenaHeld is one held unit at the end of a match.
type ArenaHeld struct {
	Unit    string `json:"unit"`
	Order   string `json:"order"`
	X       int32  `json:"x"`
	Z       int32  `json:"z"`
	GoalX   int32  `json:"goal_x"`
	GoalZ   int32  `json:"goal_z"`
	Ticks   uint32 `json:"ticks"`
	Routed  bool   `json:"routed"`
	Refused bool   `json:"refused"`
	// By is what refused it: "ground", "structure", "parked", "mover",
	// "other player" or "".
	By string `json:"by,omitempty"`
}

// ArenaJam is the ground units of a match that has ended, and the ground as
// one of them reads it: a picture to look into a jam with.
type ArenaJam struct {
	Tick  uint32         `json:"tick"`
	CellW int32          `json:"cell_w"`
	CellH int32          `json:"cell_h"`
	Units []ArenaJamUnit `json:"units"`
	// Of names the unit whose reading of the ground Search and Static are,
	// over the cell rectangle X0, Z0 to X1, Z1: a row of digits for each row
	// of anchors, 0 for one the unit may not stand on. Static is the reading
	// with every mobile unit absent.
	Of     string   `json:"of,omitempty"`
	X0     int32    `json:"x0"`
	Z0     int32    `json:"z0"`
	X1     int32    `json:"x1"`
	Z1     int32    `json:"z1"`
	Search []string `json:"search,omitempty"`
	Static []string `json:"static,omitempty"`
	// Cells is movement.LabCells over the same rectangle: what stands on
	// each cell, a row of letters for each row of cells.
	Cells []string `json:"cells,omitempty"`
}

// ArenaJamUnit is one ground unit of an ArenaJam.
type ArenaJamUnit struct {
	ID     int    `json:"id"`
	Owner  int    `json:"owner"`
	Unit   string `json:"unit"`
	X      int32  `json:"x"`
	Z      int32  `json:"z"`
	FootX  int32  `json:"foot_x"`
	FootZ  int32  `json:"foot_z"`
	Order  string `json:"order,omitempty"`
	GoalX  int32  `json:"goal_x,omitempty"`
	GoalZ  int32  `json:"goal_z,omitempty"`
	Routed bool   `json:"routed,omitempty"`
	// Created is the tick the order was made, Phase its phase, Queued how
	// many orders wait behind it and Target whether it names a unit.
	Created uint32 `json:"created,omitempty"`
	Phase   int32  `json:"phase,omitempty"`
	Queued  int    `json:"queued,omitempty"`
	Target  bool   `json:"target,omitempty"`
	// Refused and By are the last step's verdict, as in ArenaHeld; Blocker
	// is the unit that refused it, when one did.
	Refused bool   `json:"refused,omitempty"`
	By      string `json:"by,omitempty"`
	Blocker int    `json:"blocker,omitempty"`
	Speed   int32  `json:"speed,omitempty"`
	// Stood is how many ticks ago the unit last changed its cells, and Held
	// for how many it has been held (zero for a unit that is not).
	Stood uint32 `json:"stood,omitempty"`
	Held  uint32 `json:"held,omitempty"`
	// Walked is the way a held unit has gone since its hold began, in world
	// units, and Open whether the ground connects where it stands with its
	// goal (the census's test, which no search's reading enters).
	Walked int32 `json:"walked,omitempty"`
	Open   bool  `json:"open,omitempty"`
	// Heading is the unit's heading, Route what is left of its route, in
	// world units, and Ring movement.LabRing's reading of the anchors round
	// it.
	Heading uint16     `json:"heading"`
	Route   [][2]int32 `json:"route,omitempty"`
	Ring    string     `json:"ring,omitempty"`
	// Views is what a route search answers for a held unit, by
	// movement.LabRouteViews.
	Views []movement.LabRouteView `json:"views,omitempty"`
}

// ArenaCircling is one unit a sample counted as circling.
type ArenaCircling struct {
	Tick   uint32 `json:"tick"`
	ID     int    `json:"id"`
	Unit   string `json:"unit"`
	X      int32  `json:"x"`
	Z      int32  `json:"z"`
	Held   uint32 `json:"held"`
	Walked int32  `json:"walked"`
}

// ArenaWatch is one tick of one watched unit: where it is, in sixteenths of
// a world unit, its heading and speed, whether its step was refused and by
// what unit, and the point of its route it is making for.
type ArenaWatch struct {
	Tick    uint32 `json:"t"`
	ID      int    `json:"id"`
	X       int32  `json:"x"`
	Z       int32  `json:"z"`
	Heading uint16 `json:"h"`
	Speed   int32  `json:"s"`
	Refused bool   `json:"r,omitempty"`
	Blocker int    `json:"b,omitempty"`
	Points  int    `json:"n,omitempty"`
	NextX   int32  `json:"nx,omitempty"`
	NextZ   int32  `json:"nz,omitempty"`
	Order   string `json:"o,omitempty"`
	// Side, Steering and Ahead are movement.LabSteer's.
	Side     int8  `json:"sd,omitempty"`
	Steering bool  `json:"st,omitempty"`
	Ahead    uint8 `json:"ah,omitempty"`
}

// ArenaResult is the outcome of one match.
type ArenaResult struct {
	// Jam is the picture ArenaRequest.Jam asks for.
	Jam *ArenaJam `json:"jam,omitempty"`
	// Watch is what ArenaRequest.Watch asks for.
	Watch []ArenaWatch `json:"watch,omitempty"`
	// Circling lists the first units the samples counted as circling.
	Circling []ArenaCircling `json:"circling,omitempty"`
	Map      string          `json:"map"`
	Seed     uint32          `json:"seed"` // the requested seed
	// BattleSeed is the seed both battle streams started from: Seed, or
	// ArenaMapSeed(Seed, Map) when the request asked for map mixing.
	BattleSeed uint32 `json:"battle_seed"`
	// CRTSeed is the CRT stream's seed: the battle seed, except under
	// StartsSwap. Starts is the requested start assignment.
	CRTSeed     uint32              `json:"crt_seed"`
	Starts      ArenaStarts         `json:"starts"`
	ScoreKind   ArenaScore          `json:"score_kind"`
	Gameplay    string              `json:"gameplay"`
	Level       ArenaLevel          `json:"level,omitempty"`
	RuleSet     string              `json:"rule_set,omitempty"`     // the registered set the request named
	Mutators    string              `json:"mutators,omitempty"`     // the canonical mutator set the battle bound
	Content     string              `json:"content,omitempty"`      // the content section's id when not the base game's
	ValuesFixed int                 `json:"values_fixed,omitempty"` // units priced by ArenaRequest.Values
	Ticks       uint32              `json:"ticks"`
	Winner      int                 `json:"winner"` // player index in Players, -1 draw
	Reason      string              `json:"reason"` // decisive, points, timeout or adjudicated
	WallSeconds float64             `json:"wall_seconds"`
	TickMeanUS  float64             `json:"tick_mean_us"`
	TickP99US   float64             `json:"tick_p99_us"`
	Host        ArenaHostTiming     `json:"host_timing"`
	Players     []ArenaPlayerResult `json:"players"`
	Trace       *ArenaTrace         `json:"-"`
	// Adjudication is the early-end rule the match played under, if any.
	Adjudication *ArenaAdjudication `json:"adjudication,omitempty"`
}

// ArenaTrace is replay data for the viewer.
type ArenaTrace struct {
	Map      string          `json:"map"`
	WorldW   int32           `json:"world_w"`
	WorldH   int32           `json:"world_h"`
	SectorW  int32           `json:"sector_w"`
	SectorH  int32           `json:"sector_h"`
	Labels   []string        `json:"labels"`
	Frames   []ArenaFrame    `json:"frames"`
	Explains []ArenaExplain  `json:"explains"`
	Spots    [][3]int32      `json:"spots"`
	Starts   [][2]int32      `json:"starts"`
	Events   []ArenaEvent    `json:"events"`
	Series   [][]ArenaSample `json:"series"`
	// Log is every unit finished and every finished unit lost, in tick
	// order, for the play review (internal/aikit/brains/utility/README.md §15).
	Log []ArenaLogEntry `json:"log"`
}

// ArenaLogEntry is one unit finished ("done") or one finished unit lost
// ("lost"). By is the side that damaged a lost unit last, or -1.
type ArenaLogEntry struct {
	Tick   uint32 `json:"t"`
	Player int    `json:"p"`
	Kind   string `json:"k"`
	Unit   string `json:"u"`
	// Role is the unit's class (logClass) and Value its metal-equivalent
	// cost, as the AI's table has them.
	Role  string `json:"r"`
	Value int32  `json:"v"`
	X     int32  `json:"x"`
	Z     int32  `json:"z"`
	By    int    `json:"by"`
}

// ArenaFrame packs every live unit as [player, role, x, z, hp%, built].
type ArenaFrame struct {
	Tick  uint32     `json:"t"`
	Units [][6]int32 `json:"u"`
}

// ArenaExplain is one brain self-description.
type ArenaExplain struct {
	Tick   uint32        `json:"t"`
	Player int           `json:"p"`
	X      aikit.Explain `json:"x"`
}

// ArenaEvent is a notable moment (death of a commander, first attack).
type ArenaEvent struct {
	Tick   uint32 `json:"t"`
	Player int    `json:"p"`
	Kind   string `json:"k"`
	X      int32  `json:"x"`
	Z      int32  `json:"z"`
}

// Role codes for trace frames.
const (
	traceCommander = iota
	traceBuilder
	traceFactory
	traceEco
	traceDefense
	traceGround
	traceAir
	traceNaval
	traceOther
)

// traceRoleNames names the trace role classes in constant order.
var traceRoleNames = [traceOther + 1]string{"commander", "builder", "factory", "eco", "defense", "ground", "air", "naval", "other"}

func traceRole(info *aikit.UnitInfo) int32 {
	switch {
	case info == nil:
		return traceOther
	case info.Role.Has(aikit.RoleCommander):
		return traceCommander
	case info.Role.Has(aikit.RoleBuilder):
		return traceBuilder
	case info.Role.Has(aikit.RoleFactory):
		return traceFactory
	case info.Role.Any(aikit.RoleExtractor | aikit.RoleEnergy | aikit.RoleMetalMaker | aikit.RoleStorage):
		return traceEco
	case info.Role.Has(aikit.RoleDefense):
		return traceDefense
	case info.Role.Has(aikit.RoleAir):
		return traceAir
	case info.Role.Has(aikit.RoleNaval):
		return traceNaval
	case info.Role.Has(aikit.RoleCombat):
		return traceGround
	}
	return traceOther
}

// arenaProbe times host steps and thinks. It is host-side and never
// reached by the simulation's own decisions [I6]. One probe serves one
// match, and every field is indexed by player: an asynchronous persona calls
// ThinkBegin/ThinkEnd on its own worker goroutine, and the preparation calls
// PrepBegin/PrepEnd on it, so two players never share a buffer (the metric
// read buffers included) and a worker's fields are never the simulation
// thread's.
type arenaProbe struct {
	stepStart  [10]time.Time
	thinkStart [10]time.Time
	steps      [10][]int32 // µs×10
	thinks     [10][]int32
	allocB     [10]uint64
	allocO     [10]uint64
	allocStart [10][2]uint64
	samples    [10][2]metrics.Sample
	measure    bool

	// Simulation thread: step allocations, parts, and the tick's AI sum.
	stepAllocB     [10]uint64
	stepAllocO     [10]uint64
	stepAllocStart [10][2]uint64
	stepSamples    [10][2]metrics.Sample
	partMark       [10]time.Time
	parts          [10][aikit.NumStepParts][]int32 // µs×10 per occurrence
	stepPart       [10][aikit.NumStepParts]int32   // this step's parts (µs×10), until StepEnd
	stepRan        [10][aikit.NumStepParts]bool
	tickAI         int64                       // ns, this tick's steps
	tickParts      [aikit.NumStepParts]int64   // ns, this tick's parts
	partTicks      [aikit.NumStepParts][]int32 // µs×10 per tick, every player's part
	// Worker: the preparation.
	prepStart [10]time.Time
	prepNS    [10]int64
}

func newArenaProbe(measure bool) *arenaProbe {
	p := &arenaProbe{measure: measure}
	for i := range p.samples {
		p.samples[i] = [2]metrics.Sample{{Name: "/gc/heap/allocs:bytes"}, {Name: "/gc/heap/allocs:objects"}}
		p.stepSamples[i] = [2]metrics.Sample{{Name: "/gc/heap/allocs:bytes"}, {Name: "/gc/heap/allocs:objects"}}
	}
	return p
}

// allocs reads the process-wide allocation counters into buf.
// The counters are process-wide, so a measurement is only meaningful with a
// synchronous persona and one match per process.
func allocs(buf *[2]metrics.Sample) (uint64, uint64) {
	s := buf[:]
	metrics.Read(s)
	return s[0].Value.Uint64(), s[1].Value.Uint64()
}

func (p *arenaProbe) StepBegin(player uint8, tick uint32) {
	if p.measure {
		p.stepAllocStart[player][0], p.stepAllocStart[player][1] = allocs(&p.stepSamples[player])
	}
	now := time.Now()
	p.stepStart[player] = now
	p.partMark[player] = now
}
func (p *arenaProbe) StepEnd(player uint8, tick uint32) {
	d := time.Since(p.stepStart[player]).Nanoseconds()
	// The step's allocations are read before the probe's own bookkeeping
	// (the series appends) allocates.
	if p.measure {
		b, o := allocs(&p.stepSamples[player])
		p.stepAllocB[player] += b - p.stepAllocStart[player][0]
		p.stepAllocO[player] += o - p.stepAllocStart[player][1]
	}
	p.steps[player] = append(p.steps[player], int32(d/100))
	p.tickAI += d
	for part, ran := range p.stepRan[player] {
		if ran {
			p.parts[player][part] = append(p.parts[player][part], p.stepPart[player][part])
			p.stepRan[player][part] = false
		}
	}
}
func (p *arenaProbe) ThinkBegin(player uint8, tick uint32) {
	if p.measure {
		p.allocStart[player][0], p.allocStart[player][1] = allocs(&p.samples[player])
	}
	p.thinkStart[player] = time.Now()
}
func (p *arenaProbe) ThinkEnd(player uint8, tick uint32) {
	p.thinks[player] = append(p.thinks[player], int32(time.Since(p.thinkStart[player]).Nanoseconds()/100))
	if p.measure {
		b, o := allocs(&p.samples[player])
		p.allocB[player] += b - p.allocStart[player][0]
		p.allocO[player] += o - p.allocStart[player][1]
	}
}

// Part implements aikit.PartProbe.
func (p *arenaProbe) Part(player uint8, tick uint32, part aikit.StepPart) {
	now := time.Now()
	d := now.Sub(p.partMark[player]).Nanoseconds()
	// Kept until StepEnd, which appends it to the series outside the
	// step's allocation window.
	p.stepPart[player][part], p.stepRan[player][part] = int32(d/100), true
	p.tickParts[part] += d
	p.partMark[player] = now
}

// PrepBegin implements aikit.PartProbe.
func (p *arenaProbe) PrepBegin(player uint8, tick uint32) { p.prepStart[player] = time.Now() }

// PrepEnd implements aikit.PartProbe.
func (p *arenaProbe) PrepEnd(player uint8, tick uint32) {
	p.prepNS[player] = time.Since(p.prepStart[player]).Nanoseconds()
}

// endTick closes the tick's AI sum and returns it (µs×10).
func (p *arenaProbe) endTick() int32 {
	v := int32(p.tickAI / 100)
	p.tickAI = 0
	for i := range p.tickParts {
		p.partTicks[i] = append(p.partTicks[i], int32(p.tickParts[i]/100))
		p.tickParts[i] = 0
	}
	return v
}

var stepPartNames = [aikit.NumStepParts]string{"begin", "join", "apply", "observe", "think", "upkeep"}

// gcRead is one read of the collector's counters.
type gcRead struct {
	ms  runtime.MemStats
	cpu [1]metrics.Sample
}

func (g *gcRead) read() {
	runtime.ReadMemStats(&g.ms)
	g.cpu[0].Name = "/cpu/classes/gc/total:cpu-seconds"
	metrics.Read(g.cpu[:])
}

// gcDelta is the collector's activity between two reads.
func gcDelta(a, b *gcRead) ArenaGC {
	out := ArenaGC{
		Cycles:       b.ms.NumGC - a.ms.NumGC,
		PauseTotalUS: float64(b.ms.PauseTotalNs-a.ms.PauseTotalNs) / 1000,
		AllocMB:      float64(b.ms.TotalAlloc-a.ms.TotalAlloc) / (1 << 20),
		AllocObjects: b.ms.Mallocs - a.ms.Mallocs,
	}
	if a.cpu[0].Value.Kind() == metrics.KindFloat64 && b.cpu[0].Value.Kind() == metrics.KindFloat64 {
		out.CPUSeconds = b.cpu[0].Value.Float64() - a.cpu[0].Value.Float64()
	}
	// The pause ring holds the last 256 cycles.
	n := min(out.Cycles, uint32(len(b.ms.PauseNs)))
	for i := uint32(0); i < n; i++ {
		v := float64(b.ms.PauseNs[(b.ms.NumGC-1-i)%uint32(len(b.ms.PauseNs))]) / 1000
		out.PauseMaxUS = max(out.PauseMaxUS, v)
	}
	return out
}

func percentiles(v []int32) (mean, p99, max float64) {
	if len(v) == 0 {
		return 0, 0, 0
	}
	s := append([]int32(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum int64
	for _, x := range s {
		sum += int64(x)
	}
	mean = float64(sum) / float64(len(s)) / 10
	p99 = float64(s[(len(s)-1)*99/100]) / 10
	max = float64(s[len(s)-1]) / 10
	return
}

// arenaConfig lays out an AI-only skirmish: every contestant is a computer
// player in its own ally group. The lobby's "at least one human" rule is a
// front-end rule; the arena records the first contestant as the local slot.
func arenaConfig(req ArenaRequest) session.SkirmishConfig {
	cfg := session.SkirmishConfig{MapName: req.Map, NumPlayers: len(req.Players)}
	for i := range cfg.Players {
		cfg.Players[i] = session.SkirmishPlayer{}
	}
	metal, energy := req.StartMetal, req.StartEnergy
	if metal == 0 {
		metal = session.SkirmishDefaultMetal
	}
	if energy == 0 {
		energy = session.SkirmishDefaultEnergy
	}
	for i, p := range req.Players {
		cfg.Players[i].Controller = session.SkirmishControllerComputer
		cfg.Players[i].AllyGroup = i
		cfg.Players[i].Color = i
		cfg.Players[i].Side = p.Side
		cfg.Players[i].Metal = metal
		cfg.Players[i].Energy = energy
	}
	cfg.Location = 1
	cfg.ApplyDefaults()
	for i, p := range req.Players {
		cfg.Players[i].Side = p.Side
	}
	return cfg
}

// ArenaMapSeed mixes a map name into a tournament seed, so one seed list
// samples different random draws on every map. The map name is trimmed and
// lower-cased (so "Great Divide" and "great divide" agree), hashed with 32-bit
// FNV-1a, and placed in the low word under the seed in the high word; the
// 64-bit value is finished with the SplitMix64 output function and its low 32
// bits are the battle seed. The derivation is part of the evaluation
// protocol (docs/MODERN_AI_RESEARCH.md §5): changing it changes every
// recorded game.
func ArenaMapSeed(seed uint32, mapName string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(mapName))))
	z := uint64(seed)<<32 | uint64(h.Sum32())
	z += 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return uint32(z ^ (z >> 31))
}

// battleSeed is the seed the request's battle streams start from.
func (req ArenaRequest) battleSeed() uint32 {
	if req.MapSeed {
		return ArenaMapSeed(req.Seed, req.Map)
	}
	return req.Seed
}

// RunArenaMatch mounts the install, composes the match and plays it out.
func RunArenaMatch(req ArenaRequest) (ArenaResult, error) {
	fs, err := mountContentRootsWithAddons(req.Root, req.Roots, req.Addons)
	if err != nil {
		return ArenaResult{}, err
	}
	defer fs.Close()
	// The arena plays the request's content section, the base game's by
	// default, and no Community source.
	view, profile := contentView(fs, req.Content)
	catalog, err := content.CompileWithOptions(view, content.Options{Limits: content.LimitsFromProfile(profile.Limits)})
	if err != nil {
		return ArenaResult{}, diagnostic("catalog compile failed: "+err.Error(), req.Map, fs.ProviderIDs(), "a complete compiled catalog")
	}
	if req.TickLimit == 0 {
		req.TickLimit = 30 * 60 * 25
	}
	if req.SampleEvery == 0 {
		req.SampleEvery = 150
	}
	difficulty, err := req.Level.word()
	if err != nil {
		return ArenaResult{}, err
	}
	cfg := arenaConfig(req)
	seed := req.battleSeed()
	crtSeed := seed
	switch req.Starts {
	case "", StartsSlot:
		req.Starts = StartsSlot
	case StartsRandom:
		cfg.Location = 0
	case StartsSwap:
		if len(req.Players) != 2 {
			return ArenaResult{}, fmt.Errorf("arena: starts %q needs exactly two players, have %d", req.Starts, len(req.Players))
		}
		cfg.Location = 0
		if crtSeed, err = startSwapSeed(seed); err != nil {
			return ArenaResult{}, err
		}
	default:
		return ArenaResult{}, fmt.Errorf("arena: unknown starts %q (have slot, random, swap)", req.Starts)
	}
	switch req.Score {
	case "":
		req.Score = ScoreDefault
	case ScoreDefault, ScoreInvested:
	default:
		return ArenaResult{}, fmt.Errorf("arena: unknown score %q (have default, invested)", req.Score)
	}
	composed, err := ComposeFreshBattle(FreshBattleRequest{
		Gameplay: req.Gameplay,
		Kind:     ScenarioDirectOTA, Map: req.Map, LocalOwner: -1,
		Difficulty: difficulty, Skirmish: cfg,
		SimulationSeed: seed, CRTSeed: crtSeed,
		FS: view, Catalog: catalog, AutomatedPlayers: true, Mutators: req.Mutators,
	})
	if err != nil {
		return ArenaResult{}, err
	}
	if !req.Publish {
		// With no buffer the session skips publication (and the arena its
		// event drain); the simulation never reads a published frame [I6].
		composed.Session.Snapshot = nil
	}
	return playArena(req, composed.Session)
}

type arenaTracker struct {
	lostClass   [10][traceOther + 1]int64 // value lost by trace role
	valueLost   [10]int64
	valueKilled [10]int64
	commander   [10]pool.Handle
	diedTick    [10]uint32
	built       [10]map[string]int
	table       *aikit.Table
	events      []ArenaEvent
	firstAttack [10]uint32
	anchors     map[pool.Handle]trapAnchor
	holds       map[pool.Handle]trapAnchor
	heldOrders  [10][]int // by player and order id
	heldRefused [10]int
	// refused reports a unit whose last ground step was refused.
	refused func(u *units.Unit) bool
	// circling is the first circling units the samples counted.
	circling []ArenaCircling
	// follow is the ground guard's order.
	follow orders.ID
	// reach is the ground labelled for the kinds and owners of the units
	// asked about at tick reachTick, and reachKeys their keys.
	reach     []*movement.LabReach
	reachKeys []string
	reachTick uint32
	// logOn keeps the trace's build and loss log; done holds the units
	// already logged as finished.
	logOn bool
	log   []ArenaLogEntry
	done  map[pool.Handle]bool
}

// trapAnchor is where a ground combat unit was first seen, and whether it
// has ever left trapRadius of that point.
type trapAnchor struct {
	x, z int32
	tick uint32
	left bool
	// order, target and goalX, goalZ name what the unit was doing when a
	// hold's clock started: another order, or the same for another place,
	// starts it again. walked is the way the unit has gone since, in world
	// units, and lastX, lastZ where it was a tick ago, in sixteenths.
	order        orders.ID
	target       uint32
	goalX, goalZ int32
	walked       int32
	walked16     int32
	lastX, lastZ int32
	seen         bool
}

const (
	trapRadius = 320
	trapAge    = 3600 // two minutes
	holdRadius = 48
	holdAge    = 300 // ten seconds
	// holdGoal is how far, in world units, the goal of an order of one kind
	// may move and the order still be the one a hold's clock started on.
	holdGoal = 128
	// circleWalk is the way a held unit has gone, in world units, for it to
	// count as circling: it is on the move and goes nowhere.
	circleWalk = 160
)

func playArena(req ArenaRequest, sess *session.Session) (ArenaResult, error) {
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	n := len(req.Players)
	probe := newArenaProbe(req.MeasureAllocs)
	var drained []frame.EventView
	table := aikit.BuildTable(sess.Catalog, sess.Rules.Construction)
	// The arena's table is its own, so repricing it changes the scores and
	// nothing a brain decides.
	fixed := 0
	for key, v := range req.Values {
		if info := table.Lookup(key); info != nil {
			info.Value = v
			fixed++
		}
	}
	tr := &arenaTracker{table: table, anchors: map[pool.Handle]trapAnchor{}, holds: map[pool.Handle]trapAnchor{}, follow: orders.Lookup("Follow_Ground")}
	if req.TraceEvery > 0 {
		tr.logOn, tr.done = true, map[pool.Handle]bool{}
	}
	tr.refused = func(u *units.Unit) bool {
		mv := sess.Movement
		return mv != nil && int(u.Handle) < len(mv.Collisions) && mv.Collisions[u.Handle] != nil && mv.Collisions[u.Handle].Blocked
	}
	hosts := make([]*aikit.Host, n)
	for i, p := range req.Players {
		m := sess.AI[i]
		if m == nil {
			return ArenaResult{}, fmt.Errorf("arena: slot %d has no computer manager", i)
		}
		tr.built[i] = map[string]int{}
		if p.Brain == nil {
			m.Ext = &aikit.RetailTimer{Probe: probe}
			continue
		}
		h := aikit.NewHost(m, p.Brain, p.Persona)
		h.Probe = probe
		m.Ext = h
		hosts[i] = h
	}
	// Commanders at entry, and value accounting on death. A commander is
	// the table's: a side record's commander counts though its definition
	// lacks the flag, since the side record is what names a player's
	// commander [08 R-TRIG-01 §3].
	isCommander := func(u *units.Unit) bool {
		if u.Def.Commander {
			return true
		}
		info := table.Of(u.Def)
		return info != nil && info.Role.Has(aikit.RoleCommander)
	}
	starts := make([]int, n)
	for i := range starts {
		starts[i] = -1
	}
	for _, u := range sess.Units.IterSliced() {
		if u.Def != nil && int(u.Owner) < n && isCommander(u) {
			tr.commander[u.Owner] = u.Handle
			starts[u.Owner] = arenaStartOf(sess, u)
		}
	}
	if req.Starts == StartsSwap && (starts[0] != 1 || starts[1] != 0) {
		return ArenaResult{}, fmt.Errorf("arena: starts %q placed slots at starts %v, want [1 0]; the session's randomized assignment no longer follows [08 \"Randomization for skirmish starts\"]", req.Starts, starts)
	}
	prevDeath := sess.Units.DeathHook()
	sess.Units.SetDeathHook(func(handle pool.Handle, cause units.DeathCause, u *units.Unit) {
		if u != nil && int(u.Owner) < n && u.Def != nil {
			info := table.Of(u.Def)
			if tr.logOn && u.Remaining == 0 && sess.Clock != nil {
				by := int(u.LastDamageSide)
				if by >= n || by == int(u.Owner) {
					by = -1
				}
				tr.log = append(tr.log, logEntry(table, u, sess.Clock.GlobalTick, "lost", by))
			}
			if tr.logOn {
				delete(tr.done, handle)
			}
			if info != nil && u.Remaining == 0 {
				tr.valueLost[u.Owner] += int64(info.Value)
				tr.lostClass[u.Owner][traceRole(info)] += int64(info.Value)
				killer := u.LastDamageSide
				if int(killer) < n && killer != u.Owner {
					tr.valueKilled[killer] += int64(info.Value)
					if tr.firstAttack[killer] == 0 && sess.Clock != nil {
						tr.firstAttack[killer] = sess.Clock.GlobalTick
					}
				}
			}
			if isCommander(u) && tr.diedTick[u.Owner] == 0 && sess.Clock != nil {
				tr.diedTick[u.Owner] = sess.Clock.GlobalTick
				tr.events = append(tr.events, ArenaEvent{Tick: sess.Clock.GlobalTick, Player: int(u.Owner), Kind: "commander_death", X: int32(int64(u.X) >> 16), Z: int32(int64(u.Z) >> 16)})
			}
		}
		if prevDeath != nil {
			prevDeath(handle, cause, u)
		}
	})
	prevCreate := sess.Units.CreateHook()
	sess.Units.SetCreateHook(func(handle pool.Handle, u *units.Unit) {
		if u != nil && int(u.Owner) < n && u.Def != nil {
			tr.built[u.Owner][u.Def.UnitName]++
		}
		if prevCreate != nil {
			prevCreate(handle, u)
		}
	})

	result := ArenaResult{Map: req.Map, Seed: req.Seed, BattleSeed: sess.RNGSimSeed, CRTSeed: sess.RNGCrtSeed, Starts: req.Starts, ScoreKind: req.Score, Gameplay: string(sess.Gameplay), Level: req.Level, RuleSet: string(req.Gameplay), Mutators: sess.Mutators.String(), Content: req.Content.Name, ValuesFixed: fixed, Winner: -1}
	var adj *adjudicator
	var adjScores []int64
	var adjAlive []bool
	if req.Adjudicate.RatioPct > 0 {
		rule := req.Adjudicate
		result.Adjudication = &rule
		adj = &adjudicator{rule: rule, leader: -1}
		adjScores, adjAlive = make([]int64, n), make([]bool, n)
	}
	series := make([][]ArenaSample, n)
	var trace *ArenaTrace
	if req.TraceEvery > 0 {
		trace = &ArenaTrace{Map: req.Map, WorldW: sess.World.CellW * 16, WorldH: sess.World.CellH * 16}
		for _, p := range req.Players {
			trace.Labels = append(trace.Labels, p.Label)
		}
	}
	var tickDur, aiTick []int32
	var gc0 gcRead
	gc0.read()
	started := time.Now()
	var walk []*units.Unit
	var watch []ArenaWatch
	classesByID := orderClasses()
	var paceNext time.Time
	if req.PaceTPS > 0 {
		paceNext = time.Now()
	}
	for sess.State != session.StatePostBattle && sess.Clock.GlobalTick < req.TickLimit {
		if req.PaceTPS > 0 {
			paceNext = paceNext.Add(time.Second / time.Duration(req.PaceTPS))
			if d := time.Until(paceNext); d > 0 {
				time.Sleep(d)
			}
		}
		t0 := time.Now()
		drained = arenaStep(sess, drained)
		tickDur = append(tickDur, int32(time.Since(t0).Nanoseconds()/100))
		aiTick = append(aiTick, probe.endTick())
		tick := sess.Clock.GlobalTick
		walk = sess.Units.AppendLiveSliced(walk[:0])
		tr.walked(walk)
		if tr.logOn {
			for _, u := range walk {
				if u.Def == nil || int(u.Owner) >= n || u.Remaining != 0 || tr.done[u.Handle] {
					continue
				}
				tr.done[u.Handle] = true
				if tick > 1 {
					tr.log = append(tr.log, logEntry(table, u, tick, "done", -1))
				}
			}
		}
		if len(req.Watch) > 0 && tick >= req.WatchFrom && sess.Movement != nil {
			mv := sess.Movement
			for _, id := range req.Watch {
				u := sess.Units.Unit(pool.Handle(id))
				if u == nil || !u.Alive || id >= len(mv.Collisions) || mv.Collisions[id] == nil {
					continue
				}
				c := mv.Collisions[id]
				row := ArenaWatch{Tick: tick, ID: id, X: int32(int64(u.X) >> 12), Z: int32(int64(u.Z) >> 12), Heading: c.Heading, Speed: c.Speed, Refused: c.Blocked}
				if c.Blocked {
					row.Blocker = c.BlockerID
				}
				if id < len(mv.Routes) && mv.Routes[id] != nil && mv.Routes[id].Active {
					r := mv.Routes[id]
					row.Points = int(r.Count)
					if r.Count > 1 {
						row.NextX, row.NextZ = r.Points[1].X, r.Points[1].Z
					}
				}
				if q := orders.QueueOfUnit(u); q != nil && q.Head() != nil {
					row.Order = orders.DescriptorFor(q.Head().ID).Name
				}
				row.Side, row.Steering, row.Ahead = mv.LabSteer(pool.Handle(id))
				watch = append(watch, row)
			}
		}
		if tick%req.SampleEvery == 0 {
			walk = sess.Units.AppendLiveSliced(walk[:0])
			for i := 0; i < n; i++ {
				series[i] = append(series[i], sampleArena(sess, walk, table, tr, i, tick, classesByID))
			}
		}
		if trace != nil && tick%req.TraceEvery == 0 {
			walk = sess.Units.AppendLiveSliced(walk[:0])
			trace.Frames = append(trace.Frames, traceFrame(walk, table, n, tick))
		}
		if trace != nil && req.ExplainEvery > 0 && tick%req.ExplainEvery == 0 {
			for i, h := range hosts {
				if h == nil {
					continue
				}
				if ex, ok := h.Brain().(aikit.Explainer); ok {
					h.Join()
					var x aikit.Explain
					ex.Explain(&x)
					trace.Explains = append(trace.Explains, ArenaExplain{Tick: tick, Player: i, X: x})
				}
			}
		}
		if tick%30 == 0 {
			alive := 0
			last := -1
			for i := 0; i < n; i++ {
				if arenaAlive(sess, tr, i) {
					alive++
					last = i
				}
			}
			if alive <= 1 {
				result.Winner = last
				result.Reason = "decisive"
				break
			}
		}
		if adj != nil && tick%req.SampleEvery == 0 {
			for i := 0; i < n; i++ {
				adjScores[i] = sampleScore(series[i][len(series[i])-1], req.Score)
				adjAlive[i] = arenaAlive(sess, tr, i)
			}
			if w := adj.observe(tick, adjScores, adjAlive); w >= 0 {
				result.Winner = w
				result.Reason = "adjudicated"
				break
			}
		}
	}
	for _, h := range hosts {
		if h != nil {
			h.Close()
		}
	}
	result.Ticks = sess.Clock.GlobalTick
	result.WallSeconds = time.Since(started).Seconds()
	var gc1 gcRead
	gc1.read()
	result.Host.GC = gcDelta(&gc0, &gc1)
	result.TickMeanUS, result.TickP99US, result.Host.TickMaxUS = percentiles(tickDur)
	result.Host.AITickMeanUS, result.Host.AITickP99US, result.Host.AITickMaxUS = percentiles(aiTick)
	result.Host.Ticks, result.Host.AITicks, result.Host.PartTicks = tickDur, aiTick, probe.partTicks
	for _, v := range aiTick {
		if v > 10000 {
			result.Host.AITicksOver1ms++
		}
		if v > 40000 {
			result.Host.AITicksOver4ms++
		}
	}
	if result.Reason == "" {
		result.Reason = "timeout"
	}
	var best, second int64 = -1, -1
	bestI := -1
	for i, p := range req.Players {
		pr := ArenaPlayerResult{Slot: i, Label: p.Label, Persona: p.Persona.Name, Side: p.Side, Start: starts[i], Series: series[i]}
		pr.Brain = "retail"
		if p.Brain != nil {
			pr.Brain = p.Brain.Name()
		}
		if p.Brain == nil {
			pr.Persona = "retail"
		}
		pr.Alive = arenaAlive(sess, tr, i)
		pr.DiedTick = tr.diedTick[i]
		pr.ValueKilled, pr.ValueLost = tr.valueKilled[i], tr.valueLost[i]
		if i < len(sess.Econ.Players) {
			pr.Kills = int32(sess.Econ.Players[i].Kills)
			pr.Losses = int32(sess.Econ.Players[i].Losses)
		}
		if len(series[i]) > 0 {
			s := series[i][len(series[i])-1]
			pr.ScoreDefault = int64(s.ArmyValue) + int64(s.EcoValue) + pr.ValueKilled - pr.ValueLost/2
			pr.ScoreInvested = pr.ScoreDefault + int64(s.BuilderValue) + int64(s.FrameValue)
		}
		if !pr.Alive {
			pr.ScoreDefault, pr.ScoreInvested = -1, -1
		}
		pr.Score = pr.ScoreDefault
		if req.Score == ScoreInvested {
			pr.Score = pr.ScoreInvested
		}
		if hosts[i] != nil {
			pr.Commands = hosts[i].Stats()
		}
		c := &pr.Cost
		c.Steps = len(probe.steps[i])
		c.StepMeanUS, c.StepP99US, c.StepMaxUS = percentiles(probe.steps[i])
		c.Thinks = len(probe.thinks[i])
		c.ThinkMeanUS, c.ThinkP99US, c.ThinkMaxUS = percentiles(probe.thinks[i])
		if c.Thinks > 0 && probe.measure {
			c.ThinkAllocB = float64(probe.allocB[i]) / float64(c.Thinks)
			c.ThinkAllocObjs = float64(probe.allocO[i]) / float64(c.Thinks)
		}
		if c.Steps > 0 && probe.measure {
			c.StepAllocB = float64(probe.stepAllocB[i]) / float64(c.Steps)
			c.StepAllocObjs = float64(probe.stepAllocO[i]) / float64(c.Steps)
		}
		for part, v := range probe.parts[i] {
			if len(v) == 0 {
				continue
			}
			pc := ArenaPartCost{Part: stepPartNames[part], Count: len(v)}
			pc.MeanUS, pc.P99US, pc.MaxUS = percentiles(v)
			c.Parts = append(c.Parts, pc)
		}
		c.JoinWaits = len(probe.parts[i][aikit.PartJoin])
		c.PrepUS = float64(probe.prepNS[i]) / 1000
		pr.Built = tr.built[i]
		var trappedSum, heldSum, heldMovingSum, groundSum, openSum, movingOpenSum, circlingSum int64
		for _, smp := range series[i] {
			trappedSum += int64(smp.Trapped)
			heldSum += int64(smp.Held)
			heldMovingSum += int64(smp.HeldMoving)
			groundSum += int64(smp.Ground)
			openSum += int64(smp.HeldOpen)
			movingOpenSum += int64(smp.HeldMovingOpen)
			pr.HeldMovingMax = max(pr.HeldMovingMax, smp.HeldMoving)
			pr.HeldOpenMax = max(pr.HeldOpenMax, smp.HeldOpen)
			circlingSum += int64(smp.Circling)
			pr.CirclingMax = max(pr.CirclingMax, smp.Circling)
			if smp.Trapped > pr.TrappedMax {
				pr.TrappedMax = smp.Trapped
			}
		}
		if len(series[i]) > 0 {
			pr.TrappedMean = float64(trappedSum) / float64(len(series[i]))
			pr.HeldMean = float64(heldSum) / float64(len(series[i]))
			pr.HeldMovingMean = float64(heldMovingSum) / float64(len(series[i]))
			pr.GroundMean = float64(groundSum) / float64(len(series[i]))
			pr.HeldOpenMean = float64(openSum) / float64(len(series[i]))
			pr.HeldMovingOpenMean = float64(movingOpenSum) / float64(len(series[i]))
			pr.CirclingMean = float64(circlingSum) / float64(len(series[i]))
			pr.HeldRefused = tr.heldRefused[i]
			pr.HeldLast = tr.heldLast(sess, i, sess.Clock.GlobalTick)
			for id, n := range tr.heldOrders[i] {
				if n == 0 {
					continue
				}
				if pr.HeldByOrder == nil {
					pr.HeldByOrder = map[string]int{}
				}
				pr.HeldByOrder[orders.DescriptorFor(orders.ID(id)).Name] = n
			}
		}
		if hosts[i] != nil {
			if rep, ok := hosts[i].Brain().(aikit.Reporter); ok {
				pr.Extra = map[string]float64{}
				rep.Report(func(name string, v int64) { pr.Extra[name] = float64(v) })
			}
		}
		pr.FirstAttack = tr.firstAttack[i]
		pr.LostByClass = map[string]int64{}
		for k, name := range traceRoleNames {
			if v := tr.lostClass[i][k]; v != 0 {
				pr.LostByClass[name] = v
			}
		}
		result.Players = append(result.Players, pr)
		if pr.Score > best {
			second, best, bestI = best, pr.Score, i
		} else if pr.Score > second {
			second = pr.Score
		}
	}
	if result.Reason == "timeout" && bestI >= 0 {
		// Adjudicate on points only with a clear margin; otherwise a draw.
		if second < 0 || best*10 >= second*13 {
			result.Winner = bestI
			result.Reason = "points"
		}
	}
	if req.Jam {
		result.Jam = tr.jam(sess, sess.Clock.GlobalTick, req.JamOf)
	}
	result.Watch = watch
	result.Circling = tr.circling
	if trace != nil {
		trace.Events = tr.events
		trace.Series = series
		trace.Log = tr.log
		if hosts[0] != nil || len(hosts) > 1 {
			for _, h := range hosts {
				if h != nil && h.Kit().Map != nil {
					mi := h.Kit().Map
					trace.SectorW, trace.SectorH = mi.SectorW, mi.SectorH
					trace.Starts = mi.Starts
					for _, s := range mi.Spots {
						trace.Spots = append(trace.Spots, [3]int32{s.X, s.Z, s.Metal})
					}
					break
				}
			}
		}
		result.Trace = trace
	}
	fmt.Fprintf(log, "arena: %s seed %d (battle %d): %d ticks in %.1fs, winner %d (%s)\n", req.Map, req.Seed, result.BattleSeed, result.Ticks, result.WallSeconds, result.Winner, result.Reason)
	return result, nil
}

// arenaStartOf is the stored number of the map start position u stands on
// at battle entry, or -1. The skirmish stamp puts a commander exactly on its
// StartPos coordinates [08 R-ENTRY-01 §5].
func arenaStartOf(sess *session.Session, u *units.Unit) int {
	if sess.Mission == nil {
		return -1
	}
	x, z := int32(int64(u.X)>>16), int32(int64(u.Z)>>16)
	for _, sp := range sess.Mission.Specials {
		if sp.Kind == 1 && int32(sp.X) == x && int32(sp.Z) == z {
			return int(sp.ID)
		}
	}
	return -1
}

// arenaStep advances one authoritative tick, as the windowed host does, and
// drains the committed events into the match's own buffer, which it returns
// for reuse. The arena has no presentation consumer, so the events are
// discarded; the buffer belongs to one match so that two matches in one
// process share nothing (the simulation benchmark's step keeps a single
// package buffer, which is safe only for one run at a time).
func arenaStep(sess *session.Session, drained []frame.EventView) []frame.EventView {
	sess.Step(sess.Clock.ScaledAnchor + 1)
	if sess.Snapshot != nil {
		drained = sess.Snapshot.DrainCommittedEvents(drained)
	}
	return drained
}

func arenaAlive(sess *session.Session, tr *arenaTracker, i int) bool {
	if tr.diedTick[i] != 0 {
		return false
	}
	return sess.Units.LiveCountForPlayer(i) > 0
}

func orderClasses() []uint8 {
	table := orders.Table()
	out := make([]uint8, len(table))
	for i, d := range table {
		switch d.Name {
		case "MobileBuild", "VTOL_MobileBuild", "BuildingBuild", "HelpBuild", "VTOL_HelpBuild",
			"RepairUnit", "VTOL_RepairUnit", "Reclaim", "ReclaimUnit", "VTOL_Reclaim", "VTOL_ReclaimUnit",
			"RepairPatrol", "VTOL_RepairPatrol", "Resurrect", "Capture", "BuildWeapon",
			"Follow_Ground", "VTOL_Follow", "Guard_NoMove":
			out[i] = 1 // productive work
		}
	}
	return out
}

func unitWorking(u *units.Unit, classes []uint8) bool {
	q := orders.QueueOfUnit(u)
	if q == nil {
		return false
	}
	for _, node := range q.Primary() {
		if node != nil && int(node.ID) < len(classes) && classes[node.ID] == 1 {
			return true
		}
	}
	return false
}

func sampleArena(sess *session.Session, walk []*units.Unit, table *aikit.Table, tr *arenaTracker, player int, tick uint32, classes []uint8) ArenaSample {
	s := ArenaSample{Tick: tick}
	if player < len(sess.Econ.Players) {
		p := &sess.Econ.Players[player]
		s.MetalIncome = int32(p.AIProduction[economy.Metal])
		s.EnergyIncome = int32(p.AIProduction[economy.Energy])
		s.MetalStock = int32(p.Stock[economy.Metal])
		s.EnergyStock = int32(p.Stock[economy.Energy])
		s.MetalWaste = int32(p.Waste[economy.Metal])
		s.MetalUse = int32(p.AIConsumption[economy.Metal])
		s.EnergyUse = int32(p.AIConsumption[economy.Energy])
	}
	for _, u := range walk {
		if int(u.Owner) != player || u.Def == nil || u.Dying {
			continue
		}
		info := table.Of(u.Def)
		if info == nil {
			continue
		}
		s.Units++
		if u.Remaining == 0 && info.Role.Has(aikit.RoleMobile) && !info.Role.Has(aikit.RoleAir) {
			s.Ground++
			if held, moving := tr.held(u, tick); held {
				s.Held++
				if moving {
					s.HeldMoving++
				}
				if open, ok := tr.open(sess, u, tick); ok && open {
					s.HeldOpen++
					if moving {
						s.HeldMovingOpen++
					}
				}
				if a := tr.holds[u.Handle]; a.walked >= circleWalk {
					s.Circling++
					if len(tr.circling) < 64 {
						tr.circling = append(tr.circling, ArenaCircling{Tick: tick, ID: int(u.Handle), Unit: u.Def.UnitName, X: int32(int64(u.X) >> 16), Z: int32(int64(u.Z) >> 16), Held: tick - a.tick, Walked: a.walked})
					}
				}
			}
		}
		if u.Remaining != 0 {
			// Remaining runs 1 → 0 as the frame is built [04 §2.3].
			s.FrameValue += int32(float32(info.Value) * (1 - u.Remaining))
			continue
		}
		switch {
		case info.Role.Has(aikit.RoleFactory):
			s.Factories++
			s.EcoValue += info.Value
			if !unitWorking(u, classes) {
				s.IdleFactory++
			}
		case info.Role.Has(aikit.RoleBuilder) || info.Role.Has(aikit.RoleCommander):
			s.Builders++
			if !info.Role.Has(aikit.RoleCommander) {
				s.BuilderValue += info.Value
			}
			if !unitWorking(u, classes) {
				s.IdleBuilders++
			}
		case info.Role.Any(aikit.RoleCombat):
			s.ArmyValue += info.Value
			if info.Role.Has(aikit.RoleMobile) && !info.Role.Has(aikit.RoleAir) && tr.trapped(u, tick) {
				s.Trapped++
			}
		case info.Role.Has(aikit.RoleDefense):
			s.ArmyValue += info.Value / 2
			s.EcoValue += info.Value / 2
		default:
			s.EcoValue += info.Value
		}
		if info.Role.Has(aikit.RoleExtractor) {
			s.Extractors++
		}
		// First attack: an armed unit of this player inside another start area.
	}
	s.ValueLost = tr.valueLost[player]
	s.ValueKilled = tr.valueKilled[player]
	return s
}

// logEntry is u's line in the trace log.
func logEntry(table *aikit.Table, u *units.Unit, tick uint32, kind string, by int) ArenaLogEntry {
	e := ArenaLogEntry{Tick: tick, Player: int(u.Owner), Kind: kind, Unit: u.Def.UnitName, X: int32(int64(u.X) >> 16), Z: int32(int64(u.Z) >> 16), By: by}
	info := table.Of(u.Def)
	e.Role = logClass(info)
	if info != nil {
		e.Value = info.Value
	}
	return e
}

// logClass names a unit's class for the play review, finer than the trace
// role: the kind of economy building, defence or combat unit it is.
func logClass(info *aikit.UnitInfo) string {
	if info == nil {
		return "other"
	}
	r := info.Role
	air, naval := r.Has(aikit.RoleAir), r.Has(aikit.RoleNaval)
	switch {
	case r.Has(aikit.RoleCommander):
		return "commander"
	case r.Has(aikit.RoleBuilder) && air:
		return "builder-air"
	case r.Has(aikit.RoleBuilder) && naval:
		return "builder-naval"
	case r.Has(aikit.RoleBuilder):
		return "builder"
	case r.Has(aikit.RoleFactory):
		return "factory"
	case r.Has(aikit.RoleAssist):
		return "assist"
	case r.Has(aikit.RoleExtractor):
		return "extractor"
	case r.Has(aikit.RoleEnergy):
		return "energy"
	case r.Has(aikit.RoleMetalMaker):
		return "metal-maker"
	case r.Has(aikit.RoleStorage):
		return "storage"
	case r.Has(aikit.RoleDefense) && r.Has(aikit.RoleAntiAir):
		return "defense-aa"
	case r.Has(aikit.RoleDefense) && r.Has(aikit.RoleArtillery):
		return "defense-artillery"
	case r.Has(aikit.RoleDefense):
		return "defense"
	case r.Any(aikit.RoleRadar|aikit.RoleSonar|aikit.RoleJammer) && !r.Has(aikit.RoleCombat):
		// An armed ship or aircraft that also carries sonar or radar is
		// classed by its fight, below.
		if r.Has(aikit.RoleMobile) {
			return "sensor-mobile"
		}
		return "sensor"
	case r.Has(aikit.RoleTransport):
		return "transport"
	case air && r.Has(aikit.RoleFighter):
		return "air-fighter"
	case air && r.Has(aikit.RoleBomber):
		return "air-bomber"
	case air && r.Has(aikit.RoleScout):
		return "air-scout"
	case air:
		return "air"
	case naval:
		return "naval"
	case r.Has(aikit.RoleCombat) && r.Has(aikit.RoleScout):
		return "ground-scout"
	case r.Has(aikit.RoleCombat) && r.Has(aikit.RoleAntiAir):
		return "ground-aa"
	case r.Has(aikit.RoleCombat) && r.Has(aikit.RoleArtillery):
		return "ground-artillery"
	case r.Has(aikit.RoleCombat) && r.Any(aikit.RoleHover|aikit.RoleAmphibious):
		return "ground-amphibious"
	case r.Has(aikit.RoleCombat):
		return "ground"
	}
	return "other"
}

// walked adds this tick's step to the way every unit with a hold has gone.
func (tr *arenaTracker) walked(walk []*units.Unit) {
	for _, u := range walk {
		a, ok := tr.holds[u.Handle]
		if !ok || u.Def == nil || u.Def.CanFly {
			continue
		}
		x, z := int32(int64(u.X)>>12), int32(int64(u.Z)>>12)
		if a.seen && (x != a.lastX || z != a.lastZ) {
			dx, dz := int64(x-a.lastX), int64(z-a.lastZ)
			// Sixteenths of a world unit a tick, summed whole: a step is
			// well under a cell.
			a.walked16 += int32(isqrt64(dx*dx + dz*dz))
			a.walked = a.walked16 / 16
		}
		a.lastX, a.lastZ, a.seen = x, z, true
		tr.holds[u.Handle] = a
	}
}

func isqrt64(v int64) int64 {
	if v <= 0 {
		return 0
	}
	x := v
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + v/x) / 2
	}
	return x
}

// open reports whether the ground connects where u stands with the goal of
// its head order, for its kind and owner and with every mobile unit absent:
// a held unit it answers false for is kept by the ground from a goal it
// cannot reach, which no way of moving mends.
func (tr *arenaTracker) open(sess *session.Session, u *units.Unit, tick uint32) (open, ok bool) {
	mv := sess.Movement
	if mv == nil {
		return false, false
	}
	if tr.reachTick != tick {
		// The ground is labelled afresh at every sample: structures and
		// wrecks come and go.
		tr.reachTick = tick
		tr.reachKeys = tr.reachKeys[:0]
	}
	key := mv.LabReachKey(u)
	for i, k := range tr.reachKeys {
		if k == key {
			return mv.LabGoalOpen(u, tr.reach[i])
		}
	}
	i := len(tr.reachKeys)
	if i == len(tr.reach) {
		tr.reach = append(tr.reach, &movement.LabReach{})
	}
	if !mv.LabReachOf(u, tr.reach[i]) {
		return false, false
	}
	tr.reachKeys = append(tr.reachKeys, key)
	return mv.LabGoalOpen(u, tr.reach[i])
}

// trapped reports whether u has stayed within trapRadius of where it was
// first seen for trapAge ticks without ever leaving, while its current
// order's goal lies beyond trapRadius.
func (tr *arenaTracker) trapped(u *units.Unit, tick uint32) bool {
	x, z := int32(int64(u.X)>>16), int32(int64(u.Z)>>16)
	a, ok := tr.anchors[u.Handle]
	if !ok {
		tr.anchors[u.Handle] = trapAnchor{x: x, z: z, tick: tick}
		return false
	}
	if a.left {
		return false
	}
	dx, dz := int64(x-a.x), int64(z-a.z)
	if dx*dx+dz*dz > trapRadius*trapRadius {
		a.left = true
		tr.anchors[u.Handle] = a
		return false
	}
	if tick-a.tick < trapAge {
		return false
	}
	q := orders.QueueOfUnit(u)
	if q == nil {
		return false
	}
	for _, node := range q.Primary() {
		if node == nil {
			continue
		}
		gx, gz := int64(int64(node.GoalX)>>16), int64(int64(node.GoalZ)>>16)
		if gx == 0 && gz == 0 {
			return false
		}
		dx, dz = gx-int64(x), gz-int64(z)
		return dx*dx+dz*dz > trapRadius*trapRadius
	}
	return false
}

// held reports whether u has stood within holdRadius of one point for
// holdAge ticks while its current order's goal lies beyond trapRadius, and
// whether that order is a plain ground move.
func (tr *arenaTracker) held(u *units.Unit, tick uint32) (held, moving bool) {
	x, z := int32(int64(u.X)>>16), int32(int64(u.Z)>>16)
	a, ok := tr.holds[u.Handle]
	dx, dz := int64(x-a.x), int64(z-a.z)
	var now trapAnchor
	if q := orders.QueueOfUnit(u); q != nil && q.Head() != nil {
		n := q.Head()
		now.order, now.target = n.ID, uint32(n.Target)
		if n.Target == 0 {
			now.goalX, now.goalZ = int32(int64(n.GoalX)>>16), int32(int64(n.GoalZ)>>16)
		}
	}
	gx, gz := int64(now.goalX-a.goalX), int64(now.goalZ-a.goalZ)
	if !ok || dx*dx+dz*dz > holdRadius*holdRadius || now.order != a.order || now.target != a.target || gx*gx+gz*gz > holdGoal*holdGoal {
		now.x, now.z, now.tick = x, z, tick
		now.lastX, now.lastZ, now.seen = a.lastX, a.lastZ, a.seen
		tr.holds[u.Handle] = now
		return false, false
	}
	if tick-a.tick < holdAge {
		return false, false
	}
	q := orders.QueueOfUnit(u)
	if q == nil {
		return false, false
	}
	for _, node := range q.Primary() {
		if node == nil {
			continue
		}
		if node.ID == tr.follow {
			// A guard's goal is where it stands by its ward, kept as an
			// offset from it: not a place on the map.
			return false, false
		}
		gx, gz := int64(int64(node.GoalX)>>16), int64(int64(node.GoalZ)>>16)
		if gx == 0 && gz == 0 {
			return false, false
		}
		dx, dz = gx-int64(x), gz-int64(z)
		far := dx*dx+dz*dz > trapRadius*trapRadius
		if far && int(u.Owner) < len(tr.heldOrders) {
			row := tr.heldOrders[u.Owner]
			for int(node.ID) >= len(row) {
				row = append(row, 0)
			}
			row[node.ID]++
			tr.heldOrders[u.Owner] = row
			if tr.refused != nil && tr.refused(u) {
				tr.heldRefused[u.Owner]++
			}
		}
		return far, node.ID == orders.Lookup("Move_Ground")
	}
	return false, false
}

// heldLast lists player's ground units that are held at tick.
func (tr *arenaTracker) heldLast(sess *session.Session, player int, tick uint32) []ArenaHeld {
	var out []ArenaHeld
	mv := sess.Movement
	for _, u := range sess.Units.AppendLive(nil) {
		if int(u.Owner) != player || u.Def == nil || u.Dying || u.Remaining != 0 {
			continue
		}
		info := tr.table.Of(u.Def)
		if info == nil || !info.Role.Has(aikit.RoleMobile) || info.Role.Has(aikit.RoleAir) {
			continue
		}
		a, ok := tr.holds[u.Handle]
		if !ok || tick-a.tick < holdAge {
			continue
		}
		q := orders.QueueOfUnit(u)
		if q == nil || q.Head() == nil {
			continue
		}
		node := q.Head()
		h := ArenaHeld{Unit: u.Def.UnitName, Order: orders.DescriptorFor(node.ID).Name, X: int32(int64(u.X) >> 16), Z: int32(int64(u.Z) >> 16),
			GoalX: int32(int64(node.GoalX) >> 16), GoalZ: int32(int64(node.GoalZ) >> 16), Ticks: tick - a.tick}
		dx, dz := int64(h.GoalX-h.X), int64(h.GoalZ-h.Z)
		if (h.GoalX == 0 && h.GoalZ == 0) || dx*dx+dz*dz <= trapRadius*trapRadius {
			continue
		}
		if mv != nil && int(u.Handle) < len(mv.Collisions) && mv.Collisions[u.Handle] != nil {
			c := mv.Collisions[u.Handle]
			h.Refused = c.Blocked
			if int(u.Handle) < len(mv.Routes) && mv.Routes[u.Handle] != nil {
				h.Routed = mv.Routes[u.Handle].Active
			}
			if c.Blocked {
				h.By = "ground"
				if id := c.BlockerID; id >= 0 && id < len(mv.Collisions) && mv.Collisions[id] != nil {
					o := mv.Collisions[id]
					ou := sess.Units.Unit(pool.Handle(id))
					switch {
					case o.Building:
						h.By = "structure"
					case ou != nil && ou.Owner != u.Owner:
						h.By = "other player"
					case o.Speed == 0:
						h.By = "parked"
					default:
						h.By = "mover"
					}
				}
			}
		}
		out = append(out, h)
	}
	return out
}

// jam draws the picture ArenaRequest.Jam asks for.
func (tr *arenaTracker) jam(sess *session.Session, tick uint32, kind string) *ArenaJam {
	mv := sess.Movement
	if mv == nil || sess.World == nil {
		return nil
	}
	j := &ArenaJam{Tick: tick, CellW: sess.World.CellW, CellH: sess.World.CellH}
	var of, named *units.Unit
	first := true
	for _, u := range sess.Units.AppendLive(nil) {
		if u.Def == nil || u.Dying || u.Def.CanFly {
			continue
		}
		if int(u.Handle) >= len(mv.Collisions) || mv.Collisions[u.Handle] == nil {
			continue
		}
		c := mv.Collisions[u.Handle]
		if kind != "" && named == nil && strings.EqualFold(u.Def.UnitName, kind) {
			named = u
		}
		ju := ArenaJamUnit{ID: int(u.Handle), Owner: int(u.Owner), Unit: u.Def.UnitName, X: int32(int64(u.X) >> 16), Z: int32(int64(u.Z) >> 16),
			FootX: int32(c.FootPrintX), FootZ: int32(c.FootPrintZ), Speed: int32(c.Speed), Refused: c.Blocked}
		if c.Building || !u.Def.CanMove {
			ju.By = "is a structure"
			ju.Refused = false
			j.Units = append(j.Units, ju)
			continue
		}
		if tick >= c.LastStampTick {
			ju.Stood = tick - c.LastStampTick
		}
		ju.Heading = c.Heading
		ju.Ring = mv.LabRing(u)
		if int(u.Handle) < len(mv.Routes) && mv.Routes[u.Handle] != nil {
			r := mv.Routes[u.Handle]
			ju.Routed = r.Active && r.Count >= 2
			if r.Active {
				for k := 0; k < int(r.Count); k++ {
					ju.Route = append(ju.Route, [2]int32{r.Points[k].X, r.Points[k].Z})
				}
			}
		}
		if c.Blocked {
			ju.By = "ground"
			if id := c.BlockerID; id >= 0 && id < len(mv.Collisions) && mv.Collisions[id] != nil {
				o := mv.Collisions[id]
				ou := sess.Units.Unit(pool.Handle(id))
				ju.Blocker = id
				switch {
				case o.Building:
					ju.By = "structure"
				case ou != nil && ou.Owner != u.Owner:
					ju.By = "other player"
				case o.Speed == 0:
					ju.By = "parked"
				default:
					ju.By = "mover"
				}
			}
		}
		if q := orders.QueueOfUnit(u); q != nil && q.Head() != nil {
			node := q.Head()
			ju.Order = orders.DescriptorFor(node.ID).Name
			ju.GoalX, ju.GoalZ = int32(int64(node.GoalX)>>16), int32(int64(node.GoalZ)>>16)
			ju.Created, ju.Phase, ju.Queued, ju.Target = node.CreationTick, int32(node.Phase), q.LenPrimary()-1, node.Target != 0
			if held, _ := tr.held(u, tick); held {
				a := tr.holds[u.Handle]
				{
					ju.Held = tick - a.tick
					ju.Walked = a.walked
					if open, ok := tr.open(sess, u, tick); ok {
						ju.Open = open
					}
					if v, ok := mv.LabRouteViews(u); ok {
						ju.Views = v[:]
					}
					if !ju.Routed && ju.Order == "Move_Ground" && of == nil {
						of = u
					}
				}
			}
		}
		if ju.Held > 0 {
			cx, cz := ju.X/16, ju.Z/16
			if first {
				j.X0, j.Z0, j.X1, j.Z1 = cx, cz, cx, cz
				first = false
			}
			j.X0, j.Z0, j.X1, j.Z1 = min(j.X0, cx), min(j.Z0, cz), max(j.X1, cx), max(j.Z1, cz)
		}
		j.Units = append(j.Units, ju)
	}
	if named != nil {
		of = named
		if first {
			first = false
		}
	}
	if of == nil {
		return j
	}
	// The whole map, up to a size a picture is still read at.
	const margin = 512
	j.X0, j.Z0 = max(j.X0-margin, 0), max(j.Z0-margin, 0)
	j.X1, j.Z1 = min(j.X1+margin, j.CellW-1), min(j.Z1+margin, j.CellH-1)
	search, static, ok := mv.LabGround(of, j.X0, j.Z0, j.X1, j.Z1)
	if !ok {
		return j
	}
	j.Of = of.Def.UnitName
	w := int(j.X1 - j.X0 + 1)
	if cells := mv.LabCells(of, j.X0, j.Z0, j.X1, j.Z1); len(cells) > 0 {
		for at := 0; at+w <= len(cells); at += w {
			j.Cells = append(j.Cells, string(cells[at:at+w]))
		}
	}
	row := make([]byte, w)
	for at := 0; at+w <= len(search); at += w {
		for i := 0; i < w; i++ {
			row[i] = '0' + search[at+i]
		}
		j.Search = append(j.Search, string(row))
		for i := 0; i < w; i++ {
			row[i] = '0' + static[at+i]
		}
		j.Static = append(j.Static, string(row))
	}
	return j
}

func traceFrame(walk []*units.Unit, table *aikit.Table, n int, tick uint32) ArenaFrame {
	f := ArenaFrame{Tick: tick}
	for _, u := range walk {
		if int(u.Owner) >= n || u.Def == nil || u.Dying {
			continue
		}
		info := table.Of(u.Def)
		hp := int32(100)
		if u.MaxHealth > 0 {
			hp = u.Health * 100 / u.MaxHealth
		}
		built := int32(1)
		if u.Remaining != 0 {
			built = 0
		}
		f.Units = append(f.Units, [6]int32{int32(u.Owner), traceRole(info), int32(int64(u.X) >> 16), int32(int64(u.Z) >> 16), hp, built})
	}
	return f
}
