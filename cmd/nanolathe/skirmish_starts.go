package main

// Chosen start positions on the skirmish setup screen
// (docs/DESIGN_INTERFACE_HUD_INPUT.md §2.6 "Start positions",
// docs/DESIGN_SESSIONS_AI_SAVE.md "Modern chosen start positions"). Under
// Modern and Community rules a Place Starts button under the player box
// opens a window over the setup screen: pick a player, click the map where
// its commander should begin, and a right click puts it back on Auto.
// Battle entry moves each chosen point to the nearest cell the commander can
// stand on. The map picker's preview marks the map's own start positions.

import (
	"strconv"
	"strings"

	"github.com/nanolathe-gg/nanolathe/formats"
	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/gui"
	"github.com/nanolathe-gg/nanolathe/internal/input"
	"github.com/nanolathe-gg/nanolathe/internal/mission"
	"github.com/nanolathe-gg/nanolathe/internal/session"
	"github.com/nanolathe-gg/nanolathe/internal/ui"
)

const (
	placeStartsButton = "PlaceStarts"
	placeStartsLabel  = "PLACESTARTSLABEL"
	placeStartsSource = "NANOLATHE_PLACE_STARTS"
	// The third slot under the player box, beside Players and Allied Sight.
	placeStartsX, placeStartsLabelY, placeStartsY = 305, 290, 306
)

// installSkirmishPlaceStarts appends the Start Positions heading, cloned from
// the Location label, and the Place Starts button, a copy of Select Map.
func installSkirmishPlaceStarts(window *gui.Window) {
	if window == nil {
		return
	}
	selectMap := window.GadgetIndex("SelectMap")
	if selectMap < 0 {
		return
	}
	for i := range window.Gadgets {
		if window.Gadgets[i].Kind == gui.KindLabel && window.Gadgets[i].Text == "Location" {
			l := window.Gadgets[i]
			l.Name, l.SourceName, l.Text = placeStartsLabel, placeStartsSource, "Start Positions"
			l.Rect.X, l.Rect.Y = placeStartsX, placeStartsLabelY
			window.Gadgets = append(window.Gadgets, l)
			break
		}
	}
	b := window.Gadgets[selectMap]
	b.Name, b.SourceName, b.Art, b.QuickKey = placeStartsButton, placeStartsSource, "SelectMap", 0
	b.Rect.X, b.Rect.Y = placeStartsX, placeStartsY
	window.Gadgets = append(window.Gadgets, b)
}

// placerRows are the shown rows that are players, in row order.
func (g *gameShell) placerRows() []int {
	var rows []int
	for i := 0; i < g.shownSkirmishRows(); i++ {
		if g.retailControllers[i] != 0 {
			rows = append(rows, i)
		}
	}
	return rows
}

// placeStartsCaption is the button's text: Auto, or how many players have a
// chosen point.
func (g *gameShell) placeStartsCaption() string {
	placed := 0
	for _, row := range g.placerRows() {
		if g.setup.Players[row].Start.Set {
			placed++
		}
	}
	if placed == 0 {
		return "Auto"
	}
	return strconv.Itoa(placed) + " Placed"
}

// refreshSkirmishPlaceStarts shows the button where the selected rules honour
// chosen starts; Strict 3.1 and the Survival screen have none.
func (g *gameShell) refreshSkirmishPlaceStarts(p *ui.Panel) {
	offered := !g.survivalMenu && g.setupSeatRules().ChosenStarts()
	p.SetActive(placeStartsButton, offered)
	p.SetActive(placeStartsLabel, offered)
	if !offered {
		return
	}
	p.SetText(placeStartsButton, g.placeStartsCaption())
	p.SetHelp(placeStartsButton, "Click to place each player's commander on the map. Players left on Auto follow the Location setting.")
}

// clearChosenStarts drops every row's chosen point; a point belongs to the
// map it was placed on, so a new map clears them.
func (g *gameShell) clearChosenStarts() {
	for i := range g.setup.Players {
		g.setup.Players[i].Start = session.StartPoint{}
	}
}

// setSetupMap makes name the setup's map. Every path that changes the map
// goes through it, so chosen points never outlive their map: a different
// map clears them, and the same map, in any letter case, keeps them.
func (g *gameShell) setSetupMap(name string) {
	if !strings.EqualFold(g.setup.MapName, name) {
		g.clearChosenStarts()
	}
	g.setup.MapName = name
}

// startPlacer is the open Place Starts window. selected is a shown row, or
// -1; pressed is the button a held left press began on.
type startPlacer struct {
	selected int
	pressed  int
	canvas   []byte
	canvasOf string
}

const (
	placerPressNone = iota
	placerPressClear
	placerPressDone
)

