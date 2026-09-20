package service

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"tileforge/internal/geo"
	"tileforge/internal/imageproc"
	"tileforge/internal/ingest"
	"tileforge/internal/model"
	"tileforge/internal/store"
)

type BuildRequest struct {
	Region geo.BBox `json:"region"`
	Zoom   int      `json:"zoom"`
}

type Service struct {
	store *store.Store
	mu    sync.Mutex
}

func New(st *store.Store) *Service {
	return &Service{store: st}
}

func (s *Service) Store() *store.Store { return s.store }

func (s *Service) IngestPackage(data []byte, idemKey string) (model.Package, int, error) {
	if idemKey != "" {
		if result, ok := s.store.Idem(idemKey); ok {
			return s.idemPackage(result)
		}
	}
	result, err := ingest.ParseArchive(data)
	if err != nil {
		if idemKey != "" {
			_ = s.store.Append(model.Event{Type: "idempotency_result", IdemKey: idemKey, Idem: &model.IdemResult{Status: 400, Kind: "package_upload", Body: []byte(err.Error())}})
		}
		return model.Package{}, 400, err
	}
	pkg := result.Package
	if existing, err := s.store.Package(pkg.ID); err == nil {
		if idemKey != "" {
			body, _ := json.Marshal(existing)
			_ = s.store.Append(model.Event{Type: "idempotency_result", IdemKey: idemKey, Idem: &model.IdemResult{Status: 409, Kind: "package_conflict", ID: existing.ID, Body: body}})
		}
		return existing, 409, fmt.Errorf("package %s already exists", pkg.ID)
	}
	for hash, blob := range result.Files {
		if _, err := s.store.PutCAS(hash, blob); err != nil {
			return model.Package{}, 500, fmt.Errorf("write tile content: %w", err)
		}
	}
	sort.Strings(pkg.BlobHashes)
	eventType := "package_registered"
	if pkg.Status == model.StatusRejected {
		eventType = "package_rejected"
	}
	if err := s.store.Append(model.Event{Type: eventType, IdemKey: idemKey, Package: &pkg}); err != nil {
		return model.Package{}, 500, err
	}
	return pkg, 201, nil
}

func (s *Service) idemPackage(result model.IdemResult) (model.Package, int, error) {
	if len(result.Body) > 0 && (result.Kind == "package_conflict" || strings.HasPrefix(result.Kind, "package")) {
		var pkg model.Package
		if err := json.Unmarshal(result.Body, &pkg); err == nil {
			return pkg, result.Status, nil
		}
	}
	if result.ID != "" {
		pkg, err := s.store.Package(result.ID)
		if err == nil {
			return pkg, result.Status, nil
		}
	}
	return model.Package{}, result.Status, errors.New(string(result.Body))
}

func (s *Service) DecidePackage(id, status string, idemKey string) (model.Package, int, error) {
	if status != model.StatusAccepted && status != model.StatusRejected {
		return model.Package{}, 400, errors.New("decision must be accepted or rejected")
	}
	if idemKey != "" {
		if result, ok := s.store.Idem(idemKey); ok {
			return s.idemPackage(result)
		}
	}
	pkg, err := s.store.Package(id)
	if errors.Is(err, store.ErrNotFound) {
		return model.Package{}, 404, err
	}
	if err != nil {
		return model.Package{}, 500, err
	}
	if pkg.Status != model.StatusPending {
		return pkg, 409, fmt.Errorf("package is already %s", pkg.Status)
	}
	pkg.Status = status
	pkg.DecidedAt = time.Now().UTC()
	if status == model.StatusRejected {
		pkg.Reason = "rejected during review"
	}
	eventType := "package_accepted"
	if status == model.StatusRejected {
		eventType = "package_decision_rejected"
	}
	if err := s.store.Append(model.Event{Type: eventType, IdemKey: idemKey, Package: &pkg}); err != nil {
		return model.Package{}, 500, err
	}
	return pkg, 200, nil
}

func (s *Service) SetPolicy(policy model.Policy, idemKey string) (model.Policy, int, error) {
	if !policy.Region.Valid() {
		return policy, 400, errors.New("policy region must be valid and south < north")
	}
	if idemKey != "" {
		if result, ok := s.store.Idem(idemKey); ok {
			if existing := s.store.CurrentPolicy(result.ID); existing != nil {
				return *existing, result.Status, nil
			}
		}
	}
	policy.UpdatedAt = time.Now().UTC()
	if err := s.store.Append(model.Event{Type: "policy_set", IdemKey: idemKey, Policy: &policy, Idem: &model.IdemResult{Status: 200, Kind: "policy", ID: policy.Region.String()}}); err != nil {
		return policy, 500, err
	}
	return policy, 200, nil
}

