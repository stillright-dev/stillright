package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/stillright-dev/stillright/internal/effects"
	"github.com/stillright-dev/stillright/internal/invariant"
	"github.com/stillright-dev/stillright/internal/perturb"
	"github.com/stillright-dev/stillright/internal/scenario"
	"github.com/stillright-dev/stillright/internal/shrink"
)

// DefaultRuns is how many sequences are generated when the scenario
// perturbs deliveries but does not set runs.
const DefaultRuns = 100

// DefaultShrinkBudget bounds how many candidate sequences are tried when
// shrinking one failure. Each candidate costs up to Options.Attempts runs.
const DefaultShrinkBudget = 60

type Options struct {
	HTTPTimeout time.Duration
	Seed        uint64
	Runs        int  // overrides the scenario's runs when > 0
	KeepGoing   bool // keep running after a sequence fails
	// Only, when set, runs just this sequence (a replayed or hand-written
	// one) up to Attempts times instead of generating Runs sequences.
	Only *perturb.Sequence
	// Attempts is how many times a sequence is retried before concluding it
	// passes, when replaying and when shrinking. Timing bugs do not
	// reproduce on every run.
	Attempts int
	// Shrink reduces each failure to a smaller sequence that still fails.
	Shrink       bool
	ShrinkBudget int
	// Progress, when set, is called after each run.
	Progress func(done, planned int)
	// ShrinkProgress, when set, is called after each shrink candidate.
	ShrinkProgress func(invariant string, tried, budget int)
}

func (o Options) attempts() int { return max(1, o.Attempts) }

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
	// Only is the single sequence that was run, when not generating.
	Only *perturb.Sequence
	// Attempts is how many times a sequence was retried when replaying and
	// shrinking.
	Attempts int
	// ProxyAddr is where the effects proxy listened, and ProxyCalls how many
	// outbound calls it recorded in total; zero calls usually means the app
	// is not routed through it.
	ProxyAddr  string
	ProxyCalls int
	ProxyHosts []string // every host the proxy saw
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
	// Shrunk is the reduction of FirstSequence, when shrinking ran.
	Shrunk *Shrunk
}

// Shrunk is the smallest sequence found that still breaks an invariant.
type Shrunk struct {
	Sequence perturb.Sequence
	// Reproduced of Attempts confirmation runs of Sequence failed. When
	// Tried is 0 the original failure could not be reproduced at all and
	// Sequence is the unshrunk original.
	Reproduced int
	Attempts   int
	Evidence   invariant.Rows // from a failing confirmation run
	Detail     string         // that run's invariant.Result.Detail
	Tried      int            // candidates evaluated
	Exceeded   bool           // the budget ran out before a minimum
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
	var proxy *effects.Proxy
	if sc.UsesProxy() {
		stubs := make([]effects.Stub, len(sc.Proxy.Stubs))
		for i, s := range sc.Proxy.Stubs {
			stubs[i] = effects.Stub{Host: s.Host, Status: s.Status, Body: s.Body, Headers: s.Headers}
		}
		proxy, err = effects.Listen(sc.Proxy.Listen, stubs)
		if err != nil {
			ex := newExploration(sc, opts)
			ex.Error = fmt.Sprintf("cannot start the effects proxy on %s: %v", sc.Proxy.Listen, err)
			return ex
		}
		defer proxy.Close()
		r.Effects = proxy
	}

	ex := r.Explore(ctx, sc, opts)
	if proxy != nil {
		ex.ProxyAddr, ex.ProxyCalls, ex.ProxyHosts = proxy.Addr(), proxy.Total(), proxy.Hosts()
	}
	return ex
}

// Explore runs sequences 0..planned-1 of opts.Seed (or opts.Only, up to
// opts.Attempts times), stopping at the first run that does not pass unless
// opts.KeepGoing is set, then shrinks each failure when opts.Shrink is set.
// The database is reset before every run.
func (r *Runner) Explore(ctx context.Context, sc *scenario.Scenario, opts Options) *Exploration {
	ex := newExploration(sc, opts)
	for i := 0; i < ex.Planned; i++ {
		var seq perturb.Sequence
		if opts.Only != nil {
			seq = *opts.Only
		} else {
			seq = perturb.Generate(sc, opts.Seed, i)
		}
		res := r.RunSequence(ctx, sc, seq)
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

	if opts.Shrink && !ex.Interrupted {
		for i := range ex.Invariants {
			if ex.Invariants[i].FirstFailure != nil {
				ex.Invariants[i].Shrunk = r.shrink(ctx, sc, i, ex.Invariants[i].FirstSequence, opts)
			}
			if ctx.Err() != nil {
				ex.Interrupted = true
				break
			}
		}
	}
	return ex
}

// shrink reduces seq, which broke invariant inv once, to a smaller sequence
// that still breaks it. A candidate counts as failing if it fails in any of
// opts.Attempts runs.
func (r *Runner) shrink(ctx context.Context, sc *scenario.Scenario, inv int, seq perturb.Sequence, opts Options) *Shrunk {
	attempts := opts.attempts()
	budget := opts.ShrinkBudget
	if budget <= 0 {
		budget = DefaultShrinkBudget
	}
	// confirm runs seq up to n times, stopping at the first failure when
	// stopEarly; it returns the failures and the first failing result.
	confirm := func(seq perturb.Sequence, n int, stopEarly bool) (int, *invariant.Result) {
		fails := 0
		var first *invariant.Result
		for a := 0; a < n && ctx.Err() == nil; a++ {
			res := r.RunSequence(ctx, sc, seq)
			if ctx.Err() == nil && res.Invariants[inv].Status == invariant.Fail {
				fails++
				if first == nil {
					first = &res.Invariants[inv]
				}
				if stopEarly {
					break
				}
			}
		}
		return fails, first
	}

	if fails, _ := confirm(seq, attempts, true); fails == 0 {
		return &Shrunk{Sequence: seq, Attempts: attempts}
	}
	tried := 0
	res := shrink.Shrink(seq, func(cand perturb.Sequence) bool {
		fails, _ := confirm(cand, attempts, true)
		tried++
		if opts.ShrinkProgress != nil {
			opts.ShrinkProgress(sc.Checks()[inv], tried, budget)
		}
		return fails > 0
	}, budget)

	out := &Shrunk{Sequence: res.Sequence, Attempts: attempts, Tried: res.Tried, Exceeded: res.Exceeded}
	fails, first := confirm(res.Sequence, attempts, false)
	out.Reproduced = fails
	if first != nil {
		out.Evidence, out.Detail = first.Evidence, first.Detail
	}
	return out
}

func newExploration(sc *scenario.Scenario, opts Options) *Exploration {
	ex := &Exploration{Scenario: sc, Seed: opts.Seed, Planned: PlannedRuns(sc, opts.Runs), Only: opts.Only, Attempts: opts.attempts()}
	if opts.Only != nil {
		ex.Planned = opts.attempts()
	}
	for _, name := range sc.Checks() {
		ex.Invariants = append(ex.Invariants, InvariantStats{Name: name})
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
