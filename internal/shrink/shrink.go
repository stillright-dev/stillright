// Package shrink reduces a failing delivery sequence to a smaller one that
// still fails, so the report shows the essence of the bug rather than the
// noise of the random sequence that found it.
package shrink

import (
	"github.com/stillright-dev/stillright/internal/perturb"
)

// Oracle reports whether a candidate sequence still fails. It runs the
// sequence against the real application, so it is expensive and, for timing
// bugs, not perfectly repeatable.
type Oracle func(perturb.Sequence) bool

// Result describes a reduction.
type Result struct {
	Sequence perturb.Sequence
	Tried    int  // candidates evaluated
	Accepted int  // candidates that still failed and were kept
	Exceeded bool // stopped because the budget ran out
}

// Shrink greedily applies the first reduction that still fails and starts
// over, until no reduction fails or budget candidates have been tried. Each
// accepted step removes a send, or merges two batches, or removes a delay, so
// the process always terminates. Reductions are tried in a fixed order, so
// with a repeatable oracle the result is deterministic.
func Shrink(seq perturb.Sequence, fails Oracle, budget int) Result {
	res := Result{Sequence: seq}
	for {
		progressed := false
		for _, cand := range candidates(res.Sequence) {
			if res.Tried >= budget {
				res.Exceeded = true
				return res
			}
			res.Tried++
			if fails(cand) {
				res.Sequence = cand
				res.Accepted++
				progressed = true
				break
			}
		}
		if !progressed {
			return res
		}
	}
}

// candidates lists the one-step reductions of seq, cheapest wins first:
// dropping every delay at once makes later runs faster, then whole batches,
// single sends, merged neighbours and finally single delays.
func candidates(seq perturb.Sequence) []perturb.Sequence {
	var out []perturb.Sequence

	if hasDelay(seq) {
		out = append(out, edit(seq, func(bs []perturb.Batch) []perturb.Batch {
			for _, b := range bs {
				for j := range b {
					b[j].Delay = 0
				}
			}
			return bs
		}))
	}
	if len(seq.Batches) > 1 {
		for i := range seq.Batches {
			out = append(out, edit(seq, func(bs []perturb.Batch) []perturb.Batch {
				return append(bs[:i], bs[i+1:]...)
			}))
		}
	}
	if seq.Sends() > 1 {
		for i, b := range seq.Batches {
			if len(b) < 2 {
				continue // dropping its only send was tried as dropping the batch
			}
			for j := range b {
				out = append(out, edit(seq, func(bs []perturb.Batch) []perturb.Batch {
					bs[i] = append(bs[i][:j], bs[i][j+1:]...)
					return bs
				}))
			}
		}
	}
	for i := 0; i+1 < len(seq.Batches); i++ {
		out = append(out, edit(seq, func(bs []perturb.Batch) []perturb.Batch {
			bs[i] = append(bs[i], bs[i+1]...)
			return append(bs[:i+1], bs[i+2:]...)
		}))
	}
	if countDelays(seq) > 1 {
		for i, b := range seq.Batches {
			for j, s := range b {
				if s.Delay > 0 {
					out = append(out, edit(seq, func(bs []perturb.Batch) []perturb.Batch {
						bs[i][j].Delay = 0
						return bs
					}))
				}
			}
		}
	}
	return out
}

// edit applies f to a deep copy of seq's batches. The result is not one of
// the generated sequences, so its index is -1.
func edit(seq perturb.Sequence, f func([]perturb.Batch) []perturb.Batch) perturb.Sequence {
	bs := make([]perturb.Batch, len(seq.Batches))
	for i, b := range seq.Batches {
		bs[i] = append(perturb.Batch(nil), b...)
	}
	return perturb.Sequence{Index: -1, Batches: f(bs)}
}

func hasDelay(seq perturb.Sequence) bool { return countDelays(seq) > 0 }

func countDelays(seq perturb.Sequence) int {
	n := 0
	for _, b := range seq.Batches {
		for _, s := range b {
			if s.Delay > 0 {
				n++
			}
		}
	}
	return n
}
