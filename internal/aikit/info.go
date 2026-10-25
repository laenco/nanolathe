package aikit

import (
	"slices"
	"strconv"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/construction"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/sim/numeric"
)

// Role is a bit set describing what a unit definition is for, derived from
// its authored keys alone (never from a stock unit name), so the same brain
// plays any content set that authors the ordinary keys.
type Role uint32

const (
	RoleCommander  Role = 1 << iota
	RoleBuilder         // mobile unit that can construct buildings
	RoleFactory         // immobile unit that produces units
	RoleExtractor       // metal extractor
	RoleEnergy          // energy producer (solar, wind, tidal, fusion)
	RoleMetalMaker      // converts energy to metal
	RoleStorage         // resource storage
	RoleRadar
	RoleSonar
	RoleJammer
	RoleDefense // immobile and armed
	RoleCombat  // mobile and armed, not a builder
	RoleAir
	RoleNaval
	RoleHover
	RoleAmphibious
	RoleScout
	RoleAntiAir
	RoleArtillery
	RoleTransport
	RoleBomber
	RoleFighter
	RoleKamikaze
	RoleMobile
	RoleAssist      // mobile builder whose products are all units (nano tower style) or immobile nano
	RoleStockpile   // active stockpile weapon that attacks units
	RoleInterceptor // active weapon that intercepts projectiles
	RoleModule      // upgrade structure: an immobile builder whose products are all fixtures (modules.go)
	RoleUpgrade     // a fixture an upgrade module makes and keeps (modules.go)
	RoleGate        // a gate: a structure whose aim dials a peer gate and whose script carries units to it (classifyGates)
)

// Has reports whether every bit of r2 is set.
func (r Role) Has(r2 Role) bool { return r&r2 == r2 }

// Any reports whether any bit of r2 is set.
func (r Role) Any(r2 Role) bool { return r&r2 != 0 }

// UnitInfo is the immutable per-definition summary a brain reasons with.
// Every number is an integer: costs in metal and energy, rates per second.
type UnitInfo struct {
	Index int32 // position in Table.Units
	Key   string
	Def   *content.UnitDef
	Side  string
	Role  Role
	// FinishedFeature is the authored feature an IsFeature product becomes,
	// for wall planning and preserving finished defences during cleanup
	// (docs/DESIGN_SURVIVAL.md §16.8). It is immutable catalog data.
	FinishedFeature *content.FeatureDef

	Metal, Energy int32 // build cost
	BuildTime     int32 // authored build time (work units)
	Value         int32 // metal-equivalent cost: metal + energy/EnergyPerMetal
	HP            int32
	DPS           int32 // sustained default damage per second (excludes stockpiles and interceptors)
	AirDPS        int32 // the part of DPS whose weapons can engage aircraft
	WaterDPS      int32 // the part of DPS from water-only weapons (torpedoes): no use against land
	StunDPS       int32 // the part of DPS from paralyzer weapons: stuns, never kills
	Range         int32 // longest range of those weapons, world units
	Speed         int32 // world units per second
	Sight         int32 // world units
	Radar         int32
	BuildPower    int32 // workertime
	BuildRange    int32 // builddistance
	MetalMake     int32 // metal per second ×100 (extraction multiplier ×100 for extractors)
	EnergyMake    int32 // steady energy per second (excludes wind and tidal)
	WindGen       int32 // wind output at full wind: output = WindGen × Obs.WindPermille / 1000
	TidalGen      int32 // tidal output at full tide: output = TidalGen × MapInfo.TidalPermille / 1000
	EnergyUse     int32 // energy per second consumed while active
	MetalStore    int32
	EnergyStore   int32
	FootX, FootZ  int32
	Depth         int32 // build-tree depth from a commander (0 = commander)
	// Cap is the per-player cap unit restrictions put on the definition,
	// immutable per battle: -1 for No limit, otherwise the limit field the
	// allocator compares its census with [05 R-SHARE-01 §8]
	// (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI restriction caps").
	Cap int32

	Builds []*UnitInfo // products, authored order, filtered by the session's build rules

	// capSlot is 1 + the definition's index in Table.Capped (and so in
	// Obs.Capped), 0 when the table does not cap it. Only BuildTable sets
	// it, so an info a test builds by hand is never capped.
	capSlot int32
	// banned marks an economy producer the content forbids to computer
	// players; no build list holds it (Table.banForbidden).
	banned bool
}

