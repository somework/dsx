// Package reply turns a measured tool reply into lines for a person.
//
// It is a leaf on purpose. The decoders here are the same ones the live suite
// judges the protocol with, so the shape a renderer believes in and the shape
// PROTOCOL.md claims cannot drift apart — and a leaf is the only place both
// `internal/cmd/...` (above) and `internal/syncer`'s live tests (between) can
// reach without inverting the layering.
//
// Every function has the shape cmd.Human wants and every one may refuse. No
// tool on this server declares an output schema, so each shape below was
// measured against the real endpoint rather than read; three protocol facts
// were guessed wrong before that rule existed. A reply that does not match is
// not rendered from a guess — the caller prints it as it arrived.
package reply

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/somework/dsx/internal/fmtutil"
)

// idWidth is the column a 36-char UUID sits in, matching `project ls`.
const idWidth = 36

// plural is the difference between "1 file" and "2 files", which is the whole
// reason a count line reads as a sentence rather than a debug print.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// DesignSystemRow is list_design_systems' measured element.
type DesignSystemRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}

// DecodeDesignSystems accepts the measured shape and nothing else. An element
// with no id is the signal that the reply is something other than this list —
// an error envelope, a wrapper object, a future rename — and the caller must
// fall through rather than print a table of blanks.
func DecodeDesignSystems(text string) ([]DesignSystemRow, bool) {
	var rows []DesignSystemRow
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		return nil, false
	}
	// nil, not len==0: `null` unmarshals into a nil slice without erroring, and
	// an empty listing is a real answer worth rendering ("0 design systems")
	// while `null` is a shape this is not.
	if rows == nil {
		return nil, false
	}
	for _, r := range rows {
		// Every field PROTOCOL.md claims, not just the identifying one. A
		// decoder that gates on `id` alone accepts a reply that renamed `name`
		// and then prints a blank column — the table of blanks this package
		// exists to refuse, for the fields the refusal never covered. It is
		// also what makes the live suite able to falsify the claim: these
		// decoders ARE its judges.
		if len(r.ID) != idWidth || r.Name == "" {
			return nil, false
		}
	}
	return rows, true
}

func DesignSystems(text string) (string, bool) {
	rows, ok := DecodeDesignSystems(text)
	if !ok {
		return "", false
	}
	var b strings.Builder
	for _, r := range rows {
		// The id first, for the reason `project ls` puts it first: names hold
		// spaces, so it is the only column order that survives awk '{print $1}'.
		fmt.Fprintf(&b, "%-*s  %s", idWidth, fmtutil.Printable(r.ID), fmtutil.Printable(r.Name))
		if r.IsDefault {
			b.WriteString("  (default)")
		}
		b.WriteString("\n")
	}
	b.WriteString(plural(len(rows), "design system", "design systems"))
	return b.String(), true
}

// ProjectRow is list_projects' measured element (PROTOCOL.md, list_projects).
type ProjectRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

func DecodeProjects(text string) ([]ProjectRow, bool) {
	var rows []ProjectRow
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		return nil, false
	}
	// See DecodeDesignSystems: `null` is not this shape, an empty list is.
	if rows == nil {
		return nil, false
	}
	for _, r := range rows {
		if len(r.ID) != idWidth || r.Name == "" || r.URL == "" {
			return nil, false
		}
	}
	return rows, true
}

// Projects is `project ls`, moved here from internal/cmd/projects so it shares
// the one decision point and the one fallback. Its old hand-rolled fallback
// ran fmtutil.Printable — the FIELD sanitiser — over a whole document, which
// collapsed every line break in an unrecognised reply into "?"; that is the
// exact defect PrintableDoc was written for, and the command that named it in
// its own comment was still doing it.
func Projects(text string) (string, bool) {
	rows, ok := DecodeProjects(text)
	if !ok {
		return "", false
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%-*s  %s\n", idWidth, fmtutil.Printable(r.ID), fmtutil.Printable(r.Name))
	}
	b.WriteString(plural(len(rows), "project", "projects"))
	return b.String(), true
}

// ProjectDetail is get_project's measured shape.
type ProjectDetail struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	URL     string `json:"url"`
	Sharing struct {
		Scope          string `json:"scope"`
		LinkPermission string `json:"link_permission"`
		ViewMode       string `json:"view_mode"`
	} `json:"sharing"`
}

func DecodeProject(text string) (ProjectDetail, bool) {
	var p ProjectDetail
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return ProjectDetail{}, false
	}
	// As in DecodeDesignSystems: PROTOCOL.md claims name, type and the sharing
	// block, and Project prints two of the three, so all three gate the answer.
	if len(p.ID) != idWidth || p.Name == "" || p.Type == "" || p.Sharing.Scope == "" {
		return ProjectDetail{}, false
	}
	return p, true
}

