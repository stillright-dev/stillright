package shrink

import (
	"reflect"
	"testing"
	"time"

	"github.com/stillright-dev/stillright/internal/perturb"
	"github.com/stillright-dev/stillright/internal/scenario"
)

var sc = &scenario.Scenario{Events: []scenario.Event{{ID: "paid"}, {ID: "refunded"}, {ID: "shipped"}}}

// big is a noisy sequence like the generator produces.
func big() perturb.Sequence {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	return perturb.Sequence{Index: 6, Batches: []perturb.Batch{
		{{Event: 2, Delay: ms(40)}, {Event: 1}},
		{{Event: 0, Delay: ms(120)}, {Event: 2}, {Event: 0, Delay: ms(5)}},
		{{Event: 1, Delay: ms(30)}},
		{{Event: 0}, {Event: 2, Delay: ms(200)}},
	}}
}

// concurrentPaid fails when two "paid" deliveries share a batch.
func concurrentPaid(s perturb.Sequence) bool {
	for _, b := range s.Batches {
		n := 0
		for _, x := range b {
			if x.Event == 0 {
				n++
			}
		}
		if n >= 2 {
			return true
		}
	}
	return false
}

// refundBeforePaid fails when a refund is delivered in a batch strictly
// before some later paid.
func refundBeforePaid(s perturb.Sequence) bool {
	refunded := false
	for _, b := range s.Batches {
		for _, x := range b {
			if x.Event == 0 && refunded {
				return true
			}
		}
		for _, x := range b {
			refunded = refunded || x.Event == 1
		}
	}
	return false
}

func TestShrinkConcurrentDuplicate(t *testing.T) {
	res := Shrink(big(), concurrentPaid, 1000)
	if got := perturb.Format(sc, res.Sequence); got != "[paid ‖ paid]" {
		t.Errorf("got %s", got)
	}
	if res.Exceeded || res.Accepted == 0 || res.Sequence.Index != -1 {
		t.Errorf("res = %+v", res)
	}
}

func TestShrinkOrdering(t *testing.T) {
	res := Shrink(big(), refundBeforePaid, 1000)
	if got := perturb.Format(sc, res.Sequence); got != "[refunded] → [paid]" {
		t.Errorf("got %s", got)
	}
}

func TestShrinkKeepsNeededDelay(t *testing.T) {
	slowPaid := func(s perturb.Sequence) bool {
		for _, b := range s.Batches {
			for _, x := range b {
				if x.Event == 0 && x.Delay >= 100*time.Millisecond {
					return true
				}
			}
		}
		return false
	}
	res := Shrink(big(), slowPaid, 1000)
	if got := perturb.Format(sc, res.Sequence); got != "[paid+120ms]" {
		t.Errorf("got %s", got)
	}
}

func TestShrinkAlwaysFailing(t *testing.T) {
	res := Shrink(big(), func(perturb.Sequence) bool { return true }, 1000)
	if res.Sequence.Sends() != 1 || len(res.Sequence.Batches) != 1 {
		t.Errorf("got %s", perturb.Format(sc, res.Sequence))
	}
}

func TestShrinkDoesNotMutateInput(t *testing.T) {
	in := big()
	Shrink(in, concurrentPaid, 1000)
	if !reflect.DeepEqual(in, big()) {
		t.Error("input sequence was modified")
	}
}

func TestShrinkBudget(t *testing.T) {
	calls := 0
	res := Shrink(big(), func(s perturb.Sequence) bool { calls++; return concurrentPaid(s) }, 3)
	if calls != 3 || res.Tried != 3 || !res.Exceeded {
		t.Errorf("calls=%d res=%+v", calls, res)
	}
	if !concurrentPaid(res.Sequence) {
		t.Error("a budget-limited result must still be a failing sequence")
	}
}

func TestShrinkDeterministic(t *testing.T) {
	a := Shrink(big(), refundBeforePaid, 1000)
	b := Shrink(big(), refundBeforePaid, 1000)
	if !reflect.DeepEqual(a, b) {
		t.Error("same oracle gave different results")
	}
}

// With a flaky oracle the result is still a sequence the oracle accepted.
func TestShrinkFlakyOracle(t *testing.T) {
	n := 0
	flaky := func(s perturb.Sequence) bool {
		n++
		return concurrentPaid(s) && n%3 != 0
	}
	res := Shrink(big(), flaky, 1000)
	if !concurrentPaid(res.Sequence) {
		t.Errorf("got %s", perturb.Format(sc, res.Sequence))
	}
}