// EnergyPerMetal is the exchange rate used for Value. It is a planning
// convenience, not a game rule: metal makers convert at roughly this rate.
const EnergyPerMetal = 60

// SurfaceDPS is the damage per second a unit can bring against targets on
// land (everything but water-only weapons).
func (u *UnitInfo) SurfaceDPS() int32 { return u.DPS - u.WaterDPS }

// KillDPS is the part of DPS that destroys (all but paralyzer damage).
func (u *UnitInfo) KillDPS() int32 { return u.DPS - u.StunDPS }

// Strength is the Lanchester-style fighting product of a unit: damage it
// deals per second times the damage it can absorb, scaled down to stay well
// inside int64 when summed over armies.
func (u *UnitInfo) Strength() int64 { return int64(u.DPS) * int64(u.HP) / 16 }

// Table is the catalog summarized for one session's build rules.
type Table struct {
	checkpoint *checkpointTableSnapshot // detached construction provenance; never worker state
	Units      []*UnitInfo
	// Capped lists the definitions with a cap (UnitInfo.Cap >= 0), in table
	// order; empty when the battle restricts nothing, so a battle without
	// caps adds no observation work.
	Capped            []*UnitInfo
	byKey             map[string]*UnitInfo // lookup only; never ranged in a decision path (I1)
	byDef             map[*content.UnitDef]*UnitInfo
	defensiveFeatures map[*content.FeatureDef]bool
}

// Lookup returns the info for a canonical key.
func (t *Table) Lookup(key string) *UnitInfo {
	if t == nil {
		return nil
	}
	return t.byKey[content.CanonicalKey(key)]
}

// Of returns the info for a definition pointer.
func (t *Table) Of(def *content.UnitDef) *UnitInfo {
	if t == nil || def == nil {
		return nil
	}
	return t.byDef[def]
}

