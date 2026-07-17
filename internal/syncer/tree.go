package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

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
		return nil, fmt.Errorf("list_files %q: malformed listing: %w", path, err)
	}
	return entries, nil
}

func WalkTree(ctx context.Context, c *mcp.Client, projectID string, concurrency int) (map[string]RemoteEntry, error) {
	concurrency = max(concurrency, 1)

	var (
		mu    sync.Mutex
		files = map[string]RemoteEntry{}
		errs  []error
		wg    sync.WaitGroup
		sem   = make(chan struct{}, concurrency)
	)

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
		entries, err := listDir(ctx, c, projectID, dir)
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
