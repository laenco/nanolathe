package main

import (
	"bytes"
	"io"
	"runtime"
	"strings"
	"testing"
)

// The live scene grammar is part of the documented command line
// (docs/BATTLE_BENCHMARK.md "Live window trace").
func TestParseLiveScene(t *testing.T) {
	for text, want := range map[string]liveScene{
		"":                   {},
		"coastal":            {Kind: "coastal", Scale: 1},
		"coastal:2.5":        {Kind: "coastal", Scale: 2.5},
		"field":              {Kind: "field", Army: 250},
		"field:534":          {Kind: "field", Army: 534},
		"capture:/tmp/c":     {Kind: "capture", Capture: "/tmp/c", Copies: 1},
		"capture:/tmp/c:2":   {Kind: "capture", Capture: "/tmp/c", Copies: 2},
		"capture:/tmp/c d:4": {Kind: "capture", Capture: "/tmp/c d", Copies: 4},
	} {
		got, err := parseLiveScene(text)
		if err != nil || got != want {
			t.Errorf("parseLiveScene(%q) = %+v, %v; want %+v", text, got, err, want)
		}
	}
	if runtime.GOOS == "windows" {
		for text, want := range map[string]liveScene{
			`capture:C:\cap`:   {Kind: "capture", Capture: `C:\cap`, Copies: 1},
			`capture:C:\cap:3`: {Kind: "capture", Capture: `C:\cap`, Copies: 3},
		} {
			if got, err := parseLiveScene(text); err != nil || got != want {
				t.Errorf("parseLiveScene(%q) = %+v, %v; want %+v", text, got, err, want)
			}
		}
	}
	for _, text := range []string{"coast", "coastal:0", "coastal:x", "field:10", "field:5000", "capture", "capture:", "capture:/tmp/c:0", "capture:/tmp/c:9"} {
		if _, err := parseLiveScene(text); err == nil {
			t.Errorf("parseLiveScene(%q) accepted", text)
		}
	}
}

func TestLiveTraceFlags(t *testing.T) {
	opts, err := parseFlags([]string{"--map=Town & Country", "--live-trace=/tmp/unused-live", "--live-scene=field:267", "--live-seconds=12"}, io.Discard)
	if err != nil || opts.LiveTrace != "/tmp/unused-live" || opts.LiveScene != "field:267" || opts.LiveSeconds != 12 || opts.Seed != 7 {
		t.Fatalf("live trace flags: opts=%+v err=%v", opts, err)
	}
	// Without --map the trace times play from the menus, until the window
	// closes unless --live-seconds says otherwise.
	opts, err = parseFlags([]string{"--live-trace=/tmp/unused-live"}, io.Discard)
	if err != nil || opts.LiveSeconds != 0 {
		t.Fatalf("menu play trace: opts=%+v err=%v", opts, err)
	}
	opts, err = parseFlags([]string{"--live-trace=/tmp/unused-live", "--load-save=/tmp/unused.sav", "--live-seconds=40"}, io.Discard)
	if err != nil || opts.LiveSeconds != 40 {
		t.Fatalf("menu play trace of a save: opts=%+v err=%v", opts, err)
	}
	for _, args := range [][]string{
		{"--live-trace=/tmp/unused-live", "--live-scene=field"},
		{"--map=Town & Country", "--live-trace=/tmp/unused-live", "--load-save=/tmp/unused.sav"},
		{"--map=Town & Country", "--live-scene=field"},
		{"--map=Moon Quartet", "--live-trace=/tmp/unused-live", "--live-scene=capture:/tmp/c"},
		{"--map=Town & Country", "--live-trace=/tmp/unused-live", "--live-scene=bogus"},
		{"--map=Town & Country", "--live-trace=/tmp/unused-live", "--headless"},
		{"--battle-benchmark=/tmp/unused-battle", "--benchmark-capture-copies=5"},
	} {
		if _, err := parseFlags(args, io.Discard); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

// A refusal after the flags parse prints its reason: mainOptions exits with
// status 2 and prints nothing itself, so an unprinted one is a silent exit.
func TestLateFlagRefusalsSayWhy(t *testing.T) {
	for _, args := range [][]string{
		{"--map=x", "--live-trace=/tmp/unused-live", "--live-scene=capture:/tmp/unused-capture"},
		{"--map=x", "--live-trace=/tmp/unused-live", "--live-speed=30"},
		{"--live-unpaced"},
		{"--benchmark-capture=/tmp/unused-capture"},
	} {
		var out bytes.Buffer
		if _, err := parseFlags(args, &out); err == nil || !strings.Contains(out.String(), err.Error()) {
			t.Errorf("parseFlags(%q) = %v; printed %q", args, err, out.String())
		}
	}
}