func Project(text string) (string, bool) {
	p, ok := DecodeProject(text)
	if !ok {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", fmtutil.Printable(p.Name))
	fmt.Fprintf(&b, "  id       %s\n", fmtutil.Printable(p.ID))
	// scope and link permission are one fact for a reader — who can open this,
	// and what may they do — so they share a line.
	share := fmtutil.Printable(p.Sharing.Scope)
	if p.Sharing.LinkPermission != "" {
		share += " (link: " + fmtutil.Printable(p.Sharing.LinkPermission) + ")"
	}
	fmt.Fprintf(&b, "  sharing  %s\n", share)
	if p.URL != "" {
		fmt.Fprintf(&b, "  url      %s", fmtutil.Printable(p.URL))
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// Members renders only the empty case, and that is not laziness.
// list_members' own description says it excludes the owner and answers empty
// for callers outside the project's organization, so an empty array is the
// answer a person most often gets and the one worth spelling out. The
// non-empty element has never been seen: measuring it would mean granting a
// real teammate access, so rendering its columns would be exactly the guess
// this package refuses to make.
func Members(text string) (string, bool) {
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		return "", false
	}
	if rows == nil || len(rows) > 0 {
		return "", false
	}
	return "no members — the owner is not listed, and a project outside your organization lists none", true
}

// FileRow is list_files' measured element (PROTOCOL.md, list_files).
type FileRow struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Etag string `json:"etag"`
}

func (r FileRow) isDir() bool { return r.Type == "directory" }

func DecodeFiles(text string) ([]FileRow, bool) {
	var rows []FileRow
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		return nil, false
	}
	// See DecodeDesignSystems: an empty directory is an answer, `null` is not.
	if rows == nil {
		return nil, false
	}
	for _, r := range rows {
		if r.Path == "" || r.Type == "" {
			return nil, false
		}
	}
	return rows, true
}

// Files lays a directory out the way `files tree` lays out the whole tree, so
// the two READ verbs of one noun do not answer in two formats. Directories
// carry no etag by measurement, so their columns are the ones left blank.
func Files(text string) (string, bool) {
	rows, ok := DecodeFiles(text)
	if !ok {
		return "", false
	}
	var (
		b     strings.Builder
		files int
		dirs  int
		total int64
	)
	for _, r := range rows {
		if r.isDir() {
			dirs++
			fmt.Fprintf(&b, "%-10s %16s  %s/\n", "dir", "", fmtutil.Printable(r.Path))
			continue
		}
		files++
		total += r.Size
		fmt.Fprintf(&b, "%-10s %16s  %s\n", fmtutil.Bytes(r.Size), fmtutil.Printable(r.Etag), fmtutil.Printable(r.Path))
	}
	b.WriteString(plural(files, "file", "files"))
	if dirs > 0 {
		b.WriteString(", " + plural(dirs, "directory", "directories"))
	}
	fmt.Fprintf(&b, ", %s", fmtutil.Bytes(total))
	return b.String(), true
}

// WriteAck is write_files' measured reply (PROTOCOL.md, Writing).
type WriteAck struct {
	Etags map[string]string `json:"etags"`
	// A pointer because create_support_js also answers with an `etags` map and
	// no `written` at all: an int would default to zero and let this decoder
	// claim a support-js reply as a write of nothing.
	Written *int   `json:"written"`
	URL     string `json:"url"`
}

func DecodeWritten(text string) (WriteAck, bool) {
	var w WriteAck
	if err := json.Unmarshal([]byte(text), &w); err != nil {
		return WriteAck{}, false
	}
	if len(w.Etags) == 0 || w.Written == nil {
		return WriteAck{}, false
	}
	return w, true
}

