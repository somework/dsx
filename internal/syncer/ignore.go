package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const ignoreFileName = ".dsxignore"

// The trailing * on the dsx-owned entries covers save()'s os.CreateTemp sibling
// and the fold probe's os.MkdirTemp directory. Anchored patterns never matched
// those, and an unmatched leftover is an untracked local file the next push
// uploads — for the ledger sibling, dsx's own project id and file map.
var builtinIgnores = []string{
	".git", ".svn", ".hg", "node_modules", ".DS_Store",
	".dsx", oldStateFileName + "*", ignoreFileName, caseProbeName + "*",
}

// builtinIgnoreSet is the builtins compiled once, so that every consumer of
// builtinIgnores reads it as globs. isBuiltinIgnoredName compared literals and
// would have stopped guarding checkRemotePath the moment an entry grew a glob.
var builtinIgnoreSet = sync.OnceValue(func() *ignoreSet {
	s, err := parseIgnore("")
	if err != nil {
		panic("builtinIgnores does not compile: " + err.Error())
	}
	return s
})

type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	source  string
}

type ignoreSet struct {
	rules []ignoreRule

	builtins int

	hasNegation bool
}

func loadIgnore(dir string) (*ignoreSet, error) {
	b, err := os.ReadFile(filepath.Join(dir, ignoreFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return parseIgnore("")
	}
	if err != nil {
		return nil, err
	}
	s, err := parseIgnore(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, ignoreFileName), err)
	}
	return s, nil
}

func parseIgnore(text string) (*ignoreSet, error) {
	s := &ignoreSet{}

	for _, p := range builtinIgnores {
		re, err := compileIgnorePattern(p)
		if err != nil {
			return nil, err
		}

		ci, err := regexp.Compile("(?i)" + re.String())
		if err != nil {
			return nil, err
		}
		s.rules = append(s.rules, ignoreRule{re: ci, source: p})
	}
	builtinCount := len(s.rules)

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{source: line}
		if after, ok := strings.CutPrefix(line, "!"); ok {
			r.negate = true
			line = after
		}
		if trimmed, ok := strings.CutSuffix(line, "/"); ok {
			r.dirOnly = true
			line = trimmed
		}
		if line == "" {
			return nil, fmt.Errorf("empty pattern %q", r.source)
		}
		re, err := compileIgnorePattern(line)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", r.source, err)
		}
		r.re = re
		if r.negate {
			s.hasNegation = true
		}
		s.rules = append(s.rules, r)
	}
	s.builtins = builtinCount
	return s, nil
}

func compileIgnorePattern(pat string) (*regexp.Regexp, error) {
	anchored := strings.Contains(strings.TrimSuffix(pat, "/"), "/")
	pat = strings.TrimPrefix(pat, "/")

	var sb strings.Builder
	sb.WriteString("^")
	if !anchored {
		sb.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '*':
			if i+1 < len(pat) && pat[i+1] == '*' {
				i++

				if i+1 < len(pat) && pat[i+1] == '/' {
					i++
					sb.WriteString("(?:.*/)?")
					continue
				}
				sb.WriteString(".*")
				continue
			}
			sb.WriteString("[^/]*")
		case '?':
			sb.WriteString("[^/]")
		case '[':
			return nil, errors.New("character classes are not supported; escape or drop the '['")
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

func (s *ignoreSet) match(rel string) bool { return s.matchPath(rel, false) }

func (s *ignoreSet) matchDir(rel string) bool { return s.matchPath(rel, true) }

func (s *ignoreSet) canSkipDir(rel string) bool {
	if s.matchesBuiltinDir(rel) {
		return true
	}
	if !s.matchDir(rel) {
		return false
	}

	return !s.hasNegation
}

func (s *ignoreSet) matchesBuiltinDir(rel string) bool {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "" {
		return false
	}

	segments := strings.Split(rel, "/")
	for i := range segments {
		prefix := strings.Join(segments[:i+1], "/")
		for _, r := range s.rules[:s.builtins] {
			if r.re.MatchString(prefix) {
				return true
			}
		}
	}
	return false
}

func (s *ignoreSet) matchPath(rel string, leafIsDir bool) bool {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "" {
		return false
	}
	segments := strings.Split(rel, "/")

	ignored := false
	for i := range segments {
		prefix := strings.Join(segments[:i+1], "/")
		isDir := i < len(segments)-1 || leafIsDir

		for n, r := range s.rules {
			if r.dirOnly && !isDir {
				continue
			}
			if !r.re.MatchString(prefix) {
				continue
			}
			if n < s.builtins {
				return true
			}
			ignored = !r.negate
		}
	}
	return ignored
}

func filterRemote(remote map[string]RemoteEntry, ig *ignoreSet) map[string]RemoteEntry {
	out := make(map[string]RemoteEntry, len(remote))
	for path, e := range remote {
		if ig.match(path) {
			continue
		}
		out[path] = e
	}
	return out
}

func survey(dir string, remote map[string]RemoteEntry) (map[string]RemoteEntry, map[string]localFile, error) {
	ig, err := loadIgnore(dir)
	if err != nil {
		return nil, nil, err
	}
	local, err := scanLocal(dir, ig)
	if err != nil {
		return nil, nil, err
	}
	return filterRemote(remote, ig), local, nil
}
