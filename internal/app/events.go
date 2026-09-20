package app

import "time"

// Event bodies. Each body is the full post-command fact, so replay never has
// to recompute decisions (and an idempotent replay returns the exact first
// result).
type evPackageImported struct {
	Package Package `json:"package"`
}

type evQuarantine struct {
	PackageID string       `json:"package_id"`
	Name      string       `json:"name"`
	Items     []Quarantine `json:"items"`
	At        time.Time    `json:"at"`
}

type evRegionUpserted struct {
	Region Region `json:"region"`
}

type evPlanPrepared struct {
	Plan CandidatePlan `json:"plan"`
}

type evBuildCreated struct {
	Build Build `json:"build"`
}

type evBlockWritten struct {
	BuildID string    `json:"build_id"`
	Tile    tileKey   `json:"tile"`
	Seq     int       `json:"seq"`
	At      time.Time `json:"at"`
}

type evBuildFailed struct {
	BuildID string    `json:"build_id"`
	Reason  string    `json:"reason"`
	At      time.Time `json:"at"`
}

type evBuildRecovered struct {
	BuildID string    `json:"build_id"`
	At      time.Time `json:"at"`
}

type evBuildAccepted struct {
	Build Build `json:"build"`
}

type evBuildRejected struct {
	BuildID string    `json:"build_id"`
	At      time.Time `json:"at"`
}

type tileKey struct {
	Z int `json:"z"`
	X int `json:"x"`
	Y int `json:"y"`
}

const (
	evNamePackageImported = "package_imported"
	evNameQuarantine      = "quarantine"
	evNameRegionUpserted  = "region_upserted"
	evNamePlanPrepared    = "plan_prepared"
	evNameBuildCreated    = "build_created"
	evNameBuildStarted    = "build_started"
	evNameBlockWritten    = "block_written"
	evNameBuildFailed     = "build_failed"
	evNameBuildRecovered  = "build_recovered"
	evNameBuildAccepted   = "build_accepted"
	evNameBuildRejected   = "build_rejected"
)
