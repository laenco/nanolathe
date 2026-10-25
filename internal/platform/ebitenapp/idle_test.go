package ebitenapp

import (
	"testing"
	"time"

	"github.com/nanolathe-gg/nanolathe/internal/client"
	"github.com/nanolathe-gg/nanolathe/internal/frame"
	"github.com/nanolathe-gg/nanolathe/internal/input"
)

func TestPausedIdleWaitsForInputAndViewToSettle(t *testing.T) {
	var idle idlePresentation
	start := time.Unix(1, 0)
	view := client.PresentationInputs{Tick: 7, CamX: 10}
	idle.observeInput(start, sampledInput{x: 200, y: 100})
	if idle.ready(start, true, true, view) {
		t.Fatal("slept before panel transitions could finish")
	}
	view.Epoch++
	view.Committed = &frame.Frame{}
	view.CameraFraction16 = 40000
	if !idle.ready(start.Add(pausedIdleDelay), true, true, view) {
		t.Fatal("paused republication or blend fraction prevented resting")
	}
	view.CamX++
	if idle.ready(start.Add(2*pausedIdleDelay), true, true, view) {
		t.Fatal("slept while camera was moving")
	}
	idle.observeInput(start.Add(3*pausedIdleDelay), sampledInput{x: 201, y: 100})
	if idle.ready(start.Add(3*pausedIdleDelay), true, true, view) {
		t.Fatal("pointer motion failed to defer sleep")
	}
	if !idle.ready(start.Add(4*pausedIdleDelay), true, true, view) {
		t.Fatal("settled view never rested")
	}
	if idle.ready(start.Add(5*pausedIdleDelay), false, false, view) {
		t.Fatal("running or screen-owned work slept while unfocused")
	}
	if !idle.ready(start.Add(5*pausedIdleDelay), true, false, view) {
		t.Fatal("paused unfocused window waited for foreground inactivity")
	}
}

func TestPausedIdleKeepsHeldAndTransientInputLive(t *testing.T) {
	for _, sample := range []sampledInput{
		{keys: [input.KeyCount]bool{input.KeyLeft: true}},
		{buttons: input.MouseButtons{Left: true}},
		{wheelY: 1}, {zoomWheelY: 1}, {panX: 1},
		{characters: []rune{'a'}}, {pinches: []input.PinchEvent{{}}}, {dropped: []string{"mod.zip"}},
	} {
		var idle idlePresentation
		start := time.Unix(1, 0)
		view := client.PresentationInputs{}
		idle.ready(start, true, true, view)
		idle.observeInput(start.Add(2*pausedIdleDelay), sample)
		if idle.ready(start.Add(2*pausedIdleDelay), true, true, view) {
			t.Fatalf("slept during input: %+v", sample)
		}
	}
}