// BuildTable summarizes cat under rules. The walk is in sorted key order and
// the result is deterministic for a given catalog and rule set.
func BuildTable(cat *content.Catalog, rules construction.Rules) *Table {
	t := &Table{byKey: map[string]*UnitInfo{}, byDef: map[*content.UnitDef]*UnitInfo{}, defensiveFeatures: map[*content.FeatureDef]bool{}}
	if cat == nil {
		t.snapshotCheckpointBindings(cat, rules)
		return t
	}
	commanders := sideCommanders(cat)
	for _, key := range cat.SortedUnitKeys() {
		def, ok := cat.Unit(key)
		if !ok || def == nil {
			continue
		}
		if _, dup := t.byDef[def]; dup {
			continue
		}
		info := summarize(key, def, def.Commander || commanders[content.CanonicalKey(def.UnitName)])
		if def.IsFeature {
			info.FinishedFeature = cat.Features[content.CanonicalKey(def.Corpse)]
			if f := info.FinishedFeature; f != nil && f.Blocking && info.DPS == 0 {
				t.defensiveFeatures[f] = true
			}
		}
		info.Index = int32(len(t.Units))
		t.Units = append(t.Units, info)
		t.byKey[content.CanonicalKey(key)] = info
		t.byDef[def] = info
	}
	for _, info := range t.Units {
		menu := cat.BuildMenus[content.CanonicalKey(info.Key)]
		for _, product := range construction.BuildProducts(rules, menu) {
			if p := t.byKey[content.CanonicalKey(product)]; p != nil {
				info.Builds = append(info.Builds, p)
			}
		}
		// A product the builder's own build pages install is one a human
		// builds with the same unit whatever CANBUILD says [07 §9]; Switeck's
		// bugfix pages offer CORE's radar tower to a construction vehicle
		// whose list omits it (DESIGN_SESSIONS_AI_SAVE "Modern AI page
		// products").
		if menu != nil {
			for _, product := range menu.InstalledProducts {
				if p := t.byKey[content.CanonicalKey(product)]; p != nil && !slices.Contains(info.Builds, p) {
					info.Builds = append(info.Builds, p)
				}
			}
		}
		classifyBuilder(info)
		// Factories, towers and the like author a little storage; they are
		// not storage buildings.
		if info.Role.Any(RoleFactory | RoleDefense | RoleRadar | RoleSonar | RoleCommander | RoleBuilder | RoleMobile | RoleStockpile | RoleInterceptor) {
			info.Role &^= RoleStorage
		}
	}
	t.banForbidden(cat)
	t.classifyModules()
	t.computeAntiAir()
	t.armDamageTableAntiAir()
	t.armIdleBuilders()
	t.classifyGates()
	for _, u := range t.Units {
		scaleDPSByHint(u, u.Def)
	}
	t.computeDepth()
	for _, u := range t.Units {
		if u.Cap >= 0 {
			t.Capped = append(t.Capped, u)
			u.capSlot = int32(len(t.Capped))
		}
	}
	// Anti-air credited from the damage table is known only now: an armed
	// aircraft that is mostly anti-air is a fighter, not a bomber — unless
	// it drops bombs, which makes it a bomber whatever else it carries (the
	// tech-2 bombers also carry an anti-air gun that outweighs their bombs).
	for _, u := range t.Units {
		if u.Role.Has(RoleAir|RoleCombat) && !dropsBombs(u.Def) && u.AirDPS > 0 && u.AirDPS*2 >= u.DPS {
			u.Role = u.Role&^RoleBomber | RoleFighter
		}
	}
	t.snapshotCheckpointBindings(cat, rules)
	return t
}

// armDamageTableAntiAir arms a unit whose only damage is anti-air credited
// from the damage table: a flak weapon may author no default damage and name
// the aircraft it hits (Talon's flak tanks and towers), so summarize saw no
// damage at all and left it unarmed — never produced as combat, never placed
// as a defense. It now takes the roles an armed unit of its kind takes and is
// no longer a scout. Its DPS stays
// zero: that weapon never fires at a target that is not airborne, so it is
// worth its AirDPS where anti-air is wanted and nothing on the ground. A unit
// with any other damage was armed already and is unchanged.
func (t *Table) armDamageTableAntiAir() {
	for _, u := range t.Units {
		def := u.Def
		if u.DPS > 0 || u.AirDPS <= 0 || def == nil {
			continue
		}
		commander := u.Role.Has(RoleCommander)
		mobile := u.Role.Has(RoleMobile)
		if !mobile && def.BMCode == 0 {
			u.Role |= RoleDefense
		}
		if mobile && !commander && !def.Builder {
			u.Role = u.Role&^RoleScout | RoleCombat
			if def.CanFly {
				u.Role |= RoleFighter
			}
		}
	}
}

// armIdleBuilders makes combat units of armed mobile units that author
// builder=1 yet build nothing: classifyBuilder gave them no builder role,
// and summarize had kept them out of combat for the flag alone. Talon's
// Vanguard flak tank authors builder=1 with no worker time and no products
// instead.
func (t *Table) armIdleBuilders() {
	for _, u := range t.Units {
		def := u.Def
		if def == nil || !def.Builder || u.DPS+u.AirDPS <= 0 || !u.Role.Has(RoleMobile) || u.Role.Any(RoleCommander|RoleBuilder|RoleCombat) {
			continue
		}
		u.Role = u.Role&^RoleScout | RoleCombat
		if def.CanFly {
			if u.AirDPS > 0 && u.AirDPS*2 >= u.DPS && !dropsBombs(def) {
				u.Role |= RoleFighter
			} else {
				u.Role |= RoleBomber
			}
		}
	}
}

