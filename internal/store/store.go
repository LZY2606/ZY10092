package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tileforge/internal/model"
)

var ErrNotFound = errors.New("not found")

type CASFault struct {
	mu       sync.Mutex
	failAt   int
	puts     int
	hashOnce map[string]bool
}

func (f *CASFault) FailAfter(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt = n
}

func (f *CASFault) FailHashOnce(hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hashOnce == nil {
		f.hashOnce = map[string]bool{}
	}
	f.hashOnce[hash] = true
}

func (f *CASFault) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt = 0
	f.puts = 0
	f.hashOnce = nil
}

func (f *CASFault) check(hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if f.failAt > 0 && f.puts >= f.failAt {
		f.failAt = 0
		return errors.New("injected block write failure")
	}
	if f.hashOnce[hash] {
		delete(f.hashOnce, hash)
		return errors.New("injected content-addressed write failure")
	}
	return nil
}

type Store struct {
	root     string
	mu       sync.Mutex
	eventOut *os.File
	seq      int
	packages map[string]*model.Package
	builds   map[string]*model.BuildRecord
	policies []model.Policy
	idem     map[string]model.IdemResult
	Fault    *CASFault
}

func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "cas", "sha256"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "builds"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		return nil, err
	}
	eventPath := filepath.Join(root, "events.log")
	if err := repairEventLog(eventPath); err != nil {
		return nil, err
	}
	s := &Store{
		root:     root,
		packages: map[string]*model.Package{},
		builds:   map[string]*model.BuildRecord{},
		idem:     map[string]model.IdemResult{},
		Fault:    &CASFault{},
	}
	if err := s.replay(); err != nil {
		return nil, err
	}
	out, err := os.OpenFile(eventPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s.eventOut = out
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventOut == nil {
		return nil
	}
	err := s.eventOut.Sync()
	if closeErr := s.eventOut.Close(); err == nil {
		err = closeErr
	}
	s.eventOut = nil
	return err
}

func repairEventLog(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	good := make([]string, 0, len(lines))
	corrupt := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event model.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			corrupt = true
			break
		}
		good = append(good, line)
	}
	if !corrupt {
		return nil
	}
	tmp := path + ".recover.tmp"
	content := strings.Join(good, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := fsyncParent(tmp); err != nil {
		return err
	}
	backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if err := os.Rename(path, backup); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return fsyncParent(path)
}

func (s *Store) replay() error {
	path := filepath.Join(s.root, "events.log")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event model.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return fmt.Errorf("event log contains unrepaired record: %w", err)
		}
		if err := s.apply(event); err != nil {
			return err
		}
		if event.Seq > s.seq {
			s.seq = event.Seq
		}
	}
	return nil
}

func (s *Store) apply(event model.Event) error {
	if event.IdemKey != "" {
		result := model.IdemResult{Status: 200, Kind: event.Type, ID: ""}
		if event.Idem != nil {
			result = *event.Idem
		}
		if event.Package != nil {
			result.ID = event.Package.ID
		}
		if event.Build != nil {
			result.ID = event.Build.BuildID
		}
		if _, exists := s.idem[event.IdemKey]; !exists {
			s.idem[event.IdemKey] = result
		}
	}
	switch event.Type {
	case "package_registered", "package_rejected", "package_accepted", "package_decision_rejected":
		if event.Package == nil {
			return fmt.Errorf("event %d missing package", event.Seq)
		}
		copyValue := *event.Package
		s.packages[copyValue.ID] = &copyValue
	case "policy_set":
		if event.Policy != nil {
			s.policies = append(s.policies, *event.Policy)
		}
	case "build_started", "build_recovering", "build_failed", "build_published":
		if event.Build == nil {
			return fmt.Errorf("event %d missing build", event.Seq)
		}
		copyValue := *event.Build
		s.builds[copyValue.BuildID] = &copyValue
	case "build_block_written":
		if event.Build != nil {
			build, ok := s.builds[event.Build.BuildID]
			if !ok {
				copyValue := *event.Build
				build = &copyValue
				s.builds[build.BuildID] = build
			}
			if event.BlockHash != "" && !contains(build.Manifest.BlockHashes, event.BlockHash) {
				build.Manifest.BlockHashes = append(build.Manifest.BlockHashes, event.BlockHash)
			}
		}
	case "idempotency_result":
		if event.Idem != nil {
			s.idem[event.IdemKey] = *event.Idem
		}
	}
	return nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (s *Store) Append(event model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Seq = s.seq + 1
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := s.eventOut.Write(line); err != nil {
		return err
	}
	if err := s.eventOut.Sync(); err != nil {
		return err
	}
	if err := s.apply(event); err != nil {
		return err
	}
	s.seq = event.Seq
	return nil
}

func (s *Store) Idem(key string) (model.IdemResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, ok := s.idem[key]
	return result, ok
}

func (s *Store) Packages() []model.Package {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Package, 0, len(s.packages))
	for _, pkg := range s.packages {
		out = append(out, *pkg)
	}
	return out
}

