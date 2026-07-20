package syncer

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// baselineBaseName is the fetch baseline's filename inside StateDir.
const baselineBaseName = "baseline.json"

// BaselinePath is the fetch baseline's on-disk location, beside the ledger
// inside StateDir. Covered by the same bare ".dsx" builtin ignore that
// covers the ledger — matchesBuiltinDir skips the whole directory by prefix
// on the local side, and checkRemotePath refuses any remote path with a
// ".dsx" segment on the other — so no second ignore pattern is needed.
func BaselinePath(dir string) string { return filepath.Join(dir, DirName, baselineBaseName) }

// Baseline is the fetch baseline: proof that specific paths were downloaded
// and their bytes verified against a specific listing. Unlike State it
// carries no damage guard — an empty ProjectID with a populated Verified map
// is harmless here, because a baseline entry can never reach a prune loop
// (see BaselineEntry, plan.go) — and it is discarded, never refused, when
// its binding disagrees with the run; see bound.
type Baseline struct {
	ProjectID string                   `json:"project_id"`
	Endpoint  string                   `json:"endpoint,omitempty"`
	Verified  map[string]BaselineEntry `json:"verified"`
}

// loadBaseline reads the baseline, fixing up a nil map the way LoadState
// does for State.Files. A missing file is an empty baseline, not an error —
// and so is any other unusable one. baseline.json is a cache of what fetch
// downloaded, not a claim of ownership (see Baseline's doc comment): an
// unreadable file, a directory at that path, or bytes that fail to decode
// all cost the same thing either way, one re-verify on the next fetch, so
// none of them may block pull or push outright. Blocking would send the user
// toward `rm -rf .dsx`, which also erases state.json.
func loadBaseline(dir string) (Baseline, error) {
	empty := Baseline{Verified: map[string]BaselineEntry{}}

	b, err := os.ReadFile(BaselinePath(dir))
	if err != nil {
		return empty, nil
	}
	var loaded Baseline
	if err := json.Unmarshal(b, &loaded); err != nil {
		return empty, nil
	}
	if loaded.Verified == nil {
		loaded.Verified = map[string]BaselineEntry{}
	}
	return loaded, nil
}

// bound reports whether b was recorded against this run's project and
// endpoint (invariant 13's binding). A caller finding this false discards
// b's Verified map and proceeds with an empty one — the baseline is a cache,
// not a claim of ownership, so a mismatch is never a refusal.
func (b Baseline) bound(projectID, endpoint string) bool {
	return b.ProjectID == projectID && sameEndpoint(b.Endpoint, endpoint)
}

// save writes the baseline the way State.save writes the ledger: temp
// beside the destination inside StateDir, then rename.
func (b Baseline) save(dir string) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := ensureLedgerHome(dir); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(StateDir(dir), baselineBaseName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), BaselinePath(dir))
}
