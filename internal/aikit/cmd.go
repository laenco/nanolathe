package aikit

import (
	"math"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/pool"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
	"github.com/nanolathe-gg/nanolathe/internal/units"
	"github.com/nanolathe-gg/nanolathe/internal/world"
)

// CmdKind names a command a brain can emit. Each maps to an ordinary
// command code or construction producer — the same surface a human's clicks
// reach — so a brain has no privileged path into the simulation.
type CmdKind uint8

const (
	CmdMove       CmdKind = iota + 1 // command code 2
	CmdAttack                        // code 3 on a unit
	CmdAttackPos                     // code 3 on the ground
	CmdPatrol                        // code 9
	CmdGuard                         // code 7
	CmdRepair                        // code 8: help build or repair
	CmdReclaim                       // code 12 on a unit
	CmdStop                          // purge unprotected orders
	CmdBuild                         // code 14: mobile build near a point or on a metal spot
	CmdProduce                       // factory queue
	CmdReplace                       // code 12 on an own building, then code 14 on its metal spot
	CmdUnblock                       // code 12 on whatever seals an own factory's exit
	CmdClear                         // code 12 on features around a point (streets first, then wrecks)
	CmdStockpile                     // counted BuildWeapon in the secondary queue
	CmdLoad                          // code 6 on a unit: a transport picks it up
	CmdUnload                        // code 5 on the ground: a transport unloads there
	CmdActivate                      // the Activate state order: an on/off unit is switched on
	CmdDeactivate                    // the Deactivate state order: switched off
	CmdCloak                         // the Cloak_On state order: cloak requested
	CmdDecloak                       // the Cloak_Off state order: the request cleared
	CmdBreach                        // code 12 on the wall pieces nearest a point (walls.go)
)

// stateOrders names the state order each of the four switch commands pushes,
// as the side panel's ON/OFF and CLOAK gadgets do (session HumanActivation
// and HumanCloak).
var stateOrders = [...]string{CmdActivate: "Activate", CmdDeactivate: "Deactivate", CmdCloak: "Cloak_On", CmdDecloak: "Cloak_Off"}

// Command is one player-level action. A group order is one command.
type Command struct {
	Kind         CmdKind
	Queued       bool
	first, count int32 // actor span in the batch's handle buffer
	Target       pool.Handle
	X, Z         int32
	Product      *UnitInfo
	Slot         int32 // weapon slot for CmdStockpile
	Count        int32
	Spot         int32 // metal spot index for an extractor, -1 otherwise
	Spacing      int32 // extra cells kept free around a placed building
	// Keep asks the site search to refuse a site that would cut an own
	// factory's exit off from open ground (layout.go).
	Keep bool
	// Exact preserves a planned wall segment's corridors by refusing a
	// blocked requested site instead of relocating the piece.
	Exact bool
	// target is the unit Target named when the command was issued.
	target *units.Unit
}

// batch is the command output of one think, applied at due.
type batch struct {
	due     uint32
	cmds    []Command
	actors  []pool.Handle
	inst    []*units.Unit // the unit each actor slot held when the command was issued
	pending bool
	// rowNear is the brain's row-extension radius (Kit.SetRowNear), world
	// units, 0 for the default. The host keeps one batch for its life and
	// reset leaves this alone, so a setting holds until the brain changes it.
	rowNear int32
	// watch holds the points this think asked Obs.Walls to cover
	// (Kit.WatchWalls), nWatch how many.
	watch  [maxWallWatch][2]int32
	nWatch int
}

func (b *batch) reset() {
	b.cmds = b.cmds[:0]
	b.actors = b.actors[:0]
	b.inst = b.inst[:0]
	b.pending = false
	b.nWatch = 0
}

// ApplyStats counts what happened to a batch.
type ApplyStats struct {
	Applied, DroppedAPM, Stale, Failed int32
	// Why commands failed, by FailReason.
	Reasons [failReasons]int32 `json:"reasons"`
	// Base upkeep (layout.go): sealed factory exits opened, feature
	// clearing orders given, and the metal those features held.
	Unblocks   int32 `json:"unblocks,omitempty"`
	Clears     int32 `json:"clears,omitempty"`
	ClearMetal int32 `json:"clear_metal,omitempty"`
	// Row buildings placed under the layout rules by extending a row of
	// their class, and by starting a new one (layout.go).
	RowExtend int32 `json:"row_extend,omitempty"`
	RowNew    int32 `json:"row_new,omitempty"`
	// Capped counts the placements and factory requests dropped as stale
	// because the product's cap was reached in the reaction window (also
	// counted in Stale; capReached).
	Capped int32 `json:"capped,omitempty"`
	// Breaches counts the wall-reclaim orders given (walls.go).
	Breaches int32 `json:"breaches,omitempty"`
	// FailKinds and FailActors break FailResolve down by command kind and by
	// the first actor's authored role. They are diagnostics only.
	FailKinds  [int(CmdBreach)]int32 `json:"fail_kinds,omitempty"`
	FailActors [3]int32              `json:"fail_actors,omitempty"`
}

