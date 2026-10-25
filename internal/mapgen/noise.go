package mapgen

// stream is the generator's private random stream: SplitMix64, seeded from
// the parameters. It is content generation's own and never a simulation
// stream (INVARIANTS I4 governs those).
type stream struct{ s uint64 }

func newStream(seed uint64) *stream { return &stream{s: seed} }

func (r *stream) next() uint64 {
	r.s += 0x9E3779B97F4A7C15
	z := r.s
	z = (z ^ z>>30) * 0xBF58476D1CE4E5B9
	z = (z ^ z>>27) * 0x94D049BB133111EB
	return z ^ z>>31
}

// intn is uniform in [0, n); n is small, so the modulo bias is negligible.
func (r *stream) intn(n int) int {
	if n <= 1 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// rangeInt is uniform in [lo, hi].
func (r *stream) rangeInt(lo, hi int) int { return lo + r.intn(hi-lo+1) }

// mix derives an independent seed for one purpose.
func mix(seed uint64, salt uint64) uint64 {
	r := stream{s: seed ^ salt*0xD6E8FEB86659FD93}
	return r.next()
}

// lattice is the hash of one noise lattice point, 0..65535.
func lattice(seed uint64, x, y int64) int32 {
	h := uint64(x)*0x9E3779B97F4A7C15 ^ uint64(y)*0xC2B2AE3D27D4EB4F ^ seed
	h ^= h >> 31
	h *= 0xBF58476D1CE4E5B9
	h ^= h >> 29
	return int32(h >> 48)
}

// smooth is 3t²-2t³ in 16.16.
func smooth(t int64) int64 {
	t2 := t * t >> 16
	return (3*t2 - 2*(t2*t>>16))
}

// valueNoise is smooth value noise at (x, y) in 16.16 lattice units,
// 0..65535.
func valueNoise(seed uint64, x, y int64) int32 {
	ix, iy := x>>16, y>>16
	fx, fy := smooth(x&0xFFFF), smooth(y&0xFFFF)
	a, b := int64(lattice(seed, ix, iy)), int64(lattice(seed, ix+1, iy))
	c, d := int64(lattice(seed, ix, iy+1)), int64(lattice(seed, ix+1, iy+1))
	top := a + (b-a)*fx>>16
	bottom := c + (d-c)*fx>>16
	return int32(top + (bottom-top)*fy>>16)
}

// fbm sums octaves of value noise, each at twice the frequency and gain/256
// of the previous one's weight, normalized back to 0..65535. period is the
// first octave's lattice spacing in cells; x and y are cells in 16.16.
func fbm(seed uint64, x, y int64, period int64, octaves int, gain int64) int32 {
	var sum, weight int64
	amp := int64(256)
	for o := 0; o < octaves; o++ {
		v := int64(valueNoise(mix(seed, uint64(o)), x/period, y/period))
		sum += v * amp
		weight += amp
		amp = amp * gain >> 8
		period = max(1, period/2)
		if amp == 0 {
			break
		}
	}
	return int32(sum / weight)
}

// ridged is fbm folded about its middle: sharp crests where the noise crosses
// half, for mountain chains.
func ridged(seed uint64, x, y int64, period int64, octaves int, gain int64) int32 {
	v := int64(fbm(seed, x, y, period, octaves, gain))
	r := 65535 - 2*abs64(v-32768)
	return int32(max(0, r) * r >> 16)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
