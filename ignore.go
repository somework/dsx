package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const ignoreFileName = ".dsxignore"

// builtinIgnores are excluded from every sync and cannot be negated.
//
// They are not preference. The ledger is dsx's own bookkeeping; VCS metadata
// and dependency trees are not the project's content and a Design project is
// not a place to put them. A `!` rule that could re-include them would let a
// checked-in .dsxignore -- or a project we do not control, through some future
// path -- talk dsx into uploading a .git directory or clobbering its own state.
var builtinIgnores = []string{
	".git", ".svn", ".hg", "node_modules", ".DS_Store",
	stateFileName, ignoreFileName, caseProbeName,
}

// ignoreRule is one line of .dsxignore, compiled.
type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	source  string
}

// ignoreSet decides which paths are outside dsx's business.
//
// The semantics are gitignore's, minus character classes:
//
//	# line          a comment
//	!pattern        re-include something an earlier rule excluded
//	pattern/        directories only
//	/pattern        anchored to the sync root
//	pattern         matches that segment at any depth
//	*  ?            match within one path segment
//	**              matches across segments
//
// The last matching rule wins. Unlike git, a negation can rescue a path whose
// parent directory is excluded -- git gives that up for speed, and dsx has no
// reason to inherit the surprise.
type ignoreSet struct {
	rules []ignoreRule
	// builtins is how many leading rules are built-in. match() stops at the
	// first one that hits, so a user rule can never reach past it.
	builtins int
	// hasNegation records whether any user rule can re-include a path. It gates
	// pruning the walk: see canSkipDir.
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

	// Built-ins go in first and without the negate flag, then user rules are
	// appended. Because match() consults the built-ins separately rather than
	// letting last-rule-wins reach them, a user `!` cannot undo one.
	for _, p := range builtinIgnores {
		re, err := compileIgnorePattern(p)
		if err != nil {
			return nil, err
		}
		// Built-ins match case-insensitively because the filesystem does:
		// macOS's default APFS makes ".GIT" and ".git" the same directory, so a
		// case-sensitive rule would leave the real one exposed. User rules stay
		// case-sensitive, as gitignore's are.
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

// compileIgnorePattern turns one glob into an anchored regexp over a
// slash-separated path.
//
// A pattern carrying a slash is anchored to the sync root; a bare name matches
// that segment at any depth, which is what makes `node_modules` mean what
// everyone expects it to mean.
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
				// `**/` spans zero or more whole segments; a bare `**` runs to
				// the end of the path.
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
			// Character classes are not supported: honouring half of one is
			// worse than refusing it, because the user would believe their
			// rule applied.
			return nil, errors.New("character classes are not supported; escape or drop the '['")
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// match reports whether a slash-separated project-relative path is outside the
// sync.
//
// Every ancestor is tested as well as the path itself: excluding a directory
// excludes what is under it, and the server's listing only ever names files, so
// `dist/` has to recognise `dist/app.js` without ever seeing `dist`.
func (s *ignoreSet) match(rel string) bool { return s.matchPath(rel, false) }

// matchDir reports whether a directory is excluded, so a walk can prune it
// without reading what is inside.
func (s *ignoreSet) matchDir(rel string) bool { return s.matchPath(rel, true) }

// canSkipDir reports whether a walk may skip a directory whole.
//
// Excluded is not the same as skippable. `dist/` followed by `!dist/keep.css`
// excludes the directory and re-includes one file inside it -- and pruning the
// walk there dropped keep.css from the scan while filterRemote, which has no
// walk to prune, kept it in the listing. That one-sided filtering is exactly
// what invariant 9 exists to prevent: pull overwrites the local edit, and
// push --prune deletes the server's copy.
//
// Built-ins can never be negated, so they are always safe to prune -- which is
// what keeps node_modules from being read file by file even when the user has
// written a `!` rule somewhere.
func (s *ignoreSet) canSkipDir(rel string) bool {
	if s.matchesBuiltinDir(rel) {
		return true
	}
	if !s.matchDir(rel) {
		return false
	}
	// A user rule excluded it, and some `!` may re-include a path underneath.
	// Descending costs a walk; guessing costs a file.
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
		for n, r := range s.rules {
			if n >= s.builtins {
				return false
			}
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
				// Built-in: no later rule gets to reverse it.
				return true
			}
			ignored = !r.negate
		}
	}
	return ignored
}

// filterRemote drops ignored paths from a server listing.
//
// This is not symmetry for its own sake. A path hidden from the local scan but
// left in the listing looks exactly like a file the user deleted, so
// `push --prune` would delete it from the server -- data loss produced by a
// file whose entire purpose is to say "leave this alone".
func filterRemote(remote map[string]remoteEntry, ig *ignoreSet) map[string]remoteEntry {
	out := make(map[string]remoteEntry, len(remote))
	for path, e := range remote {
		if ig.match(path) {
			continue
		}
		out[path] = e
	}
	return out
}
