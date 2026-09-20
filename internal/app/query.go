package app

import "sort"

// Package returns one package.
func (a *App) Package(id string) (*Package, bool) {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	p, ok := a.state.data.Packages[id]
	if !ok {
		return nil, false
	}
	cp := *p
	return &cp, true
}

// Packages lists all packages.
func (a *App) Packages() []Package {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	ps := a.state.packageList()
	out := make([]Package, 0, len(ps))
	for _, p := range ps {
		out = append(out, *p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ImportedAt.Before(out[j].ImportedAt) })
	return out
}

// Manifest returns the accepted manifest JSON bytes for a build.
func (a *App) Manifest(id string) ([]byte, error) {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	b, ok := a.state.data.Builds[id]
	if !ok {
		return nil, ErrUnknownBuild
	}
	if b.Status != StatusAccepted {
		return nil, ErrConflict
	}
	return a.st.Get(b.ManifestSHA)
}