const (
	failActorCommander = iota
	failActorBuilder
	failActorCombat
)

// noteResolve records one resolver refusal. The actor is the first actor the
// command still had when it was applied; nil or a definition without a role
// is counted as combat so the three classes still sum to FailResolve.
func (s *ApplyStats) noteResolve(kind CmdKind, actor *units.Unit) {
	if i := int(kind) - 1; i >= 0 && i < len(s.FailKinds) {
		s.FailKinds[i]++
	}
	a := failActorCombat
	if actor != nil && actor.Def != nil {
		switch {
		case actor.Def.Commander:
			a = failActorCommander
		case actor.Def.Builder:
			a = failActorBuilder
		}
	}
	s.FailActors[a]++
}

// FailReason indexes ApplyStats.Reasons.
const (
	FailNoSite    = iota // no valid footprint near the requested point
	FailBuildGate        // the builder cannot build there (code-14 gate)
	FailQueue            // the construction producer refused
	FailNoActor          // every actor died or changed
	FailResolve          // the order resolver rejected every actor
	FailTarget           // the target died
	failReasons
)

// executor applies batches on the simulation thread.
type executor struct {
	m       *ai.Manager
	table   *Table
	mapInfo *MapInfo
	obs     *Obs // latest observation (own factory lanes)
	places  []*placeDef
	stats   ApplyStats
	// Temporary simulation-thread diagnostic scope; never checkpoint payload.
	checkpointApplication *checkpointCommand
	checkpointBatchSerial uint64

	tokens   int64 // APM bucket, thousandths of an action
	lastFill uint32

	// Exit guard (layout.go): pictures of up to len(grids) own factories.
	dedupe      gridDedupe
	grids       [4]exitGrid
	guardFacs   [4]OwnUnit
	nGuard      int
	cutBuf      []int32
	blkBuf      []int32
	selfGrid    exitGrid // a planned factory's own exit; the factory being opened
	pending     [16]pendingSite
	nextPending int
	lastTick    uint32
	keep        bool                 // the build being placed asked for the exit guard and layout rules
	rowNear     int32                // the batch's row-extension radius (0: rowNear)
	rows        rowState             // layout.go scratch
	frees       [freeSlots]freeCache // macroFree answers for this tick, by class
	freeSlot    int                  // the slot freeKey chose
	freeSeq     uint32               // freeKey calls, for the least recently used slot
	lanes       []rowBld             // own factories' exit lanes for the placement being searched
	allyTowers  []towerSpace         // allied tower reservations for this placement search
	spotCover   []bool               // per plot cell: under some metal spot's footprint
	featDist    []int64              // refreshFeatures scratch: squared distance per listed feature
	featOrder   []int32              // refreshFeatures scratch: nearest first
}

type placeDef struct {
	ok           bool
	extent       world.FootprintExtent
	yard         []world.YardCell
	rules        world.PlacementRules
	footX, footZ int32
	mobile       bool
}

func (e *executor) placement(info *UnitInfo) *placeDef {
	for int(info.Index) >= len(e.places) {
		e.places = append(e.places, nil)
	}
	if p := e.places[info.Index]; p != nil {
		return p
	}
	p := &placeDef{}
	e.places[info.Index] = p
	cat := e.m.Catalog
	def := info.Def
	if cat == nil || def == nil {
		return p
	}
	fx, fz := world.FootprintForUnit(cat, def)
	ext, err := world.NewFootprintExtent(fx, fz)
	if err != nil {
		return p
	}
	var yard []world.YardCell
	if def.BMCode == 0 {
		yard, err = world.ParseYardMap(def.YardMap, int(fx), int(fz))
		if err != nil {
			return p
		}
	}
	rules, err := world.PlacementRulesForUnit(cat, def)
	if err != nil {
		return p
	}
	*p = placeDef{ok: true, extent: ext, yard: yard, rules: rules, footX: fx, footZ: fz, mobile: def.BMCode != 0}
	return p
}

