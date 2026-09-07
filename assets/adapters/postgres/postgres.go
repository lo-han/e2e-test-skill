package harness

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// Postgres is the suite's own connection to the store the service writes to.
//
// Scenarios never hold a driver themselves: waiting and error handling get
// reinvented per scenario and diverge. Everything the suite knows how to
// observe or provoke in the store lives here.
type Postgres struct {
	DB  *sql.DB
	DSN string
}

// OpenPostgres connects and verifies the connection actually works — sql.Open
// alone is lazy and succeeds against a database that is not there.
func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to postgres at %s: %w", redactDSN(dsn), err)
	}
	return &Postgres{DB: db, DSN: dsn}, nil
}

// Close releases the pool.
func (p *Postgres) Close() error { return p.DB.Close() }

// ApplySchemaFile runs a .sql file — the schema reconstructed from the
// service's own statements when the repository has no migrations.
func (p *Postgres) ApplySchemaFile(ctx context.Context, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading schema %s: %w", path, err)
	}
	if _, err := p.DB.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("applying schema %s: %w", path, err)
	}
	return nil
}

// Exec runs a statement, reporting the statement with the error. A failure
// here is usually a fixture bug, and the SQL is what identifies it.
func (p *Postgres) Exec(ctx context.Context, query string, args ...any) error {
	if _, err := p.DB.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("exec %s: %w", firstLine(query), err)
	}
	return nil
}

// Rows runs a query and returns every row as a map, which is what shape
// assertions want: a missing column is a failed lookup, not a zero value.
func (p *Postgres) Rows(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := p.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", firstLine(query), err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		cells := make([]any, len(cols))
		into := make([]any, len(cols))
		for i := range cells {
			into[i] = &cells[i]
		}
		if err := rows.Scan(into...); err != nil {
			return nil, fmt.Errorf("scanning %s: %w", firstLine(query), err)
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			row[name] = normalizeCell(cells[i])
		}
		out = append(out, row)
	}
	// Errors on a statement's execution can surface only here, when the rows
	// are read. Never skip this check.
	return out, rows.Err()
}

// Row returns the first row, or nil when there is none.
func (p *Postgres) Row(ctx context.Context, query string, args ...any) (map[string]any, error) {
	rows, err := p.Rows(ctx, query, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// Count is the common assertion: how many rows match.
func (p *Postgres) Count(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	if err := p.DB.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", firstLine(query), err)
	}
	return n, nil
}

// WaitForRow polls until a row matches, and reports what it last saw when it
// does not. This is the store's form of "wait on what the service did".
func (p *Postgres) WaitForRow(ctx context.Context, timeout time.Duration, match func(map[string]any) bool, query string, args ...any) (map[string]any, error) {
	var found map[string]any
	err := Until(ctx, timeout, "a row matching "+firstLine(query), func(ctx context.Context) (bool, error) {
		row, err := p.Row(ctx, query, args...)
		if err != nil {
			return false, err
		}
		found = row
		return row != nil && (match == nil || match(row)), nil
	})
	if err != nil && found != nil {
		return found, fmt.Errorf("%w (last seen: %v)", err, found)
	}
	return found, err
}

// WaitForCount polls until the number of matching rows equals want.
func (p *Postgres) WaitForCount(ctx context.Context, timeout time.Duration, want int, query string, args ...any) error {
	last := -1
	err := Until(ctx, timeout, fmt.Sprintf("%d rows from %s", want, firstLine(query)), func(ctx context.Context) (bool, error) {
		n, err := p.Count(ctx, query, args...)
		if err != nil {
			return false, err
		}
		last = n
		return n == want, nil
	})
	if err != nil {
		return fmt.Errorf("%w (last count: %d)", err, last)
	}
	return nil
}

// Ready is the readiness probe to hand to Service.Ready.
func (p *Postgres) Ready() func(context.Context) error {
	return func(ctx context.Context) error { return p.DB.PingContext(ctx) }
}

// RejectWrites installs a trigger that raises on every UPDATE of a table, so
// "the store refuses the write" can be provoked from outside. Always defer the
// returned restore, so it happens even when the scenario aborts.
func (p *Postgres) RejectWrites(ctx context.Context, table string) (func(), error) {
	fn := "e2e_reject_" + sanitize(table)
	stmts := []string{
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'e2e: writes to %s are rejected'; END; $$ LANGUAGE plpgsql`, fn, table),
		fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT OR UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION %s()`, fn, table, fn),
	}
	for _, s := range stmts {
		if err := p.Exec(ctx, s); err != nil {
			return nil, err
		}
	}
	return func() {
		_ = p.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, fn, table))
		_ = p.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, fn))
	}, nil
}

// HideTable renames a table out of the way so queries against it fail.
// Renaming keeps the table's identity, so pooled connections and cached
// statements survive it — dropping it would not.
func (p *Postgres) HideTable(ctx context.Context, table string) (func(), error) {
	hidden := table + "_e2e_hidden"
	if err := p.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, table, hidden)); err != nil {
		return nil, err
	}
	return func() {
		_ = p.Exec(context.Background(), fmt.Sprintf(`ALTER TABLE %s RENAME TO %s`, hidden, table))
	}, nil
}

// KillConnections drops the service's connections, so a mid-flight failure and
// the recovery after it can both be asserted.
func (p *Postgres) KillConnections(ctx context.Context, appName string) error {
	return p.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE pid <> pg_backend_pid() AND application_name = $1`, appName)
}

// TruncateAll empties tables between suites, keeping the schema.
func (p *Postgres) TruncateAll(ctx context.Context, tables ...string) error {
	if len(tables) == 0 {
		return nil
	}
	return p.Exec(ctx, fmt.Sprintf("TRUNCATE %s RESTART IDENTITY CASCADE", strings.Join(tables, ", ")))
}
