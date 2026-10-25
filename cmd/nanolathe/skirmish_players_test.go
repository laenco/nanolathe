package main

import "testing"

// The Players button steps the row count within 2..8 and wraps; a count the
// typed selector set above eight steps to the nearer end of the range
// (DESIGN_INTERFACE_HUD_INPUT §2.6 "Player count").
func TestSkirmishPlayerCountSteps(t *testing.T) {
	for _, c := range []struct{ n, delta, want int }{
		{4, 1, 5}, {4, -1, 3}, {8, 1, 2}, {2, -1, 8}, {10, 1, 2}, {9, -1, 8}, {0, 1, 2},
	} {
		if got := nextSkirmishPlayerCount(c.n, c.delta); got != c.want {
			t.Errorf("count %d step %+d = %d, want %d", c.n, c.delta, got, c.want)
		}
	}
}
