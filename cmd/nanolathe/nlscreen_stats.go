package main

import (
	"fmt"
	"image/color"
	"slices"
	"strconv"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// The mutator cards' numbers: what the factor does to three of the running
// content's own units, read from a clone of its catalog after
// Catalog.ApplyMutators, the transform every battle entry applies
// (docs/DESIGN_MODS_MUTATORS.md §6.4). The card's factor is applied alone, so
// the plate shows that mutator's effect and nothing else.

// nlStat is one mutator's plate: the value it scales and preferred stock examples.
type nlStat struct {
	label  string
	units  []string
	value  func(c *content.Catalog, u *content.UnitDef) (float64, bool)
	format func(float64) string
}

func weapon1(u *content.UnitDef) (*content.WeaponDef, bool) {
	for _, w := range []*content.WeaponDef{u.Weapon1Def, u.Weapon2Def, u.Weapon3Def} {
		if !content.IsWeaponInactive(w) && uint16(w.DamageDefault) > 0 {
			return w, true
		}
	}
	return nil, false
}

func formatInt(v float64) string { return strconv.FormatInt(int64(v), 10) }

var nlMutatorStats = map[string]nlStat{
	"buildSpeed": {label: "Build time", units: []string{"armfus", "armlab", "armbrtha", "corfus", "corlab"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			return float64(u.BuildTime), u.BuildTime > 0
		}, format: formatInt},
	"buildCost": {label: "Metal cost", units: []string{"armmex", "armfus", "armsilo", "cormex", "corfus"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			return float64(u.BuildCostMetal), u.BuildCostMetal > 0
		}, format: formatInt},
	"income": {label: "Energy made", units: []string{"armsolar", "armwin", "armfus", "corsolar", "corfus"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			// The mutator scales surplus above upkeep, including the negative-use
			// production arm (DESIGN_MODS_MUTATORS §6.5 "Income").
			v := u.EnergyMake - u.EnergyUse
			return v, v > 0
		},
		format: func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }},
	"salvage": {label: "Wreck metal", units: []string{"armbull", "corkrog", "armzeus", "corgol", "armpw"},
		value: func(c *content.Catalog, u *content.UnitDef) (float64, bool) {
			f := c.Features[content.CanonicalKey(u.Corpse)]
			if f == nil || f.Metal <= 0 {
				return 0, false
			}
			return float64(f.Metal), true
		}, format: formatInt},
	"health": {label: "Hit points", units: []string{"armpw", "armbull", "corkrog", "corak", "armzeus"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			return float64(u.MaxDamage), u.MaxDamage > 0
		}, format: formatInt},
	"damage": {label: "Weapon damage", units: []string{"armpw", "armbull", "armbrtha", "corak", "corgol"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			w, ok := weapon1(u)
			if !ok || w.DamageDefault <= 0 {
				return 0, false
			}
			return float64(uint16(w.DamageDefault)), true
		}, format: formatInt},
	"areaOfEffect": {label: "Blast radius", units: []string{"armbull", "armbrtha", "corgol", "armham", "corthud"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			w, ok := weapon1(u)
			if !ok || uint16(w.AreaOfEffect) <= 16 {
				return 0, false
			}
			return float64(uint16(w.AreaOfEffect)), true
		}, format: formatInt},
	"fireRate": {label: "Reload", units: []string{"armbull", "armbrtha", "armham", "corgol", "corthud"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			w, ok := weapon1(u)
			if !ok || uint16(w.ReloadTime) == 0 {
				return 0, false
			}
			return float64(uint16(w.ReloadTime)), true
		}, format: func(v float64) string { return strconv.FormatFloat(v/30, 'f', 2, 64) + " s" }},
	"unitSpeed": {label: "Top speed", units: []string{"armflash", "armpw", "armbull", "corak", "corgol"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			return float64(u.MaxVelocity) / 65536, u.MaxVelocity > 0
		}, format: func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }},
	"builderSpeed": {label: "Turn rate", units: []string{"armck", "armcv", "armcom", "corck", "corcv"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			// A mobile builder's turn rate is the agility the plate can show;
			// its build-animation timing has no definition field
			// (DESIGN_MODS_MUTATORS "Builder speed").
			return float64(uint16(u.TurnRate)), u.Builder && u.BMCode == 1 && uint16(u.TurnRate) > 0
		}, format: formatInt},
	"sight": {label: "Sight", units: []string{"armpw", "armbull", "armpeep", "corak", "corfink"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			return float64(u.SightDistance), u.SightDistance > 0
		}, format: formatInt},
	"radar": {label: "Radar range", units: []string{"armrad", "armarad", "armcom", "corrad", "corarad"},
		value: func(_ *content.Catalog, u *content.UnitDef) (float64, bool) {
			return float64(u.RadarDistance), u.RadarDistance > 0
		}, format: formatInt},
}

// nlStats holds the running content's catalog and the clones the cards have
// asked for.
type nlStats struct {
	base    *content.Catalog
	loading bool
	result  chan *content.Catalog
	mutated map[string]*content.Catalog
	roster  *nlRoster
}

