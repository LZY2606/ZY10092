package ingest

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"tileforge/internal/geo"
	"tileforge/internal/imageproc"
	"tileforge/internal/model"
)

type PackageManifest struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	CRS       string         `json:"crs"`
	Scheme    string         `json:"scheme,omitempty"`
	License   string         `json:"license"`
	UpdatedAt time.Time      `json:"updated_at"`
	Bounds    *geo.BBox      `json:"bounds,omitempty"`
	Tiles     []TileManifest `json:"tiles"`
}

type TileManifest struct {
	Z         int       `json:"z"`
	X         int       `json:"x"`
	Y         int       `json:"y"`
	Scheme    string    `json:"scheme,omitempty"`
	Path      string    `json:"path"`
	BlobHash  string    `json:"blob_hash"`
	License   string    `json:"license,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Result struct {
	Package model.Package
	Files   map[string][]byte
}

func ParseArchive(data []byte) (Result, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Result{}, fmt.Errorf("read package zip: %w", err)
	}
	files := map[string]*zip.File{}
	var manifestFile *zip.File
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		clean := path.Clean(file.Name)
		if strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") {
			return Result{}, fmt.Errorf("unsafe path %q", file.Name)
		}
		files[clean] = file
		if clean == "manifest.json" {
			manifestFile = file
		}
	}
	if manifestFile == nil {
		return Result{}, fmt.Errorf("package is missing manifest.json")
	}
	manifestData, err := readZip(manifestFile)
	if err != nil {
		return Result{}, err
	}
	var manifest PackageManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return Result{}, fmt.Errorf("parse manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Result{}, err
	}
	if manifest.Bounds != nil {
		if !manifest.Bounds.Valid() {
			return Result{}, fmt.Errorf("package bounds are invalid")
		}
		if manifest.CRS == geo.EPSG3857 {
			for _, piece := range manifest.Bounds.Pieces() {
				if piece.South < geo.WebMercatorLatitudeLimit() || piece.North > -geo.WebMercatorLatitudeLimit() {
					return Result{}, fmt.Errorf("EPSG:3857 cannot represent polar range outside %.7f..%.7f", geo.WebMercatorLatitudeLimit(), -geo.WebMercatorLatitudeLimit())
				}
			}
		}
	}
	id := manifest.ID
	if id == "" {
		id = stableID(manifestData)
	}
	pkg := model.Package{
		ID:        id,
		Name:      manifest.Name,
		Version:   manifest.Version,
		CRS:       manifest.CRS,
		Scheme:    manifest.Scheme,
		License:   manifest.License,
		UpdatedAt: manifest.UpdatedAt.UTC(),
		Status:    model.StatusPending,
		CreatedAt: time.Now().UTC(),
	}
	resultFiles := map[string][]byte{}
	blobHashes := map[string]struct{}{}
	for _, tile := range manifest.Tiles {
		zipFile := files[tile.Path]
		if zipFile == nil {
			return Result{}, fmt.Errorf("tile file %q is missing", tile.Path)
		}
		blob, err := readZip(zipFile)
		if err != nil {
			return Result{}, err
		}
		actualHash := imageproc.HashBytes(blob)
		if tile.BlobHash != actualHash {
			return Result{}, fmt.Errorf("tile %s hash mismatch: declared %s got %s", tile.Path, tile.BlobHash, actualHash)
		}
		scheme := tile.Scheme
		if scheme == "" {
			scheme = manifest.Scheme
		}
		if scheme == "" {
			scheme = geo.XYZ
		}
		bounds, bboxErr := geo.TileBBox(manifest.CRS, tile.Z, tile.X, tile.Y, scheme)
		pt := model.PackageTile{
			Z: tile.Z, X: tile.X, Y: tile.Y, Scheme: scheme, Path: tile.Path,
			BlobHash: actualHash, License: strings.TrimSpace(tile.License),
			UpdatedAt: tileTime(tile.UpdatedAt, manifest.UpdatedAt), BBox: bounds,
		}
		if bboxErr != nil {
			pt.Isolated = true
			pt.Reason = bboxErr.Error()
			pkg.Isolated = append(pkg.Isolated, pt)
			resultFiles[actualHash] = blob
			blobHashes[actualHash] = struct{}{}
			continue
		}
		img, _, err := imageproc.DecodePNG(blob)
		if err != nil {
			pt.Isolated = true
			pt.Reason = err.Error()
			pkg.Isolated = append(pkg.Isolated, pt)
		} else {
			pt.PixelHash = imageproc.PixelHash(img)
			pkg.Tiles = append(pkg.Tiles, pt)
		}
		resultFiles[actualHash] = blob
		blobHashes[actualHash] = struct{}{}
	}
	if len(pkg.Tiles) == 0 {
		pkg.Status = model.StatusRejected
		pkg.Reason = "package contains no valid in-range PNG tiles"
		pkg.DecidedAt = time.Now().UTC()
	}
	for hash := range blobHashes {
		pkg.BlobHashes = append(pkg.BlobHashes, hash)
	}
	return Result{Package: pkg, Files: resultFiles}, nil
}

func validateManifest(manifest PackageManifest) error {
	if strings.TrimSpace(manifest.Name) == "" {
		return fmt.Errorf("package name is required")
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return fmt.Errorf("package version is required")
	}
	if strings.TrimSpace(manifest.License) == "" {
		return fmt.Errorf("package license is required")
	}
	if manifest.UpdatedAt.IsZero() {
		return fmt.Errorf("package updated_at is required")
	}
	if manifest.CRS != geo.EPSG4326 && manifest.CRS != geo.EPSG3857 {
		return fmt.Errorf("crs must be EPSG:4326 or EPSG:3857")
	}
	if manifest.Scheme != "" && manifest.Scheme != geo.XYZ && manifest.Scheme != geo.TMS {
		return fmt.Errorf("package scheme must be xyz or tms")
	}
	if len(manifest.Tiles) == 0 {
		return fmt.Errorf("package must contain at least one tile")
	}
	paths := map[string]struct{}{}
	for _, tile := range manifest.Tiles {
		if strings.TrimSpace(tile.Path) == "" {
			return fmt.Errorf("tile path is required")
		}
		if tile.BlobHash == "" {
			return fmt.Errorf("tile %s blob_hash is required", tile.Path)
		}
		if tile.Scheme != "" && tile.Scheme != geo.XYZ && tile.Scheme != geo.TMS {
			return fmt.Errorf("tile %s scheme must be xyz or tms", tile.Path)
		}
		if _, exists := paths[tile.Path]; exists {
			return fmt.Errorf("duplicate tile path %s", tile.Path)
		}
		paths[tile.Path] = struct{}{}
	}
	return nil
}

func tileTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback.UTC()
	}
	return value.UTC()
}

func readZip(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if file.UncompressedSize64 > 32*1024*1024 {
		return nil, fmt.Errorf("tile %s is larger than 32 MiB", file.Name)
	}
	return io.ReadAll(io.LimitReader(reader, 32*1024*1024+1))
}

func stableID(manifest []byte) string {
	sum := sha256.Sum256(manifest)
	return fmt.Sprintf("pkg_%x", sum[:12])
}
