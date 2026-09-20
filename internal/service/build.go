package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"path"
	"sort"
	"strings"
	"time"

	"tileforge/internal/geo"
	"tileforge/internal/imageproc"
	"tileforge/internal/model"
	"tileforge/internal/store"
)

type renderedTile struct {
	manifest model.TileManifest
	image    image.Image
	blob     []byte
}

func (s *Service) finishBuild(id string) {
	s.mu.Lock()
	record, err := s.store.Build(id)
	s.mu.Unlock()
	if err != nil {
		return
	}
	if record.Status == model.StatusPublished {
		return
	}
	if err := s.renderBuild(record); err != nil {
		failed := record
		failed.Status = model.StatusFailed
		failed.Reason = err.Error()
		_ = s.store.Append(model.Event{Type: "build_failed", Build: &failed, Reason: err.Error()})
	}
}

func (s *Service) RecoverBuild(id string) (model.BuildRecord, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.store.Build(id)
	if errors.Is(err, store.ErrNotFound) {
		return record, 404, err
	}
	if err != nil {
		return record, 500, err
	}
	if record.Status == model.StatusPublished {
		return record, 200, nil
	}
	record.Status = model.StatusRecovering
	record.Reason = "resuming verified content-addressed blocks"
	if err := s.store.Append(model.Event{Type: "build_recovering", Build: &record}); err != nil {
		return record, 500, err
	}
	if err := s.renderBuild(record); err != nil {
		failed := record
		failed.Status = model.StatusFailed
		failed.Reason = err.Error()
		_ = s.store.Append(model.Event{Type: "build_failed", Build: &failed, Reason: err.Error()})
		return failed, 500, err
	}
	published, _ := s.store.Build(id)
	return published, 200, nil
}

func (s *Service) RecoverInterrupted() []model.BuildRecord {
	var recovered []model.BuildRecord
	for _, build := range s.store.Builds() {
		if build.Status == "building" {
			if record, _, err := s.RecoverBuild(build.BuildID); err == nil {
				recovered = append(recovered, record)
			}
		}
	}
	return recovered
}

func (s *Service) renderBuild(seed model.BuildRecord) error {
	record := cloneBuild(seed)
	rendered := make([]renderedTile, 0, len(record.Manifest.Tiles))
	for _, tile := range record.Manifest.Tiles {
		var blob []byte
		var img image.Image
		var err error
		if tile.Hole || tile.Selected == nil || tile.Selected.BlobHash == "" {
			blob, img, err = imageproc.RenderTransparent()
			tile.Hole = true
		} else {
			sourceBlob, readErr := s.store.ReadCAS(tile.Selected.BlobHash)
			if readErr != nil {
				return fmt.Errorf("missing source block %s: %w", tile.Selected.BlobHash, readErr)
			}
			sourceImage, _, decodeErr := imageproc.DecodePNG(sourceBlob)
			if decodeErr != nil {
				return decodeErr
			}
			blob, img, err = imageproc.RenderTile(*tile.Selected, tile.BBox, sourceImage)
		}
		if err != nil {
			return err
		}
		tile.PixelHash = imageproc.PixelHash(img)
		tile.BlobHash = imageproc.HashBytes(blob)
		if !s.store.HasCAS(tile.BlobHash) {
			if _, err := s.store.PutCAS(tile.BlobHash, blob); err != nil {
				return fmt.Errorf("write output block for %s: %w", tileKey(tile), err)
			}
			blockRecord := cloneBuild(record)
			blockRecord.Status = model.StatusRecovering
			if err := s.store.Append(model.Event{Type: "build_block_written", Build: &blockRecord, Tile: tileKey(tile), BlockHash: tile.BlobHash}); err != nil {
				return err
			}
		}
		rendered = append(rendered, renderedTile{manifest: tile, image: img, blob: blob})
	}
	tileByCoord := map[string]renderedTile{}
	for _, item := range rendered {
		tileByCoord[tileKey(item.manifest)] = item
	}
	record.Manifest.Tiles = record.Manifest.Tiles[:0]
	record.Manifest.SourceSummary = map[string]int{}
	for _, item := range rendered {
		tile := item.manifest
		coord := geo.TileCoord{Z: tile.Z, X: tile.X, Y: tile.Y}
		if left, ok := tileByCoord[geo.TileCoord{Z: coord.Z, X: coord.X - 1, Y: coord.Y}.Key()]; ok && !left.manifest.Hole && !tile.Hole {
			equal := imageproc.EdgePixelsEqual(left.image, item.image, "vertical")
			tile.Issues = append(tile.Issues, seamIssue(coord, left.manifest.Selected, item.manifest.Selected, "left", equal)...)
		}
		if above, ok := tileByCoord[geo.TileCoord{Z: coord.Z, X: coord.X, Y: coord.Y - 1}.Key()]; ok && !above.manifest.Hole && !tile.Hole {
			equal := imageproc.EdgePixelsEqual(above.image, item.image, "horizontal")
			tile.Issues = append(tile.Issues, seamIssue(coord, above.manifest.Selected, item.manifest.Selected, "above", equal)...)
		}
		if tile.Selected != nil {
			record.Manifest.SourceSummary[tile.Selected.PackageID]++
		}
		record.Manifest.Tiles = append(record.Manifest.Tiles, tile)
	}
	blockSet := map[string]struct{}{}
	for _, tile := range record.Manifest.Tiles {
		blockSet[tile.BlobHash] = struct{}{}
	}
	record.Manifest.BlockHashes = record.Manifest.BlockHashes[:0]
	for hash := range blockSet {
		record.Manifest.BlockHashes = append(record.Manifest.BlockHashes, hash)
	}
	sort.Strings(record.Manifest.BlockHashes)
	record.Manifest.Status = model.StatusPublished
	record.Manifest.UpdatedAt = time.Now().UTC()
	record.Manifest.PublishedAt = record.Manifest.UpdatedAt
	record.Manifest.ManifestHash = manifestHash(record.Manifest)
	record.Status = model.StatusPublished
	record.Reason = ""
	manifestBytes, err := imageproc.CanonicalJSON(record.Manifest)
	if err != nil {
		return err
	}
	staging := path.Join("builds", record.BuildID+".staging.json")
	if err := s.store.WriteFileAtomic(staging, manifestBytes); err != nil {
		return err
	}
	if err := s.store.Append(model.Event{Type: "build_published", Build: &record}); err != nil {
		return err
	}
	return s.store.CommitFile(staging, path.Join("builds", record.BuildID+".json"))
}