// validAt tests one footprint anchor with the canonical world validator. It
// asks the validator's yes/no form: a search rejects thousands of anchors and
// keeps none of the messages CheckPlacement would word for them.
func (e *executor) validAt(p *placeDef, cx, cz int32) bool {
	t := e.m.Terrain
	if t == nil || cx < 1 || cz < 1 || cx+p.footX >= t.CellW-1 || cz+p.footZ >= t.CellH-1 {
		return false
	}
	if !p.mobile && e.blocksAllyTower(cx, cz, p.footX, p.footZ) {
		return false
	}
	rect, err := world.NewFootprintRect(world.NewFootprintAnchor(cx, cz), p.extent)
	if err != nil {
		return false
	}
	return t.PlacementLegal(world.PlacementQuery{Rect: rect, Yard: p.yard, Rules: p.rules, Self: 0, Mobile: p.mobile})
}

// overlapsSpot reports whether a footprint would cover a metal spot, which a
// non-extractor building must leave free: whether any of its cells lies
// under a spot's extractor footprint (spotCover, built once: the spots are
// fixed).
func (e *executor) overlapsSpot(cx, cz, fx, fz int32) bool {
	m := e.mapInfo
	if m == nil || m.UniformMetal || len(m.Spots) == 0 {
		return false
	}
	w, h := m.CellW, m.CellH
	if len(e.spotCover) != int(w*h) {
		e.spotCover = make([]bool, w*h)
		for i := range m.Spots {
			s := &m.Spots[i]
			for z := max32(s.CellZ, 0); z < s.CellZ+m.FootZ && z < h; z++ {
				for x := max32(s.CellX, 0); x < s.CellX+m.FootX && x < w; x++ {
					e.spotCover[z*w+x] = true
				}
			}
		}
	}
	for z := max32(cz, 0); z < cz+fz && z < h; z++ {
		row := e.spotCover[z*w:]
		for x := max32(cx, 0); x < cx+fx && x < w; x++ {
			if row[x] {
				return true
			}
		}
	}
	return false
}

// laneCells is the depth of the exit lane kept clear in front of a factory
// (units leave toward +Z, the unrotated front).
const laneCells = 10

// prepareLanes collects the own factories' exit lanes from the latest
// observation, once per site search.
func (e *executor) prepareLanes() {
	e.lanes = e.lanes[:0]
	if e.obs == nil {
		return
	}
	for i := range e.obs.Own {
		u := &e.obs.Own[i]
		if !u.Info.Role.Has(RoleFactory) {
			continue
		}
		ffx, ffz := u.Info.FootX, u.Info.FootZ
		ax := u.X/16 - ffx/2
		az := u.Z/16 - ffz/2
		e.lanes = append(e.lanes, rowBld{x0: ax - 1, z0: az + ffz, x1: ax + ffx + 1, z1: az + ffz + laneCells})
	}
}

// blocksLane reports whether a footprint would sit in an own factory's exit
// lane (prepareLanes).
func (e *executor) blocksLane(cx, cz, fx, fz int32) bool {
	for i := range e.lanes {
		l := &e.lanes[i]
		if cx < l.x1 && l.x0 < cx+fx && cz < l.z1 && l.z0 < cz+fz {
			return true
		}
	}
	return false
}

