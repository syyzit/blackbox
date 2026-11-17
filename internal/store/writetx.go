package store

import (
	"context"
	"database/sql"
)

// writeTx is a transaction that has held the database's write lock since it began.
type writeTx struct {
	conn *sql.Conn
}

func (t writeTx) Exec(query string, args ...any) (sql.Result, error) {
	return t.conn.ExecContext(context.Background(), query, args...)
}

func (t writeTx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(context.Background(), query, args...)
}

func (t writeTx) QueryRow(query string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(context.Background(), query, args...)
}

// withWriteLock runs fn in a transaction that takes the write lock up front, for work
// that reads and then writes what it read.
//
// database/sql begins a deferred transaction: the read takes a snapshot, and the
// write that follows has to upgrade it. If another process committed in between,
// SQLite fails the upgrade at once with SQLITE_BUSY, without consulting busy_timeout,
// because waiting cannot make the snapshot current. BEGIN IMMEDIATE queues for the
// lock before reading, where busy_timeout does apply.
//
// The statement runs on one dedicated connection rather than through a DSN option,
// so every other connection (readers, the proxy's session writer) keeps the default.
func withWriteLock(db *sql.DB, fn func(tx writeTx) error) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	if err := fn(writeTx{conn}); err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		// The connection goes back to the pool; it must not carry an open transaction.
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	return nil
}
