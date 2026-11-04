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
		fmt.Fprintf(w, "runs      %s (sequence 0 is the declared order)\n", plural(ex.Planned, "sequence", "sequences"))
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
		switch checkStatus(st) {
		case invariant.Fail:
			fail++
			fmt.Fprintf(w, "FAIL   %s\n", st.Name)
			if ex.Executed > 1 || st.FirstSequence.Index != 0 {
				fmt.Fprintf(w, "       failed in %d of %d runs; first in %s\n", st.Failed, ex.Executed, seqLabel(sc, st.FirstSequence))
			}
			if st.Errored > 0 {
				fmt.Fprintf(w, "       could not be checked in %d runs: %s\n", st.Errored, st.FirstError)
			}
			writeFailure(w, ex, st)
		case invariant.Error:
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

	for _, msg := range warnings(ex) {
		fmt.Fprintf(w, "\nwarning: %s\n", wrap(msg, 80, "         "))
	}
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
	if sh := st.Shrunk; sh != nil {
		if sh.Tried == 0 {
			fmt.Fprintf(w, "       could not reproduce it in %d attempts (timing-dependent); not shrunk\n", sh.Attempts)
		} else {
			fmt.Fprintf(w, "       smallest sequence: %s   (%s; %s)\n", perturb.Format(sc, sh.Sequence), describeSequence(sh.Sequence), shrinkNote(sh))
		}
	}

	evidence, detail := failureDetail(st)
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

	for i, c := range reproduceCommands(ex, st) {
		label := "reproduce:"
		if i > 0 {
			label = strings.Repeat(" ", len(label))
		}
		fmt.Fprintf(w, "       %s %s\n", label, c)
	}
}

// shrinkNote summarizes how trustworthy a shrunk sequence is.
func shrinkNote(sh *runner.Shrunk) string {
	note := fmt.Sprintf("reproduced %d/%d", sh.Reproduced, sh.Attempts)
	if sh.Reproduced == 0 {
		note += ", timing-dependent: try --attempts 20"
	}
	if sh.Exceeded {
		note += fmt.Sprintf("; stopped after %d candidates, may not be minimal", sh.Tried)
	}
	return note
}

// wrap breaks s into lines of at most width runes, indenting continuation
// lines with indent.
func wrap(s string, width int, indent string) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > width-utf8.RuneCountInString(indent) {
			lines = append(lines, line)
			line = word
			continue
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return strings.Join(append(lines, line), "\n"+indent)
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
