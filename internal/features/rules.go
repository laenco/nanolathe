package features

import "fmt"

// Rules owns live-feature admission. Construction cannot own this decision:
// map population, fire and successor placement also allocate feature records.
// The session binds this stateless policy through its existing RuleSet
// (DESIGN_ECONOMY_CONSTRUCTION "Modern feature growth").
type Rules interface {
	ArenaLimit(cells int) int
}

type StrictRules struct{}
type CommunityRules struct{ StrictRules }
type ModernRules struct{ CommunityRules }

// ArenaLimit preserves the retail shared arena [05 R-FEAT-01 §2].
func (StrictRules) ArenaLimit(int) int { return FeatureAnimSlots }

// ArenaLimit permits one live record per map cell. Storage is allocated only
// when needed; there is no preallocated arena or eviction of older wrecks.
func (ModernRules) ArenaLimit(cells int) int { return cells }

var strictRules Rules = StrictRules{}

func (s *Service) arenaLimit() int {
	rules := s.Rules
	if rules == nil {
		rules = strictRules
	}
	cells := 0
	if s.Terrain != nil {
		cells = len(s.Terrain.Plot)
	}
	return rules.ArenaLimit(cells)
}

// ValidateRestoreCapacity refuses an expanded save before restoring any row.
// Sprite event restoration can otherwise retain a resting sprite when its
// event allocation refuses, which would silently truncate an expanded Modern
// image under Strict/Community. Valid retail images fit the original arena.
func (s *Service) ValidateRestoreCapacity(records int) error {
	if s == nil || records < 0 {
		return fmt.Errorf("nanolathe: feature restore capacity invalid: logical path save/Features, providers searched [feature pool rules], expected a present service and nonnegative record count")
	}
	if limit := s.arenaLimit(); records > limit {
		return fmt.Errorf("nanolathe: feature restore capacity exceeded (%d records): logical path save/Features, providers searched [feature pool rules], expected at most %d live records", records, limit)
	}
	return nil
}

// CheckpointRulesKind identifies reviewed stateless policies without invoking
// their methods. The session also attests the selected set and projection.
func CheckpointRulesKind(r Rules) (uint8, error) {
	switch v := r.(type) {
	case nil:
		return 0, nil
	case StrictRules:
		return 1, nil
	case *StrictRules:
		if v != nil {
			return 1, nil
		}
	case CommunityRules:
		return 2, nil
	case *CommunityRules:
		if v != nil {
			return 2, nil
		}
	case ModernRules:
		return 3, nil
	case *ModernRules:
		if v != nil {
			return 3, nil
		}
	}
	return 0, featureCheckpointError("features.Service.Rules", "nil or a nonnil reviewed stateless rule implementation")
}