// The window's fixed layout on the 640×480 menu canvas.
var (
	placerPanel = gui.Rect{X: 30, Y: 24, W: 580, H: 432}
	placerMap   = gui.Rect{X: 236, Y: 52, W: 360, H: 360}
	placerClear = gui.Rect{X: 46, Y: 386, W: 80, H: 20}
	placerDone  = gui.Rect{X: 142, Y: 386, W: 80, H: 20}
)

const (
	// Twenty-pixel rows use the common button artwork's height, with enough
	// space below ten players for both instructions above the action buttons.
	placerRowX, placerRowY, placerRowW, placerRowH, placerRowPitch = 46, 56, 176, 20, 28
	placerMarker                                                   = 14
)

func placerRowRect(i int) gui.Rect {
	return gui.Rect{X: placerRowX, Y: int32(placerRowY + i*placerRowPitch), W: placerRowW, H: placerRowH}
}

// openStartPlacer opens the window on the first player without a point.
func (g *gameShell) openStartPlacer() {
	d := g.mapDataFor(g.setup.MapName)
	if d == nil || d.tnt == nil {
		reportRetailMessageError(g.showRetailMessage("The terrain for the selected map does not exist."))
		return
	}
	rows := g.placerRows()
	pl := &startPlacer{selected: -1}
	for _, row := range rows {
		if !g.setup.Players[row].Start.Set {
			pl.selected = row
			break
		}
	}
	if pl.selected < 0 && len(rows) > 0 {
		pl.selected = rows[0]
	}
	g.startPlacer = pl
}

// closeStartPlacer closes the window, keeping the points, and saves them.
func (g *gameShell) closeStartPlacer() {
	g.startPlacer = nil
	g.saveSettings()
	g.refreshRetailPanel()
}

// placerProjection fits the map's usable terrain, Width×16−32 by
// Height×16−128 world pixels, into the map box, centred, as the map
// picker's preview does (retailMapPreviewCanvas).
func placerProjection(d *retailMapData, box gui.Rect) (inner gui.Rect, ew, eh int64, ok bool) {
	if d == nil || d.tnt == nil {
		return gui.Rect{}, 0, 0, false
	}
	ew, eh = int64(d.tnt.Width)*16-32, int64(d.tnt.Height)*16-128
	if ew <= 0 || eh <= 0 {
		return gui.Rect{}, 0, 0, false
	}
	w, h := int64(box.W), int64(box.H)
	dstW, dstH, x0, y0 := w, h, int64(0), int64(0)
	if ew < eh {
		dstW = ew * w / eh
		x0 = (w - dstW) / 2
	} else {
		dstH = eh * h / ew
		y0 = (h - dstH) / 2
	}
	return gui.Rect{X: box.X + int32(x0), Y: box.Y + int32(y0), W: int32(dstW), H: int32(dstH)}, ew, eh, dstW > 0 && dstH > 0
}

// placerScreen is a world point's position in the map box.
func placerScreen(inner gui.Rect, ew, eh int64, x, z int32) (int32, int32) {
	return inner.X + int32(int64(x)*int64(inner.W)/ew), inner.Y + int32(int64(z)*int64(inner.H)/eh)
}

// placerWorld is the world point under a position in the map box, clamped to
// the usable terrain.
func placerWorld(inner gui.Rect, ew, eh int64, sx, sy int32) (int32, int32) {
	x := min(max(int64(sx-inner.X)*ew/int64(inner.W), 0), ew-1)
	z := min(max(int64(sy-inner.Y)*eh/int64(inner.H), 0), eh-1)
	return int32(x), int32(z)
}