// classifyGates gives the gate role to a structure built the way Talon's
// Astria Porta is: no definition authors the teleporter key for it, and that
// key has no consumer anyway [fmt fbi]. The gate's mechanism is its script
// (research/extensions/talon-engine.md "Gate network"): an ordinary ground
// attack aims its weapon, and the aim script picks the peer gate nearest the
// aimed point and never fires; units that reach its pad are attached to it
// and handed to the peer through attach and drop operations, which need a
// transport capacity; and its off command unlinks. So a gate is read from
// those three authored keys together: an immobile structure that is no
// builder, authors a transport capacity, carries an active weapon that fires
// at units, and can be switched on and off. With every faction package
// mounted this finds TALGATE alone; stock content has none.
//
// Its weapon is the dialing device, not firepower: the gate keeps no damage
// rate or range, and none of the roles the weapon or the capacity gave it
// (a long-range defense, a transport), so it is placed and used as a gate and
// an enemy's gate is no threat.
func (t *Table) classifyGates() {
	for _, u := range t.Units {
		def := u.Def
		if def == nil || def.BMCode != 0 || def.Builder || def.TransportCapacity <= 0 || !def.OnOffable {
			continue
		}
		armed := false
		for _, w := range [...]*content.WeaponDef{def.Weapon1Def, def.Weapon2Def, def.Weapon3Def} {
			if firesAtUnits(w) {
				armed = true
			}
		}
		if !armed {
			continue
		}
		u.Role = RoleGate
		u.DPS, u.AirDPS, u.WaterDPS, u.StunDPS, u.Range = 0, 0, 0, 0, 0
	}
}

// dropsBombs reports whether a definition carries an active dropped weapon
// (a bomb). A dropped weapon never acquires a target on its own [06 §3.2]:
// it falls on what the aircraft is sent to attack, so its carrier is a
// bomber, and a bomb's own damage table never counts as anti-air.
func dropsBombs(def *content.UnitDef) bool {
	if def == nil {
		return false
	}
	for _, w := range [...]*content.WeaponDef{def.Weapon1Def, def.Weapon2Def, def.Weapon3Def} {
		if weaponActive(w) && w.Dropped {
			return true
		}
	}
	return false
}

// computeAntiAir credits anti-air fire from the damage table: stock
// anti-air missiles author extra damage against named aircraft rather than
// setting toairweapon, so a weapon with a damage entry naming a flying unit
// is counted as able to engage aircraft at that damage.
func (t *Table) computeAntiAir() {
	for _, u := range t.Units {
		def := u.Def
		for _, w := range [...]*content.WeaponDef{def.Weapon1Def, def.Weapon2Def, def.Weapon3Def} {
			// A bomb's damage table may name aircraft, but a dropped weapon
			// cannot engage them (dropsBombs); an interceptor engages only
			// projectiles (firesAtUnits).
			// A toairweapon with a default damage was counted by summarize;
			// one whose damage is all in its table (Talon's flak lasers name
			// each aircraft and author no default) is counted here.
			if !firesAtUnits(w) || (w.ToAirWeapon && w.DamageDefault > 0) || w.Dropped || len(w.Damage) == 0 {
				continue
			}
			var airDmg int64
			for _, key := range w.DamageKeysSorted() {
				target := t.byKey[content.CanonicalKey(key)]
				if target == nil || target.Def == nil || !target.Def.CanFly {
					continue
				}
				if d := int64(w.Damage[key]); d > airDmg {
					airDmg = d
				}
			}
			if airDmg <= int64(w.DamageDefault) || airDmg <= 0 {
				continue
			}
			burst := int64(w.Burst)
			if burst < 1 {
				burst = 1
			}
			reload := int64(w.ReloadTime)
			if reload < 1 {
				reload = 1
			}
			u.AirDPS += int32(airDmg * burst * 30 / reload)
			u.Role |= RoleAntiAir
		}
	}
}

