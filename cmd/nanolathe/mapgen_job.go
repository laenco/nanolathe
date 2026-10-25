package main

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/nanolathe-gg/nanolathe/internal/content"
	"github.com/nanolathe-gg/nanolathe/internal/mapgen"
	"github.com/nanolathe-gg/nanolathe/internal/maplibrary"
)

// The generator panel's background work (docs/DESIGN_MAP_GENERATOR.md §10):
// listing the content's worlds, indexing the chosen world's tiles once, and
// generating the map the panel's settings name. A newer request cancels the
// older one, and an older result can never replace a newer request's. At
// most one world is indexed at a time: a request whose world is being
// indexed waits for that index rather than making another.

// mapgenMinMaps is how many archive maps a world needs to be offered: fewer
// give the painter too little art to choose from.
const mapgenMinMaps = 3

// mapgenResult is a finished map and what it was made from.
type mapgenResult struct {
	m       *mapgen.Map
	world   string // the planet as its maps spell it
	class   string // the ground class the measurements were made with
	sources []mapgenSourceID
}

// mapgenJob is what the generator's goroutines share with the panel.
type mapgenJob struct {
	mu sync.Mutex
	// wg counts the goroutines reading the content; join waits for them
	// before the host may unmount it.
	wg sync.WaitGroup
	// The worlds of the content they were listed for.
	cs          *contentSet
	worlds      []mapgenWorldInfo
	worldsReady bool
	worldsErr   error
	// The last world's generator and its sources, kept while the panel is
	// open: indexing a world's tiles is most of a first generation.
	genWorld   string
	gen        *mapgen.Generator
	genClass   string
	genSources []mapgenSourceID
	dry        map[string]bool // worlds known to have no water art
	// indexing is the world being indexed, if any; indexed closes when that
	// ends, and stopIndex cancels it. latest is the latest request's world:
	// an index of it is kept even when the request that made it is older.
	indexing  string
	indexed   chan struct{}
	stopIndex context.CancelFunc
	latest    string
	// serial names the latest request; a finished older one is dropped.
	serial      int
	running     bool
	stage       string
	done, total int
	cancel      context.CancelFunc
	result      *mapgenResult
	err         error
}

// bind lists the worlds of cs in the background, unless they are listed
// already; it forgets everything made from other content.
func (j *mapgenJob) bind(cs *contentSet) {
	j.mu.Lock()
	if j.cs == cs {
		j.mu.Unlock()
		return
	}
	j.cs, j.worlds, j.worldsReady, j.worldsErr = cs, nil, false, nil
	j.gen, j.genWorld, j.genClass, j.genSources, j.result, j.dry = nil, "", "", nil, nil, map[string]bool{}
	if cs == nil {
		j.mu.Unlock()
		return
	}
	j.wg.Add(1)
	j.mu.Unlock()
	go func() {
		defer j.wg.Done()
		worlds, err := mapgenWorlds(cs.fs, cs.mapRoots)
		worlds = slices.DeleteFunc(worlds, func(w mapgenWorldInfo) bool { return len(w.Maps) < mapgenMinMaps })
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.cs == cs {
			j.worlds, j.worldsErr, j.worldsReady = worlds, err, true
		}
	}()
}

// listed is the content's worlds, and whether they are listed yet.
func (j *mapgenJob) listed() ([]mapgenWorldInfo, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.worlds, j.worldsReady, j.worldsErr
}

// waiting reports a request made before the worlds were listed, which can
// start now.
func (j *mapgenJob) waiting() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running && j.cancel == nil && j.worldsReady
}

// isDry reports a world known to have no water art.
func (j *mapgenJob) isDry(world string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.dry[world]
}

