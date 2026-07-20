package syncer

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type progBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *progBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *progBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// A remote path is untrusted input. A \r or an ESC in a name would erase what
// is already on the line — and erasing is this line's own mechanism, so a
// crafted name could wipe a warning printed a moment before.
func TestAProgressPathCannotEraseTheLine(t *testing.T) {
	for _, in := range []string{
		"a\rWIPED.css",
		"a\x1b[2Kb.css",
		"a\nb.css",
		"a\x08\x08b.css",
		"a\x00b.css",
	} {
		got := sanitizeProgressPath(in)
		for _, bad := range []rune{'\r', '\n', '\x1b', '\x08', '\x00'} {
			if strings.ContainsRune(got, bad) {
				t.Errorf("sanitizeProgressPath(%q)=%q still carries %q", in, got, bad)
			}
		}
	}
}

// fmtutil.Truncate cuts to n and appends one ellipsis rune, so the display
// width is progressPathWidth+1 — bounded, which is what keeps a long name from
// pushing the counter off a narrow terminal and defeating the \r redraw.
func TestAProgressPathIsCapped(t *testing.T) {
	long := strings.Repeat("x", 500) + ".css"
	got := sanitizeProgressPath(long)
	if n := utf8.RuneCountInString(got); n > progressPathWidth+1 {
		t.Errorf("path rendered %d runes wide, want at most %d", n, progressPathWidth+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a cut path does not show it was cut: %q", got)
	}
}

func TestAnOrdinaryPathSurvivesIntact(t *testing.T) {
	for _, in := range []string{"tokens.css", "type/scale.css", "café.css"} {
		if got := sanitizeProgressPath(in); got != in {
			t.Errorf("sanitizeProgressPath(%q)=%q, want it unchanged", in, got)
		}
	}
}

// nil is the default for every caller that did not ask, and must be inert.
func TestANilProgressIsInert(t *testing.T) {
	var p *progress
	p.step("a.css")
	p.clear()
}

func TestNoWriterMeansNoProgress(t *testing.T) {
	if p := newProgress(nil, "pulling", 5); p != nil {
		t.Error("a nil writer produced a live progress")
	}
}

func TestNothingToTransferMeansNoProgress(t *testing.T) {
	var buf progBuf
	if p := newProgress(&buf, "pulling", 0); p != nil {
		t.Error("an empty transfer produced a live progress")
	}
}

func TestProgressCountsUpToTheTotal(t *testing.T) {
	var buf progBuf
	p := newProgress(&buf, "pulling", 3)
	p.step("a.css")
	p.step("b.css")
	p.step("c.css")

	out := buf.String()
	for _, want := range []string{"1/3", "2/3", "3/3", "pulling"} {
		if !strings.Contains(out, want) {
			t.Errorf("progress output %q lacks %q", out, want)
		}
	}
}

// A shorter line after a longer one must not leave the tail of the longer one
// on screen.
func TestProgressPadsOverAShorterRedraw(t *testing.T) {
	var buf progBuf
	p := newProgress(&buf, "pulling", 2)
	p.step(strings.Repeat("l", 30) + ".css")
	p.step("s.css")

	last := buf.String()
	if i := strings.LastIndex(last, "\r"); i >= 0 {
		last = last[i:]
	}
	if !strings.HasSuffix(last, " ") {
		t.Errorf("a shorter redraw did not pad over the longer line: %q", last)
	}
}

func TestProgressClearsItself(t *testing.T) {
	var buf progBuf
	p := newProgress(&buf, "pulling", 1)
	p.step("a.css")
	p.clear()
	if !strings.HasSuffix(buf.String(), "\r") {
		t.Errorf("clear() left the cursor mid-line: %q", buf.String())
	}
}

// The counter runs from every worker goroutine.
func TestProgressIsRaceFree(t *testing.T) {
	var buf progBuf
	p := newProgress(&buf, "pulling", 50)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p.step("a.css")
		}(i)
	}
	wg.Wait()
	if !strings.Contains(buf.String(), "50/50") {
		t.Errorf("the final step was lost: %q", buf.String())
	}
}

// The counter has to reach the caller's writer through a real Pull, or the
// wiring is untested and the unit tests above only prove the widget works.
func TestPullDrawsTheCounterToTheCallersWriter(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("a.css", "e1", 3), fileEntry("b.css", "e2", 3))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", "a{}")}
	})

	var buf progBuf
	if _, err := Pull(t.Context(), fakeClient(f), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2, Progress: &buf,
	}); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "2/2") {
		t.Errorf("Pull drew %q, want it to count to 2/2", out)
	}
}

// nil Progress is every non-terminal caller, including every agent invocation.
func TestPullWithNoProgressWriterDrawsNothingAnywhere(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
		}
		return fakeReply{Text: envelopeFor("a.css", "e1", "a{}")}
	})

	if _, err := Pull(t.Context(), fakeClient(f), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 1,
	}); err != nil {
		t.Fatal(err)
	}
}
