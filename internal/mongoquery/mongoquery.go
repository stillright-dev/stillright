// Package mongoquery turns the MongoDB parts of a scenario (aggregation
// pipelines, find filters, seed files) into driver values, and renders
// documents as evidence.
//
// Queries are written in YAML in the scenario. They are converted to
// Extended JSON preserving key order (it matters for $group and $sort keys),
// so values such as {"$oid": "..."} and {"$date": "..."} work as in mongosh.
package mongoquery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"gopkg.in/yaml.v3"
)

// forbidden are operators an invariant may not use: $out and $merge write
// to the database, and the rest run JavaScript inside it.
var forbidden = map[string]string{
	"$out":         "writes to the database",
	"$merge":       "writes to the database",
	"$where":       "runs JavaScript",
	"$function":    "runs JavaScript",
	"$accumulator": "runs JavaScript",
}

// Forbidden returns the first forbidden operator in n, with its line and why
// it is refused. key is empty when there is none.
func Forbidden(n *yaml.Node) (key string, line int, why string) {
	if n == nil {
		return "", 0, ""
	}
	if n.Kind == yaml.AliasNode {
		return Forbidden(n.Alias)
	}
	for i, c := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 {
			if why, bad := forbidden[c.Value]; bad {
				return c.Value, c.Line, why
			}
			continue
		}
		if k, l, w := Forbidden(c); k != "" {
			return k, l, w
		}
	}
	return "", 0, ""
}

// JSON renders a YAML node as JSON, keeping mapping order. Scalars keep their
// YAML type: 1 is a number, true a boolean, ativa a string.
func JSON(n *yaml.Node) (string, error) {
	var b bytes.Buffer
	if err := writeJSON(&b, n); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeJSON(b *bytes.Buffer, n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			b.WriteString("null")
			return nil
		}
		return writeJSON(b, n.Content[0])
	case yaml.AliasNode:
		return writeJSON(b, n.Alias)
	case yaml.MappingNode:
		b.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			key, _ := json.Marshal(n.Content[i].Value)
			b.Write(key)
			b.WriteByte(':')
			if err := writeJSON(b, n.Content[i+1]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case yaml.SequenceNode:
		b.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, c); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return fmt.Errorf("line %d: %v", n.Line, err)
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("line %d: %v", n.Line, err)
		}
		b.Write(enc)
	}
	return nil
}

// ParsePipeline parses an aggregation pipeline written as a JSON array.
func ParsePipeline(src string) (bson.A, error) {
	var w struct {
		P bson.A `bson:"p"`
	}
	if err := bson.UnmarshalExtJSON([]byte(`{"p":`+src+`}`), false, &w); err != nil {
		return nil, err
	}
	for i, stage := range w.P {
		if d, ok := stage.(bson.D); !ok || len(d) != 1 {
			return nil, fmt.Errorf("stage %d must be one operator, like {$match: {...}}", i)
		}
	}
	return w.P, nil
}

// ParseFilter parses a find filter written as a JSON object.
func ParseFilter(src string) (bson.D, error) {
	var d bson.D
	if err := bson.UnmarshalExtJSON([]byte(src), false, &d); err != nil {
		return nil, err
	}
	return d, nil
}

// Collection is one collection's documents in a seed file.
type Collection struct {
	Name string
	Docs []any
}

// ParseSeed parses a seed file: a JSON object mapping collection names to
// arrays of documents, inserted in file order.
//
//	{ "clientes": [ { "_id": 42, "nome": "Ana" } ] }
func ParseSeed(src []byte) ([]Collection, error) {
	var d bson.D
	if err := bson.UnmarshalExtJSON(src, false, &d); err != nil {
		return nil, fmt.Errorf("seed must be a JSON object of collection: [documents]: %v", err)
	}
	var out []Collection
	for _, e := range d {
		docs, ok := e.Value.(bson.A)
		if !ok {
			return nil, fmt.Errorf("seed collection %q must be an array of documents", e.Key)
		}
		for i, doc := range docs {
			if _, ok := doc.(bson.D); !ok {
				return nil, fmt.Errorf("seed collection %q, item %d: must be a document", e.Key, i)
			}
		}
		out = append(out, Collection{Name: e.Key, Docs: docs})
	}
	return out, nil
}

// URI holds what stillright needs from a MongoDB connection string.
type URI struct {
	Hosts    []string
	Database string
	SRV      bool
}

// ParseURI reads a mongodb:// or mongodb+srv:// URI without resolving
// anything, so it is safe to call before deciding whether to connect.
func ParseURI(uri string) (URI, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return URI{}, err
	}
	if u.Scheme != "mongodb" && u.Scheme != "mongodb+srv" {
		return URI{}, fmt.Errorf("not a MongoDB URI (scheme %q)", u.Scheme)
	}
	out := URI{Database: strings.TrimPrefix(u.Path, "/"), SRV: u.Scheme == "mongodb+srv"}
	for _, h := range strings.Split(u.Host, ",") {
		if h != "" {
			out.Hosts = append(out.Hosts, h)
		}
	}
	if len(out.Hosts) == 0 {
		return URI{}, fmt.Errorf("no host in MongoDB URI")
	}
	return out, nil
}

// IsURI reports whether s looks like a MongoDB connection string.
func IsURI(s string) bool {
	return strings.HasPrefix(s, "mongodb://") || strings.HasPrefix(s, "mongodb+srv://")
}

// Render shows a field value as evidence: strings as they are, everything
// else in relaxed Extended JSON ({"$oid": ...}, numbers, nested documents).
func Render(v bson.RawValue) string {
	if s, ok := v.StringValueOK(); ok {
		return s
	}
	doc := bson.D{{Key: "v", Value: v}}
	enc, err := bson.MarshalExtJSON(doc, false, false)
	if err != nil {
		return v.String()
	}
	// {"v":<value>}
	return string(enc[5 : len(enc)-1])
}
