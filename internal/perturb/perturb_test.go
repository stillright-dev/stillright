package perturb

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stillright-dev/stillright/internal/scenario"
)

func payments(p scenario.Perturb) *scenario.Scenario {
	return &scenario.Scenario{
		Events: []scenario.Event{
			{ID: "paid"},
			{ID: "refunded", After: "paid"},
			{ID: "shipped", After: "paid"},
		},
		Perturb: p,
	}
}

var full = scenario.Perturb{Duplicate: 3, Concurrency: 5, Reorder: true, DelayMS: []int{0, 250}}

func TestBaselineIsDeclaredOrder(t *testing.T) {
	sc := payments(full)
	if got := Format(sc, Generate(sc, 99, 0)); got != "[paid] → [refunded] → [shipped]" {
		t.Errorf("sequence 0 = %s", got)
	}
}

func TestDeterministic(t *testing.T) {
	sc := payments(full)
	for i := 1; i < 50; i++ {
		a, b := Generate(sc, 8812, i), Generate(sc, 8812, i)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("sequence %d differs between generations:\n%s\n%s", i, Format(sc, a), Format(sc, b))
		}
	}
	if reflect.DeepEqual(Generate(sc, 1, 1), Generate(sc, 2, 1)) && reflect.DeepEqual(Generate(sc, 1, 2), Generate(sc, 2, 2)) {
		t.Error("different seeds should give different sequences")
	}
}

// TestGolden pins the exact output for a seed. If this fails, generation
// changed and every seed users have recorded would replay differently; only
// update it deliberately.
func TestGolden(t *testing.T) {
	sc := payments(full)
	want := []string{
		"[paid+187ms ‖ shipped+168ms ‖ shipped+36ms ‖ shipped+49ms ‖ refunded+8ms] → [paid+29ms]",
		"[paid+158ms] → [paid+135ms ‖ refunded+232ms ‖ shipped+58ms] → [refunded+203ms ‖ refunded+40ms ‖ paid+134ms]",
		"[paid+196ms ‖ paid+5ms ‖ shipped+76ms ‖ refunded+70ms ‖ shipped+215ms] → [shipped+97ms ‖ refunded+113ms]",
	}
	for i, w := range want {
		if got := Format(sc, Generate(sc, 8812, i+1)); got != w {
			t.Errorf("seed 8812 sequence %d:\n got  %s\n want %s", i+1, got, w)
		}
	}
}

func TestBounds(t *testing.T) {
	sc := payments(full)
	for i := 1; i < 500; i++ {
		seq := Generate(sc, 7, i)
		counts := make([]int, len(sc.Events))
		for _, b := range seq.Batches {
			if len(b) == 0 || len(b) > full.Concurrency {
				t.Fatalf("seq %d: batch of %d sends", i, len(b))
			}
			for _, s := range b {
				counts[s.Event]++
				if s.Delay < 0 || s.Delay > 250*time.Millisecond {
					t.Fatalf("seq %d: delay %v", i, s.Delay)
				}
			}
		}
		for e, c := range counts {
			if c < 1 || c > full.Duplicate {
				t.Fatalf("seq %d: event %s sent %d times", i, sc.Events[e].ID, c)
			}
		}
	}
}

// Over many sequences every kind of perturbation shows up.
func TestCoverage(t *testing.T) {
	sc := payments(full)
	var concurrentDup, reordered, delayed, maxCopies bool
	for i := 1; i < 200; i++ {
		seq := Generate(sc, 42, i)
		seen := map[int]bool{}
		copies := map[int]int{}
		for _, b := range seq.Batches {
			inBatch := map[int]int{}
			for _, s := range b {
				inBatch[s.Event]++
				copies[s.Event]++
				if s.Delay > 0 {
					delayed = true
				}
				if s.Event == 1 && !seen[0] {
					reordered = true // refunded before any paid
				}
			}
			for e, n := range inBatch {
				if n > 1 {
					concurrentDup = true
				}
				seen[e] = true
			}
		}
		if copies[0] == full.Duplicate {
			maxCopies = true
		}
	}
	if !concurrentDup || !reordered || !delayed || !maxCopies {
		t.Errorf("concurrentDup=%v reordered=%v delayed=%v maxCopies=%v", concurrentDup, reordered, delayed, maxCopies)
	}
}

// Without reorder, every send of a dependent event happens in a batch after
// the one holding the first send of its dependency.
func TestRespectsAfterWithoutReorder(t *testing.T) {
	p := full
	p.Reorder = false
	sc := payments(p)
	for i := 1; i < 500; i++ {
		seq := Generate(sc, 3, i)
		firstPaid := -1
		for bi, b := range seq.Batches {
			for _, s := range b {
				if s.Event == 0 && firstPaid < 0 {
					firstPaid = bi
				}
			}
		}
		for bi, b := range seq.Batches {
			for _, s := range b {
				if s.Event != 0 && bi <= firstPaid {
					t.Fatalf("seq %d violates after: %s", i, Format(sc, seq))
				}
			}
		}
	}
}

func TestNoPerturbationIsSequentialOnce(t *testing.T) {
	sc := payments(scenario.Perturb{Reorder: true})
	for i := 1; i < 50; i++ {
		seq := Generate(sc, 5, i)
		if len(seq.Batches) != 3 || seq.Sends() != 3 {
			t.Fatalf("seq %d: %s", i, Format(sc, seq))
		}
	}
	if Enabled(scenario.Perturb{}) || Enabled(scenario.Perturb{Duplicate: 1, Concurrency: 1, DelayMS: []int{0, 0}}) {
		t.Error("no perturbation should not be Enabled")
	}
	if !Enabled(scenario.Perturb{DelayMS: []int{0, 10}}) {
		t.Error("delay alone is a perturbation")
	}
}

func TestIntnUniform(t *testing.T) {
	r := newRNG(1, 1)
	var counts [3]int
	for i := 0; i < 30000; i++ {
		counts[r.intn(3)]++
	}
	for v, c := range counts {
		if c < 9500 || c > 10500 {
			t.Errorf("value %d drawn %d times of 30000", v, c)
		}
	}
}

func TestParseRoundTrip(t *testing.T) {
	sc := payments(full)
	for i := 0; i < 30; i++ {
		seq := Generate(sc, 11, i)
		got, err := Parse(sc, Format(sc, seq))
		if err != nil {
			t.Fatal(err)
		}
		got.Index = seq.Index
		if !reflect.DeepEqual(got, seq) {
			t.Fatalf("round trip of %s gave %s", Format(sc, seq), Format(sc, got))
		}
	}
}

func TestParseASCIIAndErrors(t *testing.T) {
	sc := payments(full)
	seq, err := Parse(sc, " [paid | paid+120ms] -> [refunded] ")
	if err != nil || Format(sc, seq) != "[paid ‖ paid+120ms] → [refunded]" || seq.Index != -1 {
		t.Errorf("seq = %s, err = %v", Format(sc, seq), err)
	}
	for in, want := range map[string]string{
		"[paid] → refunded": "must be written in brackets",
		"[paid ‖ nope]":     `unknown event "nope"`,
		"[paid+12]":         "must look like +120ms",
		"[]":                `unknown event ""`,
		"[paid+-5ms]":       "must look like +120ms",
	} {
		if _, err := Parse(sc, in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) error = %v, want %q", in, err, want)
		}
	}
}
