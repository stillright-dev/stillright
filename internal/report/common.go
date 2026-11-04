package report

import (
	"fmt"
	"strings"

	"github.com/stillright-dev/stillright/internal/invariant"
	"github.com/stillright-dev/stillright/internal/perturb"
	"github.com/stillright-dev/stillright/internal/runner"
)

// checkStatus is a check's overall result across runs. A check that failed
// in some run is FAIL even if it could not be checked in another; the
// exploration's exit code still reports the ERROR.
func checkStatus(st runner.InvariantStats) invariant.Status {
	switch {
	case st.Failed > 0:
		return invariant.Fail
	case st.Errored > 0:
		return invariant.Error
	default:
		return invariant.Pass
	}
}

// smallest is the sequence to show for a failed check: the shrunk one when
// shrinking reduced it, else the first failing sequence.
func smallest(st runner.InvariantStats) perturb.Sequence {
	if st.Shrunk != nil && st.Shrunk.Tried > 0 {
		return st.Shrunk.Sequence
	}
	return st.FirstSequence
}

// failureDetail returns the evidence and detail to show for a failed check,
// preferring a confirmed run of the shrunk sequence.
func failureDetail(st runner.InvariantStats) (invariant.Rows, string) {
	if sh := st.Shrunk; sh != nil && sh.Tried > 0 && sh.Reproduced > 0 {
		return sh.Evidence, sh.Detail
	}
	return st.FirstFailure.Evidence, st.FirstFailure.Detail
}

// reproduceCommands are the command lines that rerun a failed check.
func reproduceCommands(ex *runner.Exploration, st runner.InvariantStats) []string {
	sc := ex.Scenario
	var cmds []string
	if st.FirstSequence.Index >= 0 {
		cmds = append(cmds, fmt.Sprintf("stillright run %s --seed %d --replay %d", shellQuote(sc.Path), ex.Seed, st.FirstSequence.Index))
	}
	return append(cmds, fmt.Sprintf("stillright run %s --sequence '%s'", shellQuote(sc.Path), perturb.Format(sc, smallest(st))))
}

// warnings flags results that are probably vacuous: an assertion like
// count(host == "x") <= 1 passes trivially when the app's calls never reach
// the proxy.
func warnings(ex *runner.Exploration) []string {
	sc := ex.Scenario
	if !sc.UsesProxy() || ex.Executed == 0 {
		return nil
	}
	if ex.ProxyCalls == 0 {
		return []string{"the effects proxy recorded no outbound calls in any run. If the app should " +
			"make calls here, it is not using the proxy and effect results mean nothing; see docs/effects.md."}
	}
	seen := map[string]bool{}
	for _, h := range ex.ProxyHosts {
		seen[h] = true
	}
	var out []string
	for _, ef := range sc.Effects {
		if ef.Assertion == nil {
			continue
		}
		for _, h := range ef.Assertion.Hosts() {
			if !seen[h] {
				out = append(out, fmt.Sprintf("%q filters on host %q, which the proxy never saw (it saw: %s); see docs/effects.md.",
					ef.Name, h, strings.Join(ex.ProxyHosts, ", ")))
			}
		}
	}
	return out
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
