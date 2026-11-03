package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/perturb"
	"github.com/stillright/stillright/internal/scenario"
)

type fakeTarget struct {
	status map[string]int // by path; 200 when missing
	err    error

	mu   sync.Mutex
	sent []string
}

func (f *fakeTarget) Send(_ context.Context, r scenario.Request) (Response, error) {
	f.mu.Lock()
	f.sent = append(f.sent, r.Path)
	f.mu.Unlock()
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

func run(target Target, store Store, sc *scenario.Scenario) *Result {
	return (&Runner{Target: target, Store: store}).RunSequence(context.Background(), sc, perturb.Baseline(sc))
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
	res := run(target, store, testScenario())

	if got := strings.Join(store.log, "; "); got != "reset a,b; exec INSERT 1; query Q1; query Q2" {
		t.Errorf("store calls = %s", got)
	}
	if got := strings.Join(target.sent, ","); got != "/1,/2" {
		t.Errorf("sent = %s", got)
	}
	if got := statuses(res); got != "ok=PASS broken=FAIL" {
		t.Errorf("statuses = %s", got)
	}
	if res.Error != "" || res.Status() != invariant.Fail {
		t.Errorf("error=%q status=%s", res.Error, res.Status())
	}
}

func TestRunSkipsResetAndSetupWhenNotConfigured(t *testing.T) {
	sc := testScenario()
	sc.Setup = scenario.Setup{Reset: scenario.ResetNone}
	store := &fakeStore{}
	res := run(&fakeTarget{}, store, sc)
	if got := strings.Join(store.log, "; "); got != "query Q1; query Q2" {
		t.Errorf("store calls = %s", got)
	}
	if res.Status() != invariant.Pass {
		t.Errorf("status = %s", res.Status())
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
			res := run(tc.target, tc.store, testScenario())
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
			if res.Status() != invariant.Error {
				t.Errorf("status = %s, want ERROR", res.Status())
			}
		})
	}
}

func TestRun4xxIsNotAnError(t *testing.T) {
	res := run(&fakeTarget{status: map[string]int{"/1": 409}}, &fakeStore{}, testScenario())
	if res.Error != "" || res.Deliveries[0].Status != 409 || len(res.Deliveries) != 2 {
		t.Errorf("error=%q deliveries=%+v", res.Error, res.Deliveries)
	}
}

func TestQueryErrorIsPerInvariant(t *testing.T) {
	store := &fakeStore{queryErr: map[string]error{"Q1": errors.New("column x does not exist")}}
	res := run(&fakeTarget{}, store, testScenario())
	if got := statuses(res); got != "ok=ERROR broken=PASS" {
		t.Errorf("statuses = %s", got)
	}
	if !strings.Contains(res.Invariants[0].Reason, "column x does not exist") {
		t.Errorf("reason = %q", res.Invariants[0].Reason)
	}
	if res.Status() != invariant.Error {
		t.Errorf("status = %s", res.Status())
	}
}

// slowTarget tracks how many requests are in flight at once.
type slowTarget struct {
	inFlight, peak atomic.Int32
}

