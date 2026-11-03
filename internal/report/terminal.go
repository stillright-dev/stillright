// Package report renders run results.
package report

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/perturb"
	"github.com/stillright/stillright/internal/runner"
	"github.com/stillright/stillright/internal/scenario"
)

// maxCellWidth truncates long values in evidence tables.
const maxCellWidth = 60

// Terminal writes a human-readable report of an exploration to w.
func Terminal(w io.Writer, ex *runner.Exploration) {
	sc := ex.Scenario
	fmt.Fprintf(w, "scenario  %s  (%s)\n", sc.Name, sc.Path)
	fmt.Fprintf(w, "seed      %d\n", ex.Seed)
	if perturb.Enabled(sc.Perturb) {
		fmt.Fprintf(w, "perturb   %s\n", describePerturb(sc.Perturb))
		fmt.Fprintf(w, "runs      %d sequences (sequence 0 is the declared order)\n", ex.Planned)
	} else {
		fmt.Fprintf(w, "perturb   none\n")
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
	if n := len(sc.Effects); n > 0 {
		fmt.Fprintf(w, "note      %d effect assertion(s) not checked: effects are not supported in this version yet\n", n)
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
				fmt.Fprintf(w, "       failed in %d of %d sequences; first in sequence %d: %s\n",
					st.Failed, ex.Executed, st.FirstSequence.Index, perturb.Format(sc, st.FirstSequence))
			}
			if st.Errored > 0 {
				fmt.Fprintf(w, "       could not be checked in %d sequences: %s\n", st.Errored, st.FirstError)
			}
			writeEvidence(w, st.FirstFailure.Evidence)
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

	fmt.Fprintf(w, "\n%d passed, %d failed, %d errors\n", pass, fail, errs)
	fmt.Fprintf(w, "%d of %d sequences run: %d passed, %d failed, %d could not be verified\n",
		ex.Executed, ex.Planned, ex.Executed-ex.Failed-ex.Errored, ex.Failed, ex.Errored)
	switch {
	case ex.Interrupted:
		fmt.Fprintf(w, "interrupted before all sequences ran\n")
	case ex.Executed < ex.Planned:
		fmt.Fprintf(w, "stopped at the first sequence that did not pass; use --keep-going to run them all\n")
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
	label := ""
	if res.Sequence.Index == 0 {
		label = " (declared order)"
	}
	fmt.Fprintf(w, "sequence %d%s: %s\n", res.Sequence.Index, label, perturb.Format(sc, res.Sequence))

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
	if rows.Total == 1 {
		fmt.Fprintf(w, "       1 row returned\n")
	} else if shown < rows.Total {
		fmt.Fprintf(w, "       %d rows returned (showing first %d)\n", rows.Total, shown)
	} else {
		fmt.Fprintf(w, "       %d rows returned\n", rows.Total)
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
