// Package report renders run results.
package report

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stillright-dev/stillright/internal/invariant"
	"github.com/stillright-dev/stillright/internal/perturb"
	"github.com/stillright-dev/stillright/internal/runner"
	"github.com/stillright-dev/stillright/internal/scenario"
)

// maxCellWidth truncates long values in evidence tables.
const maxCellWidth = 60

// Terminal writes a human-readable report of an exploration to w.
func Terminal(w io.Writer, ex *runner.Exploration) {
	sc := ex.Scenario
	fmt.Fprintf(w, "scenario  %s  (%s)\n", sc.Name, sc.Path)
	fmt.Fprintf(w, "seed      %d\n", ex.Seed)
	perturbation := "none"
	if perturb.Enabled(sc.Perturb) {
		perturbation = describePerturb(sc.Perturb)
	}
	fmt.Fprintf(w, "perturb   %s\n", perturbation)
	switch {
	case ex.Only != nil && ex.Only.Index >= 0:
		fmt.Fprintf(w, "runs      replaying sequence %d, up to %d attempts\n", ex.Only.Index, ex.Planned)
	case ex.Only != nil:
		fmt.Fprintf(w, "runs      given sequence %s, up to %d attempts\n", perturb.Format(sc, *ex.Only), ex.Planned)
	case perturb.Enabled(sc.Perturb):
		fmt.Fprintf(w, "runs      %d sequences (sequence 0 is the declared order)\n", ex.Planned)
	default:
		fmt.Fprintf(w, "runs      %d (declared order only)\n", ex.Planned)
	}
	switch sc.Setup.Reset {
	case scenario.ResetTruncate:
		fmt.Fprintf(w, "reset     truncate %s, before every sequence\n", strings.Join(sc.Setup.ResetTables, ", "))
	default:
		fmt.Fprintf(w, "reset     none (state carries over between sequences)\n")
	}
	if sc.Setup.SQLFile != "" {
		fmt.Fprintf(w, "setup     %s\n", sc.Setup.SQLFile)
	}
	if sc.SettleMS > 0 {
		fmt.Fprintf(w, "settle    %dms after the last delivery\n", sc.SettleMS)
	}
	if sc.UsesProxy() {
		fmt.Fprintf(w, "proxy     %s records outbound calls (run the app with HTTP_PROXY=http://%s)\n", sc.Proxy.Listen, sc.Proxy.Listen)
	}

	if ex.Error != "" {
		fmt.Fprintf(w, "\nERROR  could not start: %s\n\n", ex.Error)
		for _, inv := range ex.Invariants {
			fmt.Fprintf(w, "ERROR  %s\n", inv.Name)
		}
		fmt.Fprintf(w, "\nno sequence was run\n")
		return
	}

	if ex.First != nil {
		fmt.Fprintln(w)
		writeSequence(w, ex.First)
	}
	fmt.Fprintln(w)

	var pass, fail, errs int
	for _, st := range ex.Invariants {
		switch {
		case st.Failed > 0:
			fail++
			fmt.Fprintf(w, "FAIL   %s\n", st.Name)
			if ex.Executed > 1 || st.FirstSequence.Index != 0 {
				fmt.Fprintf(w, "       failed in %d of %d runs; first in %s\n", st.Failed, ex.Executed, seqLabel(sc, st.FirstSequence))
			}
			if st.Errored > 0 {
				fmt.Fprintf(w, "       could not be checked in %d runs: %s\n", st.Errored, st.FirstError)
			}
			writeFailure(w, ex, st)
		case st.Errored > 0:
			errs++
			fmt.Fprintf(w, "ERROR  %s\n", st.Name)
			// A sequence-level error is already printed with the sequence.
			if ex.First == nil || st.FirstError != "not checked: "+ex.First.Error {
				fmt.Fprintf(w, "       %s\n", st.FirstError)
			}
		default:
			pass++
			fmt.Fprintf(w, "PASS   %s\n", st.Name)
		}
	}

	writeProxyWarnings(w, ex)
	fmt.Fprintf(w, "\n%d passed, %d failed, %d errors\n", pass, fail, errs)
	fmt.Fprintf(w, "%d of %d runs: %d passed, %d failed, %d could not be verified\n",
		ex.Executed, ex.Planned, ex.Executed-ex.Failed-ex.Errored, ex.Failed, ex.Errored)
	switch {
	case ex.Interrupted:
		fmt.Fprintf(w, "interrupted before everything ran\n")
	case ex.Executed < ex.Planned:
		fmt.Fprintf(w, "stopped at the first run that did not pass; use --keep-going to run them all\n")
	}
}

