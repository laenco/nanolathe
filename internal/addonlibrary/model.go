package addonlibrary

import (
	"path"
	"strings"
)

// ModelsOnly reports whether the add-on supplies at least one logical file
// and every file is an objects3d .3do model. The audited content, rather than
// an archive name or catalogue identity, selects the model add-on layer
// (docs/DESIGN_MODS_MUTATORS.md §5.7).
func ModelsOnly(addon Addon) (bool, error) {
	paths, err := Paths(addon)
	if err != nil {
		return false, err
	}
	if len(paths) == 0 {
		return false, nil
	}
	for logical := range paths {
		logical = strings.ToLower(logical)
		if !strings.HasPrefix(logical, "objects3d/") || path.Ext(logical) != ".3do" {
			return false, nil
		}
	}
	return true, nil
}
