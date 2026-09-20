package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"gsb/internal/geo"
	"gsb/internal/store"
)

// ErrUnknownRegion / ErrUnknownBuild / ErrConflict are sentinel errors.
var (
	ErrUnknownRegion = errors.New("unknown region")
	ErrUnknownBuild  = errors.New("unknown build")
	ErrConflict      = errors.New("conflict: build already accepted/rejected")
	ErrInvalidInput  = errors.New("invalid input")
)

type stateData struct {
	Packages map[string]*Package       `json:"packages"`
	Regions  map[string]*Region        `json:"regions"`
	Plans    map[string]*CandidatePlan `json:"plans"`
	Builds   map[string]*Build         `json:"builds"`
	Seq      int64                     `json:"seq"`
}

// State is the in-memory reduced state plus idempotency index.
type State struct {
	mu   sync.RWMutex
	data stateData
	// idem maps idempotency key -> (event seq, first response JSON).
	idem map[string]idemRecord
	// blockEvents tracks which output blocks have been journaled written.
	written map[string]map[tileKey]bool
}

type idemRecord struct {
	Seq      int64           `json:"seq"`
	Type     string          `json:"type"`
	Response json.RawMessage `json:"response"`
}

func newState() *State {
	return &State{
		data: stateData{
			Packages: map[string]*Package{},
			Regions:  map[string]*Region{},
			Plans:    map[string]*CandidatePlan{},
			Builds:   map[string]*Build{},
		},
		idem:    map[string]idemRecord{},
		written: map[string]map[tileKey]bool{},
	}
}

func (st *State) snapshot() stateData { return st.data }

func keyOf(t geo.TileID) tileKey { return tileKey{Z: t.Z, X: t.X, Y: t.Y} }

func (st *State) apply(env store.Envelope) error {
	st.data.Seq = env.Seq
	body := env.Body
	switch env.Type {
	case evNamePackageImported:
		var e evPackageImported
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		st.data.Packages[e.Package.ID] = &e.Package
	case evNameRegionUpserted:
		var e evRegionUpserted
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		st.data.Regions[e.Region.Name] = &e.Region
	case evNamePlanPrepared:
		var e evPlanPrepared
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		st.data.Plans[e.Plan.Region] = &e.Plan
	case evNameBuildCreated:
		var e evBuildCreated
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		st.data.Builds[e.Build.ID] = &e.Build
	case evNameBlockWritten:
		var e evBlockWritten
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		b := st.data.Builds[e.BuildID]
		if b == nil {
			return fmt.Errorf("block event for unknown build %s", e.BuildID)
		}
		for i := range b.Tiles {
			if keyOf(b.Tiles[i].Tile) == e.Tile {
				b.Tiles[i].Written = true
			}
		}
		b.UpdatedAt = e.At
		if st.written[e.BuildID] == nil {
			st.written[e.BuildID] = map[tileKey]bool{}
		}
		st.written[e.BuildID][e.Tile] = true
	case evNameBuildFailed:
		var e evBuildFailed
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		if b := st.data.Builds[e.BuildID]; b != nil {
			b.Status = StatusFailed
			b.Failure = e.Reason
			b.UpdatedAt = e.At
		}
	case evNameBuildStarted:
		var e evBuildRecovered
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		if b := st.data.Builds[e.BuildID]; b != nil {
			b.Status = StatusBuilding
			b.UpdatedAt = e.At
		}
	case evNameBuildRecovered:
		var e evBuildRecovered
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		if b := st.data.Builds[e.BuildID]; b != nil {
			b.Status = StatusRecovering
			b.UpdatedAt = e.At
		}
	case evNameBuildAccepted:
		var e evBuildAccepted
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		st.data.Builds[e.Build.ID] = &e.Build
	case evNameBuildRejected:
		var e evBuildRejected
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		if b := st.data.Builds[e.BuildID]; b != nil {
			b.Status = StatusRejected
			b.UpdatedAt = e.At
		}
	default:
		return fmt.Errorf("unknown event type %q", env.Type)
	}
	return nil
}

func (st *State) packageList() []*Package {
	out := make([]*Package, 0, len(st.data.Packages))
	for _, p := range st.data.Packages {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (st *State) buildList() []*Build {
	out := make([]*Build, 0, len(st.data.Builds))
	for _, b := range st.data.Builds {
		cp := *b
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func nowUTC() time.Time { return time.Now().UTC() }
