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
	var (
		mu    sync.Mutex
		files = map[string]remoteEntry{}
		errs  []error
		wg    sync.WaitGroup
		sem   = make(chan struct{}, concurrency)
	)

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
