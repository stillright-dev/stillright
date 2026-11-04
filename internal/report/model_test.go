package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stillright-dev/stillright/internal/perturb"
	"github.com/stillright-dev/stillright/internal/runner"
)

var now = time.Date(2026, 11, 3, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))

func builtReport(t *testing.T) *Report {
	sh := &runner.Shrunk{
		Sequence:   perturb.Sequence{Index: -1, Batches: []perturb.Batch{{{Event: 0}, {Event: 0}}}},
		Reproduced: 4, Attempts: 5, Tried: 14,
		Evidence: invariantRows([]string{"customer_id", "note"}, []*string{str("42"), str("<script>alert(1)</script>")}),
	}
	ex := shrunkExploration(sh)
	ex.Attempts = 5
	ex.ProxyCalls, ex.ProxyHosts = 3, []string{"api.email.local"}
	ex.First = &runner.Result{
		Scenario: ex.Scenario,
		Sequence: ex.Invariants[1].FirstSequence,
		Deliveries: []runner.Delivery{
			{EventID: "paid", Method: "POST", Path: "/p", Batch: 0, Delay: 24 * time.Millisecond, Status: 200, Duration: 1234567 * time.Nanosecond},
		},
	}
	return Build(ex, "0.1.0-test", now)
}

func TestBuild(t *testing.T) {
	rep := builtReport(t)
	if rep.Schema != SchemaVersion || rep.Status != "FAIL" || rep.ExitCode != 1 || rep.Seed != "8812" || rep.Mode != "explore" {
		t.Errorf("header = %+v", rep)
	}
	if !rep.GeneratedAt.Equal(now) || rep.GeneratedAt.Location() != time.UTC {
		t.Errorf("generated_at should be UTC: %v", rep.GeneratedAt)
	}
	if rep.Runs != (RunCounts{Planned: 100, Executed: 100, Passed: 90, Failed: 10}) {
		t.Errorf("runs = %+v", rep.Runs)
	}
	c := rep.Checks[1]
	if c.Kind != "invariant" || c.Status != "FAIL" || c.FailedRuns != 10 || len(c.Reproduce) != 2 {
		t.Errorf("check = %+v", c)
	}
	if c.FirstFailure.Sequence.Index == nil || *c.FirstFailure.Sequence.Index != 6 || c.FirstFailure.Sequence.Batches[0][0].DelayMS != 24 {
		t.Errorf("first failure = %+v", c.FirstFailure)
	}
	if c.Shrunk.Sequence.Index != nil || c.Shrunk.Sequence.Text != "[paid ‖ paid]" || !c.Shrunk.Reproducible || c.Shrunk.Evidence.Total != 1 {
		t.Errorf("shrunk = %+v", c.Shrunk)
	}
	if d := rep.StoppedAt.Deliveries[0]; d.DurationMS != 1.2 || d.DelayMS != 24 {
		t.Errorf("delivery = %+v", d)
	}
	if rep.Proxy == nil || rep.Proxy.Calls != 3 {
		t.Errorf("proxy = %+v", rep.Proxy)
	}
}

// The JSON keys are a public contract (docs/report-schema.md).
func TestJSONShape(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, builtReport(t)); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schema", "stillright_version", "generated_at", "scenario", "status", "exit_code", "interrupted",
		"seed", "mode", "attempts", "perturb", "runs", "checks", "stopped_at", "proxy", "warnings"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	check := m["checks"].([]any)[1].(map[string]any)
	for _, k := range []string{"name", "kind", "status", "failed_runs", "errored_runs", "first_failure", "shrunk", "reproduce"} {
		if _, ok := check[k]; !ok {
			t.Errorf("missing check key %q", k)
		}
	}
	if pass := m["checks"].([]any)[0].(map[string]any); pass["first_failure"] != nil || pass["shrunk"] != nil {
		t.Errorf("passing check should have null first_failure and shrunk: %v", pass)
	}
	if !strings.Contains(buf.String(), `"note"`) || !strings.Contains(buf.String(), "<script>alert(1)</script>") {
		t.Error("JSON should keep evidence verbatim")
	}
}

func TestHTMLIsSelfContainedAndEscaped(t *testing.T) {
	var buf bytes.Buffer
	if err := HTML(&buf, builtReport(t)); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	if strings.Contains(page, "<script") || regexp.MustCompile(`(src|href)="https?:`).MatchString(page) || strings.Contains(page, "@import") {
		t.Error("HTML must not load scripts or external resources")
	}
	for _, w := range []string{
		"&lt;script&gt;alert(1)&lt;/script&gt;",
		`<meta name="viewport"`,
		"one active subscription per customer",
		"Smallest sequence",
		"reproduced 4/5",
		"--seed 8812 --replay 6",
		"First in sequence 6:",
		"stillright.report/v1",
		"2026-11-03 15:00:00 UTC",
	} {
		if !strings.Contains(page, w) {
			t.Errorf("HTML missing %q", w)
		}
	}
}

func TestMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, builtReport(t)); err != nil {
		t.Fatal(err)
	}
	md := buf.String()
	for _, w := range []string{
		"### ❌ stillright: payment-confirmed — FAIL\n",
		"`stillright/my payment.yaml` · seed `8812` · 100 of 100 runs: 90 passed, 10 failed, 0 could not be verified",
		"| ❌ | one active subscription per customer | failed in 10 of 100 runs · smallest \\[paid ‖ paid] (reproduced 4/5) |",
		"| ✅ | refund leaves no active subscription | passed |",
		"stillright run 'stillright/my payment.yaml' --sequence '[paid ‖ paid]'",
	} {
		if !strings.Contains(md, w) {
			t.Errorf("markdown missing %q in:\n%s", w, md)
		}
	}
}

func TestLoadErrorReport(t *testing.T) {
	rep := LoadError("s.yaml", "v", now, errors.New("s.yaml:1: version: is required"))
	if rep.Status != "ERROR" || rep.ExitCode != 2 || !strings.Contains(rep.Error, "version: is required") {
		t.Errorf("rep = %+v", rep)
	}
	var buf bytes.Buffer
	Markdown(&buf, rep)
	if !strings.Contains(buf.String(), "**Could not run:** invalid scenario") || strings.Contains(buf.String(), "0 of 0 runs") {
		t.Errorf("markdown:\n%s", buf.String())
	}
	buf.Reset()
	if err := HTML(&buf, rep); err != nil || !strings.Contains(buf.String(), "Could not run") {
		t.Errorf("html err=%v", err)
	}
}
