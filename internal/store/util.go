package store

import (
	"crypto/sha256"
	"os"
	"path/filepath"
)

func sha256Sum(data []byte) [32]byte { return sha256.Sum256(data) }

func fsyncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func fsyncParent(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
