package session

import (
	"fmt"

	"github.com/nanolathe-gg/nanolathe/internal/ai"
	"github.com/nanolathe-gg/nanolathe/internal/combat"
	"github.com/nanolathe-gg/nanolathe/internal/community"
	"github.com/nanolathe-gg/nanolathe/internal/construction"
	"github.com/nanolathe-gg/nanolathe/internal/economy"
	"github.com/nanolathe-gg/nanolathe/internal/features"
	"github.com/nanolathe-gg/nanolathe/internal/gameplay"
	"github.com/nanolathe-gg/nanolathe/internal/movement"
	"github.com/nanolathe-gg/nanolathe/internal/orders"
	"github.com/nanolathe-gg/nanolathe/internal/path"
	"github.com/nanolathe-gg/nanolathe/internal/visibility"
)

// checkpointRuleContext carries entry facts supplied by whole-session admission,
// not inferred from the mutable current selection (DESIGN_MULTIPLAYER §16.3.45).
type checkpointRuleContext struct {
	entryMode        gameplay.Mode
	entryCommunity   community.Features
	defaultCommunity community.Features
}

// validateCheckpointRuleBindings checks concrete policy leaves and retained
// feature projections without resolving, rebinding or invoking gameplay. Required
// owner presence, aliases and callback admission belong to whole-session preflight.
func (s *Session) validateCheckpointRuleBindings(c checkpointRuleContext) error {
	if s == nil {
		return checkpointRuleBindingError("session")
	}
	var ruleKind, kernelKind, plannerKind, sharedKind uint8
	switch s.Rules.Base {
	case gameplay.Strict31:
		ruleKind, kernelKind, plannerKind, sharedKind = 1, 1, 1, 1
	case gameplay.Community39:
		ruleKind, kernelKind, plannerKind, sharedKind = 2, 1, 1, 2
	case gameplay.Modern:
		ruleKind, kernelKind, plannerKind, sharedKind = 3, 3, 2, 2
	default:
		return checkpointRuleBindingError("session.Rules.Base")
	}
	if s.Rules.Name != string(s.Rules.Base) {
		return checkpointRuleBindingError("session.Rules.Name")
	}
	if s.Gameplay != s.Rules.Base {
		return checkpointRuleBindingError("session.Gameplay")
	}
	if s.Rules.Features != (community.Overrides{}) {
		return checkpointRuleBindingError("session.Rules.Features")
	}
	if k, err := visibility.CheckpointRulesKind(s.Rules.Visibility); err != nil || k != ruleKind {
		return checkpointRuleBindingError("session.Rules.Visibility")
	}
	if k, err := movement.CheckpointRulesKind(s.Rules.Movement); err != nil || k != ruleKind {
		return checkpointRuleBindingError("session.Rules.Movement")
	}
	if k, err := orders.CheckpointRulesKind(s.Rules.Orders); err != nil || k != ruleKind {
		return checkpointRuleBindingError("session.Rules.Orders")
	}
	if k, err := construction.CheckpointRulesKind(s.Rules.Construction); err != nil || k != ruleKind {
		return checkpointRuleBindingError("session.Rules.Construction")
	}
	if k, err := combat.CheckpointRulesKind(s.Rules.Combat); err != nil || k != ruleKind {
		return checkpointRuleBindingError("session.Rules.Combat")
	}
	if k, err := features.CheckpointRulesKind(s.Rules.FeaturePool); err != nil || k != ruleKind {
		return checkpointRuleBindingError("session.Rules.FeaturePool")
	}
	if s.Features != nil {
		if k, err := features.CheckpointRulesKind(s.Features.Rules); err != nil || k != ruleKind {
			return checkpointRuleBindingError("session.Features.Rules")
		}
	}
	if k, err := economy.CheckpointRulesKind(s.Rules.Economy); s.Rules.Economy == nil || err != nil || k != ruleKind {
		return checkpointRuleBindingError("session.Rules.Economy")
	}
	// The standalone kernel classifier permits a nil retail fallback; an
	// admitted reserved set must retain its explicit bound implementation.
	if k, err := path.CheckpointKernelKind(s.Rules.Path); s.Rules.Path == nil || err != nil || k != kernelKind {
		return checkpointRuleBindingError("session.Rules.Path")
	}
	if k, err := ai.CheckpointPlannerKind(s.Rules.Planner); err != nil || k != plannerKind {
		return checkpointRuleBindingError("session.Rules.Planner")
	}
	if checkpointUnitLimitKind(s.Rules.UnitLimit) != sharedKind {
		return checkpointRuleBindingError("session.Rules.UnitLimit")
	}
	if checkpointScriptPortsKind(s.Rules.ScriptPorts) != ruleKind {
		return checkpointRuleBindingError("session.Rules.ScriptPorts")
	}
	if checkpointComputerIncomeKind(s.Rules.ComputerIncome) != ruleKind {
		return checkpointRuleBindingError("session.Rules.ComputerIncome")
	}
	if checkpointSeatsKind(s.Rules.Seats) != sharedKind {
		return checkpointRuleBindingError("session.Rules.Seats")
	}

	if len(s.CommunitySources.Content) != 0 {
		return checkpointRuleBindingError("session.CommunitySources.Content")
	}
	if s.CommunitySources.Player != (community.Overrides{}) {
		return checkpointRuleBindingError("session.CommunitySources.Player")
	}
	switch c.entryMode {
	case gameplay.Strict31:
		if c.entryCommunity != (community.Features{}) {
			return checkpointRuleBindingError("session.entryCommunity")
		}
		if len(s.CommunitySources.CommandLine) != 0 {
			return checkpointRuleBindingError("session.CommunitySources.CommandLine")
		}
	case gameplay.Community39, gameplay.Modern:
		if len(s.CommunitySources.CommandLine) != 1 {
			return checkpointRuleBindingError("session.CommunitySources.CommandLine")
		}
		actual := s.CommunitySources.CommandLine[0]
		if actual.Base == nil || actual != (community.Overrides{Base: actual.Base}) || *actual.Base != c.entryCommunity {
			return checkpointRuleBindingError("session.CommunitySources.CommandLine[0]")
		}
	default:
		return checkpointRuleBindingError("session.entryMode")
	}
	if s.EntryCommunity != c.entryCommunity {
		return checkpointRuleBindingError("session.EntryCommunity")
	}
	f := c.entryCommunity
	if s.Gameplay == gameplay.Strict31 {
		f = community.Features{}
	} else if c.entryMode == gameplay.Strict31 {
		f = c.defaultCommunity
	}
	if s.Community != f {
		return checkpointRuleBindingError("session.Community")
	}

	// These literals intentionally enumerate the current projectCommunity,
	// orderCommunity and aiCommunity contracts. A new projected field requires
	// a corresponding checkpoint contract review, never a projection call.
	if s.Vis != nil {
		if k, err := visibility.CheckpointRulesKind(s.Vis.Rules); err != nil || k != ruleKind {
			return checkpointRuleBindingError("session.Vis.Rules")
		}
		if s.Vis.Community.AlliedJammingIgnored != f.AlliedJammingIgnored ||
			s.Vis.Community.OffMapAircraftMarginTiles != f.OffMapAircraftMarginTiles {
			return checkpointRuleBindingError("session.Vis.Community")
		}
	}
	if s.Combat != nil {
		if k, err := combat.CheckpointRulesKind(s.Combat.Rules); err != nil || k != ruleKind {
			return checkpointRuleBindingError("session.Combat.Rules")
		}
		want := community.Features{
			AreaDamageOverflow: f.AreaDamageOverflow, AreaDamageDedupCap: f.AreaDamageDedupCap,
			TransportedExplosions: f.TransportedExplosions, BuildWeaponSlotGuard: f.BuildWeaponSlotGuard,
			AntinukeCircularCoverage: f.AntinukeCircularCoverage, WeaponTargetKeys: f.WeaponTargetKeys,
			Veterancy: f.Veterancy, AirCorpseFall: f.AirCorpseFall,
			OffMapAircraftMarginTiles: f.OffMapAircraftMarginTiles, TargetLockRelease: f.TargetLockRelease,
		}
		if s.Combat.Community != want {
			return checkpointRuleBindingError("session.Combat.Community")
		}
	}
	if s.Build != nil {
		if k, err := construction.CheckpointRulesKind(s.Build.Rules); err != nil || k != ruleKind {
			return checkpointRuleBindingError("session.Build.Rules")
		}
		want := community.Features{
			ConstructionKickout: f.ConstructionKickout, StructureRotation: f.StructureRotation,
			ResurrectionFinalization: f.ResurrectionFinalization, RepairRate: f.RepairRate,
			HealTimeBitmask: f.HealTimeBitmask,
		}
		if s.Build.Community != want {
			return checkpointRuleBindingError("session.Build.Community")
		}
		if b := s.Build.OrderBinding; b != nil {
			if k, err := orders.CheckpointRulesKind(b.Rules); err != nil || k != ruleKind {
				return checkpointRuleBindingError("session.Build.OrderBinding.Rules")
			}
			want := community.Features{
				GuardingBuildersHold: f.GuardingBuildersHold, PatrollingBuilderFilters: f.PatrollingBuilderFilters,
				ReclaimToggleKeepsBuild: f.ReclaimToggleKeepsBuild, ConstructionKickout: f.ConstructionKickout,
				WeaponTargetKeys: f.WeaponTargetKeys, Veterancy: f.Veterancy, BuildWeaponSlotGuard: f.BuildWeaponSlotGuard,
				WorkingWeaponsAutonomous: f.WorkingWeaponsAutonomous, AttackSingleSlotTake: f.AttackSingleSlotTake,
				ResurrectionTextFix: f.ResurrectionTextFix,
			}
			if b.Community != want {
				return checkpointRuleBindingError("session.Build.OrderBinding.Community")
			}
		}
	}
	if s.Movement != nil {
		if k, err := movement.CheckpointRulesKind(s.Movement.Rules); err != nil || k != ruleKind {
			return checkpointRuleBindingError("session.Movement.Rules")
		}
		if k, err := path.CheckpointKernelKind(s.Movement.Kernel); s.Movement.Kernel == nil || err != nil || k != kernelKind {
			return checkpointRuleBindingError("session.Movement.Kernel")
		}
		if s.Movement.Community != (community.Features{GridClaimTieBreak: f.GridClaimTieBreak}) {
			return checkpointRuleBindingError("session.Movement.Community")
		}
	}
	if s.Econ != nil && s.Econ.Community != (community.Features{AIDifficultyIncome: f.AIDifficultyIncome}) {
		return checkpointRuleBindingError("session.Econ.Community")
	}
	if s.Econ != nil {
		if k, err := economy.CheckpointRulesKind(s.Econ.Rules); s.Econ.Rules == nil || err != nil || k != ruleKind {
			return checkpointRuleBindingError("session.Econ.Rules")
		}
	}
	wantAI := community.Features{
		AIStockpileProducts: f.AIStockpileProducts, AIApplianceEnergy: f.AIApplianceEnergy,
		AIBuilderStopThreshold: f.AIBuilderStopThreshold, AIBuilderPlacementLimit: f.AIBuilderPlacementLimit,
	}
	for player, m := range s.AI {
		if m == nil {
			continue
		}
		if k, err := construction.CheckpointRulesKind(m.ConstructionRules); err != nil || k != ruleKind {
			return checkpointRuleBindingError(fmt.Sprintf("session.AI[%d].ConstructionRules", player))
		}
		if m.Community != wantAI {
			return checkpointRuleBindingError(fmt.Sprintf("session.AI[%d].Community", player))
		}
		switch m.Controller {
		case ai.ControllerClassic:
			if k, err := ai.CheckpointPlannerKind(m.Planner); err != nil || k != plannerKind {
				return checkpointRuleBindingError(fmt.Sprintf("session.AI[%d].Planner", player))
			}
		case ai.ControllerModern:
			if !s.checkpointModernPlannerMatches(m.Planner) {
				return checkpointRuleBindingError(fmt.Sprintf("session.AI[%d].Planner", player))
			}
		default:
			return checkpointRuleBindingError(fmt.Sprintf("session.AI[%d].Controller", player))
		}
	}
	return nil
}