// findSite searches rings of anchors around the world point (x, z) for a
// valid footprint, stepping by the footprint plus spacing so buildings keep
// lanes between them, and never covering a metal spot (unless extracting)
// or a factory's exit lane. It returns the anchor cell.
func (e *executor) findSite(info *UnitInfo, x, z, spacing int32, w *units.World, tick uint32) (int32, int32, bool) {
	p := e.placement(info)
	if !p.ok {
		return 0, 0, false
	}
	// Buildings of three cells or more keep at least two free cells between
	// them: one-cell gaps between rows of solars form a maze that traps
	// ground units at home.
	if !p.mobile && !info.Role.Has(RoleExtractor) && (p.footX >= 3 || p.footZ >= 3) && spacing < 2 {
		spacing = 2
	}
	stepX, stepZ := p.footX+spacing, p.footZ+spacing
	if stepX < 1 {
		stepX = 1
	}
	if stepZ < 1 {
		stepZ = 1
	}
	cx0 := x/16 - p.footX/2
	cz0 := z/16 - p.footZ/2
	// Rings out to roughly 1000 world units.
	rings := 1000 / (16 * stepX)
	if rings < 6 {
		rings = 6
	}
	extractor := info.Role.Has(RoleExtractor)
	e.prepareLanes()
	e.prepareTowerSpace()
	if e.keep && !p.mobile && !extractor && p.rules.MinWaterDepth <= 0 {
		// Land buildings under the layout rules (layout.go); a building
		// that fits nowhere under them is not placed at all.
		return e.findRowSite(info, p, x, z, w, tick)
	}
	for r := int32(0); r <= rings; r++ {
		for j := -r; j <= r; j++ {
			for i := -r; i <= r; i++ {
				if absI32(i) != r && absI32(j) != r {
					continue
				}
				cx, cz := cx0+i*stepX, cz0+j*stepZ
				if !extractor && e.overlapsSpot(cx, cz, p.footX, p.footZ) {
					continue
				}
				if !p.mobile && e.blocksLane(cx, cz, p.footX, p.footZ) {
					continue
				}
				if e.validAt(p, cx, cz) && !e.guardRefuses(info, cx, cz, p.footX, p.footZ, w, tick) {
					e.guardPlaced(cx, cz, p.footX, p.footZ)
					return cx, cz, true
				}
			}
		}
	}
	return 0, 0, false
}

// spotSite validates an extractor on a metal spot, trying the adjacent
// anchors when the exact one is blocked. Under the layout rules (keep) an
// extractor never stands in an own factory's kept lane (layout.go
// inOwnLane): factories avoid lanes over spots, but one placed with no
// other site may face a spot, whose extractor would wall its pad in.
func (e *executor) spotSite(info *UnitInfo, spot int32, w *units.World, tick uint32) (int32, int32, bool) {
	m := e.mapInfo
	if m == nil || spot < 0 || int(spot) >= len(m.Spots) {
		return 0, 0, false
	}
	p := e.placement(info)
	if !p.ok {
		return 0, 0, false
	}
	e.prepareTowerSpace()
	s := &m.Spots[spot]
	for r := int32(0); r <= 1; r++ {
		for j := -r; j <= r; j++ {
			for i := -r; i <= r; i++ {
				if absI32(i) != r && absI32(j) != r {
					continue
				}
				if e.keep && e.inOwnLane(s.CellX+i, s.CellZ+j, p.footX, p.footZ, tick) {
					continue
				}
				if e.validAt(p, s.CellX+i, s.CellZ+j) && !e.guardRefuses(info, s.CellX+i, s.CellZ+j, p.footX, p.footZ, w, tick) {
					e.guardPlaced(s.CellX+i, s.CellZ+j, p.footX, p.footZ)
					return s.CellX + i, s.CellZ + j, true
				}
			}
		}
	}
	return 0, 0, false
}

// actorOK revalidates an actor at apply time: alive, still ours and still
// the definition the brain saw.
func (e *executor) actorOK(w *units.World, h pool.Handle, inst *units.Unit) *units.Unit {
	u := w.Unit(h)
	if u == nil || u != inst || !u.Alive || u.Dying || u.Owner != e.m.Player {
		return nil
	}
	return u
}

// capReached revalidates a creation request against unit restrictions at
// apply time: the owner's live census of the product's definition — read
// through the accessor that shares the allocator gate's count — has reached
// the definition's cap [05 R-SHARE-01 §8]. The brain asked while it still had
// an allowance, but records made in the reaction window can use it up; the
// command is then stale, dropped like the executor's other revalidations
// before it spends anything (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI
// restriction caps"). A product without a cap is never stale this way.
func (e *executor) capReached(c *Command, w *units.World) bool {
	if c.Product == nil {
		return false
	}
	limit, ok := definitionCap(c.Product.Def)
	if !ok || int32(w.DefinitionCount(int(e.m.Player), c.Product.Def)) < limit {
		return false
	}
	e.stats.Stale++
	e.stats.Capped++
	e.checkpointReject()
	return true
}

// targetOK keeps every delayed target command tied to the observed instance,
// including replacement and factory cleanup (docs/MODERN_AI_RESEARCH.md §2).
func (e *executor) targetOK(w *units.World, c *Command) *units.Unit {
	u := w.Unit(c.Target)
	if u == nil || u != c.target || !u.Alive || u.Dying {
		return nil
	}
	return u
}

