// Package scenario loads and validates stillright scenario files.
//
// A scenario describes the application under test (an HTTP base URL and a
// PostgreSQL database), the events to deliver to it, how to perturb those
// deliveries, and the invariants that must hold afterwards.
package scenario

// Scenario is a parsed and validated scenario file. Relative file references
// (setup SQL, request bodies) are already resolved and loaded.
type Scenario struct {
	Path string `yaml:"-"` // path the scenario was loaded from
	Dir  string `yaml:"-"` // directory relative file references resolve against

	Version    int         `yaml:"version"`
	Name       string      `yaml:"name"`
	Target     Target      `yaml:"target"`
	Setup      Setup       `yaml:"setup"`
	Events     []Event     `yaml:"events"`
	Perturb    Perturb     `yaml:"perturb"`
	Runs       int         `yaml:"runs"`
	Invariants []Invariant `yaml:"invariants"`
	Effects    []Effect    `yaml:"effects"`
}

type Target struct {
	BaseURL     string `yaml:"base_url"`
	DatabaseURL string `yaml:"database_url"`
}

// Reset modes for Setup.Reset.
const (
	ResetNone     = "none"
	ResetTruncate = "truncate"
)

type Setup struct {
	SQLFile     string   `yaml:"sql"`
	SQL         string   `yaml:"-"` // contents of SQLFile
	Reset       string   `yaml:"reset"`
	ResetTables []string `yaml:"reset_tables"`
}

type Event struct {
	ID      string  `yaml:"id"`
	Request Request `yaml:"request"`
	After   string  `yaml:"after"`
	// AcceptStatus lists the response codes that count as a delivery the
	// application handled. When empty, anything below 500 is accepted.
	AcceptStatus []int `yaml:"accept_status"`
}

type Request struct {
	Method   string            `yaml:"method"`
	Path     string            `yaml:"path"`
	Headers  map[string]string `yaml:"headers"`
	BodyFile string            `yaml:"body_file"`
	Body     []byte            `yaml:"-"` // contents of BodyFile
}

type Perturb struct {
	Duplicate   int   `yaml:"duplicate"`
	Concurrency int   `yaml:"concurrency"`
	Reorder     bool  `yaml:"reorder"`
	DelayMS     []int `yaml:"delay_ms"`
}

type Invariant struct {
	Name string `yaml:"name"`
	SQL  string `yaml:"sql"`
}

type Effect struct {
	Name   string `yaml:"name"`
	Assert string `yaml:"assert"`
}