// writeProxyWarnings flags effect results that are probably vacuous: an
// assertion like count(host == "x") <= 1 passes trivially when the app's
// calls never reach the proxy.
func writeProxyWarnings(w io.Writer, ex *runner.Exploration) {
	sc := ex.Scenario
	if !sc.UsesProxy() || ex.Executed == 0 {
		return
	}
	if ex.ProxyCalls == 0 {
		fmt.Fprintf(w, "\nwarning: the effects proxy recorded no outbound calls in any run. If the app should\n"+
			"         make calls here, it is not using the proxy and effect results mean nothing;\n"+
			"         see docs/effects.md.\n")
		return
	}
	seen := map[string]bool{}
	for _, h := range ex.ProxyHosts {
		seen[h] = true
	}
	for _, ef := range sc.Effects {
		if ef.Assertion == nil {
			continue
		}
		for _, h := range ef.Assertion.Hosts() {
			if !seen[h] {
				fmt.Fprintf(w, "\nwarning: %q filters on host %q, which the proxy never saw\n"+
					"         (it saw: %s); see docs/effects.md.\n", ef.Name, h, strings.Join(ex.ProxyHosts, ", "))
			}
		}
	}
}

func seqLabel(sc *scenario.Scenario, seq perturb.Sequence) string {
	if seq.Index < 0 {
		return "the given sequence: " + perturb.Format(sc, seq)
	}
	return fmt.Sprintf("sequence %d: %s", seq.Index, perturb.Format(sc, seq))
}

// writeFailure prints the shrunk sequence, the evidence and how to
// reproduce a failed invariant.
func writeFailure(w io.Writer, ex *runner.Exploration, st runner.InvariantStats) {
	sc := ex.Scenario
	evidence, detail := st.FirstFailure.Evidence, st.FirstFailure.Detail
	smallest := st.FirstSequence

	if sh := st.Shrunk; sh != nil {
		switch {
		case sh.Tried == 0:
			fmt.Fprintf(w, "       could not reproduce it in %d attempts (timing-dependent); not shrunk\n", sh.Attempts)
		default:
			smallest = sh.Sequence
			note := fmt.Sprintf("reproduced %d/%d", sh.Reproduced, sh.Attempts)
			if sh.Reproduced == 0 {
				note += ", timing-dependent: try --attempts 20"
			}
			if sh.Exceeded {
				note += fmt.Sprintf("; stopped after %d candidates, may not be minimal", sh.Tried)
			}
			fmt.Fprintf(w, "       smallest sequence: %s   (%s; %s)\n", perturb.Format(sc, smallest), describeSequence(smallest), note)
			if sh.Reproduced > 0 {
				evidence, detail = sh.Evidence, sh.Detail
			}
		}
	}

	if detail != "" {
		fmt.Fprintf(w, "       %s\n", detail)
	}
	if evidence.Total == 0 {
		// An effect that expected calls and saw none has nothing to show.
	} else if evidence.Total == 1 && len(evidence.Values) == 1 {
		pairs := make([]string, len(evidence.Columns))
		for i, c := range evidence.Columns {
			pairs[i] = c + "=" + cell(evidence.Values[0][i])
		}
		fmt.Fprintf(w, "       evidence: %s\n", strings.Join(pairs, " "))
	} else {
		writeEvidence(w, evidence)
	}

	var cmds []string
	if st.FirstSequence.Index >= 0 {
		cmds = append(cmds, fmt.Sprintf("stillright run %s --seed %d --replay %d", shellQuote(sc.Path), ex.Seed, st.FirstSequence.Index))
	}
	cmds = append(cmds, fmt.Sprintf("stillright run %s --sequence '%s'", shellQuote(sc.Path), perturb.Format(sc, smallest)))
	for i, c := range cmds {
		label := "reproduce:"
		if i > 0 {
			label = strings.Repeat(" ", len(label))
		}
		fmt.Fprintf(w, "       %s %s\n", label, c)
	}
}

