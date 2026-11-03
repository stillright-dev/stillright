// Package report renders run results.
package report

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/runner"
	"github.com/stillright/stillright/internal/scenario"
)

// maxCellWidth truncates long values in evidence tables.
const maxCellWidth = 60

// Terminal writes a human-readable report of res to w.
func Terminal(w io.Writer, res *runner.Result) {
	sc := res.Scenario
	fmt.Fprintf(w, "scenario  %s  (%s)\n", sc.Name, sc.Path)

	switch sc.Setup.Reset {
	case scenario.ResetTruncate:
		fmt.Fprintf(w, "reset     truncate %s\n", strings.Join(sc.Setup.ResetTables, ", "))
	default:
		fmt.Fprintf(w, "reset     none\n")
	}
	if sc.Setup.SQLFile != "" {
		fmt.Fprintf(w, "setup     %s\n", sc.Setup.SQLFile)
	}

	idWidth := 0
	for _, d := range res.Deliveries {
		idWidth = max(idWidth, utf8.RuneCountInString(d.EventID))
	}
	for i, d := range res.Deliveries {
		label := "events"
		if i > 0 {
			label = ""
		}
		status := "no response"
		if d.Status != 0 {
			status = fmt.Sprint(d.Status)
		}
		fmt.Fprintf(w, "%-9s %s  %s %s  → %s  (%s)\n", label, pad(d.EventID, idWidth), d.Method, d.Path, status, d.Duration.Round(time.Millisecond))
	}
	if n := len(sc.Effects); n > 0 {
		fmt.Fprintf(w, "note      %d effect assertion(s) not checked: effects are not supported in this version yet\n", n)
	}
	if res.Error != "" {
		fmt.Fprintf(w, "\nERROR  run could not be verified: %s\n", res.Error)
	}
	fmt.Fprintln(w)

	var pass, fail, errs int
	for _, inv := range res.Invariants {
		fmt.Fprintf(w, "%-6s %s\n", inv.Status, inv.Name)
		switch inv.Status {
		case invariant.Pass:
			pass++
		case invariant.Fail:
			fail++
			writeEvidence(w, inv.Evidence)
		case invariant.Error:
			errs++
			// When the whole run failed the reason is already printed above.
			if res.Error == "" {
				fmt.Fprintf(w, "       %s\n", inv.Reason)
			}
		}
	}
	fmt.Fprintf(w, "\n%d passed, %d failed, %d errors\n", pass, fail, errs)
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
