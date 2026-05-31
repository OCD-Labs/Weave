// backend/db/db.go

package db

import (
	"database/sql"
	_ "github.com/mattn/go-sqlite3"
	"os"
)

// DB wraps sql.DB with the schema migration applied on open.
type DB struct {
	*sql.DB
}

// Open opens (or creates) the SQLite file at path and applies the schema.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite3", path+"?_journal=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	// Single writer, multiple readers — WAL mode handles this well for our workload.
	sqlDB.SetMaxOpenConns(1)

	schema, err := os.ReadFile("backend/db/schema.sql")
	if err != nil {
		return nil, err
	}

	if _, err := sqlDB.Exec(string(schema)); err != nil {
		return nil, err
	}

	return &DB{sqlDB}, nil
}