func (s *Service) StartBuild(req BuildRequest, idemKey string) (model.BuildRecord, int, error) {
	if idemKey != "" {
		if result, ok := s.store.Idem(idemKey); ok {
			build, err := s.store.Build(result.ID)
			return build, result.Status, err
		}
	}
	if !req.Region.Valid() {
		return model.BuildRecord{}, 400, errors.New("build region must be valid and south < north")
	}
	if req.Zoom < 0 || req.Zoom > 28 {
		return model.BuildRecord{}, 400, errors.New("zoom must be between 0 and 28")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	packages, sources, err := s.acceptedSources()
	if err != nil {
		return model.BuildRecord{}, 500, err
	}
	coords := geo.CanonicalTilesInBBox(req.Zoom, req.Region)
	now := time.Now().UTC()
	manifest := model.BuildManifest{
		Schema: "tileforge.build/v1", Region: req.Region, Zoom: req.Zoom,
		Status: "building", PackageIDs: packages, CreatedAt: now, UpdatedAt: now,
		SourceSummary: map[string]int{},
	}
	for _, coord := range coords {
		candidates := s.candidatesFor(coord, sources)
		issues := geo.AnalyzeTile(coord, candidates)
		tileManifest := model.TileManifest{Z: coord.Z, X: coord.X, Y: coord.Y, Issues: issues}
		tileManifest.BBox, _ = geo.TileBBox(geo.EPSG4326, coord.Z, coord.X, coord.Y, geo.XYZ)
		for _, candidate := range candidates {
			tileManifest.Candidates = append(tileManifest.Candidates, candidate.Tile)
		}
		if len(candidates) > 0 && candidates[0].Tile.BlobHash != "" {
			selected := candidates[0].Tile
			tileManifest.Selected = &selected
			if candidates[0].DateInverted {
				tileManifest.Issues = append(tileManifest.Issues, geo.Issue{Type: "selected_date_inversion", Tile: coord.Key(), Packages: []string{selected.PackageID}, Message: "priority or lock selected an older source than another candidate"})
			}
		} else {
			tileManifest.Hole = true
			if c := lockedPolicyFor(s.store.Policies(), tileManifest.BBox); c != nil && c.LockedPackage != "" {
				tileManifest.Issues = append(tileManifest.Issues, geo.Issue{Type: "locked_version_unavailable", Tile: coord.Key(), Packages: []string{c.LockedPackage}, Message: "locked package/version has no source intersecting this output tile"})
			}
		}
		manifest.Tiles = append(manifest.Tiles, tileManifest)
	}
	manifest.BuildID = "build_" + hashJSON(struct {
		Region geo.BBox             `json:"region"`
		Zoom   int                  `json:"zoom"`
		Tiles  []model.TileManifest `json:"tiles"`
	}{Region: req.Region, Zoom: req.Zoom, Tiles: manifest.Tiles})[:24]
	record := model.BuildRecord{BuildID: manifest.BuildID, Status: "building", Manifest: manifest}
	if _, err := s.store.Build(record.BuildID); err == nil {
		existing, _ := s.store.Build(record.BuildID)
		return existing, 409, fmt.Errorf("build %s already exists", record.BuildID)
	}
	if err := s.store.Append(model.Event{Type: "build_started", IdemKey: idemKey, Build: &record, Idem: &model.IdemResult{Status: 202, Kind: "build", ID: record.BuildID}}); err != nil {
		return model.BuildRecord{}, 500, err
	}
	if err := s.renderBuild(record); err != nil {
		failed := record
		failed.Status = model.StatusFailed
		failed.Reason = err.Error()
		_ = s.store.Append(model.Event{Type: "build_failed", Build: &failed, Reason: err.Error()})
		return failed, 500, err
	}
	published, _ := s.store.Build(record.BuildID)
	return published, 201, nil
}

func lockedPolicyFor(policies []model.Policy, bounds geo.BBox) *model.Policy {
	for i := range policies {
		if geo.BBoxesOverlap(policies[i].Region, bounds) {
			return &policies[i]
		}
	}
	return nil
}

func (s *Service) acceptedSources() ([]string, []geo.SourceTile, error) {
	var ids []string
	var sources []geo.SourceTile
	for _, pkg := range s.store.Packages() {
		if pkg.Status != model.StatusAccepted {
			continue
		}
		ids = append(ids, pkg.ID)
		for _, tile := range pkg.Tiles {
			license := tile.License
			if license == "" {
				license = pkg.License
			}
			updated := tile.UpdatedAt
			if updated.IsZero() {
				updated = pkg.UpdatedAt
			}
			sources = append(sources, geo.SourceTile{
				PackageID: pkg.ID, Version: pkg.Version, License: license, CRS: pkg.CRS,
				UpdatedAt: updated, SourceZ: tile.Z, SourceX: tile.X, SourceY: tile.Y,
				BBox: tile.BBox, BlobHash: tile.BlobHash, PixelHash: tile.PixelHash,
			})
		}
	}
	sort.Strings(ids)
	return ids, sources, nil
}

func (s *Service) candidatesFor(coord geo.TileCoord, all []geo.SourceTile) []geo.Candidate {
	candidates := geo.CandidatesForTile(coord, all)
	bounds, _ := geo.TileBBox(geo.EPSG4326, coord.Z, coord.X, coord.Y, geo.XYZ)
	policy := lockedPolicyFor(s.store.Policies(), bounds)
	if policy == nil {
		return candidates
	}
	filtered := make([]geo.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if policy.LockedPackage != "" {
			if candidate.Tile.PackageID == policy.LockedPackage && (policy.LockedVersion == "" || candidate.Tile.Version == policy.LockedVersion) {
				candidate.Tile.LockVersion = true
				filtered = append(filtered, candidate)
			}
			continue
		}
		if candidate.Tile.PackageID == policy.PriorityPackage {
			candidate.Tile.Priority = 100
		}
		filtered = append(filtered, candidate)
	}
	if policy.LockedPackage != "" && len(filtered) == 0 {
		return nil
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i].Tile, filtered[j].Tile
		if a.LockVersion != b.LockVersion {
			return a.LockVersion
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.PackageID+a.BlobHash < b.PackageID+b.BlobHash
	})
	return filtered
}

func hashJSON(value any) string {
	data, _ := imageproc.CanonicalJSON(value)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}