// describeSequence summarizes a sequence's shape in words.
func describeSequence(seq perturb.Sequence) string {
	n, batches := seq.Sends(), len(seq.Batches)
	switch {
	case n == 1:
		return "1 delivery"
	case batches == 1:
		return fmt.Sprintf("%d simultaneous deliveries", n)
	case batches == n:
		return fmt.Sprintf("%d deliveries, one at a time", n)
	default:
		return fmt.Sprintf("%d deliveries in %d batches", n, batches)
	}
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func describePerturb(p scenario.Perturb) string {
	var parts []string
	if p.Duplicate > 1 {
		parts = append(parts, fmt.Sprintf("duplicate up to %d×", p.Duplicate))
	}
	if p.Concurrency > 1 {
		parts = append(parts, fmt.Sprintf("up to %d concurrent", p.Concurrency))
	}
	if p.Reorder {
		parts = append(parts, "reorder (may violate after)")
	}
	if len(p.DelayMS) == 2 && p.DelayMS[1] > 0 {
		parts = append(parts, fmt.Sprintf("delay %d–%dms", p.DelayMS[0], p.DelayMS[1]))
	}
	return strings.Join(parts, ", ")
}

// writeSequence prints the deliveries of the sequence that stopped the run.
func writeSequence(w io.Writer, res *runner.Result) {
	sc := res.Scenario
	label := seqLabel(sc, res.Sequence)
	if res.Sequence.Index == 0 {
		label = "sequence 0 (declared order): " + perturb.Format(sc, res.Sequence)
	}
	fmt.Fprintf(w, "%s\n", label)

	idWidth := 0
	for _, d := range res.Deliveries {
		idWidth = max(idWidth, utf8.RuneCountInString(d.EventID))
	}
	for i, d := range res.Deliveries {
		batch := ""
		if i == 0 || res.Deliveries[i-1].Batch != d.Batch {
			batch = fmt.Sprintf("batch %d", d.Batch+1)
		}
		status := "no response"
		if d.Status != 0 {
			status = fmt.Sprint(d.Status)
		}
		delay := ""
		if d.Delay > 0 {
			delay = fmt.Sprintf("  after %s", d.Delay)
		}
		fmt.Fprintf(w, "  %-9s %s  %s %s  → %s  (%s)%s\n", batch, pad(d.EventID, idWidth), d.Method, d.Path, status, d.Duration.Round(time.Millisecond), delay)
	}
	if res.Error != "" {
		fmt.Fprintf(w, "\nERROR  sequence could not be verified: %s\n", res.Error)
	}
}

func writeEvidence(w io.Writer, rows invariant.Rows) {
	shown := len(rows.Values)
	if shown < rows.Total {
		fmt.Fprintf(w, "       evidence: %d rows (showing first %d)\n", rows.Total, shown)
	} else {
		fmt.Fprintf(w, "       evidence: %d rows\n", rows.Total)
	}

	cells := make([][]string, 0, shown+1)
	cells = append(cells, rows.Columns)
	for _, row := range rows.Values {
		line := make([]string, len(row))
		for i, v := range row {
			line[i] = cell(v)
		}
		cells = append(cells, line)
	}
	widths := make([]int, len(rows.Columns))
	for _, line := range cells {
		for i, c := range line {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	for n, line := range cells {
		parts := make([]string, len(line))
		for i, c := range line {
			parts[i] = pad(c, widths[i])
		}
		fmt.Fprintf(w, "       %s\n", strings.TrimRight(strings.Join(parts, " | "), " "))
		if n == 0 {
			seps := make([]string, len(widths))
			for i, wd := range widths {
				seps[i] = strings.Repeat("-", wd)
			}
			fmt.Fprintf(w, "       %s\n", strings.Join(seps, "-+-"))
		}
	}
}

func cell(v *string) string {
	if v == nil {
		return "NULL"
	}
	s := strings.Join(strings.Fields(*v), " ")
	if r := []rune(s); len(r) > maxCellWidth {
		s = string(r[:maxCellWidth-1]) + "…"
	}
	return s
}

func pad(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-utf8.RuneCountInString(s)))
}
