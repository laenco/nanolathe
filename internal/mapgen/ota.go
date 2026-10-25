package mapgen

import (
	"fmt"
	"strconv"

	"github.com/nanolathe-gg/nanolathe/formats"
)

// OTA writes the map's description file [fmt ota]: the world's climate in
// the global header and one multiplayer schema holding the start positions
// in their numbered order. Name is the map's name as the chooser lists it;
// both it and the description must be free of the characters TDF reserves.
func (m *Map) OTA(name, description string) ([]byte, error) {
	w := m.world
	itoa := strconv.Itoa
	bool01 := map[bool]string{false: "0", true: "1"}
	g := &formats.TDFWriteSection{Name: "GlobalHeader"}
	g.Set("missionname", name).
		Set("missiondescription", description).
		Set("planet", w.Name).
		Set("missionhint", "").
		Set("brief", "").
		Set("narration", "").
		Set("glamour", "").
		Set("lineofsight", "0").
		Set("mapping", "0").
		Set("tidalstrength", itoa(w.Tidal)).
		Set("solarstrength", itoa(w.Solar)).
		Set("lavaworld", bool01[w.Lava]).
		Set("killmul", "50").
		Set("timemul", "0").
		Set("minwindspeed", itoa(w.MinWind)).
		Set("maxwindspeed", itoa(w.MaxWind)).
		Set("gravity", itoa(w.Gravity)).
		Set("numplayers", itoa(len(m.Starts))).
		Set("size", fmt.Sprintf("%d x %d", m.Params.Size, m.Params.Size)).
		Set("memory", "32 mb").
		Set("SCHEMACOUNT", "1")
	s := g.Add("Schema 0")
	s.Set("Type", "Network 1").
		Set("aiprofile", "").
		Set("SurfaceMetal", itoa(w.SurfaceMetal)).
		Set("MohoMetal", itoa(w.MohoMetal)).
		Set("HumanMetal", "1000").
		Set("ComputerMetal", "1000").
		Set("HumanEnergy", "1000").
		Set("ComputerEnergy", "1000").
		Set("MeteorWeapon", "").
		Set("MeteorRadius", "0").
		Set("MeteorDensity", "0").
		Set("MeteorDuration", "0").
		Set("MeteorInterval", "0")
	specials := s.Add("specials")
	for i, st := range m.Starts {
		specials.Add("special"+itoa(i)).
			Set("specialwhat", "StartPos"+itoa(i+1)).
			Set("XPos", itoa(st.X)).
			Set("ZPos", itoa(st.Z))
	}
	return formats.EncodeTDF(g)
}