// Written names the paths rather than the count, because the caller already
// knows how many they sent and does not know which of them the server kept.
func Written(text string) (string, bool) {
	w, ok := DecodeWritten(text)
	if !ok {
		return "", false
	}
	var b strings.Builder
	for _, p := range sortedKeys(w.Etags) {
		fmt.Fprintf(&b, "wrote %s  etag %s\n", fmtutil.Printable(p), fmtutil.Printable(w.Etags[p]))
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// CopyAck is copy_files' measured reply. It carries the same pairs three ways
// — copied, etags and results — and results is the one that names both ends.
type CopyAck struct {
	Results []struct {
		Src    string `json:"src"`
		Dest   string `json:"dest"`
		Copied int    `json:"copied"`
	} `json:"results"`
	Etags map[string]string `json:"etags"`
}

func DecodeCopied(text string) (CopyAck, bool) {
	var c CopyAck
	if err := json.Unmarshal([]byte(text), &c); err != nil {
		return CopyAck{}, false
	}
	if len(c.Results) == 0 || len(c.Etags) == 0 {
		return CopyAck{}, false
	}
	for _, r := range c.Results {
		// Both ends, because Copied prints both: a result with no src renders
		// "copied  → dest.css" and reads as a copy from nowhere.
		if r.Src == "" || r.Dest == "" {
			return CopyAck{}, false
		}
	}
	return c, true
}

func Copied(text string) (string, bool) {
	c, ok := DecodeCopied(text)
	if !ok {
		return "", false
	}
	var b strings.Builder
	for _, r := range c.Results {
		fmt.Fprintf(&b, "copied %s → %s  etag %s\n",
			fmtutil.Printable(r.Src), fmtutil.Printable(r.Dest), fmtutil.Printable(c.Etags[r.Dest]))
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// DeleteAck is delete_files' measured reply: a count and nothing else. The
// paths are not echoed, so neither is anything invented here.
type DeleteAck struct {
	Deleted *int `json:"deleted"`
}

func DecodeDeleted(text string) (int, bool) {
	var d DeleteAck
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		return 0, false
	}
	// A pointer, not an int: `{}` and `{"deleted":0}` are different replies,
	// and only the second one is this shape saying nothing was deleted.
	if d.Deleted == nil {
		return 0, false
	}
	return *d.Deleted, true
}

func Deleted(text string) (string, bool) {
	n, ok := DecodeDeleted(text)
	if !ok {
		return "", false
	}
	return "deleted " + plural(n, "file", "files"), true
}

// SupportJSAck is create_support_js' measured reply. It is deliberately NOT
// WriteAck: the field is `bytes`, there is no `written`, and the path is
// echoed at the top level. Decoding it as a write would report a size of zero
// writes.
type SupportJSAck struct {
	Path  string            `json:"path"`
	Bytes int64             `json:"bytes"`
	Etags map[string]string `json:"etags"`
}

func DecodeSupportJS(text string) (SupportJSAck, bool) {
	var s SupportJSAck
	if err := json.Unmarshal([]byte(text), &s); err != nil {
		return SupportJSAck{}, false
	}
	// The etag has to be keyed by the path the reply itself names, because that
	// is the lookup SupportJS does; an etags map keyed some other way renders a
	// trailing blank where the etag belongs.
	if s.Path == "" || s.Bytes == 0 || s.Etags[s.Path] == "" {
		return SupportJSAck{}, false
	}
	return s, true
}

func SupportJS(text string) (string, bool) {
	s, ok := DecodeSupportJS(text)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("wrote %s  %s  etag %s",
		fmtutil.Printable(s.Path), fmtutil.Bytes(s.Bytes), fmtutil.Printable(s.Etags[s.Path])), true
}

// PlanAck is finalize_plan's measured reply (PROTOCOL.md, Writing).
type PlanAck struct {
	PlanToken string `json:"plan_token"`
	ProjectID string `json:"project_id"`
	Scope     string `json:"scope"`
	ExpiresAt int64  `json:"expires_at"`
}

func DecodePlan(text string) (PlanAck, bool) {
	var p PlanAck
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return PlanAck{}, false
	}
	if p.PlanToken == "" {
		return PlanAck{}, false
	}
	// Not sanitised — gated. A plan_token is a capability the caller is told to
	// copy off stdout (`files put --plan` documents exactly that), so replacing
	// a byte with '?' would hand them a token that looks right and does not
	// work. If it is not printable as it stands, this is not the shape that was
	// measured: refuse, and the reply is printed whole instead.
	if fmtutil.Printable(p.PlanToken) != p.PlanToken {
		return PlanAck{}, false
	}
	return p, true
}

// Plan prints the token, which is the point of the command: invariant 8's line
// is ambient input versus requested output, and a capability a tool returns
// because it was asked for is output. `files put --plan` documents itself as
// taking it from this stdout.
func Plan(text string) (string, bool) {
	p, ok := DecodePlan(text)
	if !ok {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", p.PlanToken)
	if p.Scope != "" {
		fmt.Fprintf(&b, "  scope    %s\n", fmtutil.Printable(p.Scope))
	}
	if p.ExpiresAt != 0 {
		fmt.Fprintf(&b, "  expires  %d (unix seconds)\n", p.ExpiresAt)
	}
	return strings.TrimRight(b.String(), "\n"), true
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
