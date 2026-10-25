package content

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/nanolathe-gg/nanolathe/internal/model"
)

// The snapshot is local admission metadata, never another content manifest.
// Original pointers prove identity; detached values prove no later mutation
// (DESIGN_MULTIPLAYER §16.3.63). In particular, Catalog.Clone would relink and
// rebuild precisely the lookup topology this snapshot must preserve.
type checkpointInputSnapshot struct {
	catalog         *Catalog
	manifest        []SimulationInput
	digest          [32]byte
	unitRecords     []*UnitDef
	units           checkpointInputMap[string, *UnitDef]
	weapons         checkpointInputMap[string, *WeaponDef]
	features        checkpointInputMap[string, *FeatureDef]
	movement        checkpointInputMap[string, *MovementClass]
	sides           []*SideDef
	weaponRecords   []*WeaponDef
	weaponByID      checkpointInputMap[int32, *WeaponDef]
	unitValues      []checkpointInputUnit
	weaponValues    []checkpointInputWeapon
	featureValues   []checkpointInputFeature
	movementValues  []checkpointInputMovement
	sideValues      []checkpointInputSide
	limits          Limits
	categories      *CategoryRegistry
	categoryEntries []CategoryEntry
	categoryIndex   checkpointInputMap[string, int]
	buildMenus      checkpointInputMap[string, *BuildMenuPage]
	buildValues     []checkpointInputBuildMenu
	downloads       []DownloadMenuPlacement
	meteor          *MeteorDefaults
	meteorValue     MeteorDefaults
	sight           *SightShapes
	sightValue      []SightShape
	sightHeader     DefinitionHeader
	los             *LOSTables
	losValue        *LOSTables
	roster          *SurvivalRoster
	rosterUnits     []SurvivalRosterEntry
	rosterBuildTree bool
	models          checkpointInputMap[string, *model.Model]
	modelTops       checkpointInputMap[string, int32]
	modelValues     []checkpointInputModel
	art             *SimArt
	// Effect hold slices need detached values rather than comparable map rows.
	effectKeys     []string
	effectValues   [][]int32
	effectsNil     bool
	sequences      checkpointInputMap[string, *simArtSequence]
	sequenceValues []checkpointInputSequence
	mapKey         string
	mapSelected    bool
	mapPresent     bool
	mapHeader      *MapHeader
	mapValue       *MapHeader
}

// Sorted expected rows make every validation a length check followed by
// lookups. Never range a live map or rebuild a lookup index at capture.
type checkpointInputPair[K cmp.Ordered, V comparable] struct {
	key   K
	value V
}
type checkpointInputMap[K cmp.Ordered, V comparable] struct {
	absent bool
	rows   []checkpointInputPair[K, V]
}

