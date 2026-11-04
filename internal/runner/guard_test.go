package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/stillright-dev/stillright/internal/scenario"
)

func guardScenario(url string) *scenario.Scenario {
	return &scenario.Scenario{
		Target: scenario.Target{DatabaseURL: url},
		Setup:  scenario.Setup{Reset: scenario.ResetTruncate, ResetTables: []string{"pagamentos", "assinaturas"}},
	}
}

func TestCheckDisposable(t *testing.T) {
	noEnv := func(string) string { return "" }
	for url, ok := range map[string]bool{
		"postgres://localhost/app":                         true,
		"postgres://127.0.0.1:5433/app":                    true,
		"postgres://[::1]/app":                             true,
		"postgres:///app?host=/tmp":                        true,
		"host=/var/run/postgresql dbname=app":              true,
		"postgres://postgres:pw@postgres:5432/app":         true, // CI / Compose service name
		"postgres://u:p@db.example.com/app_test":           true,
		"postgres://u:p@db.example.com/test_app":           true,
		"postgres://u:p@db.example.com/app-e2e":            true,
		"postgres://u:p@db.example.com/ci":                 true,
		"postgres://u:p@db.example.com/app":                false,
		"postgres://u:p@db.abcd.supabase.co:5432/postgres": false,
		"postgres://u:p@10.0.0.5/app":                      false, // private but not this machine
		"postgres://u:p@db.example.com/contest":            false, // "test" inside a word is not a marker
		"postgres://u:p@db.example.com/latest":             false,
	} {
		err := checkDisposable(guardScenario(url), noEnv)
		if (err == nil) != ok {
			t.Errorf("%s: err = %v, want allowed=%v", url, err, ok)
		}
	}
}

func TestCheckDisposableAllowList(t *testing.T) {
	sc := guardScenario("postgres://u:secret@db.abcd.supabase.co:5432/postgres")
	err := checkDisposable(sc, func(string) string { return "" })
	if err == nil {
		t.Fatal("remote database must be refused")
	}
	msg := err.Error()
	for _, w := range []string{`database "postgres" on db.abcd.supabase.co`, "empties pagamentos, assinaturas", "STILLRIGHT_ALLOW_DATABASE=db.abcd.supabase.co/postgres"} {
		if !strings.Contains(msg, w) {
			t.Errorf("message missing %q: %s", w, msg)
		}
	}
	if strings.Contains(msg, "secret") {
		t.Error("the message must not leak the password")
	}

	env := func(v string) func(string) string { return func(string) string { return v } }
	if err := checkDisposable(sc, env("db.abcd.supabase.co/postgres")); err != nil {
		t.Errorf("explicitly allowed database refused: %v", err)
	}
	if err := checkDisposable(sc, env("postgres")); err == nil {
		t.Error("allowing by name alone must not be enough")
	}
	if err := checkDisposable(sc, env("db.other.supabase.co/postgres")); err == nil {
		t.Error("allowing another host must not cover this one")
	}
}

func TestCheckDisposableOnlyWhenWriting(t *testing.T) {
	sc := guardScenario("postgres://u:p@db.example.com/app")
	sc.Setup = scenario.Setup{Reset: scenario.ResetNone}
	if err := checkDisposable(sc, func(string) string { return "" }); err != nil {
		t.Errorf("read-only scenario refused: %v", err)
	}
	sc.Setup.SQL = "INSERT INTO x VALUES (1)"
	if err := checkDisposable(sc, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "runs its setup script") {
		t.Errorf("setup SQL must be guarded too: %v", err)
	}
}

// The guard runs before connecting, so a production database is never touched.
func TestExploreRefusesBeforeConnecting(t *testing.T) {
	sc := guardScenario("postgres://u:p@db.example.invalid:1/app?connect_timeout=1")
	sc.Invariants = []scenario.Invariant{{Name: "x", SQL: "SELECT 1"}}
	ex := Explore(context.Background(), sc, Options{})
	if !strings.HasPrefix(ex.Error, "refusing to modify database") || ex.ExitCode() != 2 {
		t.Errorf("error = %q", ex.Error)
	}
}
