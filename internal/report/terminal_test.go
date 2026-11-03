package report

import (
	"strings"
	"testing"
	"time"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/runner"
	"github.com/stillright/stillright/internal/scenario"
)

func str(s string) *string { return &s }

func TestTerminal(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "payment-confirmed", Path: "stillright/payment.yaml",
		Setup:   scenario.Setup{Reset: scenario.ResetTruncate, ResetTables: []string{"subscriptions", "payments"}, SQLFile: "fixtures/seed.sql"},
		Effects: []scenario.Effect{{Name: "e", Assert: "count() <= 1"}},
	}
	res := &runner.Result{
		Scenario: sc,
		Deliveries: []runner.Delivery{
			{EventID: "paid", Method: "POST", Path: "/webhooks/payment", Status: 200, Duration: 14 * time.Millisecond},
			{EventID: "refunded", Method: "POST", Path: "/webhooks/payment", Status: 409, Duration: 9 * time.Millisecond},
		},
		Invariants: []invariant.Result{
			{Name: "refund leaves no active subscription", Status: invariant.Pass},
			{Name: "one active subscription per customer", Status: invariant.Fail, Evidence: invariant.Rows{
				Columns: []string{"customer_id", "count", "note"},
				Values:  [][]*string{{str("42"), str("2"), nil}, {str("7"), str("3"), str(strings.Repeat("x", 80))}},
				Total:   12,
			}},
			{Name: "bad", Status: invariant.Error, Reason: "query failed: column nope does not exist"},
		},
	}

	var b strings.Builder
	Terminal(&b, res)
	want := `scenario  payment-confirmed  (stillright/payment.yaml)
reset     truncate subscriptions, payments
setup     fixtures/seed.sql
events    paid      POST /webhooks/payment  → 200  (14ms)
          refunded  POST /webhooks/payment  → 409  (9ms)
note      1 effect assertion(s) not checked: effects are not supported in this version yet

PASS   refund leaves no active subscription
FAIL   one active subscription per customer
       12 rows returned (showing first 2)
       customer_id | count | note
       ------------+-------+-------------------------------------------------------------
       42          | 2     | NULL
       7           | 3     | xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx…
ERROR  bad
       query failed: column nope does not exist

1 passed, 1 failed, 1 errors
`
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTerminalRunError(t *testing.T) {
	sc := &scenario.Scenario{Name: "s", Path: "s.yaml", Setup: scenario.Setup{Reset: scenario.ResetNone}}
	res := &runner.Result{
		Scenario:   sc,
		Error:      "cannot connect to database: connection refused",
		Invariants: []invariant.Result{{Name: "a", Status: invariant.Error, Reason: "not checked: cannot connect to database: connection refused"}},
	}
	var b strings.Builder
	Terminal(&b, res)
	got := b.String()
	if !strings.Contains(got, "ERROR  run could not be verified: cannot connect to database: connection refused\n\nERROR  a\n\n0 passed, 0 failed, 1 errors") {
		t.Errorf("got:\n%s", got)
	}
}
