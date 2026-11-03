package scenario

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Error is a single problem found in a scenario file.
type Error struct {
	File  string
	Line  int    // 0 when unknown
	Field string // dotted path such as "events[1].after"; empty when not tied to a field
	Msg   string
}

func (e Error) Error() string {
	var b strings.Builder
	b.WriteString(e.File)
	if e.Line > 0 {
		fmt.Fprintf(&b, ":%d", e.Line)
	}
	b.WriteString(": ")
	if e.Field != "" {
		b.WriteString(e.Field)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

// Errors is every problem found in a scenario file, in file order.
type Errors []Error

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

// Load reads, parses and validates the scenario at path. Environment
// variables referenced as ${NAME} are expanded from the process environment.
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data, path, filepath.Dir(path), os.LookupEnv)
}

// Parse parses and validates scenario data. name is used in error messages,
// dir is where relative file references resolve, and lookupEnv resolves
// ${NAME} references. A returned error is always of type Errors.
func Parse(data []byte, name, dir string, lookupEnv func(string) (string, bool)) (*Scenario, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Errors{yamlError(name, err)}
	}
	if len(doc.Content) == 0 {
		return nil, Errors{{File: name, Msg: "file is empty"}}
	}

	sc := &Scenario{Path: name, Dir: dir}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(sc); err != nil {
		var te *yaml.TypeError
		if errors.As(err, &te) {
			errs := make(Errors, len(te.Errors))
			for i, msg := range te.Errors {
				errs[i] = yamlError(name, errors.New(msg))
			}
			return nil, errs
		}
		return nil, Errors{yamlError(name, err)}
	}

	v := &validator{file: name, root: doc.Content[0], lookupEnv: lookupEnv}
	v.validate(sc)
	if len(v.errs) > 0 {
		return nil, v.errs
	}
	return sc, nil
}

var (
	yamlLineRe    = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)
	unknownFldRe  = regexp.MustCompile(`^field (\S+) not found in type \S+$`)
	envRefRe      = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	eventIDRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	tableNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
	httpMethodSet = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true}
)

// yamlError turns a yaml.v3 message into an Error, pulling out the line
// number and rewording the decoder's Go-centric messages.
func yamlError(file string, err error) Error {
	msg := err.Error()
	e := Error{File: file, Msg: strings.TrimPrefix(msg, "yaml: ")}
	if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
		e.Line, _ = strconv.Atoi(m[1])
		e.Msg = m[2]
	}
	if m := unknownFldRe.FindStringSubmatch(e.Msg); m != nil {
		e.Msg = fmt.Sprintf("unknown field %q", m[1])
	}
	return e
}

type validator struct {
	file      string
	root      *yaml.Node
	lookupEnv func(string) (string, bool)
	errs      Errors
}

// fail records a problem with the field at path. Path elements are mapping
// keys (string) or sequence indexes (int).
func (v *validator) fail(path []any, format string, args ...any) {
	v.errs = append(v.errs, Error{
		File:  v.file,
		Line:  v.line(path),
		Field: fieldName(path),
		Msg:   fmt.Sprintf(format, args...),
	})
}

// has reports whether the field at path is present in the file.
func (v *validator) has(path ...any) bool {
	n, _ := v.lookup(path)
	return n != nil
}

// line returns the line of the field at path, or of its closest present
// ancestor when the field itself is missing.
func (v *validator) line(path []any) int {
	_, line := v.lookup(path)
	return line
}

// lookup follows path from the root. It returns the node (nil if missing)
// and the best line to report for it.
func (v *validator) lookup(path []any) (*yaml.Node, int) {
	n, line := v.root, v.root.Line
	for _, p := range path {
		switch key := p.(type) {
		case string:
			if n.Kind != yaml.MappingNode {
				return nil, line
			}
			var next *yaml.Node
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == key {
					line = n.Content[i].Line
					next = n.Content[i+1]
					break
				}
			}
			if next == nil {
				return nil, line
			}
			n = next
		case int:
			if n.Kind != yaml.SequenceNode || key >= len(n.Content) {
				return nil, line
			}
			n = n.Content[key]
			line = n.Line
		}
	}
	return n, line
}

func fieldName(path []any) string {
	var b strings.Builder
	for _, p := range path {
		switch key := p.(type) {
		case string:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(key)
		case int:
			fmt.Fprintf(&b, "[%d]", key)
		}
	}
	return b.String()
}

