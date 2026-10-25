package aikit

import (
	"slices"

	"github.com/nanolathe-gg/nanolathe/internal/construction"
	"github.com/nanolathe-gg/nanolathe/internal/content"
)

// BuildTable seals this on the simulation thread before any worker receives
// the table. The original rule tag belongs to the table even after a manager
// switches modes; no rule interface or producer is retained (§16.3.73).
type checkpointTableSnapshot struct {
	table             *Table
	catalog           *content.Catalog
	ruleKind          uint8
	rulesSupported    bool
	units, capped     []*UnitInfo
	values            []UnitInfo
	byKey             checkpointTableIndex[string, *UnitInfo]
	byDef             checkpointTableIndex[*content.UnitDef, *UnitInfo]
	defensiveFeatures checkpointTableIndex[*content.FeatureDef, bool]
}

type checkpointTableIndex[K, V comparable] struct {
	present bool
	count   int
	rows    []checkpointTableIndexRow[K, V]
}

type checkpointTableIndexRow[K, V comparable] struct {
	key   K
	value V
}

// Keys come from the constructor's ordered unit walk, not a map traversal.
// Deduplication preserves first encounter and covers shared finished features.
func snapshotCheckpointTableIndex[K, V comparable](index map[K]V, keys []K) checkpointTableIndex[K, V] {
	s := checkpointTableIndex[K, V]{present: index != nil, count: len(index), rows: make([]checkpointTableIndexRow[K, V], 0, len(index))}
	seen := make(map[K]bool, len(index))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		if value, ok := index[key]; ok {
			s.rows = append(s.rows, checkpointTableIndexRow[K, V]{key, value})
		}
	}
	return s
}

func (s checkpointTableIndex[K, V]) matches(index map[K]V) bool {
	if (index != nil) != s.present || len(index) != s.count || len(s.rows) != s.count {
		return false
	}
	for _, row := range s.rows {
		if value, ok := index[row.key]; !ok || value != row.value {
			return false
		}
	}
	return true
}

func (t *Table) snapshotCheckpointBindings(cat *content.Catalog, rules construction.Rules) {
	kind, err := construction.CheckpointRulesKind(rules)
	s := &checkpointTableSnapshot{table: t, catalog: cat, ruleKind: kind, rulesSupported: err == nil,
		units: slices.Clone(t.Units), capped: slices.Clone(t.Capped), values: make([]UnitInfo, len(t.Units))}
	keys := make([]string, len(t.Units))
	defs := make([]*content.UnitDef, len(t.Units))
	features := make([]*content.FeatureDef, len(t.Units))
	for i, u := range t.Units {
		// BuildTable never appends nil info records. Preserve that constructor
		// invariant instead of accepting an authored replacement as provenance.
		s.values[i] = *u
		s.values[i].Builds = slices.Clone(u.Builds)
		keys[i], defs[i], features[i] = content.CanonicalKey(u.Key), u.Def, u.FinishedFeature
	}
	s.byKey = snapshotCheckpointTableIndex(t.byKey, keys)
	s.byDef = snapshotCheckpointTableIndex(t.byDef, defs)
	s.defensiveFeatures = snapshotCheckpointTableIndex(t.defensiveFeatures, features)
	t.checkpoint = s
}

// ValidateCheckpointBindings verifies the original construction and every
// retained table value without rebuilding, sorting, walking a live map, or
// invoking rules or workers (DESIGN_MULTIPLAYER §16.3.73). The returned tag is
// the original closed construction rule, not the manager's current rule.
// Root owns full frozen-content validation once per capture; this leaf checks
// only the referenced unit and feature identities against those admitted keys.
func (t *Table) ValidateCheckpointBindings(cat *content.Catalog, keys *content.CheckpointKeys) (uint8, error) {
	if t == nil || cat == nil || keys == nil || t.checkpoint == nil {
		return 0, executorCheckpointError("aikit.Table", "a table, catalog, admitted keys and construction snapshot")
	}
	s := t.checkpoint
	if s.table != t || s.catalog != cat || !s.rulesSupported {
		return 0, executorCheckpointError("aikit.Table", "the original table/catalog and supported construction rule")
	}
	if !checkpointTableInfosEqual(t.Units, s.units) || !checkpointTableInfosEqual(t.Capped, s.capped) {
		return 0, executorCheckpointError("aikit.Table", "unchanged ordered Units and Capped identities")
	}
	if !s.byKey.matches(t.byKey) || !s.byDef.matches(t.byDef) || !s.defensiveFeatures.matches(t.defensiveFeatures) {
		return 0, executorCheckpointError("aikit.Table", "unchanged complete table indexes")
	}
	for i, u := range s.units {
		if !sameCheckpointUnitInfo(u, &s.values[i]) {
			return 0, executorCheckpointError("aikit.Table.Units", "unchanged derived values and ordered build edges")
		}
		if _, err := keys.Unit(u.Def); err != nil {
			return 0, err
		}
		if u.FinishedFeature != nil {
			if _, err := keys.Feature(u.FinishedFeature); err != nil {
				return 0, err
			}
		}
	}
	return s.ruleKind, nil
}

func checkpointTableInfosEqual(a, b []*UnitInfo) bool {
	return (a == nil) == (b == nil) && slices.Equal(a, b)
}

func sameCheckpointUnitInfo(a, b *UnitInfo) bool {
	return a != nil && a.Index == b.Index && a.Key == b.Key && a.Def == b.Def && a.Side == b.Side && a.Role == b.Role && a.FinishedFeature == b.FinishedFeature &&
		a.Metal == b.Metal && a.Energy == b.Energy && a.BuildTime == b.BuildTime && a.Value == b.Value && a.HP == b.HP &&
		a.DPS == b.DPS && a.AirDPS == b.AirDPS && a.WaterDPS == b.WaterDPS && a.StunDPS == b.StunDPS && a.Range == b.Range &&
		a.Speed == b.Speed && a.Sight == b.Sight && a.Radar == b.Radar && a.BuildPower == b.BuildPower && a.BuildRange == b.BuildRange &&
		a.MetalMake == b.MetalMake && a.EnergyMake == b.EnergyMake && a.WindGen == b.WindGen && a.TidalGen == b.TidalGen && a.EnergyUse == b.EnergyUse &&
		a.MetalStore == b.MetalStore && a.EnergyStore == b.EnergyStore && a.FootX == b.FootX && a.FootZ == b.FootZ && a.Depth == b.Depth && a.Cap == b.Cap &&
		a.capSlot == b.capSlot && a.banned == b.banned && checkpointTableInfosEqual(a.Builds, b.Builds)
}
