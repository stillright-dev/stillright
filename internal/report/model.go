package report

import (
	"fmt"
	"strconv"
	"time"

	"github.com/stillright-dev/stillright/internal/invariant"
	"github.com/stillright-dev/stillright/internal/perturb"
	"github.com/stillright-dev/stillright/internal/runner"
	"github.com/stillright-dev/stillright/internal/scenario"
)

// SchemaVersion identifies the report.json format. It changes only when a
// field is removed or changes meaning; new fields may be added within a
// version. See docs/report-schema.md.
const SchemaVersion = "stillright.report/v1"

// Report is the machine-readable result of one stillright run. It is what
// report.json contains and what the HTML and Markdown reports render.
type Report struct {
	Schema      string       `json:"schema"`
	Version     string       `json:"stillright_version"`
	GeneratedAt time.Time    `json:"generated_at"`
	Scenario    ScenarioInfo `json:"scenario"`
	Status      string       `json:"status"` // PASS, FAIL or ERROR
	ExitCode    int          `json:"exit_code"`
	Error       string       `json:"error,omitempty"`
	Interrupted bool         `json:"interrupted"`
	Seed        string       `json:"seed"`
	Mode        string       `json:"mode"` // explore, replay or sequence
	Attempts    int          `json:"attempts"`
	Perturb     PerturbInfo  `json:"perturb"`
	Runs        RunCounts    `json:"runs"`
	Checks      []Check      `json:"checks"`
	StoppedAt   *Run         `json:"stopped_at"`
	Proxy       *ProxyInfo   `json:"proxy"`
	Warnings    []string     `json:"warnings"`
}

type ScenarioInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type PerturbInfo struct {
	Enabled     bool   `json:"enabled"`
	Duplicate   int    `json:"duplicate"`
	Concurrency int    `json:"concurrency"`
	Reorder     bool   `json:"reorder"`
	DelayMS     [2]int `json:"delay_ms"`
	SettleMS    int    `json:"settle_ms"`
}

type RunCounts struct {
	Planned  int `json:"planned"`
	Executed int `json:"executed"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Errored  int `json:"errored"`
}

type Check struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"` // invariant or effect
	Status       string   `json:"status"`
	FailedRuns   int      `json:"failed_runs"`
	ErroredRuns  int      `json:"errored_runs"`
	Error        string   `json:"error,omitempty"`
	FirstFailure *Failure `json:"first_failure"`
	Shrunk       *Shrunk  `json:"shrunk"`
	Reproduce    []string `json:"reproduce"`
}

type Failure struct {
	Sequence Sequence `json:"sequence"`
	Detail   string   `json:"detail,omitempty"`
	Evidence Evidence `json:"evidence"`
}

type Shrunk struct {
	// Reproducible is false when the original failure did not happen again
	// in Attempts runs, so nothing was shrunk.
	Reproducible    bool      `json:"reproducible"`
	Sequence        *Sequence `json:"sequence"`
	Reproduced      int       `json:"reproduced"`
	Attempts        int       `json:"attempts"`
	CandidatesTried int       `json:"candidates_tried"`
	BudgetExhausted bool      `json:"budget_exhausted"`
	Detail          string    `json:"detail,omitempty"`
	Evidence        *Evidence `json:"evidence"`
}

type Sequence struct {
	Index   *int     `json:"index"` // nil for shrunk or hand-written sequences
	Text    string   `json:"text"`
	Batches [][]Send `json:"batches"`
}

type Send struct {
	Event   string `json:"event"`
	DelayMS int64  `json:"delay_ms"`
}

type Evidence struct {
	Columns []string    `json:"columns"`
	Rows    [][]*string `json:"rows"`
	Total   int         `json:"total"`
}

type Run struct {
	Sequence   Sequence   `json:"sequence"`
	Deliveries []Delivery `json:"deliveries"`
	Error      string     `json:"error,omitempty"`
}

