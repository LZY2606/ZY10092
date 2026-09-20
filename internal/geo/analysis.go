package geo

import (
	"sort"
	"time"
)

type SourceTile struct {
	PackageID   string    `json:"package_id"`
	Version     string    `json:"version"`
	License     string    `json:"license"`
	CRS         string    `json:"crs"`
	UpdatedAt   time.Time `json:"updated_at"`
	SourceZ     int       `json:"source_z"`
	SourceX     int       `json:"source_x"`
	SourceY     int       `json:"source_y"`
	BBox        BBox      `json:"bbox"`
	BlobHash    string    `json:"blob_hash"`
	PixelHash   string    `json:"pixel_hash"`
	LockVersion bool      `json:"-"`
	Priority    int       `json:"-"`
}

type Candidate struct {
	Tile         SourceTile `json:"tile"`
	DateInverted bool       `json:"-"`
}

type Issue struct {
	Type     string   `json:"type"`
	Tile     string   `json:"tile,omitempty"`
	Neighbor string   `json:"neighbor,omitempty"`
	Packages []string `json:"packages,omitempty"`
	Message  string   `json:"message"`
}

func CandidatesForTile(target TileCoord, sources []SourceTile) []Candidate {
	targetBounds, err := TileBBox(EPSG4326, target.Z, target.X, target.Y, XYZ)
	if err != nil {
		return nil
	}
	matches := make([]SourceTile, 0)
	for _, source := range sources {
		if BBoxesOverlap(targetBounds, source.BBox) {
			matches = append(matches, source)
		}
	}
	var newest time.Time
	for _, match := range matches {
		if match.UpdatedAt.After(newest) {
			newest = match.UpdatedAt
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.LockVersion != b.LockVersion {
			return a.LockVersion
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		if a.SourceZ != b.SourceZ {
			return a.SourceZ > b.SourceZ
		}
		if a.PackageID != b.PackageID {
			return a.PackageID < b.PackageID
		}
		return sourceTileStableKey(a) < sourceTileStableKey(b)
	})
	out := make([]Candidate, 0, len(matches))
	for _, match := range matches {
		out = append(out, Candidate{Tile: match, DateInverted: match.UpdatedAt.Before(newest)})
	}
	return out
}

func sourceTileStableKey(t SourceTile) string {
	return t.PackageID + ":" + t.Version + ":" + t.BlobHash + ":" + t.BBox.String()
}

func AnalyzeTile(target TileCoord, candidates []Candidate) []Issue {
	var issues []Issue
	if len(candidates) == 0 {
		issues = append(issues, Issue{Type: "hole", Tile: target.Key(), Message: "no accepted input tile intersects output tile"})
		return issues
	}
	packageSet := map[string]struct{}{}
	licenseSet := map[string]struct{}{}
	sourceSet := map[string]struct{}{}
	for _, candidate := range candidates {
		packageSet[candidate.Tile.PackageID] = struct{}{}
		licenseSet[candidate.Tile.License] = struct{}{}
		sourceSet[candidate.Tile.PackageID+":"+candidate.Tile.BlobHash+":"+candidate.Tile.BBox.String()] = struct{}{}
		if candidate.DateInverted {
			issues = append(issues, Issue{
				Type:     "date_inversion",
				Tile:     target.Key(),
				Packages: []string{candidate.Tile.PackageID},
				Message:  "selected or available source is older than another intersecting source",
			})
		}
	}
	if len(sourceSet) > 1 {
		issues = append(issues, Issue{Type: "duplicate_coverage", Tile: target.Key(), Packages: sortedKeys(packageSet), Message: "multiple packages cover this output tile"})
	}
	if len(licenseSet) > 1 {
		issues = append(issues, Issue{Type: "license_conflict", Tile: target.Key(), Packages: sortedKeys(packageSet), Message: "intersecting sources do not carry the same license"})
	}
	return issues
}

func SeamIssues(target TileCoord, left, above *Candidate, leftPixelsEqual, abovePixelsEqual bool) []Issue {
	var issues []Issue
	if left != nil {
		issue := seamIssue(target, left, "left", leftPixelsEqual)
		if issue != nil {
			issues = append(issues, *issue)
		}
	}
	if above != nil {
		issue := seamIssue(target, above, "above", abovePixelsEqual)
		if issue != nil {
			issues = append(issues, *issue)
		}
	}
	return issues
}

func seamIssue(target TileCoord, neighbor *Candidate, direction string, pixelsEqual bool) *Issue {
	current := neighbor.Tile
	if pixelsEqual {
		if current.License != "" || current.PixelHash != "" {
			return &Issue{Type: "metadata_conflict", Tile: target.Key(), Neighbor: direction, Packages: []string{current.PackageID}, Message: "boundary pixels are identical but source metadata or license terms differ"}
		}
		return nil
	}
	return &Issue{Type: "pixel_seam", Tile: target.Key(), Neighbor: direction, Packages: []string{current.PackageID}, Message: "adjacent output tiles have different boundary pixels"}
}

func sortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
