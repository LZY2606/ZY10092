package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"gsb/internal/geo"
	"gsb/internal/store"
)

// CreateBuild takes the prepared plan for a region and creates a build in the
// "pending" state. Nothing is visible as an output until the manifest commits.
func (a *App) CreateBuild(regionName, idemKey string) (*Build, json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()

	if idemKey != "" {
		if hit, ok := a.state.idem[idemKey]; ok {
			var b Build
			if err := json.Unmarshal(hit.Response, &b); err == nil {
				return &b, hit.Response, nil
			}
		}
	}
	region, ok := a.state.data.Regions[regionName]
	if !ok {
		return nil, nil, &regionError{name: regionName}
	}
	plan, ok := a.state.data.Plans[regionName]
	if !ok {
		return nil, nil, fmt.Errorf("%w: prepare candidates before creating a build", ErrInvalidInput)
	}
	now := nowUTC()
	keys := make([]string, 0, len(plan.Entries))
	for k := range plan.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b := &Build{ID: a.newBuildID(), Region: region.Name, Zoom: region.Zoom, Status: StatusPending, CreatedAt: now, UpdatedAt: now, RulesHash: rulesHash(region)}
	for _, k := range keys {
		cell := plan.Entries[k]
		chosen := cell.Candidates[0]
		b.Tiles = append(b.Tiles, BuildTile{
			Tile: cell.Tile, SHA: chosen.Tile.SHA, PackageID: chosen.PackageID,
			Captured: chosen.Tile.Captured, License: chosen.Tile.License,
		})
	}
	resp, err := a.appendEvent(evNameBuildCreated, idemKey, now, evBuildCreated{Build: *b}, b)
	if err != nil {
		return nil, nil, err
	}
	return b, resp, nil
}

// ErrSimulatedCrash models a hard process crash: the error is returned but no
// failure event is journaled, so on reopen the build is found "in flight" and
// surfaced as recovering (as opposed to a cleanly observed, recorded failure).
var ErrSimulatedCrash = errors.New("simulated process crash")

// enterBuilding journals the pending -> building transition so a crash
// during writes can be distinguished from a never-started build on restart.
func (a *App) enterBuilding(b *Build) error {
	now := nowUTC()
	b.Status = StatusBuilding
	b.UpdatedAt = now
	ev := evBuildRecovered{BuildID: b.ID, At: now}
	// Reuse a dedicated framing: type name is build_started via a small body.
	if _, err := a.appendEvent(evNameBuildStarted, "", now, ev, ev); err != nil {
		return err
	}
	return nil
}

// writeBlocks copies (content-addressed, so mostly verifies) every output
// block and journals progress. On error the build becomes "failed"; all
// previously committed state and blocks remain readable.
func (a *App) writeBlocks(b *Build) error {
	for i := range b.Tiles {
		bt := &b.Tiles[i]
		if bt.Written {
			continue
		}
		if a.Fault != nil {
			if ferr := a.Fault("block", b.ID); ferr != nil {
				return ferr
			}
		}
		data, err := a.st.Get(bt.SHA)
		if err != nil {
			return fmt.Errorf("verify source block %s: %w", bt.SHA, err)
		}
		if hash, _, perr := a.st.Put(data); perr != nil {
			return perr
		} else if hash != bt.SHA {
			return fmt.Errorf("content address mismatch for %s", bt.Tile.String())
		}
		now := nowUTC()
		ev := evBlockWritten{BuildID: b.ID, Tile: keyOf(bt.Tile), Seq: i, At: now}
		if _, err := a.appendEvent(evNameBlockWritten, "", now, ev, ev); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) markFailed(b *Build, reason string) {
	now := nowUTC()
	ev := evBuildFailed{BuildID: b.ID, Reason: reason, At: now}
	if _, err := a.appendEvent(evNameBuildFailed, "", now, ev, ev); err != nil {
		// Journal failure is fatal to the process; state in memory stays at
		// the previous committed value.
		panic(err)
	}
}

// StartBuild moves a pending build into block writing. It is an explicit
// transition so the UI can keep pending/building/failed apart.
func (a *App) StartBuild(id string) (*Build, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	b, ok := a.state.data.Builds[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBuild, id)
	}
	if b.Status != StatusPending {
		return nil, fmt.Errorf("%w: build %s is %s", ErrConflict, id, b.Status)
	}
	if err := a.enterBuilding(b); err != nil {
		return nil, err
	}
	if err := a.writeBlocks(b); err != nil {
		if !errors.Is(err, ErrSimulatedCrash) {
			a.markFailed(b, err.Error())
		}
		return nil, err
	}
	return b, nil
}

