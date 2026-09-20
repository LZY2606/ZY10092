package model

import (
	"fmt"
	"time"

	"tileforge/internal/geo"
)

const (
	StatusPending    = "pending"
	StatusAccepted   = "accepted"
	StatusRejected   = "rejected"
	StatusRecovering = "recovering"
	StatusFailed     = "failed"
	StatusPublished  = "published"
)

type PackageTile struct {
	Z         int       `json:"z"`
	X         int       `json:"x"`
	Y         int       `json:"y"`
	Scheme    string    `json:"scheme,omitempty"`
	Path      string    `json:"path"`
	BlobHash  string    `json:"blob_hash"`
	PixelHash string    `json:"pixel_hash,omitempty"`
	License   string    `json:"license,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	BBox      geo.BBox  `json:"bbox"`
	Isolated  bool      `json:"isolated,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

type Package struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Version    string           `json:"version"`
	CRS        string           `json:"crs"`
	Scheme     string           `json:"scheme"`
	License    string           `json:"license"`
	UpdatedAt  time.Time        `json:"updated_at"`
	Status     string           `json:"status"`
	Reason     string           `json:"reason,omitempty"`
	Tiles      []PackageTile    `json:"tiles"`
	Isolated   []PackageTile    `json:"isolated,omitempty"`
	CreatedAt  time.Time        `json:"created_at"`
	DecidedAt  time.Time        `json:"decided_at,omitempty"`
	BlobHashes []string         `json:"blob_hashes,omitempty"`
	Source     geo.SourceTile   `json:"-"`
	Sources    []geo.SourceTile `json:"-"`
}

type Policy struct {
	Region          geo.BBox  `json:"region"`
	PriorityPackage string    `json:"priority_package,omitempty"`
	LockedPackage   string    `json:"locked_package,omitempty"`
	LockedVersion   string    `json:"locked_version,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type TileManifest struct {
	Z          int              `json:"z"`
	X          int              `json:"x"`
	Y          int              `json:"y"`
	BBox       geo.BBox         `json:"bbox"`
	BlobHash   string           `json:"blob_hash,omitempty"`
	PixelHash  string           `json:"pixel_hash,omitempty"`
	Selected   *geo.SourceTile  `json:"selected,omitempty"`
	Candidates []geo.SourceTile `json:"candidates"`
	Issues     []geo.Issue      `json:"issues,omitempty"`
	Hole       bool             `json:"hole"`
}

func (t TileManifest) Key() string {
	return fmt.Sprintf("%d/%d/%d", t.Z, t.X, t.Y)
}

type BuildManifest struct {
	Schema        string         `json:"schema"`
	ManifestHash  string         `json:"manifest_hash,omitempty"`
	BuildID       string         `json:"build_id"`
	Region        geo.BBox       `json:"region"`
	Zoom          int            `json:"zoom"`
	Status        string         `json:"status"`
	PackageIDs    []string       `json:"package_ids"`
	Tiles         []TileManifest `json:"tiles"`
	BlockHashes   []string       `json:"block_hashes"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	PublishedAt   time.Time      `json:"published_at,omitempty"`
	SourceSummary map[string]int `json:"source_summary"`
}

type BuildRecord struct {
	BuildID  string        `json:"build_id"`
	Status   string        `json:"status"`
	Manifest BuildManifest `json:"manifest"`
	Reason   string        `json:"reason,omitempty"`
}

type IdemResult struct {
	Status int    `json:"status"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Body   []byte `json:"-"`
}

type Event struct {
	Seq       int          `json:"seq"`
	Type      string       `json:"type"`
	At        time.Time    `json:"at"`
	IdemKey   string       `json:"idem_key,omitempty"`
	Package   *Package     `json:"package,omitempty"`
	Policy    *Policy      `json:"policy,omitempty"`
	Build     *BuildRecord `json:"build,omitempty"`
	Idem      *IdemResult  `json:"idem,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Tile      string       `json:"tile,omitempty"`
	BlockHash string       `json:"block_hash,omitempty"`
}
