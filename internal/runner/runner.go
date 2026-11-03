// Package runner executes a scenario against an application: it resets the
// database, runs the setup SQL, delivers events and checks invariants.
package runner

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/perturb"
	"github.com/stillright/stillright/internal/scenario"
)

// Delivery is one request sent to the application.
type Delivery struct {
	EventID  string
	Method   string
	Path     string
	Batch    int
	Delay    time.Duration
	Status   int // 0 when no response was received
	Duration time.Duration
	// Err is set when the delivery was not handled: no response, or a status
	// outside the event's accept_status.
	Err string
}

// Result is the outcome of running one sequence.
type Result struct {
	Scenario   *scenario.Scenario
	Sequence   perturb.Sequence
	Deliveries []Delivery
	Invariants []invariant.Result
	// Error explains why the run could not be verified. When set, every
	// invariant is reported as ERROR without being checked.
	Error string
}

// Status is ERROR when any invariant could not be checked, FAIL when any
// failed and PASS otherwise. ERROR wins over FAIL: a run that was not fully
// verified must not look like an ordinary failure.
func (r *Result) Status() invariant.Status {
	status := invariant.Pass
	for _, inv := range r.Invariants {
		switch inv.Status {
		case invariant.Error:
			return invariant.Error
		case invariant.Fail:
			status = invariant.Fail
		}
	}
	return status
}

type Runner struct {
	Target Target
	Store  Store
}

// RunSequence executes one sequence: reset, setup, deliver each batch with
// its sends released together, then check the invariants.
func (r *Runner) RunSequence(ctx context.Context, sc *scenario.Scenario, seq perturb.Sequence) *Result {
	res := &Result{Scenario: sc, Sequence: seq}

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

	for bi, batch := range seq.Batches {
		delivered := r.deliverBatch(ctx, sc, bi, batch)
		res.Deliveries = append(res.Deliveries, delivered...)
		for _, d := range delivered {
			if d.Err != "" {
				return failed(res, fmt.Sprintf("event %q: %s", d.EventID, d.Err))
			}
		}
	}

	for _, inv := range sc.Invariants {
		res.Invariants = append(res.Invariants, invariant.Check(ctx, r.Store, inv))
	}
	return res
}

// deliverBatch sends every delivery of a batch concurrently. Goroutines wait
// on a barrier so the sends are released as close together as possible, then
// each sleeps its own delay before sending.
func (r *Runner) deliverBatch(ctx context.Context, sc *scenario.Scenario, bi int, batch perturb.Batch) []Delivery {
	out := make([]Delivery, len(batch))
	var ready, done sync.WaitGroup
	release := make(chan struct{})
	ready.Add(len(batch))
	done.Add(len(batch))
	for i, s := range batch {
		go func() {
			defer done.Done()
			ready.Done()
			<-release
			out[i] = r.deliver(ctx, sc, sc.Events[s.Event], bi, s.Delay)
		}()
	}
	ready.Wait()
	close(release)
	done.Wait()
	return out
}

func (r *Runner) deliver(ctx context.Context, sc *scenario.Scenario, ev scenario.Event, batch int, delay time.Duration) Delivery {
	d := Delivery{EventID: ev.ID, Method: ev.Request.Method, Path: ev.Request.Path, Batch: batch, Delay: delay}
	if delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			d.Err = ctx.Err().Error()
			return d
		}
	}
	resp, err := r.Target.Send(ctx, ev.Request)
	d.Status, d.Duration = resp.Status, resp.Duration
	switch {
	case err != nil:
		d.Err = fmt.Sprintf("no response from %s: %v", sc.Target.BaseURL, err)
	case len(ev.AcceptStatus) > 0 && !slices.Contains(ev.AcceptStatus, resp.Status):
		d.Err = fmt.Sprintf("application answered %d, not in accept_status %v%s", resp.Status, ev.AcceptStatus, bodySuffix(resp.Body))
	case len(ev.AcceptStatus) == 0 && resp.Status >= 500:
		d.Err = fmt.Sprintf("application answered %d%s", resp.Status, bodySuffix(resp.Body))
	}
	return d
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

// newHTTPClient never goes through HTTP_PROXY: that variable is meant for the
// application under test, and stillright's own deliveries must reach it
// directly.
func newHTTPClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.MaxIdleConnsPerHost = 64
	return &http.Client{Timeout: timeout, Transport: tr}
}
