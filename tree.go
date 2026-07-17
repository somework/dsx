package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

type remoteEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Etag string `json:"etag"`
}

func (e remoteEntry) isDir() bool { return e.Type == "directory" }

// listDir lists one directory. Pass "" for the project root. The server
// returns project-relative paths, not basenames.
func (c *client) listDir(ctx context.Context, projectID, path string) ([]remoteEntry, error) {
	args := map[string]any{"project_id": projectID}
	if path != "" {
		args["path"] = path
	}
	text, err := c.callTool(ctx, "list_files", args)
	if err != nil {
		return nil, err
	}
	var entries []remoteEntry
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		return nil, fmt.Errorf("list_files %q: malformed listing: %w", path, err)
	}
	return entries, nil
}

// walkTree enumerates every file in the project, descending concurrently.
//
// This is the whole basis of a cheap sync: one listing per directory yields
// every etag up front, so unchanged files are never requested at all.
func (c *client) walkTree(ctx context.Context, projectID string, concurrency int) (map[string]remoteEntry, error) {
	// Clamp here, beside the semaphore, not in the callers. A non-positive size
	// is not a slow walk, it is no walk: at zero the channel is unbuffered and
	// walk's send blocks on a receiver that only runs after it, so wg.Wait never
	// returns; below zero make panics outright. Both are silent from the caller's
	// side, and cmdTree reaches this function without passing through runPull, so
	// a clamp kept in the callers is a rule two of them must remember rather than
	// a property this one guarantees.
	concurrency = max(concurrency, 1)

	var (
		mu    sync.Mutex
		files = map[string]remoteEntry{}
		errs  []error
		wg    sync.WaitGroup
		sem   = make(chan struct{}, concurrency)
	)

	// Keep the caller's context: the derived one is also cancelled by our own
	// error path, so it cannot tell "the user interrupted us" from "we gave up".
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var walk func(dir string)
	walk = func(dir string) {
		defer wg.Done()

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		entries, err := c.listDir(ctx, projectID, dir)
		<-sem

		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
			cancel()
			return
		}

		for _, e := range entries {
			if e.isDir() {
				wg.Add(1)
				go walk(e.Path)
				continue
			}
			mu.Lock()
			files[e.Path] = e
			mu.Unlock()
		}
	}

	wg.Add(1)
	go walk("")
	wg.Wait()

	if len(errs) > 0 {
		return nil, errs[0]
	}
	// An interrupted walk returns a short listing. Reporting that as success
	// would let --prune read "not enumerated" as "deleted on the server".
	if err := parent.Err(); err != nil {
		return nil, fmt.Errorf("listing interrupted before the tree was complete: %w", err)
	}
	return files, nil
}

func sortedPaths[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
