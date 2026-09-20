package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"

	"gsb/internal/geo"
	"gsb/internal/store"
)

func shaHex(data []byte) string { return store.SHA256(data) }

func sortInts(s []int) { sort.Ints(s) }

func (a *App) newPackageID(name string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return fmt.Sprintf("pkg-%s", hex.EncodeToString(b))
}

func (a *App) newBuildID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("bld-%s", hex.EncodeToString(b))
}

func cloneTiles(ts []Tile) []Tile {
	out := make([]Tile, len(ts))
	copy(out, ts)
	return out
}

func tileKeysEqual(a, b geo.TileID) bool { return a == b }

func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [24]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
