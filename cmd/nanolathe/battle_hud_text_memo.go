package main

import (
	"math"

	"github.com/nanolathe-gg/nanolathe/internal/hud"
)

// hudTextMemo keeps one draw site's last text. The HUD is recorded on every
// presented draw, several per tick, while its readouts change at most once a
// tick, so formatting only on a change keeps that recording from allocating.
type hudTextMemo struct {
	key   [4]int64
	label string
	text  string
	ok    bool
}

func (m *hudTextMemo) cached(label string, key [4]int64) (string, bool) {
	return m.text, m.ok && m.key == key && m.label == label
}

func (m *hudTextMemo) store(label string, key [4]int64, text string) string {
	m.label, m.key, m.text, m.ok = label, key, text, true
	return text
}

// number memoises format(v); format must be a pure function of v.
func (m *hudTextMemo) number(v float32, format func(float32) string) string {
	key := [4]int64{int64(math.Float32bits(v))}
	if text, ok := m.cached("", key); ok {
		return text
	}
	return m.store("", key, format(v))
}

// clock memoises standaloneClockText, whose fields are all whole seconds.
func (m *hudTextMemo) clock(label string, tick uint32) string {
	key := [4]int64{int64(tick / clockTicksPerSecond)}
	if text, ok := m.cached(label, key); ok {
		return text
	}
	return m.store(label, key, standaloneClockText(label, tick))
}

// hudTextMemos are the per-site memos of one HUD.
type hudTextMemos struct {
	anchors    [len(hud.AnchorNames)]hudTextMemo
	clock      hudTextMemo
	wind, tide hudTextMemo
}

// anchorNumber is format(v) memoised for the readout at an anchor index.
func (h *retailBattleHUD) anchorNumber(index int, v float32, format func(float32) string) string {
	if index < 0 || index >= len(h.texts.anchors) {
		return format(v)
	}
	return h.texts.anchors[index].number(v, format)
}
