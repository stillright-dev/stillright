// Command stillright checks that an application's business invariants still
// hold when its events are delivered.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/stillright/stillright/internal/report"
	"github.com/stillright/stillright/internal/runner"
	"github.com/stillright/stillright/internal/scenario"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const usage = `stillright — still right when things go wrong.

Usage:
  stillright run [flags] <scenario.yaml>
  stillright version

Exit codes: 0 all invariants passed, 1 an invariant failed, 2 something could
not be verified (bad scenario, database or application unreachable...).
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "version", "--version":
		fmt.Fprintf(stdout, "stillright %s\n", version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "stillright: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	httpTimeout := fs.Duration("http-timeout", 10*time.Second, "timeout for each request to the application")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: stillright run [flags] <scenario.yaml>\n\nFlags:\n")
		fs.PrintDefaults()
	}

	// Accept flags before and after the scenario path.
	var paths []string
	for {
		if err := fs.Parse(args); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		paths = append(paths, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(paths) != 1 {
		fs.Usage()
		return 2
	}

	sc, err := scenario.Load(paths[0])
	if err != nil {
		fmt.Fprintf(stderr, "invalid scenario:\n%v\n", err)
		return 2
	}

	runner.UserAgent = "stillright/" + version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res := runner.Execute(ctx, sc, runner.Options{HTTPTimeout: *httpTimeout})
	report.Terminal(stdout, res)
	return res.ExitCode()
}
