package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/perturb"
	"github.com/stillright/stillright/internal/scenario"
)

// DefaultRuns is how many sequences are generated when the scenario
// perturbs deliveries but does not set runs.
const DefaultRuns = 100

type Options struct {
	HTTPTimeout time.Duration
	Seed        uint64
	Runs        int  // overrides the scenario's runs when > 0
	KeepGoing   bool // keep running after a sequence fails
	// Progress, when set, is called after each sequence.
	Progress func(done, planned int)
}

// PlannedRuns is how many sequences an exploration generates. A scenario
// without perturbation only has its declared order to try, so it runs once
// unless runs is forced.
func PlannedRuns(sc *scenario.Scenario, override int) int {
	switch {
	case override > 0:
		return override
	case !perturb.Enabled(sc.Perturb):
		return 1
	case sc.Runs > 0:
		return sc.Runs
	default:
		return DefaultRuns
	}
}

// Exploration is the outcome of running many sequences of one scenario.
type Exploration struct {
	Scenario    *scenario.Scenario
	Seed        uint64
	Planned     int
	Executed    int
	Failed      int     // sequences where an invariant failed
	Errored     int     // sequences that could not be verified
	First       *Result // first sequence that did not pass
	Invariants  []InvariantStats
	Interrupted bool
	// Error is set when no sequence could run at all.
	Error string
}

type InvariantStats struct {
	Name    string
	Failed  int // sequences in which it failed
	Errored int // sequences in which it could not be checked
	// FirstFailure is the invariant's result in the first sequence where it
	// failed, and FirstSequence that sequence. Nil when it never failed.
	FirstFailure  *invariant.Result
	FirstSequence perturb.Sequence
	// FirstError is why it could not be checked, the first time it could not.
	FirstError string
}

// ExitCode is 0 when every sequence passed, 1 when an invariant failed and
// 2 when something could not be verified or the run was cut short.
func (e *Exploration) ExitCode() int {
	switch {
	case e.Error != "" || e.Errored > 0 || e.Interrupted || e.Executed == 0:
		return 2
	case e.Failed > 0:
		return 1
	default:
		return 0
	}
}

// Explore connects to the scenario's database and application and runs the
// planned sequences.
func Explore(ctx context.Context, sc *scenario.Scenario, opts Options) *Exploration {
	store, err := OpenPostgres(ctx, sc.Target.DatabaseURL)
	if err != nil {
		ex := newExploration(sc, opts)
		ex.Error = fmt.Sprintf("cannot connect to database: %v", err)
		return ex
	}
	defer store.Close(context.WithoutCancel(ctx))

	r := &Runner{
		Target: &HTTPTarget{BaseURL: sc.Target.BaseURL, Client: newHTTPClient(opts.HTTPTimeout)},
		Store:  store,
	}
	return r.Explore(ctx, sc, opts)
}

// Explore runs sequences 0..planned-1 of opts.Seed, stopping at the first one
// that does not pass unless opts.KeepGoing is set. The database is reset
// before every sequence.
func (r *Runner) Explore(ctx context.Context, sc *scenario.Scenario, opts Options) *Exploration {
	ex := newExploration(sc, opts)
	for i := 0; i < ex.Planned; i++ {
		res := r.RunSequence(ctx, sc, perturb.Generate(sc, opts.Seed, i))
		if ctx.Err() != nil {
			// A sequence cut short by the user says nothing about the app.
			ex.Interrupted = true
			break
		}
		ex.record(res)
		if opts.Progress != nil {
			opts.Progress(ex.Executed, ex.Planned)
		}
		if res.Status() != invariant.Pass && !opts.KeepGoing {
			break
		}
	}
	return ex
}

func newExploration(sc *scenario.Scenario, opts Options) *Exploration {
	ex := &Exploration{Scenario: sc, Seed: opts.Seed, Planned: PlannedRuns(sc, opts.Runs)}
	for _, inv := range sc.Invariants {
		ex.Invariants = append(ex.Invariants, InvariantStats{Name: inv.Name})
	}
	return ex
}

func (e *Exploration) record(res *Result) {
	e.Executed++
	switch res.Status() {
	case invariant.Error:
		e.Errored++
	case invariant.Fail:
		e.Failed++
	}
	if res.Status() != invariant.Pass && e.First == nil {
		e.First = res
	}
	for i, inv := range res.Invariants {
		st := &e.Invariants[i]
		switch inv.Status {
		case invariant.Fail:
			st.Failed++
			if st.FirstFailure == nil {
				st.FirstFailure = &res.Invariants[i]
				st.FirstSequence = res.Sequence
			}
		case invariant.Error:
			st.Errored++
			if st.FirstError == "" {
				st.FirstError = inv.Reason
			}
		}
	}
}