func snapshotCheckpointInputMap[K cmp.Ordered, V comparable](m map[K]V) checkpointInputMap[K, V] {
	keys := make([]K, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := checkpointInputMap[K, V]{absent: m == nil, rows: make([]checkpointInputPair[K, V], len(keys))}
	for i, key := range keys {
		out.rows[i] = checkpointInputPair[K, V]{key, m[key]}
	}
	return out
}
func (s checkpointInputMap[K, V]) matches(m map[K]V) bool {
	return (m == nil) == s.absent && s.entriesMatch(m)
}
func (s checkpointInputMap[K, V]) entriesMatch(m map[K]V) bool {
	if len(m) != len(s.rows) {
		return false
	}
	for _, row := range s.rows {
		if v, ok := m[row.key]; !ok || v != row.value {
			return false
		}
	}
	return true
}
func checkpointInputSlice[E comparable](a, b []E) bool {
	return (a == nil) == (b == nil) && slices.Equal(a, b)
}
func checkpointInputF32(a, b float32) bool {
	return !math.IsNaN(float64(a)) && math.Float32bits(a) == math.Float32bits(b)
}
func checkpointInputF64(a, b float64) bool {
	return !math.IsNaN(a) && math.Float64bits(a) == math.Float64bits(b)
}

// ValidateCheckpointInputs observes the freeze-time graph without touching a
// filesystem, invoking content getters, or repairing any cached topology.
func (in *SimulationInputs) ValidateCheckpointInputs() error {
	if in == nil || in.checkpointInputs == nil || in.catalog == nil || in.view == nil {
		return checkpointReferenceError("<inputs>", "completed freeze-time input snapshot")
	}
	return in.checkpointInputs.validate(in)
}

// ValidateMovementClasses permits another map container over exactly the
// admitted class objects. Same-name copies cannot inherit their identities.
func (k *CheckpointKeys) ValidateMovementClasses(classes map[string]*MovementClass) error {
	if k == nil || k.inputSnapshot == nil {
		return checkpointReferenceError("movement", "freeze-time movement classes")
	}
	s := k.inputSnapshot
	if !s.movement.entriesMatch(classes) {
		return checkpointReferenceError("movement", "the complete admitted class membership")
	}
	for _, row := range s.movementValues {
		if !sameCheckpointInputMovement(row.pointer, &row.value) {
			return checkpointReferenceError("movement/"+row.key, "unchanged admitted movement values")
		}
	}
	return nil
}

func snapshotCheckpointInputs(in *SimulationInputs) *checkpointInputSnapshot {
	c := in.catalog
	s := &checkpointInputSnapshot{catalog: c, manifest: slices.Clone(in.manifest), digest: in.digest,
		unitRecords: slices.Clone(c.unitRecords), units: snapshotCheckpointInputMap(c.Units),
		weapons: snapshotCheckpointInputMap(c.Weapons), features: snapshotCheckpointInputMap(c.Features), movement: snapshotCheckpointInputMap(c.Movement),
		sides: slices.Clone(c.Sides), weaponRecords: slices.Clone(c.weaponRecords), weaponByID: snapshotCheckpointInputMap(c.weaponByID), limits: c.Limits,
		categories: c.Categories, buildMenus: snapshotCheckpointInputMap(c.BuildMenus), downloads: slices.Clone(c.DownloadPlacements),
		meteor: c.Meteor, sight: c.Sight, los: c.LOS, roster: c.SurvivalRoster,
		models: snapshotCheckpointInputMap(in.models), modelTops: snapshotCheckpointInputMap(in.modelTops), art: in.simArt,
	}
	seenUnits := make(map[*UnitDef]bool)
	addUnit := func(u *UnitDef) {
		if u != nil && !seenUnits[u] {
			seenUnits[u] = true
			s.unitValues = append(s.unitValues, snapshotCheckpointInputUnit(u))
		}
	}
	for _, u := range c.unitRecords {
		addUnit(u)
	}
	for _, row := range s.units.rows {
		addUnit(row.value)
	}
	seenWeapons := make(map[*WeaponDef]bool)
	addWeapon := func(w *WeaponDef) {
		if w != nil && !seenWeapons[w] {
			seenWeapons[w] = true
			s.weaponValues = append(s.weaponValues, snapshotCheckpointInputWeapon(w))
		}
	}
	for _, row := range s.weapons.rows {
		addWeapon(row.value)
	}
	for _, w := range s.weaponRecords {
		addWeapon(w)
	}
	for _, row := range s.weaponByID.rows {
		addWeapon(row.value)
	}
	for _, u := range s.unitValues {
		for _, w := range [...]*WeaponDef{u.value.Weapon1Def, u.value.Weapon2Def, u.value.Weapon3Def, u.value.ExplodeAsDef, u.value.SelfDestructAsDef, u.value.TransportedExplodeAsDef, u.value.TransportedSelfDestructAsDef} {
			addWeapon(w)
		}
	}
	// Snapshot actual reachable links, including fixture links outside name
	// indexes. Do not relink them or create additional wire tables (§16.3.63).
	seenFeatures := make(map[*FeatureDef]bool)
	addFeature := func(f *FeatureDef) {
		if f != nil && !seenFeatures[f] {
			seenFeatures[f] = true
			s.featureValues = append(s.featureValues, snapshotCheckpointInputFeature(f))
		}
	}
	for _, row := range s.features.rows {
		addFeature(row.value)
	}
	for i := 0; i < len(s.featureValues); i++ {
		f := s.featureValues[i].value
		addFeature(f.FeatureDeadDef)
		addFeature(f.FeatureReclamateDef)
		addFeature(f.FeatureBurntDef)
	}
	for _, row := range s.movement.rows {
		if row.value != nil {
			s.movementValues = append(s.movementValues, checkpointInputMovement{row.key, row.value, *row.value})
		}
	}
	for _, v := range s.sides {
		if v != nil {
			s.sideValues = append(s.sideValues, checkpointInputSide{v, *v, snapshotCheckpointInputMap(v.Anchors)})
			s.sideValues[len(s.sideValues)-1].value.Anchors = nil
		}
	}
	if c.Categories != nil {
		s.categoryEntries = slices.Clone(c.Categories.entries)
		for i := range s.categoryEntries {
			s.categoryEntries[i].Membership.words = slices.Clone(s.categoryEntries[i].Membership.words)
		}
		s.categoryIndex = snapshotCheckpointInputMap(c.Categories.byName)
	}
	for _, row := range s.buildMenus.rows {
		if row.value != nil {
			v := *row.value
			v.Buttons = slices.Clone(v.Buttons)
			v.AuthoredButtons = slices.Clone(v.AuthoredButtons)
			v.InstalledProducts = slices.Clone(v.InstalledProducts)
			s.buildValues = append(s.buildValues, checkpointInputBuildMenu{row.key, row.value, v})
		}
	}
	if c.Meteor != nil {
		s.meteorValue = *c.Meteor
	}
	if c.Sight != nil {
		s.sightHeader = c.Sight.DefinitionHeader
		s.sightValue = slices.Clone(c.Sight.Shapes)
		for i := range s.sightValue {
			s.sightValue[i].Opaque = slices.Clone(s.sightValue[i].Opaque)
		}
	}
	if c.LOS != nil {
		s.losValue = snapshotCheckpointInputLOS(c.LOS)
	}
	if c.SurvivalRoster != nil {
		s.rosterUnits = slices.Clone(c.SurvivalRoster.Units)
		s.rosterBuildTree = c.SurvivalRoster.IncludeBuildTree
	}
	for _, row := range s.models.rows {
		if row.value != nil {
			s.modelValues = append(s.modelValues, checkpointInputModel{row.key, row.value, snapshotCheckpointInputModel(row.value)})
		}
	}
	if in.simArt != nil {
		s.effectsNil = in.simArt.effects == nil
		s.effectKeys = sortedKeys(in.simArt.effects)
		for _, key := range s.effectKeys {
			s.effectValues = append(s.effectValues, slices.Clone(in.simArt.effects[key]))
		}
		s.sequences = snapshotCheckpointInputMap(in.simArt.sequences)
		for _, row := range s.sequences.rows {
			if row.value != nil {
				s.sequenceValues = append(s.sequenceValues, checkpointInputSequence{row.key, row.value, row.value.visits, slices.Clone(row.value.frames)})
			}
		}
	}
	if strings.TrimSpace(in.selection.MapOTA) != "" {
		s.mapSelected = true
		s.mapKey = CanonicalKey(baseNameWithoutExt(snapshotKey(strings.TrimSpace(in.selection.MapOTA))))
		s.mapHeader, s.mapPresent = c.Maps[s.mapKey]
		if s.mapHeader != nil {
			v := *s.mapHeader
			v.Schemas = slices.Clone(v.Schemas)
			s.mapValue = &v
		}
	}
	return s
}

func (s *checkpointInputSnapshot) validate(in *SimulationInputs) error {
	bad := func(path string) error {
		return checkpointReferenceError(path, "unchanged freeze-time input values and lookup topology")
	}
	c := in.catalog
	if c != s.catalog {
		return bad("catalog")
	}
	if in.digest != s.digest || !checkpointInputSlice(in.manifest, s.manifest) {
		return bad("manifest")
	}
	if !checkpointInputSlice(c.unitRecords, s.unitRecords) || !s.units.matches(c.Units) {
		return bad("catalog.units")
	}
	if !s.weapons.matches(c.Weapons) || !checkpointInputSlice(c.weaponRecords, s.weaponRecords) || !s.weaponByID.matches(c.weaponByID) {
		return bad("catalog.weapons")
	}
	if !s.features.matches(c.Features) {
		return bad("catalog.features")
	}
	if !s.movement.matches(c.Movement) {
		return bad("catalog.movement")
	}
	if !checkpointInputSlice(c.Sides, s.sides) {
		return bad("catalog.sides")
	}
	if c.Limits != s.limits {
		return bad("catalog.limits")
	}
	for i := range s.unitValues {
		if !s.unitValues[i].matches() {
			return bad(fmt.Sprintf("catalog.unit[%d]", i))
		}
	}
	for i := range s.weaponValues {
		if !s.weaponValues[i].matches() {
			return bad(fmt.Sprintf("catalog.weapon[%d]", i))
		}
	}
	for i := range s.featureValues {
		if !s.featureValues[i].matches() {
			return bad(fmt.Sprintf("catalog.feature[%d]", i))
		}
	}
	for _, v := range s.movementValues {
		if !sameCheckpointInputMovement(v.pointer, &v.value) {
			return bad("movement/" + v.key)
		}
	}
	for i, v := range s.sideValues {
		if !sameCheckpointInputSide(v.pointer, &v.value) || !v.anchors.matches(v.pointer.Anchors) {
			return bad(fmt.Sprintf("catalog.side[%d]", i))
		}
	}
	if c.Categories != s.categories {
		return bad("catalog.categories")
	}
	if c.Categories != nil {
		if !s.categoryIndex.matches(c.Categories.byName) || (c.Categories.entries == nil) != (s.categoryEntries == nil) || len(c.Categories.entries) != len(s.categoryEntries) {
			return bad("catalog.categories")
		}
		for i, v := range s.categoryEntries {
			actual := c.Categories.entries[i]
			if actual.Name != v.Name || !checkpointInputSlice(actual.Membership.words, v.Membership.words) {
				return bad("catalog.categories.entries")
			}
		}
	}
	if !s.buildMenus.matches(c.BuildMenus) {
		return bad("catalog.buildMenus")
	}
	for _, v := range s.buildValues {
		a := v.pointer
		b := v.value
		if !sameCheckpointInputHeader(a.DefinitionHeader, b.DefinitionHeader) || a.Builder != b.Builder || a.BaseButtonCount != b.BaseButtonCount || !checkpointInputSlice(a.Buttons, b.Buttons) || !checkpointInputSlice(a.AuthoredButtons, b.AuthoredButtons) || !checkpointInputSlice(a.InstalledProducts, b.InstalledProducts) {
			return bad("buildmenu/" + v.key)
		}
	}
	if (c.DownloadPlacements == nil) != (s.downloads == nil) || len(c.DownloadPlacements) != len(s.downloads) {
		return bad("catalog.downloads")
	}
	for i, b := range s.downloads {
		a := c.DownloadPlacements[i]
		a.Provenance.ProviderID, b.Provenance.ProviderID = "", ""
		a.Provenance.MountOrder, b.Provenance.MountOrder = 0, 0
		if a != b {
			return bad("catalog.downloads")
		}
	}
	if c.Meteor != s.meteor || c.Meteor != nil && !sameCheckpointInputMeteor(c.Meteor, &s.meteorValue) {
		return bad("catalog.meteor")
	}
	if c.Sight != s.sight {
		return bad("catalog.sight")
	}
	if c.Sight != nil {
		a := c.Sight
		if !sameCheckpointInputHeader(a.DefinitionHeader, s.sightHeader) || (a.Shapes == nil) != (s.sightValue == nil) || len(a.Shapes) != len(s.sightValue) {
			return bad("catalog.sight")
		}
		for i, b := range s.sightValue {
			v := a.Shapes[i]
			if v.W != b.W || v.H != b.H || v.AnchorX != b.AnchorX || v.AnchorY != b.AnchorY || !checkpointInputSlice(v.Opaque, b.Opaque) {
				return bad("catalog.sight.shapes")
			}
		}
	}
	if c.LOS != s.los || c.LOS != nil && !sameCheckpointInputLOS(c.LOS, s.losValue) {
		return bad("catalog.los")
	}
	if c.SurvivalRoster != s.roster || c.SurvivalRoster != nil && (c.SurvivalRoster.IncludeBuildTree != s.rosterBuildTree || !checkpointInputSlice(c.SurvivalRoster.Units, s.rosterUnits)) {
		return bad("catalog.survivalRoster")
	}
	if !s.models.matches(in.models) || !s.modelTops.matches(in.modelTops) {
		return bad("models")
	}
	for _, v := range s.modelValues {
		if !sameCheckpointInputModel(v.pointer, &v.value) {
			return bad(v.key)
		}
	}
	if in.simArt != s.art {
		return bad("simArt")
	}
	if a := in.simArt; a != nil {
		if (a.effects == nil) != s.effectsNil || len(a.effects) != len(s.effectKeys) {
			return bad("simArt.effects")
		}
		for i, key := range s.effectKeys {
			v, ok := a.effects[key]
			if !ok || !checkpointInputSlice(v, s.effectValues[i]) {
				return bad("simArt.effect/" + key)
			}
		}
		if !s.sequences.matches(a.sequences) {
			return bad("simArt.sequences")
		}
		for _, v := range s.sequenceValues {
			if v.pointer.visits != v.visits || !checkpointInputSlice(v.pointer.frames, v.frames) {
				return bad("simArt.sequence/" + v.key)
			}
		}
	}
	if s.mapSelected {
		v, ok := c.Maps[s.mapKey]
		if ok != s.mapPresent || v != s.mapHeader {
			return bad("map/" + s.mapKey)
		}
		if v != nil {
			if !sameCheckpointInputMapHeader(v, s.mapValue) || (v.Schemas == nil) != (s.mapValue.Schemas == nil) || len(v.Schemas) != len(s.mapValue.Schemas) {
				return bad("map/" + s.mapKey)
			}
			for i := range v.Schemas {
				if !sameCheckpointInputMapSchema(&v.Schemas[i], &s.mapValue.Schemas[i]) {
					return bad("map/" + s.mapKey + ".schemas")
				}
			}
		}
	}
	return nil
}
