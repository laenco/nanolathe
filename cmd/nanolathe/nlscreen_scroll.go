package main

import (
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/platform/screenkit"
)

// nlScrollList is a list window that shows only the rows that fit: the first
// row it shows and a scrollbar drag in progress. It scrolls as the content
// list does: the wheel and the arrows step whole rows, a track press centres
// the thumb and grabbing the thumb keeps the pointer's offset
// (DESIGN_INTERFACE_HUD_INPUT §3.17).
type nlScrollList struct {
	top, rows, shown int
	area             screenkit.Rect // the list and its scrollbar: the wheel target
	dragging         bool
	grab             float64
}

func (l *nlScrollList) scroll(top int) { l.top = max(0, min(top, l.rows-l.shown)) }

// layout records the frame's rows and window and keeps the scroll in range.
func (l *nlScrollList) layout(rows, shown int, list screenkit.Rect) {
	l.rows, l.shown, l.area = rows, shown, list
	l.scroll(l.top)
}

// wheel consumes the whole rows of accumulated wheel travel over the list,
// keeping a trackpad's fractional travel; it reports whether the pointer is
// over the list.
func (l *nlScrollList) wheel(acc *float64, x, y float64) bool {
	if !l.area.Contains(x, y) {
		return false
	}
	d := int(*acc)
	*acc -= float64(d)
	l.scroll(l.top - d)
	return true
}

// drawScrollbar draws the arrows and track beside list when it holds more
// rows than it shows, and widens the wheel target over them.
func (s *nlScreen) drawScrollbar(screen *ebiten.Image, id string, l *nlScrollList, list screenkit.Rect) {
	if l.rows <= l.shown {
		return
	}
	u := s.u()
	ax := list.X + list.W + 10*u
	s.arrowV(screen, id+"-up", screenkit.Rect{X: ax, Y: list.Y, W: 40 * u, H: 40 * u}, true, l.top > 0, func() { l.scroll(l.top - 1) })
	s.arrowV(screen, id+"-down", screenkit.Rect{X: ax, Y: list.Y + list.H - 40*u, W: 40 * u, H: 40 * u}, false, l.top+l.shown < l.rows, func() { l.scroll(l.top + 1) })
	track := screenkit.Rect{X: ax + 17*u, Y: list.Y + 46*u, W: 6 * u, H: list.H - 92*u}
	if track.H <= 0 {
		return
	}
	screenkit.Fill(screen, track, color.RGBA{20, 24, 20, 200})
	th := track.H * float64(l.shown) / float64(l.rows)
	ty := track.Y + (track.H-th)*float64(l.top)/float64(l.rows-l.shown)
	screenkit.Fill(screen, screenkit.Rect{X: track.X, Y: ty, W: track.W, H: th}, color.RGBA{120, 200, 120, 220})
	s.hits.Add(screenkit.Region{ID: id + "-track", Rect: screenkit.Rect{X: ax, Y: track.Y, W: 40 * u, H: track.H}, Drag: func(_, py float64) {
		if !l.dragging {
			l.dragging = true
			l.grab = th / 2
			if py >= ty && py < ty+th {
				l.grab = py - ty
			}
		}
		frac := clamp((py-track.Y-l.grab)/(track.H-th), 0, 1)
		l.scroll(int(math.Round(frac * float64(l.rows-l.shown))))
	}})
	l.area.W += 50 * u
}
