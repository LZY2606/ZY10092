package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"gsb/internal/geo"
	"gsb/internal/store"
)

// App is the application service. Safe for concurrent use.
type App struct {
	st    *store.Store
	mu    sync.Mutex // serializes writers; readers take State.mu
	state *State

	// Fault, when set, may fail a content write to exercise recovery.
	Fault func(kind, id string) error
}

// New opens (or creates) an app backed by dir and replays its journal.
func New(dir string) (*App, error) {
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := st.CleanupTmp(); err != nil {
		return nil, err
	}
	a := &App{st: st, state: newState()}
	if err := a.replay(); err != nil {
		return nil, err
	}
	if err := a.recoverInterrupted(); err != nil {
		return nil, err
	}
	return a, nil
}

// NewWithStore is used by tests that inject faults directly.
func NewWithStore(st *store.Store) (*App, error) {
	a := &App{st: st, state: newState()}
	if err := st.CleanupTmp(); err != nil {
		return nil, err
	}
	if err := a.replay(); err != nil {
		return nil, err
	}
	if err := a.recoverInterrupted(); err != nil {
		return nil, err
	}
	return a, nil
}

// Store exposes the backing store (used by export/import of blocks).
func (a *App) Store() *store.Store { return a.st }

func (a *App) replay() error {
	evs, err := a.st.ReadEvents()
	if err != nil {
		return err
	}
	a.state.mu.Lock()
	defer a.state.mu.Unlock()
	for _, ev := range evs {
		var env store.Envelope
		if err := json.Unmarshal(ev.Data, &env); err != nil {
			return fmt.Errorf("journal parse at seq %d: %w", ev.Seq, err)
		}
		if env.Seq != ev.Seq {
			return fmt.Errorf("journal seq mismatch: file %d frame %d", ev.Seq, env.Seq)
		}
		if err := a.state.apply(env); err != nil {
			return err
		}
		if env.IdemKey != "" {
			a.state.idem[env.IdemKey] = idemRecord{Seq: env.Seq, Type: env.Type, Response: env.Body}
		}
	}
	return nil
}

// appendEvent serializes a decision into the journal and reduces it. The
// caller must hold a.state.mu (write side) — except appendEvent itself also
// requires the outer writer mutex a.mu.
func (a *App) appendEvent(typ, idemKey string, at time.Time, body any, response any) (json.RawMessage, error) {
	respBytes, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	next := a.state.data.Seq + 1
	payload, err := store.Frame(next, typ, at, idemKey, body)
	if err != nil {
		return nil, err
	}
	if err := a.st.AppendEvent(next, typ, payload); err != nil {
		return nil, fmt.Errorf("journal write failed (old state remains readable): %w", err)
	}
	var env store.Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, err
	}
	if err := a.state.apply(env); err != nil {
		return nil, err
	}
	if idemKey != "" {
		a.state.idem[idemKey] = idemRecord{Seq: next, Type: typ, Response: respBytes}
	}
	return respBytes, nil
}

// idemHit returns the first recorded response for a key.
func (a *App) idemHit(key string) (json.RawMessage, bool) {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	r, ok := a.state.idem[key]
	return r.Response, ok
}

// Events returns the traceable event history.
func (a *App) Events() ([]EventRecord, error) {
	evs, err := a.st.ReadEvents()
	if err != nil {
		return nil, err
	}
	out := make([]EventRecord, 0, len(evs))
	for _, ev := range evs {
		var env store.Envelope
		if err := json.Unmarshal(ev.Data, &env); err != nil {
			return nil, err
		}
		out = append(out, EventRecord{Seq: env.Seq, Type: env.Type, At: env.At, IdemKey: env.IdemKey, Summary: summarize(env)})
	}
	return out, nil
}

func summarize(env store.Envelope) string {
	switch env.Type {
	case evNamePackageImported:
		var e evPackageImported
		if json.Unmarshal(env.Body, &e) == nil {
			return "import package " + e.Package.ID + " (" + e.Package.Name + ")"
		}
	case evNameRegionUpserted:
		var e evRegionUpserted
		if json.Unmarshal(env.Body, &e) == nil {
			return "upsert region " + e.Region.Name
		}
	case evNamePlanPrepared:
		var e evPlanPrepared
		if json.Unmarshal(env.Body, &e) == nil {
			return "prepare candidates for " + e.Plan.Region
		}
	case evNameBuildCreated:
		var e evBuildCreated
		if json.Unmarshal(env.Body, &e) == nil {
			return "create build " + e.Build.ID + " for " + e.Build.Region
		}
	case evNameBlockWritten:
		var e evBlockWritten
		if json.Unmarshal(env.Body, &e) == nil {
			return "write block " + e.Tile.tileString() + " of " + e.BuildID
		}
	case evNameBuildAccepted:
		var e evBuildAccepted
		if json.Unmarshal(env.Body, &e) == nil {
			return "accept build " + e.Build.ID
		}
	case evNameBuildRejected:
		var e evBuildRejected
		if json.Unmarshal(env.Body, &e) == nil {
			return "reject build " + e.BuildID
		}
	case evNameBuildStarted:
		var e evBuildRecovered
		if json.Unmarshal(env.Body, &e) == nil {
			return "start writing build " + e.BuildID
		}
	case evNameBuildFailed:
		var e evBuildFailed
		if json.Unmarshal(env.Body, &e) == nil {
			return "build " + e.BuildID + " failed: " + e.Reason
		}
	case evNameBuildRecovered:
		var e evBuildRecovered
		if json.Unmarshal(env.Body, &e) == nil {
			return "resume build " + e.BuildID
		}
	}
	return env.Type
}

func (k tileKey) tileString() string { return fmt.Sprintf("%d/%d/%d", k.Z, k.X, k.Y) }

// errIfInvalidTile is a small guard used by import paths.
func errIfInvalid(t geo.TileID) error {
	if !geo.ValidIndex(t.Z, t.X, t.Y) {
		return geo.ErrOutOfRange
	}
	return nil
}

var _ = errors.Is
