package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stillright/stillright/internal/invariant"
	"github.com/stillright/stillright/internal/scenario"
)

// These tests need a disposable PostgreSQL database; they create and truncate
// sr_it_* tables in it.
//
//	STILLRIGHT_TEST_DATABASE_URL=postgres://localhost/stillright_test go test ./...
func testDatabaseURL(t *testing.T) string {
	url := os.Getenv("STILLRIGHT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("STILLRIGHT_TEST_DATABASE_URL not set")
	}
	return url
}

// buggyApp mimics a webhook handler without idempotency: it checks for an
// active subscription, then inserts, so a second "paid" for the same customer
// creates a second active subscription.
func buggyApp(t *testing.T, db *pgx.Conn) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev struct {
			Type       string `json:"type"`
			CustomerID int    `json:"customer_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		ctx := r.Context()
		var err error
		switch ev.Type {
		case "paid":
			_, err = db.Exec(ctx, `INSERT INTO sr_it_subscriptions (customer_id, status) VALUES ($1, 'active')`, ev.CustomerID)
		case "crash":
			http.Error(w, "unhandled exception", 500)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(200)
	}))
}

func TestIntegrationPostgres(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	db, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	_, err = db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS sr_it_customers (id int PRIMARY KEY, name text);
		CREATE TABLE IF NOT EXISTS sr_it_subscriptions (id serial PRIMARY KEY, customer_id int, status text, note text);
		TRUNCATE sr_it_customers, sr_it_subscriptions RESTART IDENTITY;
		INSERT INTO sr_it_subscriptions (customer_id, status) VALUES (99, 'stale');`)
	if err != nil {
		t.Fatal(err)
	}

	app := buggyApp(t, db)
	defer app.Close()

	paid := []byte(`{"type":"paid","customer_id":42}`)
	sc := &scenario.Scenario{
		Name:   "integration",
		Target: scenario.Target{BaseURL: app.URL, DatabaseURL: url},
		Setup: scenario.Setup{
			Reset:       scenario.ResetTruncate,
			ResetTables: []string{"sr_it_customers", "public.sr_it_subscriptions"},
			SQLFile:     "seed.sql",
			SQL:         "INSERT INTO sr_it_customers VALUES (42, 'Ana');\nINSERT INTO sr_it_customers VALUES (43, NULL);",
		},
		Events: []scenario.Event{
			{ID: "paid", Request: scenario.Request{Method: "POST", Path: "/hook", Body: paid, BodyFile: "paid.json"}},
			{ID: "paid-again", Request: scenario.Request{Method: "POST", Path: "/hook", Body: paid, BodyFile: "paid.json"}},
		},
		Invariants: []scenario.Invariant{
			{Name: "one active subscription per customer", SQL: `
				SELECT customer_id, count(*) AS n, max(note) AS note, now() > '2000-01-01' AS recent
				FROM sr_it_subscriptions WHERE status = 'active' GROUP BY 1 HAVING count(*) > 1`},
			{Name: "reset removed stale rows", SQL: `SELECT id FROM sr_it_subscriptions WHERE status = 'stale';`},
			{Name: "ids restart after reset", SQL: `SELECT id FROM sr_it_subscriptions WHERE id > 2`},
			{Name: "seed ran", SQL: `SELECT 1 WHERE (SELECT count(*) FROM sr_it_customers) <> 2`},
			{Name: "many rows are truncated", SQL: `SELECT g FROM generate_series(1, 25) g`},
			{Name: "invariants are read-only", SQL: `DELETE FROM sr_it_customers RETURNING id`},
			{Name: "bad sql", SQL: `SELECT nope FROM sr_it_customers`},
		},
	}

	res := Execute(ctx, sc, Options{HTTPTimeout: 5 * time.Second})
	if res.Error != "" {
		t.Fatalf("run error: %s", res.Error)
	}
	want := []invariant.Status{invariant.Fail, invariant.Pass, invariant.Pass, invariant.Pass, invariant.Fail, invariant.Error, invariant.Error}
	for i, inv := range res.Invariants {
		if inv.Status != want[i] {
			t.Errorf("%s: status %s, want %s (%s)", inv.Name, inv.Status, want[i], inv.Reason)
		}
	}

	dup := res.Invariants[0].Evidence
	if strings.Join(dup.Columns, ",") != "customer_id,n,note,recent" || dup.Total != 1 {
		t.Fatalf("evidence = %+v", dup)
	}
	row := dup.Values[0]
	if *row[0] != "42" || *row[1] != "2" || row[2] != nil || *row[3] != "t" {
		t.Errorf("row should be PostgreSQL text with NULL as nil, got %v %v %v %v", *row[0], *row[1], row[2], *row[3])
	}

	many := res.Invariants[4].Evidence
	if many.Total != 25 || len(many.Values) != invariant.MaxEvidenceRows {
		t.Errorf("total=%d kept=%d", many.Total, len(many.Values))
	}
	if !strings.Contains(res.Invariants[5].Reason, "read-only") {
		t.Errorf("DELETE should be rejected by the read-only transaction: %s", res.Invariants[5].Reason)
	}
	var customers int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM sr_it_customers`).Scan(&customers); err != nil || customers != 2 {
		t.Errorf("customers = %d, %v (invariant must not delete)", customers, err)
	}
	if res.ExitCode() != 2 {
		t.Errorf("exit = %d", res.ExitCode())
	}

	t.Run("app 500 is an error", func(t *testing.T) {
		crash := *sc
		crash.Events = []scenario.Event{{ID: "crash", Request: scenario.Request{Method: "POST", Path: "/hook", Body: []byte(`{"type":"crash"}`), BodyFile: "c.json"}}}
		res := Execute(ctx, &crash, Options{HTTPTimeout: 5 * time.Second})
		if !strings.Contains(res.Error, `event "crash": application answered 500: unhandled exception`) {
			t.Errorf("error = %q", res.Error)
		}
	})

	t.Run("unreachable database is an error", func(t *testing.T) {
		bad := *sc
		bad.Target.DatabaseURL = "postgres://localhost:1/nope?connect_timeout=2"
		res := Execute(ctx, &bad, Options{})
		if !strings.HasPrefix(res.Error, "cannot connect to database") || res.ExitCode() != 2 {
			t.Errorf("error = %q exit=%d", res.Error, res.ExitCode())
		}
	})

	t.Run("unreachable app is an error", func(t *testing.T) {
		down := *sc
		down.Target.BaseURL = "http://127.0.0.1:1"
		res := Execute(ctx, &down, Options{HTTPTimeout: time.Second})
		if !strings.Contains(res.Error, "no response from http://127.0.0.1:1") {
			t.Errorf("error = %q", res.Error)
		}
	})
}