func (s *Store) Package(id string) (model.Package, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pkg, ok := s.packages[id]
	if !ok {
		return model.Package{}, ErrNotFound
	}
	return *pkg, nil
}

func (s *Store) Builds() []model.BuildRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.BuildRecord, 0, len(s.builds))
	for _, build := range s.builds {
		out = append(out, *build)
	}
	return out
}

func (s *Store) Build(id string) (model.BuildRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	build, ok := s.builds[id]
	if !ok {
		return model.BuildRecord{}, ErrNotFound
	}
	return *build, nil
}

func (s *Store) CurrentPolicy(region string) *model.Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.policies) - 1; i >= 0; i-- {
		if region == "" || s.policies[i].Region.String() == region {
			policy := s.policies[i]
			return &policy
		}
	}
	return nil
}

func (s *Store) Policies() []model.Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Policy, len(s.policies))
	copy(out, s.policies)
	return out
}

func (s *Store) Events() ([]model.Event, error) {
	s.mu.Lock()
	path := filepath.Join(s.root, "events.log")
	s.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var events []model.Event
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event model.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *Store) CASPath(hash string) string {
	if len(hash) != 71 || !strings.HasPrefix(hash, "sha256:") {
		return ""
	}
	return filepath.Join(s.root, "cas", "sha256", hash[7:9], hash[9:])
}

func (s *Store) HasCAS(hash string) bool {
	if path := s.CASPath(hash); path != "" {
		_, err := os.Stat(path)
		return err == nil
	}
	return false
}

func (s *Store) PutCAS(hash string, data []byte) (string, error) {
	if err := s.Fault.check(hash); err != nil {
		return "", err
	}
	path := s.CASPath(hash)
	if path == "" {
		return "", fmt.Errorf("invalid content hash %q", hash)
	}
	if _, err := os.Stat(path); err == nil {
		existing, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if fmt.Sprintf("sha256:%x", sha256Sum(existing)) != hash {
			return "", fmt.Errorf("existing CAS block %s is corrupt", hash)
		}
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(s.root, "tmp", fmt.Sprintf("cas-%d.tmp", time.Now().UnixNano()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := fsyncFile(tmp); err != nil {
		return "", err
	}
	actual := fmt.Sprintf("sha256:%x", sha256Sum(data))
	if actual != hash {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("CAS hash mismatch: declared %s got %s", hash, actual)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, fsyncParent(path)
}

func (s *Store) ReadCAS(hash string) ([]byte, error) {
	path := s.CASPath(hash)
	if path == "" {
		return nil, ErrNotFound
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

func (s *Store) WriteFileAtomic(relative string, data []byte) error {
	path := filepath.Join(s.root, filepath.Clean("/"+relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := fsyncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return fsyncParent(path)
}

func (s *Store) ReadFile(relative string) ([]byte, error) {
	path := filepath.Join(s.root, filepath.Clean("/"+relative))
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

func (s *Store) CommitFile(stagingRelative, finalRelative string) error {
	staging := filepath.Join(s.root, filepath.Clean("/"+stagingRelative))
	final := filepath.Join(s.root, filepath.Clean("/"+finalRelative))
	if err := os.Rename(staging, final); err != nil {
		return err
	}
	return fsyncParent(final)
}

func (s *Store) Root() string { return s.root }
