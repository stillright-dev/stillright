package report

import (
	"strings"
	"testing"
	"time"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/perturb"
	"github.com/stillright/stillright/internal/runner"
	"github.com/stillright/stillright/internal/scenario"
)

func str(s string) *string { return &s }

func testScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Name: "payment-confirmed", Path: "stillright/payment.yaml",
		Setup:   scenario.Setup{Reset: scenario.ResetTruncate, ResetTables: []string{"subscriptions", "payments"}, SQLFile: "fixtures/seed.sql"},
		Events:  []scenario.Event{{ID: "paid"}, {ID: "refunded", After: "paid"}},
		Perturb: scenario.Perturb{Duplicate: 3, Concurrency: 5, Reorder: true, DelayMS: []int{0, 250}},
		Invariants: []scenario.Invariant{
			{Name: "refund leaves no active subscription"},
			{Name: "one active subscription per customer"},
			{Name: "bad"},
		},
		Effects: []scenario.Effect{{Name: "e", Assert: "count() <= 1"}},
	}
}

func TestTerminalStopsAtFailure(t *testing.T) {
	sc := testScenario()
	seq := perturb.Sequence{Index: 37, Batches: []perturb.Batch{
		{{Event: 0}, {Event: 0, Delay: 120 * time.Millisecond}},
		{{Event: 1}},
	}}
	fail := invariant.Result{Name: "one active subscription per customer", Status: invariant.Fail, Evidence: invariant.Rows{
		Columns: []string{"customer_id", "count", "note"},
		Values:  [][]*string{{str("42"), str("2"), nil}, {str("7"), str("3"), str(strings.Repeat("x", 80))}},
		Total:   12,
	}}
	res := &runner.Result{
		Scenario: sc,
		Sequence: seq,
		Deliveries: []runner.Delivery{
			{EventID: "paid", Method: "POST", Path: "/webhooks/payment", Batch: 0, Status: 200, Duration: 14 * time.Millisecond},
			{EventID: "paid", Method: "POST", Path: "/webhooks/payment", Batch: 0, Delay: 120 * time.Millisecond, Status: 200, Duration: 11 * time.Millisecond},
			{EventID: "refunded", Method: "POST", Path: "/webhooks/payment", Batch: 1, Status: 409, Duration: 9 * time.Millisecond},
		},
		Invariants: []invariant.Result{
			{Name: "refund leaves no active subscription", Status: invariant.Pass},
			fail,
			{Name: "bad", Status: invariant.Error, Reason: "query failed: column nope does not exist"},
		},
	}
	ex := &runner.Exploration{
		Scenario: sc, Seed: 8812, Planned: 200, Executed: 38, Errored: 1, First: res,
		Invariants: []runner.InvariantStats{
			{Name: "refund leaves no active subscription"},
			{Name: "one active subscription per customer", Failed: 1, FirstFailure: &fail, FirstSequence: seq},
			{Name: "bad", Errored: 1, FirstError: "query failed: column nope does not exist"},
		},
	}

	var b strings.Builder
	Terminal(&b, ex)
	want := `scenario  payment-confirmed  (stillright/payment.yaml)
seed      8812
perturb   duplicate up to 3×, up to 5 concurrent, reorder (may violate after), delay 0–250ms
runs      200 sequences (sequence 0 is the declared order)
reset     truncate subscriptions, payments, before every sequence
setup     fixtures/seed.sql
note      1 effect assertion(s) not checked: effects are not supported in this version yet

sequence 37: [paid ‖ paid+120ms] → [refunded]
  batch 1   paid      POST /webhooks/payment  → 200  (14ms)
            paid      POST /webhooks/payment  → 200  (11ms)  after 120ms
  batch 2   refunded  POST /webhooks/payment  → 409  (9ms)

PASS   refund leaves no active subscription
FAIL   one active subscription per customer
       failed in 1 of 38 runs; first in sequence 37: [paid ‖ paid+120ms] → [refunded]
       evidence: 12 rows (showing first 2)
       customer_id | count | note
       ------------+-------+-------------------------------------------------------------
       42          | 2     | NULL
       7           | 3     | xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx…
       reproduce: stillright run stillright/payment.yaml --seed 8812 --replay 37
                  stillright run stillright/payment.yaml --sequence '[paid ‖ paid+120ms] → [refunded]'
ERROR  bad
       query failed: column nope does not exist

1 passed, 1 failed, 1 errors
38 of 200 runs: 37 passed, 0 failed, 1 could not be verified
stopped at the first run that did not pass; use --keep-going to run them all
`
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTerminalSequenceError(t *testing.T) {
	sc := testScenario()
	sc.Perturb = scenario.Perturb{}
	reason := `event "paid": no response from http://app: connection refused`
	res := &runner.Result{
		Scenario:   sc,
		Sequence:   perturb.Baseline(sc),
		Deliveries: []runner.Delivery{{EventID: "paid", Method: "POST", Path: "/p"}},
		Error:      reason,
	}
	ex := &runner.Exploration{Scenario: sc, Seed: 1, Planned: 1, Executed: 1, Errored: 1, First: res}
	for _, inv := range sc.Invariants {
		ex.Invariants = append(ex.Invariants, runner.InvariantStats{Name: inv.Name, Errored: 1, FirstError: "not checked: " + reason})
	}

	var b strings.Builder
	Terminal(&b, ex)
	got := b.String()
	for _, w := range []string{
		"perturb   none\nruns      1 (declared order only)\n",
		"sequence 0 (declared order): [paid] → [refunded]\n  batch 1   paid  POST /p  → no response  (0s)\n",
		"\nERROR  sequence could not be verified: " + reason + "\n\nERROR  refund leaves no active subscription\nERROR  one active",
		"0 passed, 0 failed, 3 errors\n1 of 1 runs: 0 passed, 0 failed, 1 could not be verified\n",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestTerminalCouldNotStart(t *testing.T) {
	sc := testScenario()
	ex := &runner.Exploration{Scenario: sc, Seed: 1, Planned: 100, Error: "cannot connect to database: connection refused"}
	for _, inv := range sc.Invariants {
		ex.Invariants = append(ex.Invariants, runner.InvariantStats{Name: inv.Name})
	}
	var b strings.Builder
	Terminal(&b, ex)
	if !strings.Contains(b.String(), "ERROR  could not start: cannot connect to database: connection refused\n\nERROR  refund") {
		t.Errorf("got:\n%s", b.String())
	}
}

func shrunkExploration(sh *runner.Shrunk) *runner.Exploration {
	sc := testScenario()
	sc.Path = "stillright/my payment.yaml"
	orig := perturb.Sequence{Index: 6, Batches: []perturb.Batch{{{Event: 0, Delay: 24 * time.Millisecond}, {Event: 1}, {Event: 0}}}}
	fail := invariant.Result{Status: invariant.Fail, Evidence: invariant.Rows{
		Columns: []string{"customer_id", "count"}, Values: [][]*string{{str("42"), str("3")}}, Total: 1,
	}}
	ex := &runner.Exploration{Scenario: sc, Seed: 8812, Planned: 100, Executed: 100, Failed: 10}
	ex.Invariants = []runner.InvariantStats{
		{Name: "refund leaves no active subscription"},
		{Name: "one active subscription per customer", Failed: 10, FirstFailure: &fail, FirstSequence: orig, Shrunk: sh},
		{Name: "bad"},
	}
	return ex
}

func TestTerminalShrunk(t *testing.T) {
	sh := &runner.Shrunk{
		Sequence:   perturb.Sequence{Index: -1, Batches: []perturb.Batch{{{Event: 0}, {Event: 0}}}},
		Reproduced: 5, Attempts: 5, Tried: 14,
		Evidence: invariant.Rows{Columns: []string{"customer_id", "count"}, Values: [][]*string{{str("42"), str("2")}}, Total: 1},
	}
	var b strings.Builder
	Terminal(&b, shrunkExploration(sh))
	want := `FAIL   one active subscription per customer
       failed in 10 of 100 runs; first in sequence 6: [paid+24ms ‖ refunded ‖ paid]
       smallest sequence: [paid ‖ paid]   (2 simultaneous deliveries; reproduced 5/5)
       evidence: customer_id=42 count=2
       reproduce: stillright run 'stillright/my payment.yaml' --seed 8812 --replay 6
                  stillright run 'stillright/my payment.yaml' --sequence '[paid ‖ paid]'
PASS   bad
`
	if got := b.String(); !strings.Contains(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTerminalShrunkFlakyAndBudget(t *testing.T) {
	sh := &runner.Shrunk{
		Sequence: perturb.Sequence{Index: -1, Batches: []perturb.Batch{{{Event: 1}}, {{Event: 0}}}},
		Attempts: 5, Tried: 60, Exceeded: true,
	}
	var b strings.Builder
	Terminal(&b, shrunkExploration(sh))
	got := b.String()
	for _, w := range []string{
		"smallest sequence: [refunded] → [paid]   (2 deliveries, one at a time; reproduced 0/5, timing-dependent: try --attempts 20; stopped after 60 candidates, may not be minimal)\n",
		// with no reproduction, the evidence is the original failure's
		"evidence: customer_id=42 count=3\n",
		"--sequence '[refunded] → [paid]'",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestTerminalNotReproducible(t *testing.T) {
	sh := &runner.Shrunk{Sequence: perturb.Sequence{Index: 6}, Attempts: 5}
	var b strings.Builder
	Terminal(&b, shrunkExploration(sh))
	got := b.String()
	for _, w := range []string{
		"could not reproduce it in 5 attempts (timing-dependent); not shrunk\n       evidence: customer_id=42 count=3\n",
		"--sequence '[paid+24ms ‖ refunded ‖ paid]'",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}
