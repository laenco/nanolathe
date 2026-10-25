package mapgen

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is the generator's algorithm version. It is part of every map code:
// a change to what any step computes makes old codes name other maps, so it
// moves with any such change.
const Version = 1

// Params are what the player chooses.
type Params struct {
	// Players is the number of start positions, 2..10.
	Players int
	// Teams groups the starts: 0 or 1 spreads them evenly for a game of
	// every player for themselves, 2..5 places that many groups.
	Teams int
	// Size is the map's side in the OTA's units of 512 pixels, 6..24.
	Size int
	// Water is the share of the map under water: 0 none, 1 lakes, 2 seas.
	Water int
	// Relief is how rough the land is: 0 plains, 1 rolling, 2 hills,
	// 3 mountains.
	Relief int
	// Metal is how many deposits each player has: 0 few, 1 normal, 2 many.
	Metal int
	// Seed picks the map among all those the other parameters allow.
	Seed uint64
}

// The parameters' ranges.
const (
	MinPlayers, MaxPlayers = 2, 10
	MinSize, MaxSize       = 6, 24
	MaxTeams               = 5
	MaxWater               = 2
	MaxRelief              = 3
	MaxMetal               = 2
)

// Normalize clamps every parameter into its range; a team count that does
// not divide the players evenly, or leaves a team of one, plays free for all.
func (p Params) Normalize() Params {
	p.Players = clampInt(p.Players, MinPlayers, MaxPlayers)
	p.Size = clampInt(p.Size, MinSize, MaxSize)
	p.Water = clampInt(p.Water, 0, MaxWater)
	p.Relief = clampInt(p.Relief, 0, MaxRelief)
	p.Metal = clampInt(p.Metal, 0, MaxMetal)
	if p.Teams < 2 || p.Teams > MaxTeams || p.Players%p.Teams != 0 || p.Players == p.Teams {
		p.Teams = 0
	}
	return p
}

// Code names the map: world, every parameter, the seed and the generator
// version, so that the same code makes the same map again.
func Code(world string, p Params) string {
	p = p.Normalize()
	return fmt.Sprintf("%s-%d-%d-%d-%d-%d-%d-%s-v%d", Slug(world), p.Players, p.Teams, p.Size, p.Water, p.Relief, p.Metal,
		strconv.FormatUint(p.Seed, 36), Version)
}

// ParseCode reads a code back into its world slug and parameters; a code of
// another generator version is refused, since it would make another map.
func ParseCode(code string) (world string, p Params, err error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(code)), "-")
	if len(parts) < 9 {
		return "", Params{}, fmt.Errorf("mapgen: code %q: expected world-players-teams-size-water-relief-metal-seed-version", code)
	}
	n := len(parts)
	if parts[n-1] != "v"+strconv.Itoa(Version) {
		return "", Params{}, fmt.Errorf("mapgen: code %q is from generator %s, this is v%d", code, parts[n-1], Version)
	}
	nums := make([]int, 6)
	for i := range nums {
		v, err := strconv.Atoi(parts[n-8+i])
		if err != nil {
			return "", Params{}, fmt.Errorf("mapgen: code %q: %v", code, err)
		}
		nums[i] = v
	}
	seed, err := strconv.ParseUint(parts[n-2], 36, 64)
	if err != nil {
		return "", Params{}, fmt.Errorf("mapgen: code %q: %v", code, err)
	}
	p = Params{Players: nums[0], Teams: nums[1], Size: nums[2], Water: nums[3], Relief: nums[4], Metal: nums[5], Seed: seed}
	return strings.Join(parts[:n-8], "-"), p, nil
}

// Slug is a world's name as a code carries it: lower case, letters and
// digits, words joined by underscores.
func Slug(world string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(world) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if sep && b.Len() > 0 {
				b.WriteByte('_')
			}
			sep = false
			b.WriteRune(r)
		default:
			sep = true
		}
	}
	if b.Len() == 0 {
		return "world"
	}
	return b.String()
}

func clampInt(v, lo, hi int) int { return min(max(v, lo), hi) }
