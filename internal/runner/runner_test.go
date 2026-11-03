package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/scenario"
)

type fakeTarget struct {
	status map[string]int // by path; 200 when missing
	err    error
	sent   []string
}

func (f *fakeTarget) Send(_ context.Context, r scenario.Request) (Response, error) {
	f.sent = append(f.sent, r.Path)
	if f.err != nil {
		return Response{}, f.err
	}
	if s, ok := f.status[r.Path]; ok {
		return Response{Status: s, Body: []byte("boom\n  at handler")}, nil
	}
	return Response{Status: 200}, nil
}

type fakeStore struct {
	log      []string
	resetErr error
	execErr  error
	rows     map[string]invariant.Rows // by SQL
	queryErr map[string]error
}

func (f *fakeStore) Reset(_ context.Context, tables []string) error {
	f.log = append(f.log, "reset "+strings.Join(tables, ","))
	return f.resetErr
}

func (f *fakeStore) Exec(_ context.Context, sql string) error {
	f.log = append(f.log, "exec "+sql)
	return f.execErr
}

func (f *fakeStore) QueryRows(_ context.Context, sql string, _ int) (invariant.Rows, error) {
	f.log = append(f.log, "query "+sql)
	return f.rows[sql], f.queryErr[sql]
}

func (f *fakeStore) Close(context.Context) error { return nil }

func testScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Name:   "t",
		Target: scenario.Target{BaseURL: "http://app"},
		Setup: scenario.Setup{
			Reset: scenario.ResetTruncate, ResetTables: []string{"a", "b"},
			SQLFile: "seed.sql", SQL: "INSERT 1",
		},
		Events: []scenario.Event{
			{ID: "first", Request: scenario.Request{Method: "POST", Path: "/1"}},
			{ID: "second", Request: scenario.Request{Method: "POST", Path: "/2"}},
		},
		Invariants: []scenario.Invariant{
			{Name: "ok", SQL: "Q1"},
			{Name: "broken", SQL: "Q2"},
		},
	}
}

func statuses(res *Result) string {
	var s []string
	for _, inv := range res.Invariants {
		s = append(s, inv.Name+"="+string(inv.Status))
	}
	return strings.Join(s, " ")
}

func TestRunOrderAndResults(t *testing.T) {
	target := &fakeTarget{}
	store := &fakeStore{rows: map[string]invariant.Rows{
		"Q2": {Columns: []string{"id"}, Total: 1},
	}}
	res := (&Runner{Target: target, Store: store}).Run(context.Background(), testScenario())

	if got := strings.Join(store.log, "; "); got != "reset a,b; exec INSERT 1; query Q1; query Q2" {
		t.Errorf("store calls = %s", got)
	}
	if got := strings.Join(target.sent, ","); got != "/1,/2" {
		t.Errorf("sent = %s", got)
	}
	if got := statuses(res); got != "ok=PASS broken=FAIL" {
		t.Errorf("statuses = %s", got)
	}
	if res.Error != "" || res.ExitCode() != 1 {
		t.Errorf("error=%q exit=%d", res.Error, res.ExitCode())
	}
}

func TestRunSkipsResetAndSetupWhenNotConfigured(t *testing.T) {
	sc := testScenario()
	sc.Setup = scenario.Setup{Reset: scenario.ResetNone}
	store := &fakeStore{}
	res := (&Runner{Target: &fakeTarget{}, Store: store}).Run(context.Background(), sc)
	if got := strings.Join(store.log, "; "); got != "query Q1; query Q2" {
		t.Errorf("store calls = %s", got)
	}
	if res.ExitCode() != 0 {
		t.Errorf("exit = %d", res.ExitCode())
	}
}

// Every way a run can fail to be verified must end as ERROR, never PASS.
func TestRunErrors(t *testing.T) {
	cases := []struct {
		name       string
		target     *fakeTarget
		store      *fakeStore
		wantReason string
		wantSent   int
	}{
		{"reset fails", &fakeTarget{}, &fakeStore{resetErr: errors.New("no such table")}, "reset failed: no such table", 0},
		{"setup fails", &fakeTarget{}, &fakeStore{execErr: errors.New("syntax")}, "setup SQL seed.sql failed: syntax", 0},
		{"app unreachable", &fakeTarget{err: errors.New("connection refused")}, &fakeStore{}, `event "first": no response from http://app: connection refused`, 1},
		{"app 5xx", &fakeTarget{status: map[string]int{"/2": 503}}, &fakeStore{}, `event "second": application answered 503: boom at handler`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := (&Runner{Target: tc.target, Store: tc.store}).Run(context.Background(), testScenario())
			if res.Error != tc.wantReason {
				t.Errorf("error = %q, want %q", res.Error, tc.wantReason)
			}
			if got := statuses(res); got != "ok=ERROR broken=ERROR" {
				t.Errorf("statuses = %s", got)
			}
			if len(tc.target.sent) != tc.wantSent {
				t.Errorf("sent %d requests, want %d", len(tc.target.sent), tc.wantSent)
			}
			for _, l := range tc.store.log {
				if strings.HasPrefix(l, "query") {
					t.Errorf("invariants must not be checked after a failed step, got %q", l)
				}
			}
			if res.ExitCode() != 2 {
				t.Errorf("exit = %d, want 2", res.ExitCode())
			}
		})
	}
}

func TestRun4xxIsNotAnError(t *testing.T) {
	res := (&Runner{Target: &fakeTarget{status: map[string]int{"/1": 409}}, Store: &fakeStore{}}).Run(context.Background(), testScenario())
	if res.Error != "" || res.Deliveries[0].Status != 409 || len(res.Deliveries) != 2 {
		t.Errorf("error=%q deliveries=%+v", res.Error, res.Deliveries)
	}
}

func TestQueryErrorIsPerInvariant(t *testing.T) {
	store := &fakeStore{queryErr: map[string]error{"Q1": errors.New("column x does not exist")}}
	res := (&Runner{Target: &fakeTarget{}, Store: store}).Run(context.Background(), testScenario())
	if got := statuses(res); got != "ok=ERROR broken=PASS" {
		t.Errorf("statuses = %s", got)
	}
	if !strings.Contains(res.Invariants[0].Reason, "column x does not exist") {
		t.Errorf("reason = %q", res.Invariants[0].Reason)
	}
	if res.ExitCode() != 2 {
		t.Errorf("exit = %d", res.ExitCode())
	}
}