func seamIssue(coord geo.TileCoord, a, b *geo.SourceTile, direction string, pixelsEqual bool) []geo.Issue {
	if a == nil || b == nil {
		return nil
	}
	neighbor := geo.TileCoord{Z: coord.Z}
	if direction == "left" {
		neighbor.X = coord.X - 1
		neighbor.Y = coord.Y
	} else {
		neighbor.X = coord.X
		neighbor.Y = coord.Y - 1
	}
	metadataEqual := a.PackageID == b.PackageID && a.Version == b.Version && a.License == b.License && a.UpdatedAt.Equal(b.UpdatedAt) && a.BlobHash == b.BlobHash
	if pixelsEqual && !metadataEqual {
		return []geo.Issue{{Type: "metadata_conflict", Tile: coord.Key(), Neighbor: neighbor.Key(), Packages: []string{a.PackageID, b.PackageID}, Message: "boundary pixels are identical, but provenance, license, version, or update time differs"}}
	}
	if !pixelsEqual {
		issueType := "pixel_seam"
		if !metadataEqual {
			issueType = "boundary_seam"
		}
		return []geo.Issue{{Type: issueType, Tile: coord.Key(), Neighbor: neighbor.Key(), Packages: []string{a.PackageID, b.PackageID}, Message: "adjacent output tiles expose a boundary pixel seam"}}
	}
	return nil
}

func tileKey(tile model.TileManifest) string {
	return fmt.Sprintf("%d/%d/%d", tile.Z, tile.X, tile.Y)
}

func cloneBuild(record model.BuildRecord) model.BuildRecord {
	data, _ := json.Marshal(record)
	var clone model.BuildRecord
	_ = json.Unmarshal(data, &clone)
	return clone
}

func manifestHash(manifest model.BuildManifest) string {
	copyManifest := cloneManifest(manifest)
	copyManifest.ManifestHash = ""
	return "sha256:" + hashJSON(copyManifest)
}

func cloneManifest(manifest model.BuildManifest) model.BuildManifest {
	data, _ := json.Marshal(manifest)
	var clone model.BuildManifest
	_ = json.Unmarshal(data, &clone)
	return clone
}

func (s *Service) BuildManifestBytes(id string) ([]byte, error) {
	return s.store.ReadFile(path.Join("builds", id+".json"))
}

func (s *Service) ExportBuild(id string) ([]byte, error) {
	record, err := s.store.Build(id)
	if err != nil {
		return nil, err
	}
	if record.Status != model.StatusPublished {
		return nil, fmt.Errorf("build %s is not published", id)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	manifestBytes, err := imageproc.CanonicalJSON(record.Manifest)
	if err != nil {
		return nil, err
	}
	if err := writeZip(writer, "build.json", manifestBytes); err != nil {
		return nil, err
	}
	exportPackages := make([]model.Package, 0)
	for _, pkg := range s.store.Packages() {
		if record.Manifest.SourceSummary[pkg.ID] > 0 {
			exportPackages = append(exportPackages, pkg)
		}
	}
	packageBytes, _ := imageproc.CanonicalJSON(exportPackages)
	if err := writeZip(writer, "packages.json", packageBytes); err != nil {
		return nil, err
	}
	outputBlocks := map[string]struct{}{}
	for _, hash := range record.Manifest.BlockHashes {
		outputBlocks[hash] = struct{}{}
	}
	for _, hash := range record.Manifest.BlockHashes {
		data, err := s.store.ReadCAS(hash)
		if err != nil {
			return nil, err
		}
		if err := writeZip(writer, "cas/"+strings.TrimPrefix(hash, "sha256:"), data); err != nil {
			return nil, err
		}
	}
	sourceHashes := map[string]struct{}{}
	for _, pkg := range exportPackages {
		for _, hash := range pkg.BlobHashes {
			sourceHashes[hash] = struct{}{}
		}
	}
	for hash := range sourceHashes {
		if _, exists := outputBlocks[hash]; exists {
			continue
		}
		data, err := s.store.ReadCAS(hash)
		if err != nil {
			return nil, err
		}
		if err := writeZip(writer, "sources/"+strings.TrimPrefix(hash, "sha256:"), data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeZip(writer *zip.Writer, name string, data []byte) error {
	file, err := writer.Create(name)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	return err
}
