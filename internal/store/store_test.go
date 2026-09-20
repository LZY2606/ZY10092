package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContentAddressedPutGet(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	hash, existed, err := s.Put([]byte("tile-bytes"))
	if err != nil || existed {
		t.Fatalf("first put: %v %v", hash, err)
	}
	if _, existed, _ = s.Put([]byte("tile-bytes")); !existed {
		t.Fatal("identical content must be deduplicated")
	}
	got, err := s.Get(hash)
	if err != nil || string(got) != "tile-bytes" {
		t.Fatalf("get mismatch %q %v", got, err)
	}
}

func TestFailedWriteLeavesOldStateReadable(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	goodHash, _, err := s.Put([]byte("committed"))
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("disk full")
	s.Fault = func(op string) error {
		if len(op) > 4 && op[:4] == "put:" {
			return injected
		}
		return nil
	}
	if _, _, err := s.Put([]byte("new")); !errors.Is(err, injected) {
		t.Fatalf("expected injected failure, got %v", err)
	}
	s.Fault = nil
	if !s.Has(goodHash) {
		t.Fatal("committed block must remain readable after a failed write")
	}
	tmpEntries, _ := os.ReadDir(filepath.Join(dir, "tmp"))
	if len(tmpEntries) != 0 {
		t.Fatalf("partial temp files left behind: %d", len(tmpEntries))
	}
}

func TestJournalAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	now := time.Now().UTC()
	for i := int64(1); i <= 3; i++ {
		data, err := Frame(i, "test", now, "", map[string]int64{"n": i})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AppendEvent(i, "test", data); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[2].Seq != 3 {
		t.Fatalf("replay mismatch %+v", evs)
	}
	last, _ := s.LastSeq()
	if last != 3 {
		t.Fatalf("last seq = %d", last)
	}

	s.Fault = func(op string) error {
		if op == "event:test" {
			return errors.New("journal io error")
		}
		return nil
	}
	bad, _ := Frame(4, "test", now, "", nil)
	if err := s.AppendEvent(4, "test", bad); err == nil {
		t.Fatal("expected journal failure")
	}
	s.Fault = nil
	last, _ = s.LastSeq()
	if last != 3 {
		t.Fatalf("failed event append must not advance journal, last=%d", last)
	}
}
