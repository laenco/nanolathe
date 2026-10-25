package main

// The skirmish setup screen's visible player count
// (docs/DESIGN_INTERFACE_HUD_INPUT.md §2.6 "Player count"). Retail changes the
// number of rows only through the hidden *III..*X typed selector
// [07 R-FE-02 §10][08 R-SKIR-01 §1]; this button sets the same count, 2 to 8
// rows, and rebuilds the rows the same way.

import (
	"strconv"

	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
)

const (
	skirmishPlayersButton = "SkirmishPlayers"
	skirmishPlayersSource = "NANOLATHE_SKIRMISH_PLAYERS"
	// The button's range. The engine and the typed selector go to ten; the
	// button stops at eight (user decision 2026-10-18).
	skirmishPlayersMin = 2
	skirmishPlayersMax = 8
	// The slot under the player box the Survival screen gives its first
	// wave control (applySurvivalLayout).
	skirmishPlayersX, skirmishPlayersLabelY, skirmishPlayersY = 45, 290, 306
)

// installSkirmishPlayerCount appends the Players heading, cloned from the
// Location label, and the count button, a copy of row 0's Metal button.
func installSkirmishPlayerCount(window *gui.Window) {
	if window == nil {
		return
	}
	metal := window.GadgetIndex("Metal0")
	if metal < 0 {
		return
	}
	for i := range window.Gadgets {
		if window.Gadgets[i].Kind == gui.KindLabel && window.Gadgets[i].Text == "Location" {
			l := window.Gadgets[i]
			l.Text, l.SourceName = "Players", skirmishPlayersSource
			l.Rect.X, l.Rect.Y = skirmishPlayersX, skirmishPlayersLabelY
			window.Gadgets = append(window.Gadgets, l)
			break
		}
	}
	b := window.Gadgets[metal]
	b.Name, b.SourceName = skirmishPlayersButton, skirmishPlayersSource
	b.Rect.X, b.Rect.Y = skirmishPlayersX, skirmishPlayersY
	window.Gadgets = append(window.Gadgets, b)
}

// nextSkirmishPlayerCount steps the count one row, wrapping within the
// button's range; a count the typed selector set above it steps to the
// range's nearer end.
func nextSkirmishPlayerCount(n, delta int) int {
	switch {
	case n > skirmishPlayersMax:
		if delta < 0 {
			return skirmishPlayersMax
		}
		return skirmishPlayersMin
	case n < skirmishPlayersMin:
		return skirmishPlayersMin
	}
	return cycleInt(n, skirmishPlayersMin, skirmishPlayersMax, delta)
}

// stepSkirmishPlayerCount is the button's callback: forward on the left
// button, back on the right. Like the typed selector it saves the count and
// rebuilds the window, since the row builder lays out every row from it; the
// rows keep their choices in the setup.
func (g *gameShell) stepSkirmishPlayerCount(delta int) {
	g.setup.NumPlayers = nextSkirmishPlayerCount(g.setup.NumPlayers, delta)
	if g.selectedSlot >= g.setup.NumPlayers {
		g.selectedSlot = g.setup.NumPlayers - 1
	}
	g.fillShownRowResources()
	g.saveSettings()
	g.openMenuWithTokenFlush(modeMenuSkirmish, false)
}

// fillShownRowResources gives a shown row with no starting resources the
// default 1000 metal and energy. Zero is never a choice — the buttons' floor
// is 200 — but a row no settings file described has none, and battle entry
// would read it as the default anyway (SkirmishConfig.ApplyDefaults); this
// makes the screen say so when the count first shows the row
// [08 R-SKIR-01 §1] "Registry mirror".
func (g *gameShell) fillShownRowResources() {
	for i := 0; i < g.shownSkirmishRows(); i++ {
		p := &g.setup.Players[i]
		if p.Metal == 0 {
			p.Metal = session.SkirmishDefaultMetal
		}
		if p.Energy == 0 {
			p.Energy = session.SkirmishDefaultEnergy
		}
	}
}

// refreshSkirmishPlayerCount shows the count.
func (g *gameShell) refreshSkirmishPlayerCount(p *ui.Panel) {
	if g.survivalMenu {
		return
	}
	p.SetText(skirmishPlayersButton, strconv.Itoa(g.setup.NumPlayers))
	p.SetStatus(skirmishPlayersButton, 0)
	p.SetHelp(skirmishPlayersButton, "Click to add a player row; right click removes one (2 to 8 rows).")
}
