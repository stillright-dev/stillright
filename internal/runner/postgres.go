package runner

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/stillright-dev/stillright/internal/invariant"
)

// Store is the database the application under test writes to.
type Store interface {
	invariant.Querier
	// Reset empties the given tables.
	Reset(ctx context.Context, tables []string) error
	// Exec runs a SQL script, which may hold several statements.
	Exec(ctx context.Context, sql string) error
	Close(ctx context.Context) error
}

// Postgres is a Store backed by a single PostgreSQL connection.
type Postgres struct {
	conn *pgx.Conn
}

func OpenPostgres(ctx context.Context, url string) (*Postgres, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Postgres{conn: conn}, nil
}

// Reset truncates tables. Names are validated as plain identifiers when the
// scenario is loaded, so they are safe to splice into the statement unquoted
// (which keeps PostgreSQL's usual case folding).
func (p *Postgres) Reset(ctx context.Context, tables []string) error {
	_, err := p.conn.Exec(ctx, "TRUNCATE TABLE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE")
	return err
}

func (p *Postgres) Exec(ctx context.Context, sql string) error {
	// The simple protocol accepts multi-statement scripts.
	return p.conn.PgConn().Exec(ctx, sql).Close()
}

// QueryRows runs sql in a read-only transaction that is always rolled back,
// so an invariant can never change the data it inspects. Results are fetched
// in text format to get PostgreSQL's own rendering of every type.
func (p *Postgres) QueryRows(ctx context.Context, sql string, limit int) (invariant.Rows, error) {
	var out invariant.Rows
	tx, err := p.conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, sql, pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for _, fd := range rows.FieldDescriptions() {
		out.Columns = append(out.Columns, fd.Name)
	}
	for rows.Next() {
		out.Total++
		if out.Total > limit {
			continue
		}
		raw := rows.RawValues()
		row := make([]*string, len(raw))
		for i, b := range raw {
			if b != nil {
				s := string(b)
				row[i] = &s
			}
		}
		out.Values = append(out.Values, row)
	}
	return out, rows.Err()
}

func (p *Postgres) Close(ctx context.Context) error {
	return p.conn.Close(ctx)
}
