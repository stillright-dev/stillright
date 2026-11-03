package perturb

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stillright/stillright/internal/scenario"
)

// Parse reads a sequence written as Format writes it. ASCII "->" and "|" are
// accepted in place of "→" and "‖" so sequences are easy to type. The result
// is not a generated sequence, so its index is -1.
func Parse(sc *scenario.Scenario, s string) (Sequence, error) {
	ids := map[string]int{}
	for i, ev := range sc.Events {
		ids[ev.ID] = i
	}
	seq := Sequence{Index: -1}
	s = strings.ReplaceAll(s, "->", "→")
	for _, part := range strings.Split(s, "→") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "[") || !strings.HasSuffix(part, "]") {
			return Sequence{}, fmt.Errorf("batch %q must be written in brackets, like [paid ‖ paid]", part)
		}
		inner := strings.ReplaceAll(part[1:len(part)-1], "|", "‖")
		var batch Batch
		for _, tok := range strings.Split(inner, "‖") {
			tok = strings.TrimSpace(tok)
			id, delay, hasDelay := strings.Cut(tok, "+")
			ev, ok := ids[id]
			if !ok {
				return Sequence{}, fmt.Errorf("unknown event %q", id)
			}
			send := Send{Event: ev}
			if hasDelay {
				ms, err := strconv.Atoi(strings.TrimSuffix(delay, "ms"))
				if err != nil || !strings.HasSuffix(delay, "ms") || ms < 0 {
					return Sequence{}, fmt.Errorf("delay %q must look like +120ms", "+"+delay)
				}
				send.Delay = time.Duration(ms) * time.Millisecond
			}
			batch = append(batch, send)
		}
		seq.Batches = append(seq.Batches, batch)
	}
	return seq, nil
}
