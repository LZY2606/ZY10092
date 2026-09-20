package app

import (
	"encoding/json"
	"time"

	"gsb/internal/geo"
)

// PreparePlan computes and journals the candidate stitching plan for a
// region. Re-running it after a rules change recomputes only this region.
func (a *App) PreparePlan(regionName, idemKey string) (*CandidatePlan, json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()

	if idemKey != "" {
		if hit, ok := a.state.idem[idemKey]; ok {
			var p CandidatePlan
			if err := json.Unmarshal(hit.Response, &p); err == nil {
				return &p, hit.Response, nil
			}
		}
	}
	region, ok := a.state.data.Regions[regionName]
	if !ok {
		return nil, nil, &regionError{name: regionName}
	}
	cells := a.gatherCells(region)
	plan := &CandidatePlan{Region: region.Name, Zoom: region.Zoom, Entries: map[string]PlanCell{}, Computed: nowUTC()}

	cover := map[geo.TileID]bool{}
	for _, c := range cells {
		cover[c.tile] = true
	}
	warnByTile := map[tileKey][]string{}
	for _, w := range dedupWarnings(a.collectWarnings(region, cells)) {
		plan.Warnings = append(plan.Warnings, w)
		warnByTile[keyOf(w.Tile)] = append(warnByTile[keyOf(w.Tile)], w.Code)
	}
	for _, c := range cells {
		pc := PlanCell{Tile: c.tile, Candidates: c.cands, ChosenSHA: c.cands[0].Tile.SHA}
		pc.Warnings = warnByTile[keyOf(c.tile)]
		plan.Entries[c.tile.String()] = pc
	}
	resp, err := a.appendEvent(evNamePlanPrepared, idemKey, plan.Computed, evPlanPrepared{Plan: *plan}, plan)
	if err != nil {
		return nil, nil, err
	}
	return plan, resp, nil
}

// collectWarnings mirrors the warning rules in Analyze without locking.
func (a *App) collectWarnings(region *Region, cells map[tileKey]*cell) []Warning {
	var ws []Warning
	for _, c := range cells {
		if len(c.cands) < 2 {
			continue
		}
		chosen := c.cands[0]
		lic := map[string]bool{chosen.Tile.License: true}
		hashSet := map[string]bool{chosen.Tile.SHA: true}
		var pkgs []string
		for _, cand := range c.cands {
			lic[cand.Tile.License] = true
			hashSet[cand.Tile.SHA] = true
			pkgs = append(pkgs, cand.PackageID)
		}
		for _, cand := range c.cands[1:] {
			if cand.Tile.Captured.After(chosen.Tile.Captured) {
				ws = append(ws, Warning{
					Code: "date_inversion", Tile: c.tile,
					Detail:   "selected " + chosen.PackageID + " is older than rejected newer " + cand.PackageID,
					Packages: []string{chosen.PackageID, cand.PackageID},
				})
			}
		}
		if len(lic) > 1 {
			ws = append(ws, Warning{Code: "license_conflict", Tile: c.tile, Detail: "candidate licenses differ", Packages: pkgs})
		}
		if len(hashSet) == 1 && len(lic) > 1 {
			ws = append(ws, Warning{Code: "same_pixels_metadata_conflict", Tile: c.tile, Detail: "identical image but conflicting authorization metadata", Packages: pkgs})
		}
	}
	return ws
}

// Provenance returns every candidate input behind one output coordinate.
func (a *App) Provenance(regionName string, t geo.TileID) (*Provenance, error) {
	a.mu.Lock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	defer a.mu.Unlock()
	region, ok := a.state.data.Regions[regionName]
	if !ok {
		return nil, &regionError{name: regionName}
	}
	if t.Z != region.Zoom || !region.Extent.Contains(t) {
		return &Provenance{Tile: t}, nil
	}
	cells := a.gatherCells(region)
	c := cells[keyOf(t)]
	if c == nil {
		return &Provenance{Tile: t}, nil
	}
	return &Provenance{Tile: t, Candidates: c.cands}, nil
}

var _ = time.Now
