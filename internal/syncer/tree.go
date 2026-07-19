package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

type RemoteEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Etag string `json:"etag"`
}

func (e RemoteEntry) isDir() bool { return e.Type == "directory" }

func listDir(ctx context.Context, c *mcp.Client, projectID, path string) ([]RemoteEntry, error) {
	args := map[string]any{"project_id": projectID}
	if path != "" {
		args["path"] = path
	}
	text, err := c.CallTool(ctx, "list_files", args)
	if err != nil {
		return nil, err
	}
	var entries []RemoteEntry
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		// The server broke its half of the protocol; the taxonomy names that
		// specifically, and WalkTree returns this error verbatim to the caller.
		return nil, &dsxerr.Error{Kind: dsxerr.KindProtocol, Msg: fmt.Sprintf("list_files %q: malformed listing", path), Err: err}
	}
	return entries, nil
}

// maxTreeDepth bounds how deep WalkTree will recurse. A directory listing is
// untrusted (invariant 7); strictlyDescends already forces each level to grow,
// so a legitimate tree never approaches this, but an ever-deepening malicious
// listing needs a hard backstop or the walk never terminates.
const maxTreeDepth = 64

// strictlyDescends reports whether child is a proper descendant of parent. The
// empty string is the root, from which any non-empty path descends. A child that
// equals its parent, is empty, or points sideways/upward is refused: that is the
// shape of a re-list-root, a self-listing directory, or a cycle.
func strictlyDescends(parent, child string) bool {
	if child == "" {
		return false
	}
	if parent == "" {
		return true
	}
	return strings.HasPrefix(child, parent+"/") && len(child) > len(parent)+1
}

func WalkTree(ctx context.Context, c *mcp.Client, projectID string, concurrency int) (map[string]RemoteEntry, error) {
	concurrency = max(concurrency, 1)

	var (
		mu      sync.Mutex
		files   = map[string]RemoteEntry{}
		visited = map[string]bool{}
		errs    []error
		wg      sync.WaitGroup
		sem     = make(chan struct{}, concurrency)
	)

	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
		cancel()
	}

	var walk func(dir string)
	walk = func(dir string) {
		defer wg.Done()

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		entries, err := listDir(ctx, c, projectID, dir)
		<-sem

		if err != nil {
			fail(err)
			return
		}

		for _, e := range entries {
			if !e.isDir() {
				mu.Lock()
				files[e.Path] = e
				mu.Unlock()
				continue
			}

			// A directory entry's path is untrusted. Refuse anything that does
			// not strictly descend from the directory we just listed (empty,
			// self, sideways, upward) — that is the shape of a cycle — and cap
			// the depth so an ever-deepening listing cannot run forever. On a
			// refusal, fail the whole walk (invariant 3: an abused listing is a
			// failure, not a partial tree that --prune reads as deletions).
			if !strictlyDescends(dir, e.Path) {
				fail(fmt.Errorf("list_files %q: directory entry %q does not descend from it; refusing a cyclic listing", dir, e.Path))
				return
			}
			if strings.Count(e.Path, "/") >= maxTreeDepth {
				fail(fmt.Errorf("list_files %q: directory entry %q exceeds the %d-level depth cap; refusing a runaway listing", dir, e.Path, maxTreeDepth))
				return
			}

			mu.Lock()
			seen := visited[e.Path]
			if !seen {
				visited[e.Path] = true
			}
			mu.Unlock()
			if seen {
				continue
			}

			wg.Add(1)
			go walk(e.Path)
		}
	}

	wg.Add(1)
	go walk("")
	wg.Wait()

	if len(errs) > 0 {
		return nil, errs[0]
	}

	if err := parent.Err(); err != nil {
		return nil, fmt.Errorf("listing interrupted before the tree was complete: %w", err)
	}
	return files, nil
}

func SortedPaths[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