func (e *executor) refill(tick uint32, persona *Persona) {
	if persona.APM <= 0 {
		return
	}
	burst := int64(persona.Burst)
	if burst < 1 {
		burst = 1
	}
	if e.lastFill == 0 && e.tokens == 0 {
		e.tokens = burst * 1000
	}
	if tick > e.lastFill {
		e.tokens += int64(tick-e.lastFill) * int64(persona.APM) * 1000 / 1800
		if e.tokens > burst*1000 {
			e.tokens = burst * 1000
		}
		e.lastFill = tick
	}
}

// projectBudget is the number of actions a batch emitted now can spend at
// its due tick: the bucket refilled to now, plus the reaction window's
// refill, capped at the burst. -1 means unlimited.
func (e *executor) projectBudget(tick uint32, persona *Persona) int32 {
	if persona.APM <= 0 {
		return -1
	}
	e.refill(tick, persona)
	t := e.tokens + int64(persona.Reaction)*int64(persona.APM)*1000/1800
	burst := int64(persona.Burst)
	if burst < 1 {
		burst = 1
	}
	if t > burst*1000 {
		t = burst * 1000
	}
	return int32(t / 1000)
}

// apply executes one batch in emission order.
func (e *executor) apply(b *batch, tick uint32, w *units.World, persona *Persona) {
	e.refill(tick, persona)
	e.lastTick = tick
	e.rowNear = b.rowNear
	e.refreshFeatures(tick)
	e.refreshWalls(b, tick)
	for i := range b.cmds {
		c := &b.cmds[i]
		func() {
			if uint64(i) > math.MaxUint32 {
				e.m.CheckpointApplicationHistory().Fail(executorCheckpointError("aikit.application.ordinal", "a representable command ordinal"))
			}
			state := e.newCheckpointCommand(c, b, tick, e.checkpointBatchSerial, uint32(i))
			previous := e.checkpointApplication
			e.checkpointApplication = state
			restore := e.observeCheckpointLayout(e.checkpointAttempt())
			apm, returned := uint8(1), false
			defer func() {
				restore()
				e.checkpointApplication = previous
				if returned {
					state.finish(apm)
				}
			}()
			if persona.APM > 0 {
				if e.tokens < 1000 {
					e.stats.DroppedAPM++
					apm, returned = 3, true
					return
				}
				e.tokens -= 1000
				apm = 2
			}
			if e.exec(c, b, tick, w) {
				e.stats.Applied++
			}
			returned = true
		}()
	}
}

var orderCodes = [...]int{CmdMove: 2, CmdAttack: 3, CmdAttackPos: 3, CmdPatrol: 9, CmdGuard: 7, CmdRepair: 8, CmdReclaim: 12, CmdLoad: 6, CmdUnload: 5}