func weaponActive(w *content.WeaponDef) bool {
	return w != nil && !content.IsWeaponInactive(w)
}

// firesAtUnits reports whether an active weapon supplies sustained unit fire.
// Stockpile rounds are separately funded ammunition [06 §11.1]. An
// interceptor (a stock anti-nuke) never does: when its slot re-acquires it
// scans projectiles and aims at the winning one's position [06 §3.2], so it
// adds nothing to what a unit's fire does to other units — and its authored
// range (tens of thousands of world units, the coverage it intercepts over)
// is not a reach into the enemy base.
func firesAtUnits(w *content.WeaponDef) bool {
	return weaponActive(w) && !w.Interceptor && !w.Stockpile
}

// intercepts reports whether a definition carries an active interceptor.
func intercepts(def *content.UnitDef) bool {
	for _, w := range [...]*content.WeaponDef{def.Weapon1Def, def.Weapon2Def, def.Weapon3Def} {
		if weaponActive(w) && w.Interceptor {
			return true
		}
	}
	return false
}

// definitionCap is the per-player cap the allocator's third test applies to
// def: its limit field when set, false when the field holds the parser's -1
// No limit [05 R-SHARE-01 §8]. A malformed negative limit refuses every
// creation, as a cap of 0 would.
func definitionCap(def *content.UnitDef) (int32, bool) {
	if def == nil || !def.LimitEnabled || def.Limit == -1 {
		return -1, false
	}
	return max(def.Limit, 0), true
}

// sideCommanders is the set of canonical definition names the side records
// name as their commander. A unit is a player's commander because its name
// is the commander name of its owner's side record, not because its
// definition sets the commander flag [08 R-TRIG-01 §3][08 R-SKIR-01 §3];
// content may author one without the other, as converted Mynn's commander
// carries no flag (docs/DESIGN_MODS_MUTATORS.md §5.8). Lookup only; never
// ranged in a decision path (I1).
func sideCommanders(cat *content.Catalog) map[string]bool {
	set := map[string]bool{}
	for _, side := range cat.Sides {
		if side == nil {
			continue
		}
		if name := content.CanonicalKey(side.Commander); name != "" {
			set[name] = true
		}
	}
	return set
}

