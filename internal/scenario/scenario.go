// Package scenario loads and validates stillright scenario files.
//
// A scenario describes the application under test (an HTTP base URL and a
// PostgreSQL database), the events to deliver to it, how to perturb those
// deliveries, and the invariants that must hold afterwards.
package scenario

import (
	"gopkg.in/yaml.v3"

	"github.com/stillright-dev/stillright/internal/effects"
)

// Database kinds, from the scheme of target.database_url.
const (
	Postgres = "postgres"
	MongoDB  = "mongodb"
)

// DefaultProxyListen is where the effects proxy listens when the scenario
// has effects but does not say where.
const DefaultProxyListen = "127.0.0.1:8899"

// Scenario is a parsed and validated scenario file. Relative file references
// (setup SQL, request bodies) are already resolved and loaded.
type Scenario struct {
	Path string `yaml:"-"` // path the scenario was loaded from
	Dir  string `yaml:"-"` // directory relative file references resolve against

	Version int     `yaml:"version"`
	Name    string  `yaml:"name"`
	Target  Target  `yaml:"target"`
	Setup   Setup   `yaml:"setup"`
	Events  []Event `yaml:"events"`
	Perturb Perturb `yaml:"perturb"`
	Runs    int     `yaml:"runs"`
	// SettleMS is how long to wait after the last delivery before checking,
	// for applications that finish work in the background.
	SettleMS   int         `yaml:"settle_ms"`
	Invariants []Invariant `yaml:"invariants"`
	Proxy      Proxy       `yaml:"proxy"`
	Effects    []Effect    `yaml:"effects"`
}

// UsesProxy reports whether the effects proxy should run.
func (sc *Scenario) UsesProxy() bool { return sc.Proxy.Listen != "" }

// Checks returns the names of everything verified after a run: invariants
// first, then effects.
func (sc *Scenario) Checks() []string {
	var names []string
	for _, inv := range sc.Invariants {
		names = append(names, inv.Name)
	}
	for _, ef := range sc.Effects {
		names = append(names, ef.Name)
	}
	return names
}

type Target struct {
	BaseURL     string `yaml:"base_url"`
	DatabaseURL string `yaml:"database_url"`
	// Database is Postgres or MongoDB, decided by DatabaseURL's scheme.
	Database string `yaml:"-"`
}

// Reset modes for Setup.Reset.
const (
	ResetNone     = "none"
	ResetTruncate = "truncate"
)

type Setup struct {
	SQLFile string `yaml:"sql"` // PostgreSQL: SQL statements
	SQL     string `yaml:"-"`   // contents of SQLFile
	// SeedFile is MongoDB's setup: a JSON object of collection: [documents].
	SeedFile         string   `yaml:"seed"`
	Seed             string   `yaml:"-"` // contents of SeedFile
	Reset            string   `yaml:"reset"`
	ResetTables      []string `yaml:"reset_tables"`      // PostgreSQL
	ResetCollections []string `yaml:"reset_collections"` // MongoDB
}

// ResetTargets is what reset empties: tables or collections.
func (s Setup) ResetTargets() []string {
	if len(s.ResetCollections) > 0 {
		return s.ResetCollections
	}
	return s.ResetTables
}

// Script returns the setup file and its contents, empty when there is none.
func (s Setup) Script() (file, content string) {
	if s.SeedFile != "" {
		return s.SeedFile, s.Seed
	}
	return s.SQLFile, s.SQL
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

// Invariant is a query that must return nothing: SQL for PostgreSQL, or a
// collection with an aggregation pipeline or a find filter for MongoDB.
type Invariant struct {
	Name       string    `yaml:"name"`
	SQL        string    `yaml:"sql"`
	Collection string    `yaml:"collection"`
	Pipeline   yaml.Node `yaml:"pipeline"`
	Filter     yaml.Node `yaml:"filter"`
	// PipelineJSON and FilterJSON are Pipeline and Filter as Extended JSON.
	PipelineJSON string `yaml:"-"`
	FilterJSON   string `yaml:"-"`
}

type Proxy struct {
	Listen string `yaml:"listen"`
	Stubs  []Stub `yaml:"stubs"`
}

// Stub is the response the proxy gives for one host. Body defaults to "{}".
type Stub struct {
	Host    string            `yaml:"host"`
	Status  int               `yaml:"status"`
	Body    string            `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
}

type Effect struct {
	Name      string             `yaml:"name"`
	Assert    string             `yaml:"assert"`
	Assertion *effects.Assertion `yaml:"-"`
}