func p(elems ...any) []any { return elems }

// expand replaces ${NAME} references in s, failing the field at path when a
// variable is not set.
func (v *validator) expand(s string, path []any) string {
	return envRefRe.ReplaceAllStringFunc(s, func(ref string) string {
		name := envRefRe.FindStringSubmatch(ref)[1]
		val, ok := v.lookupEnv(name)
		if !ok {
			v.fail(path, "environment variable %s is not set", name)
		}
		return val
	})
}

// readFile loads a file referenced by the scenario, relative to its directory.
func (v *validator) readFile(dir, ref string, path []any) []byte {
	full := ref
	if !filepath.IsAbs(full) {
		full = filepath.Join(dir, ref)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			v.fail(path, "file %q not found (resolved to %s)", ref, full)
		} else {
			v.fail(path, "cannot read %q: %v", ref, err)
		}
		return nil
	}
	return data
}

func (v *validator) validate(sc *Scenario) {
	switch {
	case !v.has("version"):
		v.fail(p("version"), "is required (use version: 1)")
	case sc.Version != 1:
		v.fail(p("version"), "unsupported version %d (this build supports 1)", sc.Version)
	}
	if strings.TrimSpace(sc.Name) == "" {
		v.fail(p("name"), "is required")
	}

	v.validateTarget(sc)
	v.validateSetup(sc)
	v.validateEvents(sc)
	v.validatePerturb(sc)

	if v.has("runs") && sc.Runs < 1 {
		v.fail(p("runs"), "must be at least 1")
	}

	if len(sc.Invariants) == 0 {
		v.fail(p("invariants"), "at least one invariant is required")
	}
	seen := map[string]bool{}
	for i := range sc.Invariants {
		inv := &sc.Invariants[i]
		inv.Name = strings.TrimSpace(inv.Name)
		switch {
		case inv.Name == "":
			v.fail(p("invariants", i, "name"), "is required")
		case seen[inv.Name]:
			v.fail(p("invariants", i, "name"), "duplicate invariant name %q", inv.Name)
		}
		seen[inv.Name] = true
		if strings.TrimSpace(inv.SQL) == "" {
			v.fail(p("invariants", i, "sql"), "is required")
		}
	}

	for i, ef := range sc.Effects {
		if strings.TrimSpace(ef.Name) == "" {
			v.fail(p("effects", i, "name"), "is required")
		}
		if strings.TrimSpace(ef.Assert) == "" {
			v.fail(p("effects", i, "assert"), "is required")
		}
	}
}

func (v *validator) validateTarget(sc *Scenario) {
	t := &sc.Target
	t.BaseURL = v.expand(t.BaseURL, p("target", "base_url"))
	t.DatabaseURL = v.expand(t.DatabaseURL, p("target", "database_url"))

	if t.BaseURL == "" {
		v.fail(p("target", "base_url"), "is required")
	} else if !strings.HasPrefix(t.BaseURL, "http://") && !strings.HasPrefix(t.BaseURL, "https://") {
		v.fail(p("target", "base_url"), "must start with http:// or https:// (got %q)", t.BaseURL)
	}
	t.BaseURL = strings.TrimRight(t.BaseURL, "/")
	if t.DatabaseURL == "" {
		v.fail(p("target", "database_url"), "is required")
	}
}

func (v *validator) validateSetup(sc *Scenario) {
	s := &sc.Setup
	if s.SQLFile != "" {
		s.SQL = string(v.readFile(sc.Dir, s.SQLFile, p("setup", "sql")))
	}
	switch s.Reset {
	case "":
		s.Reset = ResetNone
	case ResetNone, ResetTruncate:
	default:
		v.fail(p("setup", "reset"), "must be %q or %q (got %q)", ResetTruncate, ResetNone, s.Reset)
	}
	switch {
	case s.Reset == ResetTruncate && len(s.ResetTables) == 0:
		v.fail(p("setup", "reset_tables"), "is required when reset is %q", ResetTruncate)
	case s.Reset != ResetTruncate && len(s.ResetTables) > 0:
		v.fail(p("setup", "reset"), "reset_tables is set but reset is %q; use reset: %s", s.Reset, ResetTruncate)
	}
	for i, t := range s.ResetTables {
		if !tableNameRe.MatchString(t) {
			v.fail(p("setup", "reset_tables", i), "%q is not a plain table name (letters, digits, _; optionally schema.table)", t)
		}
	}
}

