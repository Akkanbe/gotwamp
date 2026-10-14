//declscope:namespace sqlite

// Package sqlite is the persistent result store (SPEC §9.4, §13): the run
// and window summaries and the run errors of the monitoring sessions, in an
// SQLite database that survives a restart of gotwampd.
//
// The Store keeps a store.Memory in front of the database: Publish feeds it
// and the watchers as before, and queues the results to keep for one writer
// goroutine, so that a slow disk never holds up a run. Recent, Packets,
// Sessions and Watch answer from memory, which Open fills with the latest
// results of each session; Range reads the database, so that it reaches back
// as far as the retention. Per-packet records stay in memory only.
//
// The database uses the WAL journal. Its schema carries a version, upgraded
// on Open; results older than the retention are deleted every hour.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	_ "modernc.org/sqlite" // the database/sql driver "sqlite", pure Go (CGO_ENABLED=0)
)

// Defaults of Config.
const (
	// DefaultRetention is how long results are kept.
	//
	//declscope:ignore overexported // the default of store.sqlite.retention, for internal/config, which impl-stats wires next
	DefaultRetention = 30 * 24 * time.Hour
	// defaultBuffer is the number of results waiting for the writer.
	defaultBuffer = 4096
	// defaultSweep is how often expired results are deleted.
	defaultSweep = time.Hour
)

// Errors of Open.
//
//declscope:ignore overexported // internal/daemon tells a corrupt or newer database apart with them once it selects store.backend sqlite
var (
	// ErrCorrupt means the file is not a gotwamp database or failed its
	// integrity check; Config.ResetCorrupt moves it aside instead.
	ErrCorrupt = errors.New("result database is corrupt")
	// ErrNewerSchema means the database was written by a newer gotwampd.
	ErrNewerSchema = errors.New("result database has a newer schema")
)

// DefinitionCodec turns store.SessionInfo.Definition, which the store does
// not know, into octets and back. gotwampd passes one for its session
// definitions; without it the definitions are not kept and come back nil.
type DefinitionCodec struct {
	Encode func(def any) ([]byte, error)
	Decode func(b []byte) (any, error)
}

// Config configures a Store. Zero values take the defaults.
type Config struct {
	// Path is the database file (store.sqlite.path). Required.
	Path string
	// Retention is how long results are kept (store.sqlite.retention);
	// zero means 30 days, negative keeps them for ever.
	Retention time.Duration
	// MaxSize is the size of the database and its WAL above which every
	// sweep logs a warning; zero never warns.
	MaxSize int64
	// History and MaxPackets size the memory in front of the database, as
	// for store.NewMemory: Recent returns at most History results per
	// session.
	History, MaxPackets int
	// Buffer is how many results may wait for the writer; more are dropped
	// and counted. Zero means 4096.
	Buffer int
	// Definitions keeps the session definitions; see DefinitionCodec.
	Definitions DefinitionCodec
	// ResetCorrupt moves a corrupt database aside (to Path plus
	// ".corrupt-" and the time) and starts an empty one, rather than fail.
	ResetCorrupt bool
	// Logger receives the warnings; nil discards them.
	Logger *slog.Logger
	// Now returns the current time, for the retention; nil means time.Now.
	Now func() time.Time
	// Sweep is how often expired results are deleted; zero means an hour.
	Sweep time.Duration
}

func (c *Config) withDefaults() {
	if c.Retention == 0 {
		c.Retention = DefaultRetention
	}
	if c.Buffer <= 0 {
		c.Buffer = defaultBuffer
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Sweep <= 0 {
		c.Sweep = defaultSweep
	}
}

// schemaVersion is the version this code writes; migrations[i] upgrades a
// database of version i to i+1.
const schemaVersion = 2

var migrations = []string{
	// 0 → 1: the session descriptions, shared by their results, and the
	// results with the JSON of their Summary or RunError.
	`CREATE TABLE infos (
		id     INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused: caches keep ids
		digest BLOB NOT NULL UNIQUE,
		data   BLOB NOT NULL
	);
	CREATE TABLE results (
		id      INTEGER PRIMARY KEY,
		session TEXT    NOT NULL,
		kind    INTEGER NOT NULL,
		time_ns INTEGER NOT NULL,
		run_id  TEXT    NOT NULL,
		sid     BLOB,
		info_id INTEGER NOT NULL REFERENCES infos(id),
		data    BLOB    NOT NULL
	);
	CREATE INDEX results_by_session_time ON results(session, time_ns);
	CREATE INDEX results_by_time ON results(time_ns);`,
	// 1 → 2: results.data may hold the compressed payload (compact.go),
	// which a gotwampd of version 1 would misread: the version keeps it
	// out. The rows of version 1 stay as they are and read as before.
	// payload_formats tells someone reading the file with sqlite3.
	`CREATE TABLE payload_formats (
		first_octet INTEGER PRIMARY KEY,
		description TEXT NOT NULL
	);
	INSERT INTO payload_formats VALUES
		(123, 'JSON of the Summary or RunError (the first octet is "{")'),
		(1, 'DEFLATE, dictionary 1 of gotwamp internal/store/sqlite, of the JSON without zero-valued fields');`,
}

// openDB opens path and brings its schema up to date. A file that is not a
// database, fails its integrity check or has no readable version wraps
// ErrCorrupt.
func openDB(path string) (*sql.DB, error) {
	// busy_timeout covers a second process reading the file (sqlite3 CLI).
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("sqlite store: open %s: %w", path, err)
	}
	// One connection: the writer and the readers take turns, and the
	// pragmas above apply to it.
	db.SetMaxOpenConns(1)
	if err := checkIntegrity(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite store: %s: %w: %w", path, ErrCorrupt, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite store: %s: %w", path, err)
	}
	return db, nil
}

func checkIntegrity(db *sql.DB) error {
	var result string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("quick_check: %s", result)
	}
	return nil
}

// migrate creates the version table and applies the migrations the
// database lacks, each in its own transaction.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("%w: schema_version: %w", ErrCorrupt, err)
	}
	var version int
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("%w: schema_version: %w", ErrCorrupt, err)
	case version > schemaVersion:
		return fmt.Errorf("%w: version %d, this gotwampd knows %d", ErrNewerSchema, version, schemaVersion)
	}
	for v := version; v < schemaVersion; v++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate to version %d: %w", v+1, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET version = ?`, v+1); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// moveAside renames a corrupt database and its WAL and shared-memory files
// out of the way and returns the new name of the database.
func moveAside(path string, now time.Time) (string, error) {
	aside := path + ".corrupt-" + now.UTC().Format("20060102T150405Z")
	if err := os.Rename(path, aside); err != nil {
		return "", fmt.Errorf("sqlite store: move the corrupt %s aside: %w", path, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Rename(path+suffix, aside+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("sqlite store: move the corrupt %s aside: %w", path+suffix, err)
		}
	}
	return aside, nil
}
