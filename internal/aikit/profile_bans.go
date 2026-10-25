package aikit

import "github.com/nanolathe-gg/nanolathe/internal/content"

// economyRoles are the producers a profile ban keeps from the Modern AI:
// extractors, energy producers, metal makers and storage
// (docs/DESIGN_SESSIONS_AI_SAVE.md "Modern AI profile bans").
const economyRoles = RoleExtractor | RoleEnergy | RoleMetalMaker | RoleStorage

// banForbidden marks the economy producers the content forbids to every
// computer player, whose default AI profile (ai/default.txt) limits them to
// none under every difficulty, and takes them out of every build list. The
// Modern AI then plays as if the content's menus did not offer them, as it
// does a definition a restriction of 0 removes from the battle catalog.
// Content can ship a unit only so that old recordings play and forbid it
// that way, as TA Unofficial Patch 3.9.02 does with the McnTerra-NS units,
// whose output swamps every other economic choice. A map's own profile and
// one plan's limits tune the Classic planner for that map and that
// difficulty; the Modern AI reads the map itself and takes its difficulty
// from its persona, so neither bans a unit here.
func (t *Table) banForbidden(cat *content.Catalog) {
	p := cat.AIProfiles["default"]
	if p == nil {
		return
	}
	banned := false
	for _, u := range t.Units {
		if u.Def != nil && u.Role&economyRoles != 0 && forbiddenEverywhere(p, u.Def) {
			u.banned, banned = true, true
		}
	}
	if !banned {
		return
	}
	for _, u := range t.Units {
		kept := u.Builds[:0]
		for _, b := range u.Builds {
			if !b.banned {
				kept = append(kept, b)
			}
		}
		u.Builds = kept
	}
}

// forbiddenEverywhere reports whether profile p limits def to none under
// each difficulty: for each of easy, medium and hard, that difficulty's plan
// or the any plan names it, and every one of them that names it limits it to
// 0. The limit directive applies only to computer slots [08 R-AI-01 §12],
// which a Modern player always is.
func forbiddenEverywhere(p *content.AIProfile, def *content.UnitDef) bool {
	key := content.CanonicalKey(def.UnitName)
	anyPlan := p.Plans["any"]
	for _, d := range [...]string{"easy", "medium", "hard"} {
		named := false
		for _, plan := range [...]*content.AIPlan{p.Plans[d], anyPlan} {
			if plan == nil {
				continue
			}
			if v, ok := plan.Limits[key]; ok {
				if v != 0 {
					return false
				}
				named = true
			}
		}
		if !named {
			return false
		}
	}
	return true
}