// catalog returns the base catalog once it has compiled, starting the
// compile on first use; nil until then.
func (st *nlStats) catalog(cs *contentSet) *content.Catalog {
	if st.base != nil {
		return st.base
	}
	if !st.loading && cs != nil {
		st.loading = true
		st.result = make(chan *content.Catalog, 1)
		go func(ch chan *content.Catalog) {
			c, err := cs.nlPreviewCatalog()
			if err != nil {
				c = nil
			}
			ch <- c
		}(st.result)
	}
	select {
	case c := <-st.result:
		st.base = c
	default:
	}
	return st.base
}

func (st *nlStats) contentRoster() *nlRoster {
	if st.base == nil {
		return nil
	}
	if st.roster == nil {
		st.roster = newNLRoster(st.base, nil)
	}
	return st.roster
}

// statUnitNames uses the same capability resolver as the scenes, then fills
// missing examples from the active content. A unit must carry the scaled value;
// a missing stock name never leaves a mod's plate empty.
func (st *nlStats) statUnitNames(stat nlStat) []string {
	r := st.contentRoster()
	if r == nil {
		return nil
	}
	var names []string
	add := func(name string) {
		if u, ok := st.base.Unit(name); ok && u != nil {
			if _, ok := stat.value(st.base, u); ok && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	for _, preferred := range stat.units {
		add(r.resolve(preferred))
	}
	for _, name := range r.keys {
		if len(names) >= 3 {
			break
		}
		add(name)
	}
	return names
}

// with returns the base catalog with one mutator applied, cached.
func (st *nlStats) with(key string, f content.Factor) *content.Catalog {
	id := key + "=" + f.String()
	if c, ok := st.mutated[id]; ok {
		return c
	}
	var m content.Mutators
	if m.SetFactor(key, f) != nil {
		return nil
	}
	c := st.base.Clone()
	if c.ApplyMutators(m) != nil {
		c = nil
	}
	if st.mutated == nil {
		st.mutated = map[string]*content.Catalog{}
	}
	st.mutated[id] = c
	return c
}

// heroMutatorStats draws the plate under a mutator card's description.
func (s *nlScreen) heroMutatorStats(screen *ebiten.Image, key string, x, y, a float64) float64 {
	stat, ok := nlMutatorStats[key]
	g := s.shell()
	if !ok || g == nil {
		return y
	}
	u := s.u()
	base := s.stats.catalog(g.cs)
	names := s.art.pictureNames()
	if base == nil || names == nil {
		s.fonts.Body.Draw(screen, "Reading this content's units…", x, y+14*u, screenkit.Style{Size: 11 * u, Top: alphaC(nlDim, a)})
		return y + 26*u
	}
	f, _ := s.draft.mutators.Factor(key)
	mutated := base
	if !f.IsIdentity() {
		if mutated = s.stats.with(key, f); mutated == nil {
			return y
		}
	}
	df, bf := s.fonts.Display, s.fonts.Body
	df.Draw(screen, "In this content", x, y+12*u, screenkit.Style{Size: 11 * u, Tracking: 0.24, Top: alphaC(nlKicker, a), Upper: true})
	y += 22 * u
	shown := 0
	for _, name := range names.stats[key] {
		if shown == 3 {
			break
		}
		bu, ok := base.Unit(name)
		mu, ok2 := mutated.Unit(name)
		if !ok || !ok2 {
			continue
		}
		before, ok := stat.value(base, bu)
		after, ok2 := stat.value(mutated, mu)
		if !ok || !ok2 {
			continue
		}
		row := screenkit.Rect{X: x, Y: y, W: 560 * u, H: 40 * u}
		screenkit.Fill(screen, row, color.RGBA{8, 12, 8, uint8(170 * a)})
		pic := screenkit.Rect{X: row.X + 3*u, Y: row.Y + 3*u, W: 34 * u, H: 34 * u}
		if img := s.art.pic(name); img != nil {
			screenkit.Image(screen, img, pic, a, false)
		}
		label := bu.Name
		if label == "" {
			label = name
		}
		st := screenkit.Style{Size: 12 * u, Top: alphaC(nlBody, a), Shadow: 0.1}
		bf.Draw(screen, label, row.X+48*u, row.Y+25*u, st)
		st.Top, st.Size = alphaC(nlDim, a), 11*u
		// The stat's name ends before its values in any language.
		bf.Draw(screen, stat.label, row.X+230*u, row.Y+25*u, fitStyle(bf, stat.label, st, 124*u, 8*u))
		vx := row.X + 360*u
		st.Top, st.Size = alphaC(nlDim, a), 12*u
		vx += bf.Draw(screen, stat.format(before), vx, row.Y+25*u, st) + 10*u
		s.chevron(screen, vx+3*u, row.Y+21*u, 1, 0, 3.5*u, alphaC(nlGreen, a))
		st.Top = alphaC(nlGreenText, a)
		if after == before {
			st.Top = alphaC(nlBody, a)
		}
		bf.Draw(screen, stat.format(after), vx+16*u, row.Y+25*u, st)
		y += 44 * u
		shown++
	}
	if shown == 0 {
		bf.Draw(screen, fmt.Sprintf("No unit here shows %s.", stat.label), x, y+12*u, screenkit.Style{Size: 11 * u, Top: alphaC(nlDim, a)})
		y += 24 * u
	}
	return y
}
