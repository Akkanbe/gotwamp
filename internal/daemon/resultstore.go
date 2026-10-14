package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/export/prom"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/store/sqlite"
)

// resultStore is the daemon's result store (store.backend): in memory, or
// in memory in front of a SQLite database that keeps the summaries across
// restarts.
//
//declscope:package // Start opens it; the services, the exporter and shutdown use it
type resultStore struct {
	store.Store
	//declscope:private
	backend string
	// db is the SQLite store, nil in memory.
	//
	//declscope:private
	db *sqlite.Store
	// path is the database file, empty in memory.
	//
	//declscope:private
	path string
}

// openStore opens the result store of cfg. A database that cannot be opened,
// is corrupt (with on_corrupt: fail) or of a newer schema stops the start.
//
//declscope:package // Start opens the store with it
func openStore(cfg *config.Config, log *slog.Logger) (*resultStore, error) {
	limits := cfg.Daemon.Limits
	if cfg.Store.Backend != "sqlite" {
		return &resultStore{Store: store.NewMemory(limits.ResultHistory, limits.MaxSamplesPerRun), backend: "memory"}, nil
	}
	q := cfg.Store.SQLite
	retention := q.Retention
	if retention == 0 {
		retention = -1 // store.sqlite.retention 0 keeps the results for ever
	}
	db, err := sqlite.Open(sqlite.Config{
		Path: q.Path, Retention: retention, MaxSize: int64(q.MaxSize), ResetCorrupt: q.OnCorrupt == "reset",
		History: limits.ResultHistory, MaxPackets: limits.MaxSamplesPerRun,
		Definitions: sqlite.DefinitionCodec{Encode: encodeDefinition, Decode: decodeDefinition},
		Logger:      log,
	})
	if err != nil {
		return nil, fmt.Errorf("store.sqlite: %w", err)
	}
	log.Info("result store opened", "backend", "sqlite", "path", q.Path, "retention", retention)
	return &resultStore{Store: db, backend: "sqlite", db: db, path: q.Path}, nil
}

// encodeDefinition keeps a session definition (config.Session) as JSON.
func encodeDefinition(def any) ([]byte, error) { return json.Marshal(def) }

// decodeDefinition reads back a session definition kept by encodeDefinition.
func decodeDefinition(b []byte) (any, error) {
	var s config.Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return s, nil
}

// close writes the database's queue out and closes it. In memory it does
// nothing.
//
//declscope:package // shutdown and a failed start close the store
func (r *resultStore) close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// status reports the store in GetStatus.
//
//declscope:package // GetStatus reports it
func (r *resultStore) status() *gotwampv1.StoreStatus {
	if r == nil {
		return nil
	}
	st := &gotwampv1.StoreStatus{Backend: r.backend, Path: r.path}
	if r.db != nil {
		s := r.db.Stats()
		st.ResultsWritten, st.ResultsDropped, st.ResultsFailed = s.Written, s.Dropped, s.Failed
	}
	return st
}

// stats reports the database's counters to the exporter; nil in memory.
//
//declscope:package // Start hands it to the exporter
func (r *resultStore) stats() func() prom.StoreStats {
	if r == nil || r.db == nil {
		return nil
	}
	return func() prom.StoreStats {
		s := r.db.Stats()
		return prom.StoreStats{Written: s.Written, Dropped: s.Dropped, Failed: s.Failed}
	}
}
