package mapgen

// World is what the generator knows of the planet a map is made for, all of
// it read by the caller from the player's own content: the world's existing
// maps as the source of tile art, the features those maps use, the movement
// classes that decide whether a route exists, and the climate the maps
// author.
type World struct {
	// Name is the planet as the OTA's planet key names it.
	Name string
	// Sources are existing maps of this planet; their tiles paint the new
	// one. At least one is needed.
	Sources []Source
	// Deposit is the metal deposit every player's spots use: an
	// indestructible feature whose metal value seeds its footprint. A world
	// without one gets none, its ground holding the same surface metal
	// everywhere.
	Deposit Feature
	// Trees block and are reclaimed for energy; Foliage neither blocks nor
	// holds metal; Rocks hold metal to reclaim.
	Trees, Foliage, Rocks []Feature
	// Classes decide passability. The first is the ground class a route
	// between starts must exist for; it should be the most restrictive
	// common land vehicle.
	Classes []MoveClass
	// Palette is the RGB of every palette index, for matching tile edges.
	Palette *[256][3]byte
	// The climate and schema values written to the OTA, typical of the
	// world's maps.
	MinWind, MaxWind, Tidal, Gravity, Solar int
	SurfaceMetal, MohoMetal                 int
	Lava                                    bool
}

// Source is one existing map: its cells' heights and its tile art.
type Source struct {
	Name string
	// Width and Height are in 16-pixel cells; Heights has Width*Height.
	Width, Height int
	Heights       []byte
	SeaLevel      byte
	// TileIndices are the (Width/2)*(Height/2) tile numbers; TileGraphics
	// holds 32x32 palette indices per tile.
	TileIndices  []uint16
	TileGraphics []byte
	// Avoid marks cells whose art must not be copied elsewhere, nil or
	// Width*Height long. The generator copies every other cell's art, so
	// the caller marks what would show something that is not there: the
	// void, legacy painted metal, the strips outside the play area and the
	// metal art under a deposit.
	Avoid []bool
}

// Feature is one feature definition the generator may place.
type Feature struct {
	Name         string
	FootX, FootZ int
	Metal        int
	// Blocking features stop movement over their footprint.
	Blocking bool
	// Weight is how often the world's maps use it.
	Weight int
}

// MoveClass is a movement class's passability record [04 §6.1]: footprint,
// slope tiers and water depths, as the content compiles them.
type MoveClass struct {
	Name                         string
	FootX, FootZ                 int
	MaxSlope, BadSlope           int
	MaxWaterSlope, BadWaterSlope int
	MaxWaterDepth, MinWaterDepth int
}
