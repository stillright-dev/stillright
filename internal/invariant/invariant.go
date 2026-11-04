// Package invariant checks scenario invariants. An invariant is a SQL query
// that must return zero rows; any rows it returns are the evidence that the
// invariant is broken.
package invariant

import (
	"context"
	"fmt"

	"github.com/stillright-dev/stillright/internal/scenario"
)

// MaxEvidenceRows is how many violating rows are kept per failed invariant.
const MaxEvidenceRows = 10

// Querier runs a read-only query and returns up to limit rows of it, along
// with the total row count.
type Querier interface {
	QueryRows(ctx context.Context, sql string, limit int) (Rows, error)
}

// Rows is the (possibly truncated) result of an invariant query. Values are
// PostgreSQL's text rendering of each column; nil means NULL.
type Rows struct {
	Columns []string
	Values  [][]*string
	Total   int
}

type Status string

const (
	Pass  Status = "PASS"
	Fail  Status = "FAIL"
	Error Status = "ERROR"
)

// Result is the outcome of one check: an invariant, or an effect assertion
// (whose evidence rows are the matching outbound calls).
type Result struct {
	Name     string
	Status   Status
	Evidence Rows   // rows returned, when Status is Fail
	Detail   string // what was expected, for effects: "2 matching calls, expected <= 1"
	Reason   string // why it could not be checked, when Status is Error
}

// Check runs a single invariant.
func Check(ctx context.Context, q Querier, inv scenario.Invariant) Result {
	rows, err := q.QueryRows(ctx, inv.SQL, MaxEvidenceRows)
	if err != nil {
		return Result{Name: inv.Name, Status: Error, Reason: fmt.Sprintf("query failed: %v", err)}
	}
	if rows.Total == 0 {
		return Result{Name: inv.Name, Status: Pass}
	}
	return Result{Name: inv.Name, Status: Fail, Evidence: rows}
}

// NotChecked is the result for a check that could not run because an
// earlier step of the run failed.
func NotChecked(name, reason string) Result {
	return Result{Name: name, Status: Error, Reason: "not checked: " + reason}
}
