package ebitenapp

import (
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/nanolathe-gg/nanolathe/internal/client"
)

// The close button is latched from its one-tick report and handed to the
// client once; the window waits for the game's own exit request, and closes
// anyway when the grace period passes (docs/DESIGN_SESSIONS_AI_SAVE.md §5,
// "Autosave, quick save and quick load").
func TestWindowCloseIsHandedToTheGameThenForced(t *testing.T) {
	c, err := client.New(client.Options{Width: 640, Height: 480})
	if err != nil {
		t.Fatal(err)
	}
	pressed := false
	saved := windowBeingClosed
	windowBeingClosed = func() bool { return pressed }
	t.Cleanup(func() { windowBeingClosed = saved })
	a := &app{c: c}
	start := time.Unix(1000, 0)

	a.serviceWindowCloseAt(start)
	if c.WindowClosing() || a.exitPending {
		t.Fatal("a closed window was reported without a close request")
	}
	pressed = true
	a.serviceWindowCloseAt(start)
	pressed = false // Ebitengine reports the press for one tick only
	if !c.WindowClosing() || a.exitPending {
		t.Fatalf("close request: client told %t, exit forced %t", c.WindowClosing(), a.exitPending)
	}
	a.serviceWindowCloseAt(start.Add(windowCloseGrace))
	if a.exitPending {
		t.Fatal("the exit was forced before the grace period passed")
	}
	a.serviceWindowCloseAt(start.Add(windowCloseGrace + time.Millisecond))
	if !a.exitPending {
		t.Fatal("a game that never answered kept the window open")
	}
}

func TestPendingWindowCloseCannotRestBeforeHostStep(t *testing.T) {
	c, err := client.New(client.Options{Width: 32, Height: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	c.SetPresentationPaused(true)
	c.SetFocused(ebiten.IsFocused())
	a := &app{c: c, fullscreen: ebiten.IsFullscreen()}
	// The close can arrive between due host steps, after the paused view has
	// settled. Keep rendering until its host step answers or grace expires.
	view := c.PresentationDigest()
	view.Epoch, view.Committed = 0, nil
	view.TickFraction16, view.CameraFraction16 = 0, 0
	a.idle.valid, a.idle.view, a.idle.quietAt = true, view, time.Now().Add(-2*pausedIdleDelay)
	old := windowBeingClosed
	windowBeingClosed = func() bool { return true }
	t.Cleanup(func() { windowBeingClosed = old })
	a.serviceWindowCloseAt(time.Now())
	a.restPresentation()
	t.Cleanup(a.idle.stopTimer)
	if a.idle.sleeping {
		t.Fatal("pending close slept before the shell could save and exit")
	}
	a.idle.sleeping = true
	a.idle.maintenance.Store(true)
	if resting, _ := a.serviceResting(); resting {
		t.Fatal("audio maintenance bypassed a pending close")
	}
}

func TestWindowCloseWaitsForRecorderBeforeNotifying(t *testing.T) {
	c, err := client.New(client.Options{Width: 32, Height: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	stage := lifecycleBlockingUI{make(chan struct{}), make(chan struct{})}
	c.SetUIStage(stage)
	c.StartPreRecord(0, 0, false)
	<-stage.entered
	old := windowBeingClosed
	windowBeingClosed = func() bool { return true }
	t.Cleanup(func() { windowBeingClosed = old })
	a := &app{c: c}
	done := make(chan struct{})
	go func() { a.serviceWindowCloseAt(time.Now()); close(done) }()
	early := false
	select {
	case <-done:
		early = true
	case <-time.After(20 * time.Millisecond):
	}
	close(stage.release)
	<-done
	c.CancelPreRecord()
	if early || !c.WindowClosing() {
		t.Fatal("close notification failed to join the recorder before changing the client")
	}
}
