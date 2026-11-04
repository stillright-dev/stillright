package report

import (
	"fmt"
	"io"
	"strings"
)

// Markdown writes a short summary of rep, meant for a CI job summary such as
// GitHub's $GITHUB_STEP_SUMMARY. Several summaries can be appended to the
// same file.
func Markdown(w io.Writer, rep *Report) error {
	var b strings.Builder
	name := rep.Scenario.Name
	if name == "" {
		name = rep.Scenario.Path
	}
	fmt.Fprintf(&b, "### %s stillright: %s — %s\n\n", icon(rep.Status), mdText(name), rep.Status)
	if rep.Runs.Planned > 0 {
		fmt.Fprintf(&b, "`%s` · seed `%s` · %d of %d runs: %d passed, %d failed, %d could not be verified\n\n",
			mdCode(rep.Scenario.Path), rep.Seed, rep.Runs.Executed, rep.Runs.Planned, rep.Runs.Passed, rep.Runs.Failed, rep.Runs.Errored)
	} else {
		fmt.Fprintf(&b, "`%s`\n\n", mdCode(rep.Scenario.Path))
	}
	if rep.Error != "" {
		fmt.Fprintf(&b, "**Could not run:** %s\n\n", mdText(rep.Error))
	}

	if len(rep.Checks) > 0 {
		b.WriteString("| | Check | Result |\n|---|---|---|\n")
		for _, c := range rep.Checks {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", icon(c.Status), mdText(c.Name), mdText(checkSummary(c, rep.Runs.Executed)))
		}
		b.WriteString("\n")
	}

	var cmds []string
	for _, c := range rep.Checks {
		cmds = append(cmds, c.Reproduce...)
	}
	if len(cmds) > 0 {
		b.WriteString("<details><summary>Reproduce locally</summary>\n\n```sh\n")
		b.WriteString(strings.Join(cmds, "\n"))
		b.WriteString("\n```\n\n</details>\n\n")
	}
	for _, msg := range rep.Warnings {
		fmt.Fprintf(&b, "> [!WARNING]\n> %s\n\n", mdText(msg))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// checkSummary is one line describing a check's outcome.
func checkSummary(c Check, executed int) string {
	switch c.Status {
	case "PASS":
		return "passed"
	case "ERROR":
		return c.Error
	}
	s := fmt.Sprintf("failed in %d of %d runs", c.FailedRuns, executed)
	detail := ""
	if c.FirstFailure != nil {
		detail = c.FirstFailure.Detail
	}
	if sh := c.Shrunk; sh != nil {
		if sh.Evidence != nil {
			detail = sh.Detail
		}
		if sh.Sequence != nil {
			s += fmt.Sprintf(" · smallest %s (reproduced %d/%d)", sh.Sequence.Text, sh.Reproduced, sh.Attempts)
		} else {
			s += " · not reproducible on retry"
		}
	} else if c.FirstFailure != nil {
		s += " · " + c.FirstFailure.Sequence.Text
	}
	if detail != "" {
		s += " · " + detail
	}
	return s
}

func icon(status string) string {
	switch status {
	case "PASS":
		return "✅"
	case "FAIL":
		return "❌"
	default:
		return "⚠️"
	}
}

// mdText escapes text for a Markdown table cell or paragraph.
func mdText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := strings.NewReplacer(`\`, `\\`, "|", `\|`, "<", "&lt;", ">", "&gt;", "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`)
	return r.Replace(s)
}

func mdCode(s string) string { return strings.ReplaceAll(s, "`", "'") }
