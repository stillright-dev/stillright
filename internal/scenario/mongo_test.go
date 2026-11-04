package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mongoMinimal = `version: 1
name: mongo
target:
  base_url: http://localhost:3000
  database_url: mongodb://localhost:27017/assinaturas
setup:
  seed: seed.json
  reset: truncate
  reset_collections: [assinaturas, pagamentos]
events:
  - id: pago
    request: { method: POST, path: /p }
invariants:
  - name: uma assinatura ativa por cliente
    collection: assinaturas
    pipeline:
      - $match: { status: ativa }
      - $group: { _id: $cliente_id, n: { $sum: 1 } }
      - $match: { n: { $gt: 1 } }
  - name: nada estornado ativo
    collection: assinaturas
    filter: { status: ativa, estornada: true }
`

func parseMongo(t *testing.T, src, seed string) (*Scenario, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seed.json"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	return Parse([]byte(src), "m.yaml", dir, testEnv)
}

func TestMongoScenario(t *testing.T) {
	sc, err := parseMongo(t, mongoMinimal, `{"clientes": [{"_id": 42}]}`)
	if err != nil {
		t.Fatalf("unexpected error:\n%v", err)
	}
	if sc.Target.Database != MongoDB {
		t.Errorf("database = %q", sc.Target.Database)
	}
	if got := strings.Join(sc.Setup.ResetTargets(), ","); got != "assinaturas,pagamentos" {
		t.Errorf("reset targets = %s", got)
	}
	if file, seed := sc.Setup.Script(); file != "seed.json" || !strings.Contains(seed, `"clientes"`) {
		t.Errorf("script = %q %q", file, seed)
	}
	want := `[{"$match":{"status":"ativa"}},{"$group":{"_id":"$cliente_id","n":{"$sum":1}}},{"$match":{"n":{"$gt":1}}}]`
	if sc.Invariants[0].PipelineJSON != want {
		t.Errorf("pipeline = %s", sc.Invariants[0].PipelineJSON)
	}
	if sc.Invariants[1].FilterJSON != `{"status":"ativa","estornada":true}` {
		t.Errorf("filter = %s", sc.Invariants[1].FilterJSON)
	}
}

func TestPostgresIsDefault(t *testing.T) {
	if sc := mustParse(t, minimal); sc.Target.Database != Postgres {
		t.Errorf("database = %q", sc.Target.Database)
	}
}

func TestMongoErrors(t *testing.T) {
	cases := []struct {
		name, src, seed string
		want            []string
	}{
		{
			name: "no database in uri",
			src:  strings.Replace(mongoMinimal, "27017/assinaturas", "27017", 1),
			want: []string{"m.yaml:5: target.database_url: a MongoDB URI must name the database"},
		},
		{
			name: "postgres fields on mongo",
			src: strings.Replace(strings.Replace(mongoMinimal, "  seed: seed.json\n", "  sql: seed.sql\n", 1),
				"reset_collections", "reset_tables", 1),
			want: []string{
				"m.yaml:7: setup.sql: is for PostgreSQL; database_url is MongoDB (use seed)",
				"m.yaml:9: setup.reset_tables: is for PostgreSQL; database_url is MongoDB (use reset_collections)",
				`setup.reset_collections: is required when reset is "truncate"`,
			},
		},
		{
			name: "sql invariant on mongo",
			src:  mongoMinimal + "  - name: x\n    sql: SELECT 1\n",
			want: []string{
				"m.yaml:24: invariants[2].sql: is for PostgreSQL",
				"m.yaml:23: invariants[2]: needs exactly one of pipeline or filter",
				`invariants[2].collection: is required`,
			},
		},
		{
			name: "both pipeline and filter",
			src:  mongoMinimal + "  - name: x\n    collection: a\n    filter: {}\n    pipeline: []\n",
			want: []string{"invariants[2]: needs exactly one of pipeline or filter"},
		},
		{
			name: "pipeline not a list",
			src:  mongoMinimal + "  - name: x\n    collection: a\n    pipeline: { $match: {} }\n",
			want: []string{"m.yaml:25: invariants[2].pipeline: must be a list of stages"},
		},
		{
			name: "out stage",
			src:  mongoMinimal + "  - name: x\n    collection: a\n    pipeline:\n      - $match: {}\n      - $out: copia\n",
			want: []string{"m.yaml:27: invariants[2].pipeline: $out is not allowed in an invariant: it writes to the database"},
		},
		{
			name: "where in filter",
			src:  mongoMinimal + "  - name: x\n    collection: a\n    filter: { $where: 'this.a > 1' }\n",
			want: []string{"m.yaml:25: invariants[2].filter: $where is not allowed in an invariant: it runs JavaScript"},
		},
		{
			name: "two operators in one stage",
			src:  mongoMinimal + "  - name: x\n    collection: a\n    pipeline:\n      - { $match: {}, $sort: { a: 1 } }\n",
			want: []string{"invariants[2].pipeline: stage 0 must be one operator"},
		},
		{
			name: "bad collection names",
			src:  strings.Replace(mongoMinimal, "[assinaturas, pagamentos]", "[assinaturas, system.users, \"a$b\"]", 1),
			want: []string{
				`m.yaml:9: setup.reset_collections[1]: "system.users" is not a valid collection name`,
				`m.yaml:9: setup.reset_collections[2]: "a$b" is not a valid collection name`,
			},
		},
		{
			name: "bad seed",
			src:  mongoMinimal,
			seed: `{"clientes": {"_id": 1}}`,
			want: []string{`m.yaml:7: setup.seed: seed.json: seed collection "clientes" must be an array of documents`},
		},
		{
			name: "mongo fields on postgres",
			src:  strings.Replace(minimal, "    sql: SELECT 1 WHERE false\n", "    sql: SELECT 1 WHERE false\n    collection: x\n", 1),
			want: []string{"m.yaml:12: invariants[0].collection: is for MongoDB; database_url is PostgreSQL (use sql)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := tc.seed
			if seed == "" {
				seed = `{"clientes": [{"_id": 42}]}`
			}
			_, err := parseMongo(t, tc.src, seed)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("missing %q in:\n%v", w, err)
				}
			}
		})
	}
}