// start generates the map p names on world, cancelling any running request
// and any indexing of another world. Before the worlds are listed it only
// records that a request waits.
func (j *mapgenJob) start(world string, p mapgen.Params) {
	j.mu.Lock()
	if j.cancel != nil {
		j.cancel()
	}
	if j.indexing != "" && j.indexing != world {
		j.stopIndex()
	}
	j.serial++
	serial := j.serial
	j.latest = world
	j.running, j.stage, j.done, j.total, j.err, j.cancel = true, "Listing the worlds…", 0, 0, nil, nil
	cs, worlds, ready, listErr := j.cs, j.worlds, j.worldsReady, j.worldsErr
	if !ready {
		j.mu.Unlock()
		return
	}
	i := slices.IndexFunc(worlds, func(w mapgenWorldInfo) bool { return content.CanonicalKey(w.Planet) == world })
	if listErr != nil || i < 0 {
		j.running, j.err = false, listErr
		if listErr == nil {
			//lint:ignore ST1005 panel text, shown as written and keyed by it in the locale catalogs
			j.err = errors.New("No world has enough maps to take tiles from")
		}
		j.mu.Unlock()
		return
	}
	info := worlds[i]
	if j.dry[world] {
		p.Water = 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	j.wg.Add(1)
	j.mu.Unlock()

	limit := cs.limits.TNTBytes
	if limit <= 0 {
		limit = maplibrary.MaxTNTBytes
	}
	go func() {
		defer j.wg.Done()
		defer cancel()
		report := func(stage string, done, total int) bool {
			j.mu.Lock()
			defer j.mu.Unlock()
			if j.serial != serial {
				return false
			}
			j.stage, j.done, j.total = stage, done, total
			return true
		}
		if !report("Reading the world's maps…", 0, 0) {
			return
		}
		gen, class, sources, err := j.generator(ctx, cs, info, world, limit)
		if err != nil {
			j.mu.Lock()
			defer j.mu.Unlock()
			if j.serial == serial {
				// A failed request clears the picture too: what is shown is
				// always what Use and Save take.
				j.running, j.err, j.result = false, err, nil
			}
			return
		}
		if !gen.Wet() {
			p.Water = 0
		}
		m, err := gen.Generate(ctx, p, func(done, total int) { report("Generating…", done, total) })
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.serial != serial {
			return
		}
		// A failed request clears the picture too: what is shown is always
		// what Use and Save take, made from the settings beside it.
		j.running, j.err, j.result = false, err, nil
		if err == nil {
			j.result = &mapgenResult{m: m, world: info.Planet, class: class, sources: sources}
		}
	}()
}

// generator is world's indexed generator for a request whose context is
// ctx: the kept one, or the one being made, waited for, or a new one made
// here when no other world is being indexed. The index is kept when world
// is the latest request's, whichever request made it.
func (j *mapgenJob) generator(ctx context.Context, cs *contentSet, info mapgenWorldInfo, world string, limit int64) (*mapgen.Generator, string, []mapgenSourceID, error) {
	for {
		j.mu.Lock()
		if err := ctx.Err(); err != nil {
			j.mu.Unlock()
			return nil, "", nil, err
		}
		if j.gen != nil && j.genWorld == world {
			gen, class, sources := j.gen, j.genClass, j.genSources
			j.mu.Unlock()
			return gen, class, sources, nil
		}
		if j.indexing != "" {
			wait := j.indexed
			j.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, "", nil, ctx.Err()
			}
		}
		indexCtx, stopIndex := context.WithCancel(context.Background())
		done := make(chan struct{})
		j.indexing, j.indexed, j.stopIndex = world, done, stopIndex
		j.mu.Unlock()

		w, sources, err := mapgenWorld(indexCtx, cs.fs, info, limit, nil)
		var gen *mapgen.Generator
		if err == nil {
			gen, err = mapgen.NewGenerator(w)
		}
		stopIndex()
		j.mu.Lock()
		j.indexing, j.indexed, j.stopIndex = "", nil, nil
		close(done)
		if err == nil {
			gen.Workers = mapgenWorkers()
			j.dry[world] = !gen.Wet()
			if j.latest == world {
				j.gen, j.genWorld, j.genClass, j.genSources = gen, world, w.Classes[0].Name, sources
			}
		}
		j.mu.Unlock()
		if err != nil {
			return nil, "", nil, err
		}
		return gen, w.Classes[0].Name, sources, nil
	}
}

// state is the latest finished map, and whether a newer one is being made.
func (j *mapgenJob) state() (r *mapgenResult, running bool, stage string, done, total int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.result, j.running, j.stage, j.done, j.total, j.err
}

// stop cancels the running request and any indexing and waits for every
// goroutine reading the content, then lets the indexed world go: it is the
// bulk of the generator's memory, and the next panel indexes it again. An
// index already past reading its source maps finishes first.
func (j *mapgenJob) stop() {
	j.mu.Lock()
	if j.cancel != nil {
		j.cancel()
	}
	if j.stopIndex != nil {
		j.stopIndex()
	}
	j.serial++
	j.running, j.latest = false, ""
	j.mu.Unlock()
	j.wg.Wait()
	j.mu.Lock()
	j.gen, j.genWorld, j.genClass, j.genSources = nil, "", "", nil
	j.mu.Unlock()
}
