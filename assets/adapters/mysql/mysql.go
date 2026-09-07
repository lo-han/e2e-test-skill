package harness

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// MySQL is the suite's own connection to the store the service writes to.
//
// Same shape as the Postgres adapter, with the three differences that silently
// change what a scenario proves: the storage engine, sql_mode, and datetime
// precision. AssertStrictMode and AssertInnoDB exist so those are checked
// rather than assumed — run them in the self-check suite.
type MySQL struct {
	DB  *sql.DB
	DSN string
}

// OpenMySQL connects and verifies the connection actually works. Pass
// parseTime=true in the DSN so DATETIME columns scan into time.Time.
func OpenMySQL(ctx context.Context, dsn string) (*MySQL, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening mysql: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to mysql at %s: %w", redactDSN(dsn), err)
	}
	return &MySQL{DB: db, DSN: dsn}, nil
}

// Close releases the pool.
func (m *MySQL) Close() error { return m.DB.Close() }

// ApplySchemaFile runs a .sql file, statement by statement: unlike Postgres,
// the driver rejects multiple statements in one Exec by default.
func (m *MySQL) ApplySchemaFile(ctx context.Context, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading schema %s: %w", path, err)
	}
	for _, stmt := range strings.Split(string(body), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := m.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("applying %s from %s: %w", firstLine(stmt), path, err)
		}
	}
	return nil
}

// Exec runs a statement, reporting the statement with the error.
func (m *MySQL) Exec(ctx context.Context, query string, args ...any) error {
	if _, err := m.DB.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("exec %s: %w", firstLine(query), err)
	}
	return nil
}

// Rows runs a query and returns every row as a map.
func (m *MySQL) Rows(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := m.DB.QueryContext(ctx, query, args...)
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
	return out, rows.Err()
}

// Row returns the first row, or nil when there is none.
func (m *MySQL) Row(ctx context.Context, query string, args ...any) (map[string]any, error) {
	rows, err := m.Rows(ctx, query, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// Count is the common assertion: how many rows match.
func (m *MySQL) Count(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	if err := m.DB.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", firstLine(query), err)
	}
	return n, nil
}

// WaitForRow polls until a row matches, reporting what it last saw when it
// does not.
func (m *MySQL) WaitForRow(ctx context.Context, timeout time.Duration, match func(map[string]any) bool, query string, args ...any) (map[string]any, error) {
	var found map[string]any
	err := Until(ctx, timeout, "a row matching "+firstLine(query), func(ctx context.Context) (bool, error) {
		row, err := m.Row(ctx, query, args...)
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
func (m *MySQL) WaitForCount(ctx context.Context, timeout time.Duration, want int, query string, args ...any) error {
	last := -1
	err := Until(ctx, timeout, fmt.Sprintf("%d rows from %s", want, firstLine(query)), func(ctx context.Context) (bool, error) {
		n, err := m.Count(ctx, query, args...)
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
func (m *MySQL) Ready() func(context.Context) error {
	return func(ctx context.Context) error { return m.DB.PingContext(ctx) }
}

// AssertStrictMode fails unless the server rejects bad input rather than
// repairing it. Without STRICT_TRANS_TABLES an over-long string is truncated
// and an invalid date becomes a zero date, both with a warning — so a scenario
// asserting "the service rejects malformed input" can pass because MySQL
// quietly repaired it, leaving a real defect hidden. Report the mode the
// findings hold for.
func (m *MySQL) AssertStrictMode(ctx context.Context) error {
	var mode string
	if err := m.DB.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode); err != nil {
		return fmt.Errorf("reading sql_mode: %w", err)
	}
	if !strings.Contains(mode, "STRICT_TRANS_TABLES") && !strings.Contains(mode, "STRICT_ALL_TABLES") {
		return fmt.Errorf("sql_mode is %q: without a STRICT mode MySQL repairs bad input instead of "+
			"rejecting it, so input-validation scenarios would pass without proving anything", mode)
	}
	return nil
}

// AssertInnoDB fails when a table is not InnoDB. A FOREIGN KEY on a MyISAM
// table is parsed and ignored, so a constraint-violation scenario would pass
// against a constraint that does not exist.
func (m *MySQL) AssertInnoDB(ctx context.Context, tables ...string) error {
	for _, table := range tables {
		var engine string
		err := m.DB.QueryRowContext(ctx,
			`SELECT ENGINE FROM information_schema.TABLES
			 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table).Scan(&engine)
		if err != nil {
			return fmt.Errorf("reading engine for %s: %w", table, err)
		}
		if !strings.EqualFold(engine, "InnoDB") {
			return fmt.Errorf("table %s uses %s, not InnoDB: foreign keys are ignored, so constraint "+
				"scenarios against it prove nothing", table, engine)
		}
	}
	return nil
}

// RejectWrites installs a trigger that raises on every write to a table.
// MySQL has no RAISE, so this is SIGNAL SQLSTATE. Always defer the restore.
func (m *MySQL) RejectWrites(ctx context.Context, table string) (func(), error) {
	name := "e2e_reject_" + sanitize(table)
	stmt := fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE UPDATE ON %s FOR EACH ROW
		 SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'e2e: writes to %s are rejected'`,
		name, table, table)
	if err := m.Exec(ctx, stmt); err != nil {
		return nil, err
	}
	return func() {
		_ = m.Exec(context.Background(), "DROP TRIGGER IF EXISTS "+name)
	}, nil
}

// HideTable renames a table out of the way so queries against it fail. Verify
// the service really errors: the table cache and prepared statements behave
// differently here than in PostgreSQL.
func (m *MySQL) HideTable(ctx context.Context, table string) (func(), error) {
	hidden := table + "_e2e_hidden"
	if err := m.Exec(ctx, fmt.Sprintf("RENAME TABLE %s TO %s", table, hidden)); err != nil {
		return nil, err
	}
	return func() {
		_ = m.Exec(context.Background(), fmt.Sprintf("RENAME TABLE %s TO %s", hidden, table))
	}, nil
}

// KillConnections drops the service's connections mid-flight — cleaner here
// than in PostgreSQL — so recovery from a dropped connection can be asserted.
// user is the account the service connects as.
func (m *MySQL) KillConnections(ctx context.Context, user string) error {
	rows, err := m.Rows(ctx,
		`SELECT ID FROM information_schema.PROCESSLIST WHERE USER = ? AND ID <> CONNECTION_ID()`, user)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := m.Exec(ctx, fmt.Sprintf("KILL CONNECTION %v", row["ID"])); err != nil {
			// The connection may have gone on its own between the two statements.
			continue
		}
	}
	return nil
}

// TruncateAll empties tables between suites, with foreign key checks off so
// the order of the list does not matter.
func (m *MySQL) TruncateAll(ctx context.Context, tables ...string) error {
	if len(tables) == 0 {
		return nil
	}
	if err := m.Exec(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return err
	}
	defer func() { _ = m.Exec(context.Background(), "SET FOREIGN_KEY_CHECKS = 1") }()
	for _, table := range tables {
		if err := m.Exec(ctx, "TRUNCATE TABLE "+table); err != nil {
			return err
		}
	}
	return nil
}