func (e *executor) exec(c *Command, b *batch, tick uint32, w *units.World) bool {
	switch c.Kind {
	case CmdBuild:
		return e.execBuild(c, b, tick, w)
	case CmdStockpile:
		return e.execStockpile(c, b, tick, w)
	case CmdProduce:
		return e.execProduce(c, b, w)
	case CmdReplace:
		return e.execReplace(c, b, tick, w)
	case CmdUnblock:
		return e.execUnblock(c, b, tick, w)
	case CmdClear:
		return e.execClear(c, b, tick, w)
	case CmdBreach:
		return e.execBreach(c, b, tick, w)
	case CmdActivate, CmdDeactivate, CmdCloak, CmdDecloak:
		return e.execState(c, b, tick, w)
	}
	var target *units.Unit
	if c.Kind == CmdAttack || c.Kind == CmdGuard || c.Kind == CmdRepair || c.Kind == CmdReclaim || c.Kind == CmdLoad {
		target = e.targetOK(w, c)
		if target == nil {
			e.checkpointReject()
			e.stats.Stale++
			e.stats.Reasons[FailTarget]++
			return false
		}
	}
	x := numeric.Fixed(int64(c.X) << 16)
	z := numeric.Fixed(int64(c.Z) << 16)
	var y numeric.Fixed
	if t := e.m.Terrain; t != nil && target == nil {
		y = t.HeightAt(x, z)
	}
	if target != nil {
		x, y, z = target.X, target.Y, target.Z
	}
	issued := false
	actors := 0
	var first *units.Unit
	for i := c.first; i < c.first+c.count; i++ {
		u := e.actorOK(w, b.actors[i], b.inst[i])
		if u == nil {
			e.checkpointReject()
			continue
		}
		if first == nil {
			first = u
		}
		actors++
		q := orders.BindQueueBinding(u, e.m.OrderBinding)
		if q == nil {
			e.checkpointReject()
			continue
		}
		func() {
			restore := e.checkpointAttempt().ObserveQueue(q, u)
			actor := e.checkpointActor(u)
			defer restore()
			if c.Kind == CmdStop {
				q.PurgeUnprotected()
				q.DropLeadingAutoOps()
				issued = true
				e.checkpointAccept()
				return
			}
			if c.Kind == CmdAttack && !e.canChase(u, target) {
				e.checkpointReject()
				return // keeps its current order
			}
			id := orders.Resolve(orderCodes[c.Kind], u, target, &orders.ResolvePos{X: x, Y: y, Z: z})
			if id == 0 {
				e.checkpointReject()
				return
			}
			if !c.Queued {
				q.PurgeUnprotected()
				q.DropLeadingAutoOps()
			}
			var th pool.Handle
			if target != nil {
				th = target.Handle
			}
			node := orders.NewNodeForOrder(id, th, x, y, z, tick, u.Handle, c.Queued)
			mark := e.checkpointAttempt().InsertionIndex()
			q.Push(id, node)
			e.checkpointOrderOutcome(mark, actor, 1)
			issued = true
		}()
	}
	if !issued {
		e.checkpointReject()
		e.stats.Failed++
		if actors == 0 {
			e.stats.Reasons[FailNoActor]++
		} else {
			e.stats.Reasons[FailResolve]++
			e.stats.noteResolve(c.Kind, first)
		}
	}
	return issued
}

// canChase refuses a unit attack an actor could only chase: an aircraft
// target for an actor with no anti-air fire, or a target in the actor's
// authored no-chase categories. A group attack order would otherwise send
// ground units to maneuver under aircraft indefinitely.
func (e *executor) canChase(u, target *units.Unit) bool {
	if u.Def == nil || target == nil || target.Def == nil {
		return true
	}
	if target.Def.CanFly && e.table != nil {
		if info := e.table.Of(u.Def); info != nil && info.AirDPS == 0 {
			return false
		}
	}
	return !target.Def.DefinitionMask().Intersects(u.Def.NoChaseCategoryMask)
}

// switchApplies reports whether a switch command means anything to a unit of
// def: activation only to an on/off definition, which is all the session's
// ON/OFF gadget sends it to, and cloak only to a definition whose cloak cost
// gives it the can-cloak capability the Cloak_On and Cloak_Off handlers test
// [04 R-ORD-01 §2]. The handlers would complete without effect on any other
// unit; such an actor is skipped instead of being sent an empty record.
func switchApplies(kind CmdKind, def *content.UnitDef) bool {
	if def == nil {
		return false
	}
	if kind == CmdCloak || kind == CmdDecloak {
		return def.CloakCost > 0
	}
	return def.OnOffable
}

// execState applies a switch command: the state order the side panel's
// ON/OFF or CLOAK gadget pushes, through the same producer insertion. Both
// pairs of descriptors keep the queue (they skip the replacement purge and
// head-insert), so the unit's mission carries on behind them.
func (e *executor) execState(c *Command, b *batch, tick uint32, w *units.World) bool {
	id := orders.Lookup(stateOrders[c.Kind])
	if id == 0 {
		e.checkpointReject()
		e.stats.Failed++
		return false
	}
	issued := false
	actors := 0
	var first *units.Unit
	for i := c.first; i < c.first+c.count; i++ {
		u := e.actorOK(w, b.actors[i], b.inst[i])
		if u == nil || !switchApplies(c.Kind, u.Def) {
			e.checkpointReject()
			continue
		}
		if first == nil {
			first = u
		}
		actors++
		q := orders.BindQueueBinding(u, e.m.OrderBinding)
		if q == nil {
			e.checkpointReject()
			continue
		}
		func() {
			restore := e.checkpointAttempt().ObserveQueue(q, u)
			actor := e.checkpointActor(u)
			defer restore()
			mark := e.checkpointAttempt().InsertionIndex()
			q.Push(id, orders.NewNodeForOrder(id, 0, 0, 0, 0, tick, u.Handle, c.Queued))
			e.checkpointOrderOutcome(mark, actor, 1)
			issued = true
		}()
	}
	if !issued {
		e.checkpointReject()
		e.stats.Failed++
		if actors == 0 {
			e.stats.Reasons[FailNoActor]++
		} else {
			e.stats.Reasons[FailResolve]++
			e.stats.noteResolve(c.Kind, first)
		}
	}
	return issued
}