// AcceptBuild commits the manifest: from this point the output is visible and
// immutable, and keeps its own tile numbering and source table.
func (a *App) AcceptBuild(id, idemKey string) (*Manifest, json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()

	if idemKey != "" {
		if hit, ok := a.state.idem[idemKey]; ok {
			var m Manifest
			if err := json.Unmarshal(hit.Response, &m); err == nil {
				return &m, hit.Response, nil
			}
		}
	}
	b, ok := a.state.data.Builds[id]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrUnknownBuild, id)
	}
	if b.Status == StatusAccepted || b.Status == StatusRejected {
		return nil, nil, fmt.Errorf("%w: build %s already %s", ErrConflict, id, b.Status)
	}
	if b.Status == StatusPending {
		if err := a.enterBuilding(b); err != nil {
			return nil, nil, err
		}
		if err := a.writeBlocks(b); err != nil {
			if !errors.Is(err, ErrSimulatedCrash) {
				a.markFailed(b, err.Error())
			}
			return nil, nil, err
		}
	}
	for _, bt := range b.Tiles {
		if !bt.Written || !a.st.Has(bt.SHA) {
			return nil, nil, fmt.Errorf("%w: block %s missing", ErrInvalidInput, bt.Tile.String())
		}
	}
	man := Manifest{
		ID: b.ID, Region: b.Region, Zoom: b.Zoom, Tiles: append([]BuildTile{}, b.Tiles...),
		Sources: a.sourceSummary(b), Bounds: a.buildBounds(b), RulesHash: b.RulesHash,
		CreatedAt: b.CreatedAt,
	}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	manifestHash := store.SHA256(mb)
	if _, _, err := a.st.Put(mb); err != nil {
		return nil, nil, err
	}
	accepted := *b
	now := nowUTC()
	accepted.Status = StatusAccepted
	accepted.ManifestSHA = manifestHash
	accepted.AcceptedAt = &now
	accepted.UpdatedAt = now
	accepted.Failure = ""
	resp, err := a.appendEvent(evNameBuildAccepted, idemKey, now, evBuildAccepted{Build: accepted}, man)
	if err != nil {
		return nil, nil, err
	}
	return &man, resp, nil
}

// RejectBuild records an explicit rejection; pending blocks stay untouched.
func (a *App) RejectBuild(id, idemKey string) (*Build, json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	if idemKey != "" {
		if hit, ok := a.state.idem[idemKey]; ok {
			var b Build
			if err := json.Unmarshal(hit.Response, &b); err == nil {
				return &b, hit.Response, nil
			}
		}
	}
	b, ok := a.state.data.Builds[id]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrUnknownBuild, id)
	}
	if b.Status == StatusAccepted {
		return nil, nil, fmt.Errorf("%w: cannot reject accepted build %s", ErrConflict, id)
	}
	now := nowUTC()
	resp, err := a.appendEvent(evNameBuildRejected, idemKey, now, evBuildRejected{BuildID: id, At: now}, b)
	if err != nil {
		return nil, nil, err
	}
	out := *b
	return &out, resp, nil
}

// ResumeBuild verifies every already-written block against its content
// address, marks the build "recovering" while doing so, then continues from
// the first missing block and finishes with the manifest commit.
func (a *App) ResumeBuild(id, idemKey string) (*Manifest, json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()

	if idemKey != "" {
		if hit, ok := a.state.idem[idemKey]; ok {
			var m Manifest
			if err := json.Unmarshal(hit.Response, &m); err == nil {
				return &m, hit.Response, nil
			}
		}
	}
	b, ok := a.state.data.Builds[id]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrUnknownBuild, id)
	}
	if b.Status == StatusAccepted {
		return a.manifestOf(b)
	}
	if b.Status != StatusFailed && b.Status != StatusPending && b.Status != StatusBuilding && b.Status != StatusRecovering {
		return nil, nil, fmt.Errorf("%w: cannot resume build %s in state %s", ErrConflict, id, b.Status)
	}
	now := nowUTC()
	// Mark recovering (observable in the UI) before verification.
	recEv := evBuildRecovered{BuildID: id, At: now}
	b.Status = StatusRecovering
	b.UpdatedAt = now
	if _, err := a.appendEvent(evNameBuildRecovered, "", now, recEv, recEv); err != nil {
		return nil, nil, err
	}
	for i := range b.Tiles {
		bt := &b.Tiles[i]
		if !bt.Written {
			continue
		}
		if !a.st.Has(bt.SHA) {
			bt.Written = false // orphan marker without event; rewrite below
			continue
		}
		data, err := a.st.Get(bt.SHA)
		if err != nil || store.SHA256(data) != bt.SHA {
			bt.Written = false
		}
	}
	if err := a.enterBuilding(b); err != nil {
		return nil, nil, err
	}
	if err := a.writeBlocks(b); err != nil {
		if !errors.Is(err, ErrSimulatedCrash) {
			a.markFailed(b, err.Error())
		}
		return nil, nil, err
	}
	return a.commitManifest(b, idemKey)
}