// serviceStartPlacer takes every key and pointer event while the window is
// open, so nothing underneath reacts. Escape or Enter closes it. A left
// click on a player selects it; on the map it puts the selected player
// there and moves on to the next player without a point. A right click on a
// player or its marker puts it back on Auto. Clear All and Done act on
// release over the button they were pressed on.
func (g *gameShell) serviceStartPlacer(in *input.State) {
	pl := g.startPlacer
	if pl == nil || in == nil {
		return
	}
	tokens := in.PeekTokens()
	in.DiscardTokens(len(tokens))
	for _, tok := range tokens {
		if tok.Kind == input.TokenEdit && (tok.Key == input.KeyEscape || tok.Key == input.KeyEnter) {
			g.closeStartPlacer()
			return
		}
	}
	d := g.mapDataFor(g.setup.MapName)
	inner, ew, eh, mapOK := placerProjection(d, placerMap)
	rows := g.placerRows()
	frame := pointerFrame(in, nil, false)
	for _, ev := range frame.PointerEvents {
		x, y := ev.X, ev.Y
		switch ev.Kind {
		case input.LeftDown, input.LeftDoubleClick:
			pl.pressed = placerPressNone
			switch {
			case pointInRect(x, y, placerClear):
				pl.pressed = placerPressClear
			case pointInRect(x, y, placerDone):
				pl.pressed = placerPressDone
			case mapOK && pointInRect(x, y, inner):
				if pl.selected >= 0 {
					wx, wz := placerWorld(inner, ew, eh, x, y)
					g.setup.Players[pl.selected].Start = session.StartPoint{Set: true, X: wx, Z: wz}
					pl.selected = g.nextUnplacedRow(rows, pl.selected)
				}
			default:
				for i, row := range rows {
					if pointInRect(x, y, placerRowRect(i)) {
						pl.selected = row
					}
				}
			}
		case input.LeftUp:
			pressed := pl.pressed
			pl.pressed = placerPressNone
			switch {
			case pressed == placerPressClear && pointInRect(x, y, placerClear):
				g.clearChosenStarts()
				if len(rows) > 0 {
					pl.selected = rows[0]
				}
			case pressed == placerPressDone && pointInRect(x, y, placerDone):
				g.closeStartPlacer()
				return
			}
		case input.RightDown, input.RightDoubleClick:
			for i, row := range rows {
				if pointInRect(x, y, placerRowRect(i)) {
					g.setup.Players[row].Start = session.StartPoint{}
				}
			}
			if mapOK && pointInRect(x, y, inner) {
				for _, row := range rows {
					p := g.setup.Players[row].Start
					if !p.Set {
						continue
					}
					mx, my := placerScreen(inner, ew, eh, p.X, p.Z)
					if placerAbs(x-mx) <= placerMarker/2+2 && placerAbs(y-my) <= placerMarker/2+2 {
						g.setup.Players[row].Start = session.StartPoint{}
					}
				}
			}
		}
	}
}

// nextUnplacedRow is the next player after row, in row order and wrapping,
// that has no chosen point; row itself when every player has one.
func (g *gameShell) nextUnplacedRow(rows []int, row int) int {
	at := 0
	for i, r := range rows {
		if r == row {
			at = i
		}
	}
	for step := 1; step <= len(rows); step++ {
		next := rows[(at+step)%len(rows)]
		if !g.setup.Players[next].Start.Set {
			return next
		}
	}
	return row
}