func checkpointRuleBindingError(field string) error {
	return fmt.Errorf("nanolathe: checkpoint rule preflight failed: logical path %s, providers searched [reserved session rules and entry receipt], expected the reserved concrete policy and entry-consistent feature projection", field)
}

// Zero denotes an absent, typed-nil or unreviewed implementation.
func checkpointUnitLimitKind(r UnitLimitRules) uint8 {
	switch v := r.(type) {
	case StrictUnitLimit:
		return 1
	case *StrictUnitLimit:
		if v != nil {
			return 1
		}
	case ModernUnitLimit:
		return 2
	case *ModernUnitLimit:
		if v != nil {
			return 2
		}
	}
	return 0
}

// Zero denotes an absent, typed-nil or unreviewed implementation.
func checkpointScriptPortsKind(r ScriptPortRules) uint8 {
	switch v := r.(type) {
	case StrictScriptPorts:
		return 1
	case *StrictScriptPorts:
		if v != nil {
			return 1
		}
	case CommunityScriptPorts:
		return 2
	case *CommunityScriptPorts:
		if v != nil {
			return 2
		}
	case ModernScriptPorts:
		return 3
	case *ModernScriptPorts:
		if v != nil {
			return 3
		}
	}
	return 0
}

// Zero denotes an absent, typed-nil or unreviewed implementation.
func checkpointComputerIncomeKind(r ComputerIncomeRules) uint8 {
	switch v := r.(type) {
	case StrictComputerIncome:
		return 1
	case *StrictComputerIncome:
		if v != nil {
			return 1
		}
	case CommunityComputerIncome:
		return 2
	case *CommunityComputerIncome:
		if v != nil {
			return 2
		}
	case ModernComputerIncome:
		return 3
	case *ModernComputerIncome:
		if v != nil {
			return 3
		}
	}
	return 0
}

// Zero denotes an absent, typed-nil or unreviewed implementation.
func checkpointSeatsKind(r SeatRules) uint8 {
	switch v := r.(type) {
	case StrictSeats:
		return 1
	case *StrictSeats:
		if v != nil {
			return 1
		}
	case ModernSeats:
		return 2
	case *ModernSeats:
		if v != nil {
			return 2
		}
	}
	return 0
}