func (s *slowTarget) Send(context.Context, scenario.Request) (Response, error) {
	n := s.inFlight.Add(1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	s.inFlight.Add(-1)
	return Response{Status: 200}, nil
}

func TestBatchesRunConcurrentlyAndInOrder(t *testing.T) {
	sc := testScenario()
	target := &slowTarget{}
	seq := perturb.Sequence{Index: 1, Batches: []perturb.Batch{
		{{Event: 0}, {Event: 0}, {Event: 1}},
		{{Event: 1, Delay: 20 * time.Millisecond}},
	}}
	res := (&Runner{Target: target, Store: &fakeStore{}}).RunSequence(context.Background(), sc, seq)

	if p := target.peak.Load(); p != 3 {
		t.Errorf("peak in-flight = %d, want 3 (the whole first batch)", p)
	}
	if len(res.Deliveries) != 4 || res.Deliveries[3].Batch != 1 || res.Deliveries[3].Delay != 20*time.Millisecond {
		t.Errorf("deliveries = %+v", res.Deliveries)
	}
	if res.Status() != invariant.Pass {
		t.Errorf("status = %s (%s)", res.Status(), res.Error)
	}
}

func TestAcceptStatus(t *testing.T) {
	sc := testScenario()
	sc.Events[0].AcceptStatus = []int{200, 500}
	sc.Events[1].AcceptStatus = []int{200}

	res := run(&fakeTarget{status: map[string]int{"/1": 500}}, &fakeStore{}, sc)
	if res.Error != "" {
		t.Errorf("500 is accepted for the first event, got error %q", res.Error)
	}
	res = run(&fakeTarget{status: map[string]int{"/2": 409}}, &fakeStore{}, sc)
	if !strings.Contains(res.Error, `event "second": application answered 409, not in accept_status [200]`) {
		t.Errorf("error = %q", res.Error)
	}
}

// failingStore makes the "broken" invariant fail on chosen sequences.
type failingStore struct {
	fakeStore
	runs   int
	failOn map[int]bool // by 1-based sequence count
}

func (f *failingStore) Reset(ctx context.Context, tables []string) error {
	f.runs++
	return nil
}

func (f *failingStore) QueryRows(_ context.Context, sql string, _ int) (invariant.Rows, error) {
	if sql == "Q2" && f.failOn[f.runs] {
		return invariant.Rows{Columns: []string{"id"}, Total: 1}, nil
	}
	return invariant.Rows{}, nil
}

func TestExploreStopsAtFirstFailure(t *testing.T) {
	sc := testScenario()
	sc.Perturb = scenario.Perturb{Duplicate: 2}
	store := &failingStore{failOn: map[int]bool{3: true, 5: true}}
	ex := (&Runner{Target: &fakeTarget{}, Store: store}).Explore(context.Background(), sc, Options{Seed: 1, Runs: 10})

	if ex.Executed != 3 || ex.Failed != 1 || ex.First == nil || ex.First.Sequence.Index != 2 {
		t.Fatalf("executed=%d failed=%d first=%+v", ex.Executed, ex.Failed, ex.First)
	}
	if st := ex.Invariants[1]; st.Failed != 1 || st.FirstSequence.Index != 2 {
		t.Errorf("stats = %+v", st)
	}
	if ex.ExitCode() != 1 {
		t.Errorf("exit = %d", ex.ExitCode())
	}
}

func TestExploreKeepGoing(t *testing.T) {
	sc := testScenario()
	sc.Perturb = scenario.Perturb{Duplicate: 2}
	store := &failingStore{failOn: map[int]bool{3: true, 5: true}}
	ex := (&Runner{Target: &fakeTarget{}, Store: store}).Explore(context.Background(), sc, Options{Seed: 1, Runs: 10, KeepGoing: true})

	if ex.Executed != 10 || ex.Failed != 2 || ex.Invariants[1].Failed != 2 || ex.Invariants[0].Failed != 0 {
		t.Fatalf("executed=%d failed=%d stats=%+v", ex.Executed, ex.Failed, ex.Invariants)
	}
	if ex.First.Sequence.Index != 2 {
		t.Errorf("first failing sequence = %d", ex.First.Sequence.Index)
	}
}

func TestExploreSequencesAreReproducible(t *testing.T) {
	sc := testScenario()
	sc.Perturb = scenario.Perturb{Duplicate: 3, Concurrency: 3, Reorder: true}
	record := func() []string {
		target := &fakeTarget{}
		r := &Runner{Target: target, Store: &fakeStore{}}
		var got []string
		for i := 0; i < 20; i++ {
			got = append(got, perturb.Format(sc, r.RunSequence(context.Background(), sc, perturb.Generate(sc, 77, i)).Sequence))
		}
		return got
	}
	a, b := record(), record()
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Error("same seed produced different sequences")
	}
}

func TestPlannedRuns(t *testing.T) {
	sc := testScenario()
	sc.Runs = 200
	if n := PlannedRuns(sc, 0); n != 1 {
		t.Errorf("no perturbation: %d runs, want 1", n)
	}
	sc.Perturb.Concurrency = 2
	if n := PlannedRuns(sc, 0); n != 200 {
		t.Errorf("runs = %d, want 200", n)
	}
	if n := PlannedRuns(sc, 7); n != 7 {
		t.Errorf("override = %d", n)
	}
	sc.Runs = 0
	if n := PlannedRuns(sc, 0); n != DefaultRuns {
		t.Errorf("default = %d", n)
	}
}

func TestExploreInterrupted(t *testing.T) {
	sc := testScenario()
	sc.Perturb = scenario.Perturb{Duplicate: 2}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex := (&Runner{Target: &fakeTarget{err: context.Canceled}, Store: &fakeStore{}}).Explore(ctx, sc, Options{Runs: 5})
	if !ex.Interrupted || ex.Executed != 0 || ex.ExitCode() != 2 {
		t.Errorf("interrupted=%v executed=%d exit=%d", ex.Interrupted, ex.Executed, ex.ExitCode())
	}
}

// dupApp is a fake application and database in one: the "broken" invariant
// fails whenever /1 was delivered at least twice since the last reset. flaky
// makes every other failing run pass, like a timing bug.
type dupApp struct {
	fakeStore
	mu    sync.Mutex
	hits  int
	runs  int
	flaky bool
}