// summarize derives one definition's summary. commander is true for a
// definition that sets the commander flag or that a side record names as
// its commander (sideCommanders).
func summarize(key string, def *content.UnitDef, commander bool) *UnitInfo {
	u := &UnitInfo{Key: key, Def: def, Side: def.Side, Cap: -1}
	if limit, ok := definitionCap(def); ok {
		u.Cap = limit
	}
	u.Metal = numeric.TruncateFloat32ToLow32(def.BuildCostMetal)
	u.Energy = numeric.TruncateFloat32ToLow32(def.BuildCostEnergy)
	u.BuildTime = def.BuildTime
	u.Value = u.Metal + u.Energy/EnergyPerMetal
	u.HP = def.MaxDamage
	u.Speed = int32((int64(def.MaxVelocity) * 30) >> 16)
	u.Sight = def.SightDistance
	u.Radar = def.RadarDistance
	u.BuildPower = def.WorkerTime
	u.BuildRange = def.BuildDistance
	u.MetalMake = numeric.TruncateFloat64ToLow32(def.MetalMake * 100)
	if def.ExtractsMetal > 0 {
		u.MetalMake = numeric.TruncateFloat64ToLow32(def.ExtractsMetal * 100000) // per footprint metal unit, ×1e5
	}
	// Net energy: authored production minus upkeep. A negative energyuse is
	// how solar collectors author their output.
	u.EnergyMake = numeric.TruncateFloat64ToLow32(def.EnergyMake - def.EnergyUse)
	if u.EnergyMake < 0 {
		u.EnergyMake = 0
	}
	u.WindGen = numeric.TruncateFloat64ToLow32(def.WindGenerator)
	u.TidalGen = numeric.TruncateFloat64ToLow32(def.TidalGenerator)
	u.EnergyUse = numeric.TruncateFloat64ToLow32(def.EnergyUse)
	if u.EnergyUse < 0 {
		u.EnergyUse = 0
	}
	u.MetalStore = numeric.TruncateFloat64ToLow32(def.MetalStorage)
	u.EnergyStore = numeric.TruncateFloat64ToLow32(def.EnergyStorage)
	u.FootX, u.FootZ = def.FootprintX, def.FootprintZ

	mobile := def.BMCode != 0 && def.CanMove
	if mobile {
		u.Role |= RoleMobile
	}
	for _, w := range [...]*content.WeaponDef{def.Weapon1Def, def.Weapon2Def, def.Weapon3Def} {
		if weaponActive(w) {
			if w.Interceptor {
				u.Role |= RoleInterceptor
			} else if w.Stockpile {
				u.Role |= RoleStockpile
			}
		}
		if !firesAtUnits(w) {
			continue
		}
		dmg := int64(w.DamageDefault)
		// A manually fired super-weapon (the commander's disintegrator
		// authors a damage in the thousands) is not sustained fire.
		if dmg <= 0 || dmg >= 4000 {
			continue
		}
		burst := int64(w.Burst)
		if burst < 1 {
			burst = 1
		}
		reload := int64(w.ReloadTime)
		if reload < 1 {
			reload = 1
		}
		if w.Dropped && reload < 90 {
			reload = 90 // bombs fall in passes, not as sustained fire
		}
		dps := int32(dmg * burst * 30 / reload)
		u.DPS += dps
		if w.WaterWeapon {
			u.WaterDPS += dps
		}
		if w.Paralyzer {
			u.StunDPS += dps
		}
		if w.ToAirWeapon {
			u.AirDPS += dps
			u.Role |= RoleAntiAir
		}
		if w.Range > u.Range && w.Range < 32767 {
			u.Range = w.Range
		}
		if w.Ballistic && w.Range >= 700 {
			u.Role |= RoleArtillery
		}
	}
	switch {
	case commander:
		u.Role |= RoleCommander
	case def.ExtractsMetal > 0:
		u.Role |= RoleExtractor
	}
	if (u.EnergyMake > 0 || u.WindGen > 0 || u.TidalGen > 0) && !commander && !mobile && def.RadarDistance == 0 && def.SonarDistance == 0 && u.DPS == 0 {
		u.Role |= RoleEnergy
	}
	if def.MakesMetal > 0 || (def.MetalMake > 0 && def.EnergyUse > 0 && !mobile && !commander) {
		u.Role |= RoleMetalMaker
	}
	if (def.MetalStorage > 0 || def.EnergyStorage > 0) && !commander && !mobile && u.Role&(RoleEnergy|RoleExtractor|RoleMetalMaker) == 0 {
		u.Role |= RoleStorage
	}
	if def.RadarDistance > 0 && !commander {
		u.Role |= RoleRadar
	}
	if def.SonarDistance > 0 && !commander {
		u.Role |= RoleSonar
	}
	if def.RadarDistanceJam > 0 || def.SonarDistanceJam > 0 {
		u.Role |= RoleJammer
	}
	if def.CanFly {
		u.Role |= RoleAir
	}
	if def.CanHover {
		u.Role |= RoleHover
	}
	if def.Amphibious {
		u.Role |= RoleAmphibious
	}
	if mobile && !def.CanFly && !def.CanHover && def.MinWaterDepth > 0 {
		u.Role |= RoleNaval
	}
	if def.TransportCapacity > 0 {
		u.Role |= RoleTransport
	}
	if def.Kamikaze {
		u.Role |= RoleKamikaze
	}
	armed := u.DPS > 0
	if armed && !mobile && def.BMCode == 0 {
		u.Role |= RoleDefense
	}
	if armed && mobile && !commander && !def.Builder {
		u.Role |= RoleCombat
		if def.CanFly {
			if u.AirDPS > 0 && u.AirDPS*2 >= u.DPS && !dropsBombs(def) {
				u.Role |= RoleFighter
			} else {
				u.Role |= RoleBomber
			}
		}
	}
	if mobile && !def.Builder && !commander && u.Role&(RoleTransport|RoleStockpile|RoleInterceptor) == 0 {
		// A scout is fast and cheap for its speed class, or an unarmed
		// mobile whose only purpose can be looking — not a mobile anti-nuke,
		// whose interceptor is its purpose.
		if (!armed && !intercepts(def)) || (u.Metal <= 70 && u.Speed >= 60) {
			u.Role |= RoleScout
		}
	}
	return u
}

