// Package runner executes a scenario against an application: it resets the
// database, runs the setup SQL, delivers events and checks invariants.
package runner

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/scenario"
)

// Delivery is one request sent to the application.
type Delivery struct {
	EventID  string
	Method   string
	Path     string
	Status   int // 0 when no response was received
	Duration time.Duration
}

// Result is the outcome of running a scenario once.
type Result struct {
	Scenario   *scenario.Scenario
	Deliveries []Delivery
	Invariants []invariant.Result
	// Error explains why the run could not be verified. When set, every
	// invariant is reported as ERROR without being checked.
	Error string
}

// ExitCode is 0 when every invariant passed, 1 when any failed and 2 when any
// could not be checked. ERROR wins over FAIL: a run that was not fully
// verified must not look like an ordinary failure.
func (r *Result) ExitCode() int {
	code := 0
	for _, inv := range r.Invariants {
		switch inv.Status {
		case invariant.Error:
			return 2
		case invariant.Fail:
			code = 1
		}
	}
	return code
}

type Runner struct {
	Target Target
	Store  Store
}

// Options configure Execute.
type Options struct {
	HTTPTimeout time.Duration
}

// Execute connects to the scenario's database and application and runs it.
func Execute(ctx context.Context, sc *scenario.Scenario, opts Options) *Result {
	store, err := OpenPostgres(ctx, sc.Target.DatabaseURL)
	if err != nil {
		return failed(&Result{Scenario: sc}, fmt.Sprintf("cannot connect to database: %v", err))
	}
	defer store.Close(context.WithoutCancel(ctx))

	r := &Runner{
		Target: &HTTPTarget{BaseURL: sc.Target.BaseURL, Client: &http.Client{Timeout: opts.HTTPTimeout}},
		Store:  store,
	}
	return r.Run(ctx, sc)
}

// Run executes the scenario once: reset, setup, deliver every event once in
// declared order, then check the invariants.
func (r *Runner) Run(ctx context.Context, sc *scenario.Scenario) *Result {
	res := &Result{Scenario: sc}

	if sc.Setup.Reset == scenario.ResetTruncate {
		if err := r.Store.Reset(ctx, sc.Setup.ResetTables); err != nil {
			return failed(res, fmt.Sprintf("reset failed: %v", err))
		}
	}
	if strings.TrimSpace(sc.Setup.SQL) != "" {
		if err := r.Store.Exec(ctx, sc.Setup.SQL); err != nil {
			return failed(res, fmt.Sprintf("setup SQL %s failed: %v", sc.Setup.SQLFile, err))
		}
	}

	for _, ev := range sc.Events {
		resp, err := r.Target.Send(ctx, ev.Request)
		res.Deliveries = append(res.Deliveries, Delivery{
			EventID:  ev.ID,
			Method:   ev.Request.Method,
			Path:     ev.Request.Path,
			Status:   resp.Status,
			Duration: resp.Duration,
		})
		if err != nil {
			return failed(res, fmt.Sprintf("event %q: no response from %s: %v", ev.ID, sc.Target.BaseURL, err))
		}
		if resp.Status >= 500 {
			return failed(res, fmt.Sprintf("event %q: application answered %d%s", ev.ID, resp.Status, bodySuffix(resp.Body)))
		}
	}

	for _, inv := range sc.Invariants {
		res.Invariants = append(res.Invariants, invariant.Check(ctx, r.Store, inv))
	}
	return res
}

func failed(res *Result, reason string) *Result {
	res.Error = reason
	res.Invariants = res.Invariants[:0]
	for _, inv := range res.Scenario.Invariants {
		res.Invariants = append(res.Invariants, invariant.NotChecked(inv, reason))
	}
	return res
}

func bodySuffix(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return ": " + s
}