func (a *dupApp) Send(_ context.Context, r scenario.Request) (Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Path == "/1" {
		a.hits++
	}
	return Response{Status: 200}, nil
}

func (a *dupApp) Reset(context.Context, []string) error {
	a.hits = 0
	a.runs++
	return nil
}

func (a *dupApp) QueryRows(_ context.Context, sql string, _ int) (invariant.Rows, error) {
	if sql == "Q2" && a.hits >= 2 && !(a.flaky && a.runs%2 == 0) {
		return invariant.Rows{Columns: []string{"hits"}, Values: [][]*string{{ptr(fmt.Sprint(a.hits))}}, Total: 1}, nil
	}
	return invariant.Rows{}, nil
}

func ptr(s string) *string { return &s }

func dupScenario() *scenario.Scenario {
	sc := testScenario()
	sc.Perturb = scenario.Perturb{Duplicate: 3, Concurrency: 4, DelayMS: []int{0, 5}}
	return sc
}

func TestExploreShrinks(t *testing.T) {
	sc := dupScenario()
	app := &dupApp{}
	ex := (&Runner{Target: app, Store: app}).Explore(context.Background(), sc, Options{Seed: 3, Runs: 50, Attempts: 3, Shrink: true})

	st := ex.Invariants[1]
	if st.FirstFailure == nil || st.Shrunk == nil {
		t.Fatalf("no failure or no shrink: %+v", st)
	}
	if got := perturb.Format(sc, st.Shrunk.Sequence); got != "[first ‖ first]" {
		t.Errorf("shrunk to %s (from %s)", got, perturb.Format(sc, st.FirstSequence))
	}
	if st.Shrunk.Reproduced != 3 || st.Shrunk.Attempts != 3 || st.Shrunk.Tried == 0 {
		t.Errorf("shrunk = %+v", st.Shrunk)
	}
	if *st.Shrunk.Evidence.Values[0][0] != "2" {
		t.Errorf("evidence should come from the shrunk run, got %v", *st.Shrunk.Evidence.Values[0][0])
	}
	if ex.Invariants[0].Shrunk != nil {
		t.Error("passing invariant must not be shrunk")
	}
}

func TestShrinkFlakyReportsRate(t *testing.T) {
	sc := dupScenario()
	app := &dupApp{flaky: true}
	ex := (&Runner{Target: app, Store: app}).Explore(context.Background(), sc, Options{Seed: 3, Runs: 50, Attempts: 4, Shrink: true})
	sh := ex.Invariants[1].Shrunk
	if sh == nil || sh.Reproduced != 2 || sh.Attempts != 4 {
		t.Fatalf("shrunk = %+v", sh)
	}
}

func TestReplayRetriesUntilFailure(t *testing.T) {
	sc := dupScenario()
	app := &dupApp{flaky: true}
	seq := perturb.Sequence{Index: 9, Batches: []perturb.Batch{{{Event: 0}, {Event: 0}}}}
	// Runs alternate fail/pass; Reset is counted before the run, so the
	// first run (runs=1) fails. Skip one run to start on a passing one.
	app.runs = 1
	ex := (&Runner{Target: app, Store: app}).Explore(context.Background(), sc, Options{Only: &seq, Attempts: 5})
	if ex.Planned != 5 || ex.Executed != 2 || ex.Failed != 1 || ex.Invariants[1].FirstSequence.Index != 9 {
		t.Errorf("planned=%d executed=%d failed=%d", ex.Planned, ex.Executed, ex.Failed)
	}
}

func TestShrinkGivesUpWhenNotReproducible(t *testing.T) {
	sc := dupScenario()
	app := &onceApp{}
	ex := (&Runner{Target: app, Store: app}).Explore(context.Background(), sc, Options{Seed: 3, Runs: 5, Attempts: 3, Shrink: true})
	sh := ex.Invariants[1].Shrunk
	if sh == nil || sh.Tried != 0 || sh.Reproduced != 0 || app.runs != 4 {
		t.Errorf("shrunk=%+v runs=%d (want 1 failing run + 3 attempts)", sh, app.runs)
	}
}

// onceApp fails "broken" on its first run only.
type onceApp struct {
	fakeStore
	runs int
}

func (a *onceApp) Send(context.Context, scenario.Request) (Response, error) {
	return Response{Status: 200}, nil
}
func (a *onceApp) Reset(context.Context, []string) error { a.runs++; return nil }
func (a *onceApp) QueryRows(_ context.Context, sql string, _ int) (invariant.Rows, error) {
	if sql == "Q2" && a.runs == 1 {
		return invariant.Rows{Columns: []string{"x"}, Total: 1}, nil
	}
	return invariant.Rows{}, nil
}
