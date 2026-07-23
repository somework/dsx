package reply

import (
	"encoding/json"
	"strings"
)

// salvageFrame is one open container during the scan, with the offset just past
// the last COMPLETE member or element it held.
type salvageFrame struct {
	obj      bool
	wantKey  bool
	lastGood int64
	hasGood  bool
}

// salvageJSON returns the longest prefix of b that becomes a whole JSON
// document once the containers still open at the cut are closed, the number of
// wire bytes it had to discard, and whether anything could be salvaged at all.
// A document that is already whole comes back unchanged with nothing dropped.
//
// The scan is json.Decoder's, not a hand-rolled one, because the hard part is
// strings: an escaped quote, a trailing backslash and a \u escape all have to
// be read exactly as JSON reads them, and InputOffset reports where the lexer
// actually got to.
//
// Where to cut back to is the whole design. The last position that merely
// parsed is the wrong answer and was measured to be: on the real reply it left
// the final message holding a toolCall whose `input` had been cut away, so
// `…toolCall.input` answered null and nothing distinguished "there was none"
// from "it was truncated". Cutting back to the last complete element of the
// OUTERMOST open array drops that message whole, and then every element present
// is byte-complete. When no array is open the innermost object's last whole
// member is the same rule with nothing to choose between.
//
// The only bytes emitted that the server did not send are the closing brackets,
// and their types come from the lexer's own stack rather than from guesswork.
func salvageJSON(b []byte) (string, int, bool) {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	var stack []salvageFrame
	whole := false

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if d, isDelim := tok.(json.Delim); isDelim {
			switch d {
			case '{':
				stack = append(stack, salvageFrame{obj: true, wantKey: true})
				continue
			case '[':
				stack = append(stack, salvageFrame{})
				continue
			default:
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			}
		} else if len(stack) > 0 && stack[len(stack)-1].obj && stack[len(stack)-1].wantKey {
			// A key is not a value; the member is not complete until its value is.
			stack[len(stack)-1].wantKey = false
			continue
		}
		if len(stack) == 0 {
			// A value closed at the top level: the document is whole.
			whole = true
			continue
		}
		if stack[len(stack)-1].obj {
			stack[len(stack)-1].wantKey = true
		}
		stack[len(stack)-1].lastGood = dec.InputOffset()
		stack[len(stack)-1].hasGood = true
	}
	if whole && len(stack) == 0 {
		return string(b), 0, true
	}
	if len(stack) == 0 {
		return "", 0, false
	}

	// Three cases, and the third was found by a test rather than designed.
	//
	//  1. An open array holding at least one complete element — cut there. Every
	//     element present is then byte-complete, and the only loss is trailing
	//     keys on the objects above, which no salvage can avoid.
	//  2. No open array at all — the outermost frame with a complete member. An
	//     object's dropped member is dropped whole, so nothing is amputated.
	//  3. Arrays are open but none holds a complete element — REFUSE. Falling
	//     back to the innermost frame here is what the naive rule did: it cut
	//     inside an unfinished array element and published that element as
	//     though it were whole.
	cut, found := -1, false
	for i := range stack {
		if !stack[i].obj && stack[i].hasGood {
			cut, found = i, true
			break
		}
	}
	if !found {
		for i := range stack {
			if !stack[i].obj {
				return "", 0, false
			}
			if stack[i].hasGood {
				cut, found = i, true
				break
			}
		}
	}
	if !found {
		return "", 0, false
	}

	var sb strings.Builder
	sb.Write(b[:stack[cut].lastGood])
	for i := cut; i >= 0; i-- {
		if stack[i].obj {
			sb.WriteByte('}')
		} else {
			sb.WriteByte(']')
		}
	}
	// No json.Valid here. The prefix was read by the lexer and the closers come
	// from its own stack, so the result is well-formed by construction, and a
	// mutation removing the check broke no test. If that construction is ever
	// wrong the caller still catches it: json.Marshal refuses an invalid
	// RawMessage — measured — and ConversationJSON falls through on the error.
	return sb.String(), len(b) - int(stack[cut].lastGood), true
}