func placerAbs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// drawStartPlacer paints the window over the dimmed setup screen: the
// players with their logos and choices, the map with its authored starts as
// grey dots and each placed player's logo, the selected player outlined,
// and the two buttons.
func (g *gameShell) drawStartPlacer(c *client.Client) {
	pl := g.startPlacer
	p := g.activePanel()
	if pl == nil || p == nil || p.Window == nil {
		return
	}
	if g.assets != nil && g.assets.pal != nil {
		c.UIShadeRect(g.assets.pal, 0, 0, 640, 480, 8)
	}
	raised := retailButtonVerdict(gui.Gadget{}, 0, 0, 0, 0, false)
	sunk := retailButtonVerdict(gui.Gadget{}, 0, 0, 1, 0, false)
	bevel := func(r gui.Rect, down bool) {
		v := raised
		if down {
			v = sunk
		}
		drawGUIBevel(c, r, g.guiColor(v.top), g.guiColor(v.bot), g.guiColor(v.fill))
	}
	var common *formats.GAF
	if g.assets != nil {
		common = g.assets.common
	}
	// Use the same authored BackTile and BUTTONS0 frames as retail dialogs.
	// Only player rows are widened to fit their name and placement status;
	// the action buttons keep the original 80-by-20 artwork dimensions.
	drawWindowPanel(c, &gui.Window{Rect: placerPanel}, nil, common, g.guiColor)
	button := func(r gui.Rect, down bool) {
		state := 0
		if down {
			state = 1
		}
		if art := g.retailButtonArt(gui.Gadget{Kind: gui.KindButton, Rect: r}, state, 0, false); art != nil {
			c.UIBlitFrameScaled(art, int(r.X), int(r.Y), int(r.W), int(r.H))
		} else {
			bevel(r, down)
		}
	}
	font := g.windowGadgetFont(p, p.Window.Gadgets[max(p.Index("HELPTEXT"), 0)])
	measure, metric := g.retailTextMetrics(font)
	text := func(s string, x, y, w int) {
		g.drawRetailStringSelected(c, s, x, y, w, g.guiColor(15), 0, font)
	}
	text("Start Positions", int(placerPanel.X)+16, int(placerPanel.Y)+10, 200)
	logo := func(color int, x, y, size int) {
		if f := g.gadgetArt(gui.Gadget{Kind: gui.KindSurface, Art: "32xlogos"}, color); f != nil {
			c.UIBlitFrameScaled(f, x, y, size, size)
		}
	}
	rows := g.placerRows()
	for i, row := range rows {
		r := placerRowRect(i)
		button(r, row == pl.selected)
		player := g.setup.Players[row]
		logo(player.Color, int(r.X)+5, int(r.Y)+(int(r.H)-placerMarker)/2, placerMarker)
		name := "Player"
		if g.retailControllers[row] == 2 {
			name = computerAICaption(player.AI)
		}
		text(name, int(r.X)+24, int(r.Y)+(int(r.H)-metric)/2, 100)
		state := "Auto"
		if player.Start.Set {
			state = "Placed"
		}
		text(state, int(r.X+r.W)-measure(state)-8, int(r.Y)+(int(r.H)-metric)/2, 60)
	}
	hintY := placerRowY + len(rows)*placerRowPitch + 6
	for i, line := range []string{"Pick a player, then click", "the map. Right-click: Auto."} {
		text(line, placerRowX, hintY+i*(metric+3), placerRowW)
	}
	for _, b := range []struct {
		r       gui.Rect
		caption string
		press   int
	}{{placerClear, "Clear All", placerPressClear}, {placerDone, "Done", placerPressDone}} {
		button(b.r, pl.pressed == b.press)
		text(b.caption, int(b.r.X)+(int(b.r.W)-measure(b.caption))/2, int(b.r.Y)+(int(b.r.H)-metric)/2, int(b.r.W))
	}
	// The map.
	bevel(gui.Rect{X: placerMap.X - 3, Y: placerMap.Y - 3, W: placerMap.W + 6, H: placerMap.H + 6}, true)
	c.UIFillRect(int(placerMap.X), int(placerMap.Y), int(placerMap.W), int(placerMap.H), 0)
	d := g.mapDataFor(g.setup.MapName)
	inner, ew, eh, ok := placerProjection(d, placerMap)
	if !ok {
		return
	}
	if pl.canvasOf != g.setup.MapName || pl.canvas == nil {
		pl.canvas, pl.canvasOf = retailMapPreviewCanvas(d.tnt, int(placerMap.W), int(placerMap.H)), g.setup.MapName
	}
	c.UIBlitIndexed(pl.canvas, int(placerMap.W), int(placerMap.H), int(placerMap.X), int(placerMap.Y), int(placerMap.W), int(placerMap.H))
	for _, sp := range mapStartPositions(d, len(rows)) {
		x, y := placerScreen(inner, ew, eh, int32(sp.X), int32(sp.Z))
		c.UIFillRect(int(x)-2, int(y)-2, 5, 5, g.guiColor(raised.bot))
	}
	for _, row := range rows {
		p := g.setup.Players[row].Start
		if !p.Set {
			continue
		}
		x, y := placerScreen(inner, ew, eh, p.X, p.Z)
		logo(g.setup.Players[row].Color, int(x)-placerMarker/2, int(y)-placerMarker/2, placerMarker)
		if row == pl.selected {
			c.UIFrameRect(int(x)-placerMarker/2-2, int(y)-placerMarker/2-2, placerMarker+4, placerMarker+4, g.guiColor(15))
		}
	}
}

// mapStartPositions are the selected map's start positions under the schema
// a battle of players players runs, in authored order. Special.ID is the
// stored number, one less than the authored StartPos<n>.
func mapStartPositions(d *retailMapData, players int) []mission.Special {
	if d == nil || d.ota == nil {
		return nil
	}
	schema, err := mission.SelectNetworkSchema(d.ota, players)
	if err != nil {
		return nil
	}
	var starts []mission.Special
	for _, sch := range d.ota.Schemas {
		if sch.Name != schema.Name {
			continue
		}
		for _, sp := range mission.DecodeSpecials(sch.Section) {
			if sp.Kind == 1 {
				starts = append(starts, sp)
			}
		}
		break
	}
	return starts
}

// drawMapStartMarkers marks the map picker preview's start positions for the
// current rows, each a small bevelled square over its position in the
// preview's own projection.
func (g *gameShell) drawMapStartMarkers(c *client.Client, p *ui.Panel, d *retailMapData, r gui.Rect) {
	if d == nil || d.tnt == nil || p == nil || p.Window == nil || r.W < 8 || r.H < 8 {
		return
	}
	inner, ew, eh, ok := placerProjection(d, r)
	if !ok {
		return
	}
	v := retailButtonVerdict(gui.Gadget{}, 0, 0, 0, 0, false)
	for _, sp := range mapStartPositions(d, len(g.placerRows())) {
		x, y := placerScreen(inner, ew, eh, int32(sp.X), int32(sp.Z))
		box := gui.Rect{X: min(max(x-3, r.X), r.X+r.W-8), Y: min(max(y-3, r.Y), r.Y+r.H-8), W: 7, H: 7}
		drawGUIBevel(c, box, g.guiColor(v.top), g.guiColor(v.bot), g.guiColor(v.fill))
	}
}
