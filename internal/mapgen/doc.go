// Package mapgen generates skirmish maps (docs/DESIGN_MAP_GENERATOR.md):
// fractal terrain that looks natural, made fair by where the starts and the
// metal go and by measuring the result rather than by mirroring it.
//
// A generation runs in this order, for each of several candidates:
//
//  1. the starts, spread evenly round the map or grouped by team;
//  2. the heights: multi-scale value noise with ridges and domain warping,
//     the sea level chosen by the share of water asked for, a flat base round
//     every start, and slopes limited by thermal erosion;
//  3. the zones: every cell belongs to the start a land vehicle reaches
//     soonest, by the movement class's own passability rule;
//  4. the metal: the same deposits for every player at the same travel
//     distances from their base, and deposits shared between neighbours where
//     they are equally far from both;
//  5. the trees, foliage and rocks of the world, in clusters;
//  6. the measurement: travel distance to the nearest enemy, flat building
//     room and metal near each base, compared between players.
//
// A candidate is ranked only when it is valid: its bases stand apart, every
// start reaches every enemy start by land, every player's metal is placed,
// and every start stands on dry land with room to build. The fairest valid
// candidate is painted with the tiles of the world's own maps,
// coherent patches of real map art chosen so that water, shore and slope lie
// where the generated heights put them, and written as a TNT and an OTA.
//
// Everything is integer arithmetic over a private random stream seeded by the
// parameters, so one code makes one map on every platform for the same
// source maps. Nothing here reaches a battle: the result is ordinary map
// content, and the simulation's random streams are never touched.
package mapgen
