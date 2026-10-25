package ebitenapp

import (
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// windowCloseGrace bounds how long a close request waits for the game to
// request the exit itself, after saving a live battle
// (docs/DESIGN_SESSIONS_AI_SAVE.md §5, "Autosave, quick save and quick
// load"). A game that never answers still closes.
const windowCloseGrace = 5 * time.Second

// windowBeingClosed is Ebitengine's close-button probe; tests replace it.
var windowBeingClosed = ebiten.IsWindowBeingClosed

// serviceWindowClose latches the window's close request, which Ebitengine
// reports for one tick only, hands it to the client, and forces the exit when
// the grace period has passed.
func (a *app) serviceWindowClose() {
	a.serviceWindowCloseAt(time.Now())
}

func (a *app) serviceWindowCloseAt(now time.Time) {
	if a.closeRequested.IsZero() {
		if !windowBeingClosed() {
			return
		}
		a.closeRequested = now
		// Parallel recorders copy the Client, including this flag. Respect
		// their publication barrier before notifying the host (§13.10).
		a.c.JoinPreRecord()
		a.c.NoteWindowClosing()
		return
	}
	if now.Sub(a.closeRequested) > windowCloseGrace {
		a.exitPending = true
	}
}
