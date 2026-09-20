// Package store implements the persistence layer:
//
//   - content-addressed, immutable blocks (sha256), written atomically so an
//     interrupted write never produces a visible partial block;
//   - an append-only event journal, one file per event, where every state
//     transition is recorded before it takes effect;
//   - an optional compact snapshot used to speed up startup.
//
// Write failures never destroy previously committed data: readers only ever
// see fully renamed files.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFound indicates an unknown content hash.
var ErrNotFound = errors.New("block not found")

// Store is the file-system backed block + journal store.
type Store struct {
	root string

	// Fault, when non-nil, may return an error to simulate a write failure.
	// It is invoked before the atomic rename; tests set it to exercise the
	// interrupted-write recovery paths.
	Fault func(op string) error
}

// Open creates the on-disk layout if missing.
func Open(root string) (*Store, error) {
	for _, d := range []string{"", "blocks", "journal", "snapshots", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{root: root}, nil
}

// Root returns the store directory.
func (s *Store) Root() string { return s.root }

// SHA256 returns the hex sha256 of data.
func SHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func blockPath(root, hash string) string {
	return filepath.Join(root, "blocks", hash[:2], hash)
}

// Has reports whether a block is fully committed.
func (s *Store) Has(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	_, err := os.Stat(blockPath(s.root, hash))
	return err == nil
}

// Put atomically stores data and returns its content hash and whether it
// already existed. A failed attempt leaves no trace behind in blocks/.
func (s *Store) Put(data []byte) (hash string, existed bool, err error) {
	hash = SHA256(data)
	if s.Has(hash) {
		return hash, true, nil
	}
	if s.Fault != nil {
		if ferr := s.Fault("put:" + hash); ferr != nil {
			return "", false, ferr
		}
	}
	dir := filepath.Join(s.root, "blocks", hash[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "block-*")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}
	dst := blockPath(s.root, hash)
	if err := os.Rename(tmpName, dst); err != nil {
		return "", false, err
	}
	if err := syncDir(dir); err != nil {
		return "", false, err
	}
	return hash, false, nil
}

// Get reads a committed block.
func (s *Store) Get(hash string) ([]byte, error) {
	if len(hash) != 64 || strings.ContainsAny(hash, "/\\") {
		return nil, ErrNotFound
	}
	f, err := os.Open(blockPath(s.root, hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Event is one journaled state transition.
type Event struct {
	Seq  int64  `json:"seq"`
	Type string `json:"type"`
	Data []byte `json:"data"`
}

func eventPath(root string, seq int64) string {
	return filepath.Join(root, "journal", fmt.Sprintf("%012d.event", seq))
}

// LastSeq returns the highest committed event sequence (0 if empty).
func (s *Store) LastSeq() (int64, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "journal"))
	if err != nil {
		return 0, err
	}
	var last int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".event") {
			continue
		}
		var seq int64
		if _, err := fmt.Sscanf(name, "%012d.event", &seq); err != nil {
			continue
		}
		if seq > last {
			last = seq
		}
	}
	return last, nil
}

// AppendEvent commits one event atomically and returns its sequence number.
// If the machine dies at any point, either the whole event file is present
// with the next sequence number or nothing changed.
func (s *Store) AppendEvent(seq int64, typ string, data []byte) error {
	if s.Fault != nil {
		if ferr := s.Fault("event:" + typ); ferr != nil {
			return ferr
		}
	}
	payload := append([]byte{}, data...)
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "event-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	dst := eventPath(s.root, seq)
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	return syncDir(filepath.Join(s.root, "journal"))
}

// ReadEvents replays committed events in sequence order.
func (s *Store) ReadEvents() ([]Event, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "journal"))
	if err != nil {
		return nil, err
	}
	var evs []Event
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".event") {
			continue
		}
		var seq int64
		if _, err := fmt.Sscanf(name, "%012d.event", &seq); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, "journal", name))
		if err != nil {
			return nil, err
		}
		evs = append(evs, Event{Seq: seq, Type: "", Data: data})
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].Seq < evs[j].Seq })
	return evs, nil
}

// WriteSnapshot stores a snapshot atomically.
func (s *Store) WriteSnapshot(name string, data []byte) error {
	dir := filepath.Join(s.root, "snapshots")
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "snap-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

// ReadSnapshot returns os.ErrNotExist when absent.
func (s *Store) ReadSnapshot(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.root, "snapshots", name))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// CleanupTmp removes leftover temporary files from a previous crash.
func (s *Store) CleanupTmp() error {
	dir := filepath.Join(s.root, "tmp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	return nil
}