func (e *executor) execBuild(c *Command, b *batch, tick uint32, w *units.World) bool {
	if c.count < 1 || c.Product == nil || e.m.QueueBuildTypedHook() == nil {
		e.checkpointReject()
		e.stats.Failed++
		return false
	}
	u := e.actorOK(w, b.actors[c.first], b.inst[c.first])
	if u == nil {
		e.checkpointReject()
		e.stats.Stale++
		e.stats.Reasons[FailNoActor]++
		return false
	}
	if e.capReached(c, w) {
		return false
	}
	var cx, cz int32
	var ok bool
	e.keep = c.Keep
	e.nGuard = 0
	if c.Keep {
		e.prepareGuard(c.X, c.Z, tick)
	}
	if c.Exact {
		cx, cz, ok = e.exactSite(c.Product, c.X, c.Z, w, tick)
	} else if c.Spot >= 0 {
		cx, cz, ok = e.spotSite(c.Product, c.Spot, w, tick)
	} else {
		cx, cz, ok = e.findSite(c.Product, c.X, c.Z, c.Spacing, w, tick)
	}
	if !ok {
		e.checkpointReject()
		e.stats.Failed++
		e.stats.Reasons[FailNoSite]++
		return false
	}
	p := e.placement(c.Product)
	wx := numeric.Fixed(int64(p.footX+2*cx) << 19)
	wz := numeric.Fixed(int64(p.footZ+2*cz) << 19)
	q := orders.BindQueueBinding(u, e.m.OrderBinding)
	if q == nil {
		e.checkpointReject()
		e.stats.Failed++
		return false
	}
	restore := e.checkpointAttempt().ObserveQueue(q, u)
	actor := e.checkpointActor(u)
	defer restore()
	if orders.Resolve(14, u, nil, &orders.ResolvePos{X: wx, Z: wz}) == 0 {
		e.checkpointReject()
		e.stats.Failed++
		e.stats.Reasons[FailBuildGate]++
		return false
	}
	if !c.Queued {
		q.PurgeUnprotected()
		q.DropLeadingAutoOps()
	}
	req := ai.BuildRequest{Builder: u.Handle, UnitKey: c.Product.Key, X: wx, Z: wz, Count: 1, Kind: ai.BuildKindMobileSite}
	mark := e.checkpointAttempt().InsertionIndex()
	err := e.m.QueueBuildTypedHook()(req)
	e.checkpointBuildOutcome(mark, actor, req, err)
	if err != nil {
		e.stats.Failed++
		e.stats.Reasons[FailQueue]++
		return false
	}
	return true
}

func (e *executor) execProduce(c *Command, b *batch, w *units.World) bool {
	if c.count < 1 || c.Product == nil || e.m.QueueBuildTypedHook() == nil {
		e.checkpointReject()
		e.stats.Failed++
		return false
	}
	u := e.actorOK(w, b.actors[c.first], b.inst[c.first])
	if u == nil {
		e.checkpointReject()
		e.stats.Stale++
		e.stats.Reasons[FailNoActor]++
		return false
	}
	if e.capReached(c, w) {
		return false
	}
	n := c.Count
	if n < 1 {
		n = 1
	}
	req := ai.BuildRequest{Builder: u.Handle, UnitKey: c.Product.Key, Count: int(n), Kind: ai.BuildKindFactoryQueue}
	actor := e.checkpointActor(u)
	mark := e.checkpointAttempt().InsertionIndex()
	err := e.m.QueueBuildTypedHook()(req)
	e.checkpointBuildOutcome(mark, actor, req, err)
	if err != nil {
		e.stats.Failed++
		e.stats.Reasons[FailQueue]++
		return false
	}
	return true
}

// Replace orders a builder to reclaim one of the owner's buildings standing
// on metal spot spot and then build product there: an extractor upgrade.
// Two ordinary orders — a reclaim of the old building, then the build
// queued behind it — so the new site is validated when the builder gets
// there, after the old one is gone.
func (k *Kit) Replace(builder, old pool.Handle, product *UnitInfo, spot int32) {
	var one [1]pool.Handle
	one[0] = builder
	k.push(Command{Kind: CmdReplace, Target: old, Product: product, Spot: spot}, one[:])
}

