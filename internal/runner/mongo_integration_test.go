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

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"gopkg.in/yaml.v3"

	"github.com/stillright-dev/stillright/internal/mongoquery"
	"github.com/stillright-dev/stillright/internal/perturb"
	"github.com/stillright-dev/stillright/internal/scenario"
)

// These tests need a disposable MongoDB; they use the database named in the
// URI and its sr_it_* collections.
//
//	STILLRIGHT_TEST_MONGODB_URI=mongodb://localhost:27017/stillright_test go test ./...
func testMongoURI(t *testing.T) string {
	uri := os.Getenv("STILLRIGHT_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("STILLRIGHT_TEST_MONGODB_URI not set")
	}
	return uri
}

// mongoInvariant builds an invariant the way the scenario loader does.
func mongoInvariant(t *testing.T, name, collection, field, src string) scenario.Invariant {
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(src), &n); err != nil {
		t.Fatal(err)
	}
	js, err := mongoquery.JSON(&n)
	if err != nil {
		t.Fatal(err)
	}
	inv := scenario.Invariant{Name: name, Collection: collection}
	if field == "pipeline" {
		inv.PipelineJSON = js
	} else {
		inv.FilterJSON = js
	}
	return inv
}

func TestIntegrationMongo(t *testing.T) {
	uri := testMongoURI(t)
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	u, _ := mongoquery.ParseURI(uri)
	db := client.Database(u.Database)
	subs := db.Collection("sr_it_assinaturas")
	db.Collection("sr_it_clientes").Drop(ctx)
	subs.Drop(ctx)
	// An index the app relies on; reset must keep it.
	if _, err := subs.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "marca", Value: 1}}}); err != nil {
		t.Fatal(err)
	}
	subs.InsertOne(ctx, bson.D{{Key: "cliente_id", Value: 99}, {Key: "status", Value: "velha"}})

	// The bug: check for an active subscription, then insert one.
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev struct {
			ClienteID int `json:"cliente_id"`
		}
		json.NewDecoder(r.Body).Decode(&ev)
		ctx := r.Context()
		n, err := subs.CountDocuments(ctx, bson.D{{Key: "cliente_id", Value: ev.ClienteID}, {Key: "status", Value: "ativa"}})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		time.Sleep(20 * time.Millisecond)
		if n == 0 {
			subs.InsertOne(ctx, bson.D{{Key: "cliente_id", Value: ev.ClienteID}, {Key: "status", Value: "ativa"}})
		}
		w.WriteHeader(200)
	}))
	defer app.Close()

	sc := &scenario.Scenario{
		Name:   "mongo",
		Target: scenario.Target{BaseURL: app.URL, DatabaseURL: uri, Database: scenario.MongoDB},
		Setup: scenario.Setup{
			Reset: scenario.ResetTruncate, ResetCollections: []string{"sr_it_clientes", "sr_it_assinaturas"},
			SeedFile: "seed.json",
			Seed:     `{"sr_it_clientes": [{"_id": 42, "nome": "Ana", "ref": {"$oid": "65f1a2b3c4d5e6f708091a2b"}}, {"_id": 43}]}`,
		},
		Events:  []scenario.Event{{ID: "pago", Request: scenario.Request{Method: "POST", Path: "/hook", Body: []byte(`{"cliente_id":42}`), BodyFile: "p.json"}}},
		Perturb: scenario.Perturb{Duplicate: 3, Concurrency: 3},
		Invariants: []scenario.Invariant{
			mongoInvariant(t, "uma assinatura ativa por cliente", "sr_it_assinaturas", "pipeline", `
- $match: { status: ativa }
- $group: { _id: $cliente_id, ativas: { $sum: 1 } }
- $match: { ativas: { $gt: 1 } }`),
			mongoInvariant(t, "reset apagou as velhas", "sr_it_assinaturas", "filter", `{ status: velha }`),
			mongoInvariant(t, "seed tem um cliente sem nome", "sr_it_clientes", "filter", `{ nome: { $exists: false } }`),
		},
	}

	ex := Explore(ctx, sc, Options{HTTPTimeout: 5 * time.Second, Seed: 8812, Runs: 40, Attempts: 5, Shrink: true, KeepGoing: true})
	if ex.Error != "" {
		t.Fatalf("error: %s", ex.Error)
	}
	st := ex.Invariants
	if st[0].Failed == 0 || st[1].Failed != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if sh := st[0].Shrunk; sh == nil || perturb.Format(sc, sh.Sequence) != "[pago ‖ pago]" || sh.Reproduced == 0 {
		t.Fatalf("shrunk = %+v", st[0].Shrunk)
	}
	ev := st[0].Shrunk.Evidence
	if strings.Join(ev.Columns, ",") != "_id,ativas" || *ev.Values[0][0] != "42" || *ev.Values[0][1] != "2" {
		t.Errorf("evidence = %v %v", ev.Columns, ev.Values)
	}
	// The third invariant fails on every run (client 43 has no name):
	// evidence must show the missing field as nil and keep the seed's types.
	if st[2].FirstFailure == nil {
		t.Fatal("seed invariant should fail")
	}
	if rows := st[2].FirstFailure.Evidence; strings.Join(rows.Columns, ",") != "_id" || *rows.Values[0][0] != "43" {
		t.Errorf("seed evidence = %v %v", rows.Columns, rows.Values)
	}

	idx, _ := subs.Indexes().ListSpecifications(ctx)
	if len(idx) != 2 {
		t.Errorf("reset must keep indexes, have %d", len(idx))
	}
	var cliente bson.M
	if err := db.Collection("sr_it_clientes").FindOne(ctx, bson.D{{Key: "_id", Value: 42}}).Decode(&cliente); err != nil {
		t.Fatal(err)
	}
	if _, ok := cliente["ref"].(bson.ObjectID); !ok {
		t.Errorf("seed $oid should be an ObjectID, got %T", cliente["ref"])
	}

	t.Run("bad operator is an error", func(t *testing.T) {
		bad := *sc
		bad.Perturb = scenario.Perturb{}
		bad.Invariants = []scenario.Invariant{mongoInvariant(t, "x", "sr_it_assinaturas", "pipeline", `[{ $matchh: {} }]`)}
		ex := Explore(ctx, &bad, Options{HTTPTimeout: 5 * time.Second})
		if r := ex.Invariants[0]; r.Errored != 1 || !strings.Contains(r.FirstError, "$matchh") {
			t.Errorf("stats = %+v", r)
		}
		if ex.ExitCode() != 2 {
			t.Errorf("exit = %d", ex.ExitCode())
		}
	})

	t.Run("unreachable server is an error", func(t *testing.T) {
		down := *sc
		down.Target.DatabaseURL = "mongodb://127.0.0.1:1/x?serverSelectionTimeoutMS=500"
		ex := Explore(ctx, &down, Options{})
		if !strings.HasPrefix(ex.Error, "cannot connect to database") {
			t.Errorf("error = %q", ex.Error)
		}
	})
}