// AIDPSHintKey is the unit key through which content tells the Modern AI how
// much of its weapons' authored rate a unit really delivers, in permille: a
// unit whose script holds its fire between shots (Talon's Salamander sleeps
// two and a half seconds after each pair of shells against a 0.2-second
// reload) is worth far less than its weapon records say, and the table would
// rate it many times over. The key is a
// Nanolathe extension that only the Modern AI reads; the simulation never
// does, and stock content authors none, so stock tables are unchanged
// (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI damage hints").
const AIDPSHintKey = "nl_ai_dpspermille"

// scaleDPSByHint applies a unit's AIDPSHintKey to its damage rates, every
// one credited by then, the damage-table anti-air included. A value that is
// not an integer from 0 to 1000 is ignored.
func scaleDPSByHint(u *UnitInfo, def *content.UnitDef) {
	if def == nil || len(def.Unknown) == 0 || u.DPS+u.AirDPS == 0 {
		return
	}
	var permille int64 = -1
	for _, key := range def.UnknownKeysSorted() {
		if strings.EqualFold(key, AIDPSHintKey) {
			if v, err := strconv.Atoi(strings.TrimSpace(def.Unknown[key])); err == nil && v >= 0 && v <= 1000 {
				permille = int64(v)
			}
			break
		}
	}
	if permille < 0 {
		return
	}
	scale := func(v int32) int32 { return int32(int64(v) * permille / 1000) }
	u.DPS, u.AirDPS, u.WaterDPS, u.StunDPS = scale(u.DPS), scale(u.AirDPS), scale(u.WaterDPS), scale(u.StunDPS)
}

// classifyBuilder runs after products are linked: a builder role needs a
// building among its products, a factory needs a mobile product.
func classifyBuilder(u *UnitInfo) {
	if len(u.Builds) == 0 || u.Def == nil || !u.Def.Builder {
		return
	}
	buildsBuilding, buildsMobile := false, false
	for _, p := range u.Builds {
		if p.Def.BMCode == 0 {
			buildsBuilding = true
		} else {
			buildsMobile = true
		}
	}
	mobile := u.Role.Has(RoleMobile)
	switch {
	case mobile && buildsBuilding:
		u.Role |= RoleBuilder
	case !mobile && buildsMobile:
		u.Role |= RoleFactory
	}
}

// computeDepth labels each definition with its distance from a commander in
// the build graph: a breadth-first walk in table order, so the answer is the
// same on every run.
func (t *Table) computeDepth() {
	for _, u := range t.Units {
		u.Depth = -1
	}
	var frontier []*UnitInfo
	for _, u := range t.Units {
		if u.Role.Has(RoleCommander) {
			u.Depth = 0
			frontier = append(frontier, u)
		}
	}
	for len(frontier) > 0 {
		var next []*UnitInfo
		for _, u := range frontier {
			for _, p := range u.Builds {
				if p.Depth < 0 {
					p.Depth = u.Depth + 1
					next = append(next, p)
				}
			}
		}
		frontier = next
	}
}

// Buildable reports whether builder can produce p.
func (u *UnitInfo) Buildable(p *UnitInfo) bool {
	if u == nil || p == nil {
		return false
	}
	for _, b := range u.Builds {
		if b == p {
			return true
		}
	}
	return false
}
