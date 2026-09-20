package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"tileforge/internal/imageproc"
	"tileforge/internal/model"
)

func (s *Service) ImportBuild(data []byte, idemKey string) (model.BuildRecord, int, error) {
	if idemKey != "" {
		if result, ok := s.store.Idem(idemKey); ok {
			build, err := s.store.Build(result.ID)
			return build, result.Status, err
		}
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return model.BuildRecord{}, 400, fmt.Errorf("read export zip: %w", err)
	}
	files := map[string]*zip.File{}
	for _, file := range reader.File {
		if !file.FileInfo().IsDir() {
			files[file.Name] = file
		}
	}
	packageFile := files["packages.json"]
	var importPackages []model.Package
	if packageFile != nil {
		packageReader, err := packageFile.Open()
		if err != nil {
			return model.BuildRecord{}, 400, err
		}
		if err := json.NewDecoder(packageReader).Decode(&importPackages); err != nil {
			packageReader.Close()
			return model.BuildRecord{}, 400, err
		}
		packageReader.Close()
	}
	manifestFile := files["build.json"]
	if manifestFile == nil {
		return model.BuildRecord{}, 400, fmt.Errorf("export is missing build.json")
	}
	manifestReader, err := manifestFile.Open()
	if err != nil {
		return model.BuildRecord{}, 400, err
	}
	defer manifestReader.Close()
	var manifest model.BuildManifest
	if err := json.NewDecoder(manifestReader).Decode(&manifest); err != nil {
		return model.BuildRecord{}, 400, err
	}
	declaredHash := manifest.ManifestHash
	actualHash := manifestHash(manifest)
	if declaredHash == "" || declaredHash != actualHash {
		return model.BuildRecord{}, 400, fmt.Errorf("manifest hash mismatch: declared %s got %s", declaredHash, actualHash)
	}
	if existing, err := s.store.Build(manifest.BuildID); err == nil {
		if idemKey != "" {
			_ = s.store.Append(model.Event{Type: "idempotency_result", IdemKey: idemKey, Idem: &model.IdemResult{Status: 200, Kind: "import", ID: existing.BuildID}})
		}
		return existing, 200, nil
	}
	for _, pkg := range importPackages {
		if _, err := s.store.Package(pkg.ID); err == nil {
			continue
		}
		for _, hash := range pkg.BlobHashes {
			file := files["sources/"+strings.TrimPrefix(hash, "sha256:")]
			if file == nil {
				if _, exists := files["cas/"+strings.TrimPrefix(hash, "sha256:")]; exists {
					file = files["cas/"+strings.TrimPrefix(hash, "sha256:")]
				}
			}
			if file == nil {
				return model.BuildRecord{}, 400, fmt.Errorf("export is missing source block %s", hash)
			}
			reader, err := file.Open()
			if err != nil {
				return model.BuildRecord{}, 400, err
			}
			data, err := io.ReadAll(io.LimitReader(reader, 64*1024*1024+1))
			reader.Close()
			if err != nil || imageproc.HashBytes(data) != hash {
				return model.BuildRecord{}, 400, fmt.Errorf("import source block %s failed verification", hash)
			}
			if _, err := s.store.PutCAS(hash, data); err != nil {
				return model.BuildRecord{}, 500, err
			}
		}
		pkg.Status = model.StatusAccepted
		pkg.DecidedAt = manifest.PublishedAt
		copyPkg := pkg
		if err := s.store.Append(model.Event{Type: "package_accepted", Package: &copyPkg}); err != nil {
			return model.BuildRecord{}, 500, err
		}
	}
	for _, hash := range manifest.BlockHashes {
		file := files["cas/"+strings.TrimPrefix(hash, "sha256:")]
		if file == nil {
			return model.BuildRecord{}, 400, fmt.Errorf("export is missing block %s", hash)
		}
		reader, err := file.Open()
		if err != nil {
			return model.BuildRecord{}, 400, err
		}
		data, err := io.ReadAll(io.LimitReader(reader, 128*1024*1024+1))
		reader.Close()
		if err != nil {
			return model.BuildRecord{}, 400, err
		}
		if imageproc.HashBytes(data) != hash {
			return model.BuildRecord{}, 400, fmt.Errorf("import block %s failed verification", hash)
		}
		if _, err := s.store.PutCAS(hash, data); err != nil {
			return model.BuildRecord{}, 500, err
		}
	}
	record := model.BuildRecord{BuildID: manifest.BuildID, Status: model.StatusPublished, Manifest: manifest}
	manifestBytes, err := imageproc.CanonicalJSON(manifest)
	if err != nil {
		return model.BuildRecord{}, 500, err
	}
	staging := path.Join("builds", record.BuildID+".import.tmp")
	final := path.Join("builds", record.BuildID+".json")
	if err := s.store.WriteFileAtomic(staging, manifestBytes); err != nil {
		return model.BuildRecord{}, 500, err
	}
	if err := s.store.Append(model.Event{Type: "build_published", IdemKey: idemKey, Build: &record, Idem: &model.IdemResult{Status: 200, Kind: "import", ID: record.BuildID}}); err != nil {
		return model.BuildRecord{}, 500, err
	}
	if err := s.store.CommitFile(staging, final); err != nil {
		return model.BuildRecord{}, 500, err
	}
	return record, 200, nil
}
