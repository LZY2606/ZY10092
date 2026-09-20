// Package app is the event-sourced application core. All state changes are
// represented as events; command handlers decide, append one event, and then
// reduce it into state. Replays deterministically rebuild the same state and
// preserve the first response associated with an idempotency key.
package app

import (
	"time"

	"gsb/internal/geo"
)

// Tile is one registered raster tile.
type Tile struct {
	ID       geo.TileID `json:"id"`
	SHA      string     `json:"sha"`
	Size     int        `json:"size"`
	License  string     `json:"license"`
	Captured time.Time  `json:"captured"`
	Source   string     `json:"source"`
	NativeZ  int        `json:"native_z"`
	Scheme   string     `json:"scheme"` // xyz | tms (native scheme before normalization)
}

// Package is an imported offline data package with declared metadata.
type Package struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Zoom           int          `json:"zoom"`
	Scheme         string       `json:"scheme"`
	Projection     string       `json:"projection"`
	DeclaredBounds *geo.Bounds  `json:"declared_bounds,omitempty"`
	Extent         geo.Extent   `json:"extent"`
	Tiles          []Tile       `json:"tiles"`
	License        string       `json:"license"`
	Source         string       `json:"source"`
	ImportedAt     time.Time    `json:"imported_at"`
	Quarantined    []Quarantine `json:"quarantined,omitempty"`
}

// Quarantine explains why an input tile/package was isolated.
type Quarantine struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// Region is a named analysis region with stitching policy.
type Region struct {
	Name          string     `json:"name"`
	Extent        geo.Extent `json:"extent"`
	Zoom          int        `json:"zoom"`
	Priority      []string   `json:"priority"`
	LockedPackage string     `json:"locked_package,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Candidate is one input tile competing for an output position.
type Candidate struct {
	PackageID string `json:"package_id"`
	Tile      Tile   `json:"tile"`
	Rank      int    `json:"rank"`
	Chosen    bool   `json:"chosen"`
}

// Warning describes a detected conflict at one output position.
type Warning struct {
	Code     string     `json:"code"`
	Tile     geo.TileID `json:"tile"`
	Detail   string     `json:"detail"`
	Packages []string   `json:"packages,omitempty"`
}

// CandidatePlan is the computed stitching plan for a region.
type CandidatePlan struct {
	Region   string              `json:"region"`
	Zoom     int                 `json:"zoom"`
	Entries  map[string]PlanCell `json:"entries"`
	Warnings []Warning           `json:"warnings"`
	Computed time.Time           `json:"computed_at"`
}

// PlanCell holds the candidates for one output coordinate.
type PlanCell struct {
	Tile       geo.TileID  `json:"tile"`
	Candidates []Candidate `json:"candidates"`
	ChosenSHA  string      `json:"chosen_sha"`
	Warnings   []string    `json:"warnings"`
}

// BuildStatus enumerates the lifecycle shown separately in the UI.
type BuildStatus string

const (
	StatusPending    BuildStatus = "pending"    // planned, awaiting acceptance
	StatusBuilding   BuildStatus = "building"   // blocks being written
	StatusRecovering BuildStatus = "recovering" // resume verifying written blocks
	StatusAccepted   BuildStatus = "accepted"   // manifest committed
	StatusRejected   BuildStatus = "rejected"   // user rejected the plan
	StatusFailed     BuildStatus = "failed"     // write failed; old state intact
)

// Build is a content-addressed stitching output.
type Build struct {
	ID          string      `json:"id"`
	Region      string      `json:"region"`
	Zoom        int         `json:"zoom"`
	Status      BuildStatus `json:"status"`
	Tiles       []BuildTile `json:"tiles"`
	ManifestSHA string      `json:"manifest_sha"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	AcceptedAt  *time.Time  `json:"accepted_at,omitempty"`
	RulesHash   string      `json:"rules_hash"`
	OriginID    string      `json:"origin_id,omitempty"` // set on re-import
	Failure     string      `json:"failure,omitempty"`
}

// BuildTile maps one output coordinate to a content block and its provenance.
type BuildTile struct {
	Tile      geo.TileID `json:"tile"`
	SHA       string     `json:"sha"`
	PackageID string     `json:"package_id"`
	Captured  time.Time  `json:"captured"`
	License   string     `json:"license"`
	Written   bool       `json:"written"`
}

// SourceSummary aggregates where an output came from.
type SourceSummary struct {
	PackageID string `json:"package_id"`
	Source    string `json:"source"`
	License   string `json:"license"`
	Tiles     int    `json:"tiles"`
}

// Manifest is the complete, immutable description of an accepted build.
type Manifest struct {
	ID        string          `json:"id"`
	Region    string          `json:"region"`
	Zoom      int             `json:"zoom"`
	Tiles     []BuildTile     `json:"tiles"`
	Sources   []SourceSummary `json:"sources"`
	Bounds    geo.Bounds      `json:"bounds"`
	RulesHash string          `json:"rules_hash"`
	CreatedAt time.Time       `json:"created_at"`
}

// Provenance lists every candidate behind one output tile.
type Provenance struct {
	Tile       geo.TileID  `json:"tile"`
	Candidates []Candidate `json:"candidates"`
}

// EventRecord is the public view of a journaled event.
type EventRecord struct {
	Seq     int64     `json:"seq"`
	Type    string    `json:"type"`
	At      time.Time `json:"at"`
	IdemKey string    `json:"idem_key,omitempty"`
	Summary string    `json:"summary"`
}
