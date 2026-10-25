package mapgen

// point is a cell.
type point struct{ x, y int }

// The engine voids the two rightmost cell columns and the bottom 128 pixels
// (8 cells), so starts keep inside the play area [03 §2.2].
const (
	voidRight  = 2
	voidBottom = 8
)

// placeStarts spreads the starts. Free for all puts them evenly round an
// ellipse filling the play area, turned and jittered by the seed; teams put
// each team's members side by side across one point of that ellipse. The
// starts are numbered as retail's team maps number them: team t holds
// StartPos t+1, t+1+T, t+1+2T…, so a lobby's alternating rows share sides.
func placeStarts(p Params, w, h int, r *stream) []point {
	playW, playH := w-voidRight, h-voidBottom
	cx, cy := playW/2, playH/2
	margin := baseRadius + baseBlend/2 + 4
	rx, ry := cx-margin, cy-margin
	turn := r.intn(256)
	at := func(angle, perMille int) point {
		x := cx + int(int64(rx)*int64(perMille)/1000*int64(cos256(angle))>>16)
		y := cy + int(int64(ry)*int64(perMille)/1000*int64(sin256(angle))>>16)
		return point{clampInt(x, margin, playW-1-margin), clampInt(y, margin, playH-1-margin)}
	}
	starts := make([]point, p.Players)
	if p.Teams == 0 {
		step := 256 / p.Players
		for i := range starts {
			jitter := r.rangeInt(-step/8, step/8)
			starts[i] = at(turn+i*256/p.Players+jitter, r.rangeInt(900, 1000))
		}
		return starts
	}
	per := p.Players / p.Teams
	spacing := 2*baseRadius + 10
	for team := 0; team < p.Teams; team++ {
		angle := turn + team*256/p.Teams + r.rangeInt(-4, 4)
		centre := at(angle, 900)
		// Members stand along the tangent, alternately a little inward,
		// so a team of five does not run off the ellipse.
		tx, ty := -sin256(angle), cos256(angle)
		for k := 0; k < per; k++ {
			offset := (2*k - (per - 1)) * spacing / 2
			inward := 0
			if k%2 == 1 {
				inward = spacing / 3
			}
			x := centre.x + int(int64(offset)*int64(tx)>>16) - int(int64(inward)*int64(cos256(angle))>>16)
			y := centre.y + int(int64(offset)*int64(ty)>>16) - int(int64(inward)*int64(sin256(angle))>>16)
			starts[k*p.Teams+team] = point{clampInt(x, margin, playW-1-margin), clampInt(y, margin, playH-1-margin)}
		}
	}
	return starts
}

// teamOf is a start's team, or its own index free for all.
func teamOf(p Params, start int) int {
	if p.Teams == 0 {
		return start
	}
	return start % p.Teams
}
