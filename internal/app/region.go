package app

import (
	"encoding/json"
	"fmt"

	"gsb/internal/geo"
)

// RegionRequest defines or replaces a region and its stitching rules.
type RegionRequest struct {
	Name          string   `json:"name"`
	Zoom          int      `json:"zoom"`
	West          float64  `json:"west"`
	South         float64  `json:"south"`
	East          float64  `json:"east"`
	North         float64  `json:"north"`
	Priority      []string `json:"priority"`
	LockedPackage string   `json:"locked_package"`
}

// UpsertRegion sets a named region. Rules changes only affect plans/builds
// computed afterwards; published builds keep their own rules hash.
func (a *App) UpsertRegion(req RegionRequest, idemKey string) (*Region, json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()

	if idemKey != "" {
		if hit, ok := a.state.idem[idemKey]; ok {
			var r Region
			if err := json.Unmarshal(hit.Response, &r); err == nil {
				return &r, hit.Response, nil
			}
		}
	}
	if req.Name == "" {
		return nil, nil, fmt.Errorf("%w: region name required", ErrInvalidInput)
	}
	b := geo.Bounds{West: req.West, South: req.South, East: req.East, North: req.North}
	if err := b.Valid(); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	ext, err := geo.BoundsToExtent(b, req.Zoom)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if req.LockedPackage != "" {
		if _, ok := a.state.data.Packages[req.LockedPackage]; !ok {
			return nil, nil, fmt.Errorf("%w: locked package %s not found", ErrInvalidInput, req.LockedPackage)
		}
	}
	for _, pid := range req.Priority {
		if _, ok := a.state.data.Packages[pid]; !ok {
			return nil, nil, fmt.Errorf("%w: priority package %s not found", ErrInvalidInput, pid)
		}
	}
	region := &Region{
		Name: req.Name, Zoom: req.Zoom, Extent: ext,
		Priority:      append([]string{}, req.Priority...),
		LockedPackage: req.LockedPackage, UpdatedAt: nowUTC(),
	}
	resp, err := a.appendEvent(evNameRegionUpserted, idemKey, region.UpdatedAt, evRegionUpserted{Region: *region}, region)
	if err != nil {
		return nil, nil, err
	}
	return region, resp, nil
}

// GetRegion returns a region.
func (a *App) GetRegion(name string) (*Region, error) {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	r, ok := a.state.data.Regions[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownRegion, name)
	}
	cp := *r
	return &cp, nil
}

// Regions lists all regions.
func (a *App) Regions() []Region {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	out := make([]Region, 0, len(a.state.data.Regions))
	for _, r := range a.state.data.Regions {
		out = append(out, *r)
	}
	return out
}