// execReplace applies a Replace. The new footprint is centred on the spot;
// only its terrain (slope and water depth) can be checked now, since the
// old building still covers it.
func (e *executor) execReplace(c *Command, b *batch, tick uint32, w *units.World) bool {
	m := e.mapInfo
	if c.count < 1 || c.Product == nil || e.m.QueueBuildTypedHook() == nil || m == nil || c.Spot < 0 || int(c.Spot) >= len(m.Spots) {
		e.checkpointReject()
		e.stats.Failed++
		return false
	}
	u := e.actorOK(w, b.actors[c.first], b.inst[c.first])
	if u == nil {
		e.checkpointReject()
		e.stats.Stale++
		e.stats.Reasons[FailNoActor]++
		return false
	}
	old := e.targetOK(w, c)
	if old == nil || old.Owner != e.m.Player {
		e.checkpointReject()
		e.stats.Stale++
		e.stats.Reasons[FailTarget]++
		return false
	}
	if e.capReached(c, w) {
		return false
	}
	p := e.placement(c.Product)
	if !p.ok {
		e.checkpointReject()
		e.stats.Failed++
		e.stats.Reasons[FailNoSite]++
		return false
	}
	sp := &m.Spots[c.Spot]
	cx, cz := sp.X/16-p.footX/2, sp.Z/16-p.footZ/2
	if !e.terrainFits(p, cx, cz) {
		e.checkpointReject()
		e.stats.Failed++
		e.stats.Reasons[FailNoSite]++
		return false
	}
	q := orders.BindQueueBinding(u, e.m.OrderBinding)
	if q == nil {
		e.checkpointReject()
		e.stats.Failed++
		return false
	}
	restore := e.checkpointAttempt().ObserveQueue(q, u)
	actor := e.checkpointActor(u)
	defer restore()
	wx := numeric.Fixed(int64(p.footX+2*cx) << 19)
	wz := numeric.Fixed(int64(p.footZ+2*cz) << 19)
	if orders.Resolve(14, u, nil, &orders.ResolvePos{X: wx, Z: wz}) == 0 {
		e.checkpointReject()
		e.stats.Failed++
		e.stats.Reasons[FailBuildGate]++
		return false
	}
	id := orders.Resolve(orderCodes[CmdReclaim], u, old, &orders.ResolvePos{X: old.X, Y: old.Y, Z: old.Z})
	if id == 0 {
		e.checkpointReject()
		e.stats.Failed++
		e.stats.Reasons[FailResolve]++
		e.stats.noteResolve(c.Kind, u)
		return false
	}
	q.PurgeUnprotected()
	q.DropLeadingAutoOps()
	mark := e.checkpointAttempt().InsertionIndex()
	q.Push(id, orders.NewNodeForOrder(id, old.Handle, old.X, old.Y, old.Z, tick, u.Handle, false))
	e.checkpointOrderOutcome(mark, actor, 1)
	req := ai.BuildRequest{Builder: u.Handle, UnitKey: c.Product.Key, X: wx, Z: wz, Count: 1, Kind: ai.BuildKindMobileSite}
	mark = e.checkpointAttempt().InsertionIndex()
	err := e.m.QueueBuildTypedHook()(req)
	e.checkpointBuildOutcome(mark, actor, req, err)
	if err != nil {
		e.stats.Failed++
		e.stats.Reasons[FailQueue]++
		return false
	}
	return true
}

// terrainFits checks a building footprint's terrain alone — the floor span
// against the slope limit and the water depth band — as the placement
// validator would, ignoring whatever stands there.
func (e *executor) terrainFits(p *placeDef, cx, cz int32) bool {
	m := e.mapInfo
	if cx < 1 || cz < 1 || cx+p.footX >= m.CellW-1 || cz+p.footZ >= m.CellH-1 {
		return false
	}
	lo, hi := m.band(cx, cz, p.footX, p.footZ)
	if !p.rules.Terrain {
		return true
	}
	sea := m.SeaLevel
	return hi-lo <= p.rules.MaxSlope && lo >= sea-p.rules.MaxWaterDepth && hi <= sea-p.rules.MinWaterDepth
}