type Delivery struct {
	Event      string  `json:"event"`
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Batch      int     `json:"batch"`
	DelayMS    int64   `json:"delay_ms"`
	Status     int     `json:"status"` // 0 when there was no response
	DurationMS float64 `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
}

type ProxyInfo struct {
	Listen string   `json:"listen"`
	Calls  int      `json:"calls"`
	Hosts  []string `json:"hosts"`
}

// Build turns an exploration into a Report.
func Build(ex *runner.Exploration, version string, now time.Time) *Report {
	sc := ex.Scenario
	rep := &Report{
		Schema:      SchemaVersion,
		Version:     version,
		GeneratedAt: now.UTC(),
		Scenario:    ScenarioInfo{Name: sc.Name, Path: sc.Path},
		ExitCode:    ex.ExitCode(),
		Error:       ex.Error,
		Interrupted: ex.Interrupted,
		Seed:        strconv.FormatUint(ex.Seed, 10),
		Mode:        "explore",
		Attempts:    ex.Attempts,
		Perturb:     perturbInfo(sc),
		Runs: RunCounts{
			Planned: ex.Planned, Executed: ex.Executed,
			Passed: ex.Executed - ex.Failed - ex.Errored, Failed: ex.Failed, Errored: ex.Errored,
		},
		Checks:   []Check{},
		Warnings: warnings(ex),
	}
	rep.Status = statusFor(rep.ExitCode)
	if ex.Only != nil {
		rep.Mode = "sequence"
		if ex.Only.Index >= 0 {
			rep.Mode = "replay"
		}
	}
	if rep.Warnings == nil {
		rep.Warnings = []string{}
	}
	if sc.UsesProxy() {
		rep.Proxy = &ProxyInfo{Listen: sc.Proxy.Listen, Calls: ex.ProxyCalls, Hosts: ex.ProxyHosts}
		if rep.Proxy.Hosts == nil {
			rep.Proxy.Hosts = []string{}
		}
	}

	for i, st := range ex.Invariants {
		c := Check{
			Name:        st.Name,
			Kind:        "invariant",
			Status:      string(checkStatus(st)),
			FailedRuns:  st.Failed,
			ErroredRuns: st.Errored,
			Error:       st.FirstError,
			Reproduce:   []string{},
		}
		if i >= len(sc.Invariants) {
			c.Kind = "effect"
		}
		if ex.Error != "" {
			c.Status, c.Error = string(invariant.Error), "not checked: "+ex.Error
		}
		if st.FirstFailure != nil {
			c.FirstFailure = &Failure{
				Sequence: sequence(sc, st.FirstSequence),
				Detail:   st.FirstFailure.Detail,
				Evidence: evidence(st.FirstFailure.Evidence),
			}
			c.Reproduce = reproduceCommands(ex, st)
		}
		if sh := st.Shrunk; sh != nil {
			c.Shrunk = &Shrunk{
				Reproducible: sh.Tried > 0, Reproduced: sh.Reproduced, Attempts: sh.Attempts,
				CandidatesTried: sh.Tried, BudgetExhausted: sh.Exceeded, Detail: sh.Detail,
			}
			if sh.Tried > 0 {
				seq := sequence(sc, sh.Sequence)
				c.Shrunk.Sequence = &seq
				if sh.Reproduced > 0 {
					e := evidence(sh.Evidence)
					c.Shrunk.Evidence = &e
				}
			}
		}
		rep.Checks = append(rep.Checks, c)
	}

	if res := ex.First; res != nil {
		run := &Run{Sequence: sequence(sc, res.Sequence), Error: res.Error, Deliveries: []Delivery{}}
		for _, d := range res.Deliveries {
			run.Deliveries = append(run.Deliveries, Delivery{
				Event: d.EventID, Method: d.Method, Path: d.Path, Batch: d.Batch,
				DelayMS: d.Delay.Milliseconds(), Status: d.Status,
				DurationMS: float64(d.Duration.Microseconds()/100) / 10, Error: d.Err,
			})
		}
		rep.StoppedAt = run
	}
	return rep
}

// LoadError is the report for a scenario that could not be loaded, so CI
// still gets a result file when the YAML is broken.
func LoadError(path, version string, now time.Time, err error) *Report {
	return &Report{
		Schema: SchemaVersion, Version: version, GeneratedAt: now.UTC(),
		Scenario: ScenarioInfo{Path: path},
		Status:   string(invariant.Error), ExitCode: 2,
		Error: fmt.Sprintf("invalid scenario: %v", err),
		Mode:  "explore", Seed: "0",
		Checks: []Check{}, Warnings: []string{},
	}
}

func statusFor(exit int) string {
	switch exit {
	case 0:
		return string(invariant.Pass)
	case 1:
		return string(invariant.Fail)
	default:
		return string(invariant.Error)
	}
}

func perturbInfo(sc *scenario.Scenario) PerturbInfo {
	p := sc.Perturb
	info := PerturbInfo{
		Enabled: perturb.Enabled(p), Duplicate: max(1, p.Duplicate), Concurrency: max(1, p.Concurrency),
		Reorder: p.Reorder, SettleMS: sc.SettleMS,
	}
	if len(p.DelayMS) == 2 {
		info.DelayMS = [2]int{p.DelayMS[0], p.DelayMS[1]}
	}
	return info
}

func sequence(sc *scenario.Scenario, seq perturb.Sequence) Sequence {
	out := Sequence{Text: perturb.Format(sc, seq), Batches: [][]Send{}}
	if seq.Index >= 0 {
		i := seq.Index
		out.Index = &i
	}
	for _, b := range seq.Batches {
		sends := []Send{}
		for _, s := range b {
			sends = append(sends, Send{Event: sc.Events[s.Event].ID, DelayMS: s.Delay.Milliseconds()})
		}
		out.Batches = append(out.Batches, sends)
	}
	return out
}

func evidence(rows invariant.Rows) Evidence {
	e := Evidence{Columns: rows.Columns, Rows: rows.Values, Total: rows.Total}
	if e.Columns == nil {
		e.Columns = []string{}
	}
	if e.Rows == nil {
		e.Rows = [][]*string{}
	}
	return e
}
