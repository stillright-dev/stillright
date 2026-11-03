// Command stillright checks that an application's business invariants still
// hold when its events are delivered.
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/stillright/stillright/internal/perturb"
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
	seed := fs.Uint64("seed", 0, "seed for generating sequences (default: random, always printed)")
	runs := fs.Int("runs", 0, "number of sequences to run (default: the scenario's runs)")
	keepGoing := fs.Bool("keep-going", false, "keep running after a sequence fails and count failures")
	replay := fs.Int("replay", -1, "run only sequence `N` of --seed")
	sequence := fs.String("sequence", "", "run only this sequence, as printed in reports, e.g. '[paid | paid] -> [refunded]'")
	attempts := fs.Int("attempts", 5, "times to retry a sequence before concluding it passes (replay and shrinking)")
	noShrink := fs.Bool("no-shrink", false, "report failures as found, without reducing them")
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
	if *runs < 0 {
		fmt.Fprintln(stderr, "stillright: --runs must be at least 1")
		return 2
	}
	if *attempts < 1 {
		fmt.Fprintln(stderr, "stillright: --attempts must be at least 1")
		return 2
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["replay"] && set["sequence"] {
		fmt.Fprintln(stderr, "stillright: use either --replay or --sequence, not both")
		return 2
	}
	if set["replay"] && (*replay < 0 || !set["seed"]) {
		fmt.Fprintln(stderr, "stillright: --replay N needs N >= 0 and the --seed that generated it")
		return 2
	}

	sc, err := scenario.Load(paths[0])
	if err != nil {
		fmt.Fprintf(stderr, "invalid scenario:\n%v\n", err)
		return 2
	}

	if !set["seed"] {
		*seed = randomSeed()
	}

	opts := runner.Options{
		HTTPTimeout: *httpTimeout,
		Seed:        *seed,
		Runs:        *runs,
		KeepGoing:   *keepGoing,
		Attempts:    *attempts,
		Shrink:      !*noShrink,
	}
	switch {
	case set["replay"]:
		seq := perturb.Generate(sc, *seed, *replay)
		opts.Only = &seq
	case set["sequence"]:
		seq, err := perturb.Parse(sc, *sequence)
		if err != nil {
			fmt.Fprintf(stderr, "stillright: --sequence: %v\n", err)
			return 2
		}
		opts.Only = &seq
	}
	if isTerminal(stderr) {
		opts.Progress = func(done, planned int) {
			fmt.Fprintf(stderr, "\r\033[Krun %d/%d", done, planned)
		}
		opts.ShrinkProgress = func(name string, tried, budget int) {
			fmt.Fprintf(stderr, "\r\033[Kshrinking %q: candidate %d (max %d)", name, tried, budget)
		}
	}

	runner.UserAgent = "stillright/" + version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ex := runner.Explore(ctx, sc, opts)
	if opts.Progress != nil {
		fmt.Fprint(stderr, "\r\033[K")
	}
	report.Terminal(stdout, ex)
	return ex.ExitCode()
}

// randomSeed is short enough to type back on a command line.
func randomSeed() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano()) % 1_000_000
	}
	return binary.LittleEndian.Uint64(b[:]) % 1_000_000
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
