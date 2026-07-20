package syncer

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/somework/dsx/internal/fmtutil"
)

// progressPathWidth caps the path shown so a long name cannot push the counter
// off a narrow terminal and defeat the \r redraw.
const progressPathWidth = 40

// progress redraws one \r line naming how far a transfer has got. It is nil
// whenever nobody asked for it, which is every non-terminal caller.
type progress struct {
	w    io.Writer
	verb string

	mu    sync.Mutex
	done  int
	total int
	wide  int
}

func newProgress(w io.Writer, verb string, total int) *progress {
	if w == nil || total == 0 {
		return nil
	}
	return &progress{w: w, verb: verb, total: total}
}

// step records one finished path and redraws. The write happens outside the
// lock: a terminal in flow control blocks the writer, and holding the lock
// across that serialises every worker behind the slowest terminal.
func (p *progress) step(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.done++
	line := fmt.Sprintf("  %s %d/%d  %s", p.verb, p.done, p.total, sanitizeProgressPath(path))
	pad := p.wide - len(line)
	if pad < 0 {
		pad = 0
	}
	p.wide = max(p.wide, len(line))
	p.mu.Unlock()

	fmt.Fprintf(p.w, "\r%s%s", line, strings.Repeat(" ", pad))
}

// done clears the line so the report that follows starts clean.
func (p *progress) clear() {
	if p == nil {
		return
	}
	p.mu.Lock()
	wide := p.wide
	p.mu.Unlock()
	fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", wide))
}

// sanitizeProgressPath treats the path as the untrusted input it is
// (invariant 7). A carriage return or an escape in a remote name would erase
// what is already on the line — and erasing is exactly this line's mechanism,
// so a name could wipe a warning printed a moment earlier.
func sanitizeProgressPath(path string) string {
	return fmtutil.Truncate(fmtutil.Printable(path), progressPathWidth)
}
