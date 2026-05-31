// server/db/db.go

package db

import (
	"database/sql"
	"os"

	_ "github.com/mattn/go-sqlite3"
)

// DB wraps sql.DB with the schema migration applied on open.
type DB struct {
	*sql.DB
}

// Open opens (or creates) the SQLite file at path and applies the schema.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite3", path+"?_journal=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}

	// WAL mode supports multiple concurrent readers and one writer.
	sqlDB.SetMaxOpenConns(0)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(0)

	schema, err := os.ReadFile("server/db/schema.sql")
	if err != nil {
		return nil, err
	}

	if _, err := sqlDB.Exec(string(schema)); err != nil {
		return nil, err
	}

	return &DB{sqlDB}, nil
}