func (a *App) commitManifest(b *Build, idemKey string) (*Manifest, json.RawMessage, error) {
	man := Manifest{
		ID: b.ID, Region: b.Region, Zoom: b.Zoom, Tiles: append([]BuildTile{}, b.Tiles...),
		Sources: a.sourceSummary(b), Bounds: a.buildBounds(b), RulesHash: b.RulesHash,
		CreatedAt: b.CreatedAt,
	}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	manifestHash := store.SHA256(mb)
	if _, _, err := a.st.Put(mb); err != nil {
		return nil, nil, err
	}
	accepted := *b
	now := nowUTC()
	accepted.Status = StatusAccepted
	accepted.ManifestSHA = manifestHash
	accepted.AcceptedAt = &now
	accepted.UpdatedAt = now
	accepted.Failure = ""
	resp, err := a.appendEvent(evNameBuildAccepted, idemKey, now, evBuildAccepted{Build: accepted}, man)
	if err != nil {
		return nil, nil, err
	}
	return &man, resp, nil
}

func (a *App) manifestOf(b *Build) (*Manifest, json.RawMessage, error) {
	data, err := a.st.Get(b.ManifestSHA)
	if err != nil {
		return nil, nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil, err
	}
	return &m, data, nil
}

// recoverInterrupted marks any build left in building/recovering after a
// crash as "recovering" so the UI shows it distinctly until resumed.
func (a *App) recoverInterrupted() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	for _, b := range a.state.data.Builds {
		if b.Status == StatusBuilding || b.Status == StatusRecovering {
			now := nowUTC()
			ev := evBuildRecovered{BuildID: b.ID, At: now}
			if _, err := a.appendEvent(evNameBuildRecovered, "", now, ev, ev); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) sourceSummary(b *Build) []SourceSummary {
	counts := map[string]int{}
	for _, t := range b.Tiles {
		counts[t.PackageID]++
	}
	out := make([]SourceSummary, 0, len(counts))
	for pid, n := range counts {
		src := SourceSummary{PackageID: pid, Tiles: n}
		if p, ok := a.state.data.Packages[pid]; ok {
			src.Source = p.Source
			src.License = p.License
		}
		out = append(out, src)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PackageID < out[j].PackageID })
	return out
}

func (a *App) buildBounds(b *Build) geo.Bounds {
	west, south, east, north := 180.0, 90.0, -180.0, -90.0
	wrap := false
	// Determine crossing by x adjacency across the world edge among tiles.
	xs := map[int]bool{}
	n := 1 << uint(b.Zoom)
	for _, t := range b.Tiles {
		xs[t.Tile.X] = true
		w, s, e, nr := geo.TileBounds(t.Tile)
		if w < west {
			west = w
		}
		if nr > north {
			north = nr
		}
		if s < south {
			south = s
		}
		if e > east {
			east = e
		}
	}
	if xs[0] && xs[n-1] {
		wrap = true
	}
	_ = wrap
	return geo.Bounds{West: west, South: south, East: east, North: north}
}

// rulesHash captures the policy that produced a build.
func rulesHash(r *Region) string {
	type rules struct {
		Priority      []string
		LockedPackage string
		Zoom          int
	}
	data, _ := json.Marshal(rules{Priority: r.Priority, LockedPackage: r.LockedPackage, Zoom: r.Zoom})
	return store.SHA256(data)
}

// GetBuild returns a build copy.
func (a *App) GetBuild(id string) (*Build, error) {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	b, ok := a.state.data.Builds[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBuild, id)
	}
	cp := *b
	return &cp, nil
}

// Builds lists all builds.
func (a *App) Builds() []Build {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	bs := a.state.buildList()
	out := make([]Build, 0, len(bs))
	for _, b := range bs {
		out = append(out, *b)
	}
	return out
}

var _ = errors.Is
