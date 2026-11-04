package mongoquery

import (
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"gopkg.in/yaml.v3"
)

func node(t *testing.T, src string) *yaml.Node {
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(src), &n); err != nil {
		t.Fatal(err)
	}
	return &n
}

func TestJSONKeepsOrderAndTypes(t *testing.T) {
	src := `
- $match: { status: ativa, valor: { $gt: 10 }, pago: true, nota: null }
- $group: { _id: $cliente_id, n: { $sum: 1 }, total: { $sum: 1.5 } }
- $sort: { n: -1, _id: 1 }
`
	got, err := JSON(node(t, src))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"$match":{"status":"ativa","valor":{"$gt":10},"pago":true,"nota":null}},` +
		`{"$group":{"_id":"$cliente_id","n":{"$sum":1},"total":{"$sum":1.5}}},{"$sort":{"n":-1,"_id":1}}]`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	p, err := ParsePipeline(got)
	if err != nil {
		t.Fatal(err)
	}
	sort := p[2].(bson.D)[0].Value.(bson.D)
	if sort[0].Key != "n" || sort[1].Key != "_id" {
		t.Errorf("$sort order lost: %v", sort)
	}
}

func TestExtendedJSONValues(t *testing.T) {
	js, _ := JSON(node(t, `{ _id: { $oid: "65f1a2b3c4d5e6f708091a2b" }, criada: { $date: "2026-11-03T12:00:00Z" } }`))
	f, err := ParseFilter(js)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f[0].Value.(bson.ObjectID); !ok {
		t.Errorf("$oid should become an ObjectID, got %T", f[0].Value)
	}
	if _, ok := f[1].Value.(bson.DateTime); !ok {
		t.Errorf("$date should become a DateTime, got %T", f[1].Value)
	}
}

func TestForbidden(t *testing.T) {
	for src, want := range map[string]string{
		"- $match: { a: 1 }\n- $out: copia\n":                      "$out",
		"- $match: { a: 1 }\n- $merge: { into: x }\n":              "$merge",
		"{ $where: 'this.a > 1' }":                                 "$where",
		"- $group: { _id: 1, x: { $accumulator: { init: 'f' } } }": "$accumulator",
		"- $addFields: { y: { $function: { body: 'f' } } }":        "$function",
		"- $match: { a: 1 }\n- $group: { _id: $a }\n":              "",
		"- $match: { campo: $out }\n":                              "", // a value, not an operator
	} {
		key, line, _ := Forbidden(node(t, src))
		if key != want {
			t.Errorf("%q: forbidden = %q, want %q", src, key, want)
		}
		if want != "" && line == 0 {
			t.Errorf("%q: no line", src)
		}
	}
}

func TestParsePipelineRejectsBadStages(t *testing.T) {
	if _, err := ParsePipeline(`[{"$match":{},"$sort":{"a":1}}]`); err == nil || !strings.Contains(err.Error(), "stage 0 must be one operator") {
		t.Errorf("err = %v", err)
	}
	if _, err := ParsePipeline(`[1]`); err == nil {
		t.Error("a number is not a stage")
	}
}

func TestParseSeed(t *testing.T) {
	cols, err := ParseSeed([]byte(`{"clientes": [{"_id": 42, "nome": "Ana"}], "planos": [{"_id": {"$oid": "65f1a2b3c4d5e6f708091a2b"}}, {"_id": 2}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0].Name != "clientes" || len(cols[1].Docs) != 2 {
		t.Errorf("cols = %+v", cols)
	}
	for src, want := range map[string]string{
		`[]`:                "must be a JSON object",
		`{"clientes": {}}`:  `"clientes" must be an array`,
		`{"clientes": [1]}`: `"clientes", item 0: must be a document`,
		`{"clientes": [{]}`: "must be a JSON object",
	} {
		if _, err := ParseSeed([]byte(src)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", src, err, want)
		}
	}
}

func TestParseURI(t *testing.T) {
	u, err := ParseURI("mongodb://user:p%40ss@a.example.com:27017,b.example.com:27018/assinaturas?replicaSet=rs0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(u.Hosts, ",") != "a.example.com:27017,b.example.com:27018" || u.Database != "assinaturas" || u.SRV {
		t.Errorf("uri = %+v", u)
	}
	u, err = ParseURI("mongodb+srv://u:p@cluster0.abcd.mongodb.net/app?retryWrites=true")
	if err != nil || !u.SRV || u.Hosts[0] != "cluster0.abcd.mongodb.net" || u.Database != "app" {
		t.Errorf("srv uri = %+v, %v", u, err)
	}
	if _, err := ParseURI("postgres://localhost/x"); err == nil {
		t.Error("postgres URI accepted")
	}
}

func TestRender(t *testing.T) {
	raw, _ := bson.Marshal(bson.D{
		{Key: "s", Value: "ativa"},
		{Key: "n", Value: int32(2)},
		{Key: "id", Value: bson.ObjectID{0x65, 0xf1}},
		{Key: "doc", Value: bson.D{{Key: "a", Value: true}}},
	})
	var got []string
	elems, _ := bson.Raw(raw).Elements()
	for _, e := range elems {
		got = append(got, Render(e.Value()))
	}
	want := []string{"ativa", "2", `{"$oid":"65f100000000000000000000"}`, `{"a":true}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}