func (v *validator) validateEvents(sc *Scenario) {
	if len(sc.Events) == 0 {
		v.fail(p("events"), "at least one event is required")
		return
	}
	index := map[string]int{}
	for i := range sc.Events {
		ev := &sc.Events[i]
		switch {
		case ev.ID == "":
			v.fail(p("events", i, "id"), "is required")
		case !eventIDRe.MatchString(ev.ID):
			v.fail(p("events", i, "id"), "%q may only contain letters, digits, '_', '-' and '.'", ev.ID)
		default:
			if _, dup := index[ev.ID]; dup {
				v.fail(p("events", i, "id"), "duplicate event id %q", ev.ID)
			} else {
				index[ev.ID] = i
			}
		}

		r := &ev.Request
		r.Method = strings.ToUpper(r.Method)
		switch {
		case r.Method == "":
			v.fail(p("events", i, "request", "method"), "is required")
		case !httpMethodSet[r.Method]:
			v.fail(p("events", i, "request", "method"), "unsupported HTTP method %q", r.Method)
		}
		r.Path = v.expand(r.Path, p("events", i, "request", "path"))
		if !strings.HasPrefix(r.Path, "/") {
			v.fail(p("events", i, "request", "path"), "must start with / (got %q)", r.Path)
		}
		for _, k := range slices.Sorted(maps.Keys(r.Headers)) {
			r.Headers[k] = v.expand(r.Headers[k], p("events", i, "request", "headers", k))
		}
		if r.BodyFile != "" {
			r.Body = v.readFile(sc.Dir, r.BodyFile, p("events", i, "request", "body_file"))
		}
		if v.has("events", i, "accept_status") && len(ev.AcceptStatus) == 0 {
			v.fail(p("events", i, "accept_status"), "must list at least one status code")
		}
		for j, code := range ev.AcceptStatus {
			if code < 100 || code > 599 {
				v.fail(p("events", i, "accept_status", j), "%d is not an HTTP status code", code)
			}
		}
	}

	for i, ev := range sc.Events {
		if ev.After == "" {
			continue
		}
		if ev.After == ev.ID {
			v.fail(p("events", i, "after"), "an event cannot come after itself")
		} else if _, ok := index[ev.After]; !ok {
			v.fail(p("events", i, "after"), "unknown event %q", ev.After)
		}
	}
	v.checkAfterCycles(sc, index)
}

// checkAfterCycles reports chains like a after b, b after a.
func (v *validator) checkAfterCycles(sc *Scenario, index map[string]int) {
	reported := map[int]bool{}
	for start := range sc.Events {
		seen := map[int]bool{start: true}
		chain := []string{sc.Events[start].ID}
		for cur := start; ; {
			next, ok := index[sc.Events[cur].After]
			if !ok || next == cur {
				break
			}
			chain = append(chain, sc.Events[next].ID)
			if seen[next] {
				if next == start && !reported[start] {
					for i := range seen {
						reported[i] = true
					}
					v.fail(p("events", start, "after"), "cycle in after: %s", strings.Join(chain, " -> "))
				}
				break
			}
			seen[next] = true
			cur = next
		}
	}
}

func (v *validator) validatePerturb(sc *Scenario) {
	pt := sc.Perturb
	if v.has("perturb", "duplicate") && pt.Duplicate < 1 {
		v.fail(p("perturb", "duplicate"), "must be at least 1")
	}
	if v.has("perturb", "concurrency") && pt.Concurrency < 1 {
		v.fail(p("perturb", "concurrency"), "must be at least 1")
	}
	if v.has("perturb", "delay_ms") {
		switch {
		case len(pt.DelayMS) != 2:
			v.fail(p("perturb", "delay_ms"), "must be [min, max] in milliseconds")
		case pt.DelayMS[0] < 0 || pt.DelayMS[1] < pt.DelayMS[0]:
			v.fail(p("perturb", "delay_ms"), "must satisfy 0 <= min <= max (got [%d, %d])", pt.DelayMS[0], pt.DelayMS[1])
		}
	}
}
