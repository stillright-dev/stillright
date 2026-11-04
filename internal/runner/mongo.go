package runner

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/stillright-dev/stillright/internal/invariant"
	"github.com/stillright-dev/stillright/internal/mongoquery"
	"github.com/stillright-dev/stillright/internal/scenario"
)

// Mongo is a Store backed by a MongoDB database.
type Mongo struct {
	client *mongo.Client
	db     *mongo.Database
}

// OpenMongo connects and pings, so an unreachable server is reported here
// rather than on the first query (the driver connects lazily).
func OpenMongo(ctx context.Context, uri string) (*Mongo, error) {
	u, err := mongoquery.ParseURI(uri)
	if err != nil {
		return nil, err
	}
	// The default timeout comes first so a serverSelectionTimeoutMS in the
	// URI still wins.
	client, err := mongo.Connect(options.Client().SetServerSelectionTimeout(10 * time.Second).ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		client.Disconnect(context.WithoutCancel(ctx))
		return nil, err
	}
	return &Mongo{client: client, db: client.Database(u.Database)}, nil
}

// Reset deletes every document but keeps the collections and their indexes,
// which are often what makes the application correct.
func (m *Mongo) Reset(ctx context.Context, collections []string) error {
	for _, c := range collections {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			return fmt.Errorf("%s: %w", c, err)
		}
	}
	return nil
}

// Setup inserts the seed documents, collection by collection, in file order.
func (m *Mongo) Setup(ctx context.Context, seed string) error {
	cols, err := mongoquery.ParseSeed([]byte(seed))
	if err != nil {
		return err
	}
	for _, c := range cols {
		if len(c.Docs) == 0 {
			continue
		}
		if _, err := m.db.Collection(c.Name).InsertMany(ctx, c.Docs); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	return nil
}

// Query runs the invariant's pipeline or filter. Columns are the top-level
// fields of the returned documents, in order of first appearance; a field a
// document lacks is nil.
func (m *Mongo) Query(ctx context.Context, inv scenario.Invariant, limit int) (invariant.Rows, error) {
	var out invariant.Rows
	coll := m.db.Collection(inv.Collection)
	var cur *mongo.Cursor
	if inv.PipelineJSON != "" {
		pipeline, err := mongoquery.ParsePipeline(inv.PipelineJSON)
		if err != nil {
			return out, err
		}
		if cur, err = coll.Aggregate(ctx, pipeline); err != nil {
			return out, err
		}
	} else {
		filter, err := mongoquery.ParseFilter(inv.FilterJSON)
		if err != nil {
			return out, err
		}
		if cur, err = coll.Find(ctx, filter); err != nil {
			return out, err
		}
	}
	defer cur.Close(context.WithoutCancel(ctx))

	index := map[string]int{}
	var docs []map[string]string
	for cur.Next(ctx) {
		out.Total++
		if out.Total > limit {
			continue
		}
		elems, err := cur.Current.Elements()
		if err != nil {
			return out, err
		}
		doc := map[string]string{}
		for _, e := range elems {
			if _, seen := index[e.Key()]; !seen {
				index[e.Key()] = len(out.Columns)
				out.Columns = append(out.Columns, e.Key())
			}
			doc[e.Key()] = mongoquery.Render(e.Value())
		}
		docs = append(docs, doc)
	}
	if err := cur.Err(); err != nil {
		return out, err
	}
	for _, doc := range docs {
		row := make([]*string, len(out.Columns))
		for i, c := range out.Columns {
			if v, ok := doc[c]; ok {
				row[i] = &v
			}
		}
		out.Values = append(out.Values, row)
	}
	return out, nil
}

func (m *Mongo) Close(ctx context.Context) error {
	return m.client.Disconnect(ctx)
}
