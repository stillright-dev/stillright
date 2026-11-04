package scenario

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

var testEnv = env(map[string]string{"DATABASE_URL": "postgres://localhost/test", "HOOK_TOKEN": "s3cret"})

func TestLoadValid(t *testing.T) {
	data, err := os.ReadFile("testdata/valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := Parse(data, "testdata/valid.yaml", "testdata", testEnv)
	if err != nil {
		t.Fatalf("unexpected error:\n%v", err)
	}

	if sc.Name != "payment-confirmed" || sc.Runs != 200 {
		t.Errorf("name/runs = %q/%d", sc.Name, sc.Runs)
	}
	if sc.Target.BaseURL != "http://localhost:3000" {
		t.Errorf("base_url should lose its trailing slash, got %q", sc.Target.BaseURL)
	}
	if sc.Target.DatabaseURL != "postgres://localhost/test" {
		t.Errorf("database_url not expanded: %q", sc.Target.DatabaseURL)
	}
	if !strings.Contains(sc.Setup.SQL, "INSERT INTO customers") {
		t.Errorf("setup SQL not loaded: %q", sc.Setup.SQL)
	}
	if got := strings.Join(sc.Setup.ResetTables, ","); got != "subscriptions,payments,public.emails" {
		t.Errorf("reset_tables = %s", got)
	}
	if len(sc.Events) != 2 {
		t.Fatalf("events = %d", len(sc.Events))
	}
	paid := sc.Events[0].Request
	if paid.Method != "POST" {
		t.Errorf("method should be upper-cased, got %q", paid.Method)
	}
	if paid.Headers["X-Token"] != "s3cret" {
		t.Errorf("header not expanded: %q", paid.Headers["X-Token"])
	}
	if !strings.Contains(string(paid.Body), "payment.received") {
		t.Errorf("body not loaded: %q", paid.Body)
	}
	if got := sc.Events[1].AcceptStatus; len(got) != 3 || got[2] != 500 {
		t.Errorf("accept_status = %v", got)
	}
	if sc.Events[1].After != "paid" {
		t.Errorf("after = %q", sc.Events[1].After)
	}
	if p := sc.Perturb; p.Duplicate != 3 || p.Concurrency != 5 || !p.Reorder || len(p.DelayMS) != 2 || p.DelayMS[1] != 250 {
		t.Errorf("perturb = %+v", p)
	}
	if len(sc.Invariants) != 2 || len(sc.Effects) != 1 {
		t.Errorf("invariants/effects = %d/%d", len(sc.Invariants), len(sc.Effects))
	}
}

func TestProxyDefaults(t *testing.T) {
	if sc := mustParse(t, minimal); sc.UsesProxy() {
		t.Error("no effects and no proxy section: proxy must not run")
	}
	sc := mustParse(t, minimal+"effects:\n  - name: one email\n    assert: count(path == \"/send\") <= 1\n")
	if sc.Proxy.Listen != DefaultProxyListen || sc.Effects[0].Assertion == nil {
		t.Errorf("listen = %q, assertion = %v", sc.Proxy.Listen, sc.Effects[0].Assertion)
	}
	if got := sc.Checks(); len(got) != 2 || got[1] != "one email" {
		t.Errorf("checks = %v", got)
	}
	sc = mustParse(t, minimal+"proxy:\n  listen: :9000\n  stubs:\n    - host: API.Email.local\n      status: 202\n")
	st := sc.Proxy.Stubs[0]
	if sc.Proxy.Listen != "127.0.0.1:9000" || st.Host != "api.email.local" || st.Status != 202 || st.Body != "{}" {
		t.Errorf("proxy = %+v", sc.Proxy)
	}
	sc = mustParse(t, minimal+"proxy:\n  stubs:\n    - host: x\n      body: \"\"\n")
	if st := sc.Proxy.Stubs[0]; st.Status != 200 || st.Body != "" {
		t.Errorf("explicit empty body must stay empty: %+v", st)
	}
}

func TestResetDefaultsToNone(t *testing.T) {
	sc := mustParse(t, minimal)
	if sc.Setup.Reset != ResetNone {
		t.Errorf("reset = %q, want %q", sc.Setup.Reset, ResetNone)
	}
}

const minimal = `version: 1
name: minimal
target:
  base_url: http://localhost:3000
  database_url: postgres://localhost/test
events:
  - id: a
    request: { method: POST, path: /a }
invariants:
  - name: nothing
    sql: SELECT 1 WHERE false
`

func mustParse(t *testing.T, src string) *Scenario {
	t.Helper()
	sc, err := Parse([]byte(src), "s.yaml", t.TempDir(), testEnv)
	if err != nil {
		t.Fatalf("unexpected error:\n%v", err)
	}
	return sc
}

// TestErrors checks that each mistake is reported with the right line and field.
func TestErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string // each must appear in the error output
	}{
		{
			name: "yaml syntax",
			src:  "version: 1\nname: [unclosed\n",
			want: []string{"s.yaml:"},
		},
		{
			name: "empty file",
			src:  "",
			want: []string{"s.yaml: file is empty"},
		},
		{
			name: "unknown field",
			src:  strings.Replace(minimal, "    request:", "    reqest: {}\n    request:", 1),
			want: []string{`s.yaml:8: unknown field "reqest"`},
		},
		{
			name: "wrong type",
			src:  strings.Replace(minimal, "version: 1", "version: one", 1),
			want: []string{"s.yaml:1:", "cannot unmarshal"},
		},
		{
			name: "missing version",
			src:  strings.Replace(minimal, "version: 1\n", "", 1),
			want: []string{"version: is required"},
		},
		{
			name: "unsupported version",
			src:  strings.Replace(minimal, "version: 1", "version: 2", 1),
			want: []string{"s.yaml:1: version: unsupported version 2"},
		},
		{
			name: "unset env var",
			src:  strings.Replace(minimal, "postgres://localhost/test", "${NOPE}", 1),
			want: []string{"s.yaml:5: target.database_url: environment variable NOPE is not set"},
		},
		{
			name: "bad base_url",
			src:  strings.Replace(minimal, "http://localhost:3000", "localhost:3000", 1),
			want: []string{"s.yaml:4: target.base_url: must start with http://"},
		},
		{
			name: "bad method and path",
			src:  strings.Replace(minimal, "{ method: POST, path: /a }", "{ method: FETCH, path: a }", 1),
			want: []string{
				`s.yaml:8: events[0].request.method: unsupported HTTP method "FETCH"`,
				`s.yaml:8: events[0].request.path: must start with /`,
			},
		},
		{
			name: "missing body file",
			src:  strings.Replace(minimal, "{ method: POST, path: /a }", "{ method: POST, path: /a, body_file: nope.json }", 1),
			want: []string{`events[0].request.body_file: file "nope.json" not found`},
		},
		{
			name: "bad accept_status",
			src:  strings.Replace(minimal, "    request: { method: POST, path: /a }\n", "    request: { method: POST, path: /a }\n    accept_status: [200, 99]\n", 1),
			want: []string{"s.yaml:9: events[0].accept_status[1]: 99 is not an HTTP status code"},
		},
		{
			name: "empty accept_status",
			src:  strings.Replace(minimal, "    request: { method: POST, path: /a }\n", "    request: { method: POST, path: /a }\n    accept_status: []\n", 1),
			want: []string{"s.yaml:9: events[0].accept_status: must list at least one status code"},
		},
		{
			name: "duplicate id and unknown after",
			src: strings.Replace(minimal, "invariants:", `  - id: a
    request: { method: POST, path: /a }
  - id: b
    request: { method: POST, path: /b }
    after: c
invariants:`, 1),
			want: []string{
				`s.yaml:9: events[1].id: duplicate event id "a"`,
				`s.yaml:13: events[2].after: unknown event "c"`,
			},
		},
		{
			name: "after cycle",
			src: strings.Replace(minimal, "invariants:", `    after: b
  - id: b
    request: { method: POST, path: /b }
    after: a
invariants:`, 1),
			want: []string{"cycle in after: a -> b -> a"},
		},
		{
			name: "truncate without tables",
			src:  strings.Replace(minimal, "events:", "setup:\n  reset: truncate\nevents:", 1),
			want: []string{"setup.reset_tables: is required when reset is \"truncate\""},
		},
		{
			name: "tables without truncate",
			src:  strings.Replace(minimal, "events:", "setup:\n  reset_tables: [a]\nevents:", 1),
			want: []string{`setup.reset: reset_tables is set but reset is "none"`},
		},
		{
			name: "unsafe table name",
			src:  strings.Replace(minimal, "events:", "setup:\n  reset: truncate\n  reset_tables: [ok, \"x; DROP TABLE y\"]\nevents:", 1),
			want: []string{`s.yaml:8: setup.reset_tables[1]: "x; DROP TABLE y" is not a plain table name`},
		},
		{
			name: "bad perturb and runs",
			src:  minimal + "perturb:\n  duplicate: 0\n  delay_ms: [300, 100]\nruns: 0\n",
			want: []string{
				"s.yaml:13: perturb.duplicate: must be at least 1",
				"s.yaml:14: perturb.delay_ms: must satisfy 0 <= min <= max",
				"s.yaml:15: runs: must be at least 1",
			},
		},
		{
			name: "invariant problems",
			src: strings.Replace(minimal, "invariants:\n", `invariants:
  - name: nothing
    sql: SELECT 2
  - name: ""
    sql: ""
`, 1),
			want: []string{
				"s.yaml:12: invariants[1].name: is required",
				"s.yaml:13: invariants[1].sql: is required",
				`s.yaml:14: invariants[2].name: duplicate invariant name "nothing"`,
			},
		},
		{
			name: "bad effect assertion",
			src:  minimal + "effects:\n  - name: one email\n    assert: count(hots == \"x\") <= 1\n",
			want: []string{`s.yaml:14: effects[0].assert: column 7: unknown field "hots"`},
		},
		{
			name: "effect name clashes with invariant",
			src:  minimal + "effects:\n  - name: nothing\n    assert: count() == 0\n",
			want: []string{`s.yaml:13: effects[0].name: duplicate name "nothing"`},
		},
		{
			name: "bad proxy",
			src:  minimal + "proxy:\n  listen: 8899\n  stubs:\n    - host: a.local\n    - host: A.local\n      status: 700\n    - status: 200\n",
			want: []string{
				`s.yaml:13: proxy.listen: must be host:port`,
				`s.yaml:16: proxy.stubs[1].host: duplicate stub for "a.local"`,
				`s.yaml:17: proxy.stubs[1].status: 700 is not an HTTP status code`,
				`proxy.stubs[2].host: is required`,
			},
		},
		{
			name: "negative settle",
			src:  minimal + "settle_ms: -1\n",
			want: []string{"s.yaml:12: settle_ms: must be 0 or more"},
		},
		{
			name: "no events or invariants",
			src:  "version: 1\nname: x\ntarget: { base_url: http://x, database_url: postgres://x }\n",
			want: []string{"events: at least one event is required", "invariants: at least one invariant (or effect) is required"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src), "s.yaml", t.TempDir(), testEnv)
			if err == nil {
				t.Fatal("expected an error")
			}
			var errs Errors
			if !errors.As(err, &errs) {
				t.Fatalf("error is %T, want Errors", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("missing %q in:\n%v", w, err)
				}
			}
		})
	}
}

func TestLoadResolvesRelativeToScenario(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "fx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fx", "body.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := strings.Replace(minimal, "{ method: POST, path: /a }", "{ method: POST, path: /a, body_file: fx/body.json }", 1)
	path := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(sc.Events[0].Request.Body) != `{"ok":true}` {
		t.Errorf("body = %q", sc.Events[0].Request.Body)
	}
}
