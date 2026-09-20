package app

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"gsb/internal/store"
)

// ExportBuild streams an accepted build as a self-contained tar archive:
//
//	manifest.json      - complete build manifest
//	blocks/<sha>       - every content-addressed tile
//	meta/build.json    - export metadata
type ExportMeta struct {
	ExportedAt time.Time `json:"exported_at"`
	Format     string    `json:"format"`
}

func (a *App) ExportBuild(id string, w io.Writer) error {
	a.state.mu.RLock()
	b, ok := a.state.data.Builds[id]
	a.state.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownBuild, id)
	}
	if b.Status != StatusAccepted {
		return fmt.Errorf("%w: build %s is %s, not accepted", ErrConflict, id, b.Status)
	}
	manBytes, err := a.st.Get(b.ManifestSHA)
	if err != nil {
		return err
	}
	var man Manifest
	if err := json.Unmarshal(manBytes, &man); err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	writeFile := func(name string, data []byte) error {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: time.Unix(0, 0)}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := writeFile("manifest.json", manBytes); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, t := range man.Tiles {
		if seen[t.SHA] {
			continue
		}
		seen[t.SHA] = true
		data, err := a.st.Get(t.SHA)
		if err != nil {
			return err
		}
		if err := writeFile("blocks/"+t.SHA, data); err != nil {
			return err
		}
	}
	meta, _ := json.MarshalIndent(ExportMeta{ExportedAt: time.Now().UTC(), Format: "gsb-export/1"}, "", "  ")
	if err := writeFile("meta/build.json", meta); err != nil {
		return err
	}
	return tw.Close()
}

// ReimportResult compares a re-imported export with its origin.
type ReimportResult struct {
	BuildID     string   `json:"build_id"`
	OriginID    string   `json:"origin_id"`
	ManifestSHA string   `json:"manifest_sha"`
	SameTiles   bool     `json:"same_tiles"`
	SameBounds  bool     `json:"same_bounds"`
	SameSources bool     `json:"same_sources"`
	Manifest    Manifest `json:"manifest"`
}

// ReimportBuild reads an exported tar, verifies every block, stores them, and
// creates an accepted build with identical tile numbering, bounds and source
// summary. It returns the detailed comparison against the original build.
func (a *App) ReimportBuild(r io.Reader, originID, idemKey string) (*ReimportResult, json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.mu.Lock()
	defer a.state.mu.Unlock()

	if idemKey != "" {
		if hit, ok := a.state.idem[idemKey]; ok {
			var rr ReimportResult
			if err := json.Unmarshal(hit.Response, &rr); err == nil {
				return &rr, hit.Response, nil
			}
		}
	}
	tr := tar.NewReader(r)
	var manBytes []byte
	blocks := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case hdr.Name == "manifest.json":
			manBytes = data
		case len(hdr.Name) > 7 && hdr.Name[:7] == "blocks/":
			blocks[hdr.Name[7:]] = data
		}
	}
	if manBytes == nil {
		return nil, nil, fmt.Errorf("%w: export missing manifest", ErrInvalidInput)
	}
	var man Manifest
	if err := json.Unmarshal(manBytes, &man); err != nil {
		return nil, nil, err
	}
	for _, t := range man.Tiles {
		data, ok := blocks[t.SHA]
		if !ok {
			return nil, nil, fmt.Errorf("%w: export missing block %s", ErrInvalidInput, t.SHA)
		}
		if store.SHA256(data) != t.SHA {
			return nil, nil, fmt.Errorf("%w: block %s corrupt", ErrInvalidInput, t.SHA)
		}
		if _, _, err := a.st.Put(data); err != nil {
			return nil, nil, err
		}
	}
	newID := a.newBuildID()
	if originID == "" {
		originID = man.ID
	}
	now := nowUTC()
	tiles := make([]BuildTile, 0, len(man.Tiles))
	for _, t := range man.Tiles {
		t.Written = true
		tiles = append(tiles, t)
	}
	rebuilt := Build{
		ID: newID, Region: man.Region, Zoom: man.Zoom, Status: StatusAccepted,
		Tiles: tiles, CreatedAt: now, UpdatedAt: now, AcceptedAt: &now,
		RulesHash: man.RulesHash, OriginID: originID,
	}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	rebuilt.ManifestSHA = store.SHA256(mb)
	if _, _, err := a.st.Put(mb); err != nil {
		return nil, nil, err
	}
	// Re-derive a manifest with the new build ID but identical content
	// coordinates, bounds and sources.
	newMan := man
	newMan.ID = newID
	newMB, err := json.MarshalIndent(newMan, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	newManHash := store.SHA256(newMB)
	if _, _, err := a.st.Put(newMB); err != nil {
		return nil, nil, err
	}
	rebuilt.ManifestSHA = newManHash

	// Compare against origin if present locally.
	res := &ReimportResult{BuildID: newID, OriginID: originID, ManifestSHA: newManHash, Manifest: newMan}
	res.SameTiles, res.SameBounds, res.SameSources = true, true, true
	if origin, ok := a.state.data.Builds[originID]; ok && origin.Status == StatusAccepted {
		origManBytes, oerr := a.st.Get(origin.ManifestSHA)
		if oerr == nil {
			var origMan Manifest
			if json.Unmarshal(origManBytes, &origMan) == nil {
				res.SameTiles = sameTileList(origMan.Tiles, newMan.Tiles)
				res.SameBounds = origMan.Bounds == newMan.Bounds
				res.SameSources = sameSources(origMan.Sources, newMan.Sources)
			}
		}
	}
	resp, err := a.appendEvent(evNameBuildAccepted, idemKey, now, evBuildAccepted{Build: rebuilt}, res)
	if err != nil {
		return nil, nil, err
	}
	return res, resp, nil
}

func sameTileList(a, b []BuildTile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ta, tb := a[i], b[i]
		if ta.Tile != tb.Tile || ta.SHA != tb.SHA || ta.PackageID != tb.PackageID || !ta.Captured.Equal(tb.Captured) || ta.License != tb.License {
			return false
		}
	}
	return true
}

func sameSources(a, b []SourceSummary) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
