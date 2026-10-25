package session

import "github.com/nanolathe-gg/nanolathe/internal/visibility"

// setAlliedVisionTeams makes each team of a single-player skirmish one side
// for sight, the explored map and radar (docs/DESIGN_SESSIONS_AI_SAVE.md
// "Modern allied sight"), the vision-team mechanism Survival's survivors and
// online teams use (DESIGN_SURVIVAL §4.3, DESIGN_MULTIPLAYER §6.7).
//
// A team is read from the alliance table, not the setup's ally groups, so a
// load — which restores the table but not the groups [08 R-SKIR-01 §2] "Save
// persistence" — rebuilds the teams the fresh battle had. Skirmish alliances
// are mutual and fixed for the battle, since they come from equal group
// numbers and single-player play has no alliance command, so each player's
// mutual allies are exactly its team and the coverage reference counts stay
// balanced. It runs once, before any coverage is published. A player alone
// in its group, an observer and an absent slot join no team.
func (s *Session) setAlliedVisionTeams() {
	if s == nil || s.Vis == nil || s.Econ == nil {
		return
	}
	member := func(i int) bool {
		if i >= len(s.Econ.Players) {
			return false
		}
		p := &s.Econ.Players[i]
		return p.Exists && !p.IsObserver && p.ControllerState >= 1 && p.ControllerState <= 3
	}
	var placed [SkirmishMaxPlayers]bool
	for i := 0; i < SkirmishMaxPlayers; i++ {
		if placed[i] || !member(i) {
			continue
		}
		team := []visibility.PlayerID{visibility.PlayerID(i)}
		for j := i + 1; j < SkirmishMaxPlayers; j++ {
			if !placed[j] && member(j) && s.ownersAllied(i, j) && s.ownersAllied(j, i) {
				placed[j] = true
				team = append(team, visibility.PlayerID(j))
			}
		}
		if len(team) >= 2 {
			s.Vis.SetVisionTeam(team)
		}
	}
}
