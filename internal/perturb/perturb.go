// Package perturb generates delivery sequences for a scenario: the declared
// events, duplicated, grouped into concurrent batches, reordered and delayed.
//
// Generation is deterministic. Sequence i of seed s depends only on (s, i)
// and the scenario, so any sequence can be regenerated on its own without
// generating the ones before it.
package perturb

import (
	"fmt"
	"strings"
	"time"

	"github.com/stillright-dev/stillright/internal/scenario"
)

// Send is one delivery of an event.
type Send struct {
	Event int           // index into the scenario's events
	Delay time.Duration // wait after the batch is released, before sending
}

// Batch is a group of sends released together; they run concurrently.
type Batch []Send

// Sequence is the full delivery plan for one run. Batches run one after
// another, each starting once every send of the previous one has finished.
type Sequence struct {
	Index   int
	Batches []Batch
}

// Sends returns the total number of deliveries in the sequence.
func (s Sequence) Sends() int {
	n := 0
	for _, b := range s.Batches {
		n += len(b)
	}
	return n
}

// Baseline is the declared order: every event once, one at a time, no delay.
func Baseline(sc *scenario.Scenario) Sequence {
	seq := Sequence{Index: 0}
	for i := range sc.Events {
		seq.Batches = append(seq.Batches, Batch{{Event: i}})
	}
	return seq
}

// Enabled reports whether the scenario asks for any perturbation at all.
func Enabled(p scenario.Perturb) bool {
	return p.Duplicate > 1 || p.Concurrency > 1 || p.Reorder || (len(p.DelayMS) == 2 && p.DelayMS[1] > 0)
}

// Generate returns sequence index of seed. Index 0 is always the Baseline, so
// every exploration first checks the unperturbed behavior.
func Generate(sc *scenario.Scenario, seed uint64, index int) Sequence {
	if index == 0 {
		return Baseline(sc)
	}
	p := sc.Perturb
	r := newRNG(seed, uint64(index))

	// How many times each event is delivered: uniformly 1..duplicate.
	remaining := make([]int, len(sc.Events))
	total := 0
	for i := range remaining {
		remaining[i] = 1
		if p.Duplicate > 1 {
			remaining[i] += r.intn(p.Duplicate)
		}
		total += remaining[i]
	}

	deps := make([]int, len(sc.Events)) // event index each event must follow, or -1
	for i, ev := range sc.Events {
		deps[i] = -1
		if ev.After != "" && !p.Reorder {
			for j, other := range sc.Events {
				if other.ID == ev.After {
					deps[i] = j
				}
			}
		}
	}

	seq := Sequence{Index: index}
	placed := make([]int, len(sc.Events))     // sends of each event placed so far
	firstBatch := make([]int, len(sc.Events)) // batch of each event's first send
	var cur Batch
	batchCap := r.batchSize(p.Concurrency)
	closeBatch := func() {
		seq.Batches = append(seq.Batches, cur)
		cur = nil
		batchCap = r.batchSize(p.Concurrency)
	}

	for n := 0; n < total; n++ {
		// Pick uniformly among the sends that may go next: an event whose
		// dependency has already been sent at least once.
		var avail []int
		for i, left := range remaining {
			if left > 0 && (deps[i] < 0 || placed[deps[i]] > 0) {
				for k := 0; k < left; k++ {
					avail = append(avail, i)
				}
			}
		}
		ev := avail[r.intn(len(avail))]

		// A dependent event must not be concurrent with the first send of
		// what it depends on, so start a new batch when they would collide.
		if len(cur) == batchCap || (deps[ev] >= 0 && firstBatch[deps[ev]] == len(seq.Batches)) {
			closeBatch()
		}
		if placed[ev] == 0 {
			firstBatch[ev] = len(seq.Batches)
		}
		cur = append(cur, Send{Event: ev, Delay: r.delay(p.DelayMS)})
		placed[ev]++
		remaining[ev]--
	}
	if len(cur) > 0 {
		seq.Batches = append(seq.Batches, cur)
	}
	return seq
}

// Format renders a sequence compactly: batches in brackets separated by
// " → ", concurrent sends separated by " ‖ ", delays as "+NNms".
//
//	[pago ‖ pago+120ms] → [estornado]
func Format(sc *scenario.Scenario, seq Sequence) string {
	parts := make([]string, len(seq.Batches))
	for i, b := range seq.Batches {
		sends := make([]string, len(b))
		for j, s := range b {
			sends[j] = sc.Events[s.Event].ID
			if s.Delay > 0 {
				sends[j] += fmt.Sprintf("+%dms", s.Delay.Milliseconds())
			}
		}
		parts[i] = "[" + strings.Join(sends, " ‖ ") + "]"
	}
	return strings.Join(parts, " → ")
}

func (r *rng) batchSize(concurrency int) int {
	if concurrency <= 1 {
		return 1
	}
	return 1 + r.intn(concurrency)
}

func (r *rng) delay(rangeMS []int) time.Duration {
	if len(rangeMS) != 2 || rangeMS[1] == 0 {
		return 0
	}
	ms := rangeMS[0] + r.intn(rangeMS[1]-rangeMS[0]+1)
	return time.Duration(ms) * time.Millisecond
}
