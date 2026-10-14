//declscope:namespace sqlite

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"github.com/Akkanbe/gotwamp/internal/store"
)

// defaultHistory is the History of store.NewMemory when Config.History is
// not positive, which Open needs to know how much to load.
const defaultHistory = 1440

// batchSize bounds the results one transaction writes.
const batchSize = 256

// warnEvery bounds how often the writer warns about dropped results and
// write errors.
const warnEvery = time.Minute

// Store is the SQLite result store. It implements store.Store; its methods
// are safe for concurrent use. Close it to flush and release the database.
type Store struct {
	cfg     Config
	history int
	db      *sql.DB
	mem     *store.Memory
	ops     chan op
	done    chan struct{}

	// mu guards closed: Publish and the queued calls take it shared, so
	// that Close does not close ops under them.
	mu     sync.RWMutex
	closed bool

	written, dropped, failed atomic.Uint64

	// infoIDs maps the session descriptions written to their rows; the
	// writer owns it, and payload. Its keys are weak: a description no
	// event uses any more is collected, and its entry dropped later.
	infoIDs map[weak.Pointer[store.SessionInfo]]int64
	payload *compactPayload
	// infos maps rows back to shared descriptions for the readers, weakly
	// too: it shares a description while events hold it and never keeps
	// one alive (Codex M11). infosMu is never held while waiting for the
	// database, whose one connection the writer may hold (Codex M10).
	infosMu sync.Mutex
	infos   map[int64]weak.Pointer[store.SessionInfo]

	warnMu   sync.Mutex
	lastWarn map[string]time.Time
}

// op is one request to the writer, in the order of the calls.
type op struct {
	event   *store.Event  // a result to insert
	deleted string        // a session to delete
	sweep   bool          // delete expired results now
	done    chan struct{} // closed once everything before is committed
}

// Stats are the counters of a Store.
type Stats struct {
	// Written counts results committed to the database.
	Written uint64
	// Dropped counts results not written because the writer was behind by
	// Config.Buffer, or the store was closed.
	Dropped uint64
	// Failed counts results the database refused.
	Failed uint64
}

var _ store.Store = (*Store)(nil)

// Open opens the database at cfg.Path, creating it, upgrading its schema and
// deleting what the retention expired, and loads the latest results of each
// session into memory. A corrupt file is an error wrapping ErrCorrupt, or
// with cfg.ResetCorrupt is moved aside for an empty database.
func Open(cfg Config) (*Store, error) {
	if cfg.Path == "" {
		return nil, errors.New("sqlite store: no path")
	}
	cfg.withDefaults()
	db, err := openDB(cfg.Path)
	if errors.Is(err, ErrCorrupt) && cfg.ResetCorrupt {
		aside, merr := moveAside(cfg.Path, cfg.Now())
		if merr != nil {
			return nil, errors.Join(err, merr)
		}
		cfg.Logger.Warn("sqlite store: corrupt result database moved aside; starting empty", "path", cfg.Path, "moved_to", aside, "err", err)
		db, err = openDB(cfg.Path)
	}
	if err != nil {
		return nil, err
	}
	s := &Store{
		cfg:      cfg,
		history:  cfg.History,
		db:       db,
		mem:      store.NewMemory(cfg.History, cfg.MaxPackets),
		ops:      make(chan op, cfg.Buffer),
		done:     make(chan struct{}),
		infoIDs:  make(map[weak.Pointer[store.SessionInfo]]int64),
		payload:  newCompactPayload(),
		infos:    make(map[int64]weak.Pointer[store.SessionInfo]),
		lastWarn: make(map[string]time.Time),
	}
	if s.history <= 0 {
		s.history = defaultHistory
	}
	if err := s.sweep(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	go s.run()
	return s, nil
}

// load fills the memory with the latest History results of each session.
func (s *Store) load() error {
	rows, err := s.db.Query(`SELECT DISTINCT session FROM results ORDER BY session`)
	if err != nil {
		return fmt.Errorf("sqlite store: load: %w", err)
	}
	var sessions []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sqlite store: load: %w", err)
		}
		sessions = append(sessions, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("sqlite store: load: %w", err)
	}
	for _, name := range sessions {
		events, err := s.query(`SELECT `+columns+` FROM (SELECT id, `+columns+` FROM results WHERE session = ? ORDER BY time_ns DESC, id DESC LIMIT ?) ORDER BY time_ns, id`, name, s.history)
		if err != nil {
			return fmt.Errorf("sqlite store: load %q: %w", name, err)
		}
		for _, e := range events {
			s.mem.Publish(e)
		}
	}
	return nil
}

// Publish implements store.ResultSink: the memory and the watchers get e at
// once, and the summaries and run errors of named sessions are queued for
// the database. When the writer is Config.Buffer results behind, e is not
// written and is counted in Stats.Dropped. The Summary and Error of e must
// not be modified afterwards.
func (s *Store) Publish(e store.Event) {
	s.mem.Publish(e)
	if !kept(&e) {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.dropped.Add(1)
		return
	}
	select {
	case s.ops <- op{event: &e}:
	default:
		s.dropped.Add(1)
		s.warn("dropped", "sqlite store: writer behind; results not kept", "dropped", s.dropped.Load())
	}
}

// Recent implements store.Store from memory: at most Config.History results.
func (s *Store) Recent(session string, n int) []store.Event { return s.mem.Recent(session, n) }

// Packets implements store.Store from memory.
func (s *Store) Packets(session string) []store.PacketSample { return s.mem.Packets(session) }

// Sessions implements store.Store from memory, which Open fills with every
// session the database has results of.
func (s *Store) Sessions() []string { return s.mem.Sessions() }

// Watch implements store.Store.
func (s *Store) Watch(opts store.WatchOptions) *store.Watcher { return s.mem.Watch(opts) }

// Range implements store.Store from the database, after the writer has
// caught up: it reaches back as far as the retention, beyond History.
func (s *Store) Range(session string, from, to time.Time) []store.Event {
	if !s.queue(op{}) {
		return s.mem.Range(session, from, to)
	}
	events, err := s.query(`SELECT `+columns+` FROM results WHERE session = ? AND time_ns >= ? AND time_ns < ? ORDER BY time_ns, id`,
		session, from.UnixNano(), to.UnixNano())
	if err != nil {
		s.warn("read", "sqlite store: read failed; answering from memory", "session", session, "err", err)
		return s.mem.Range(session, from, to)
	}
	return events
}

// Delete implements store.Store: the session's results go from memory at
// once and from the database in order with the writes queued before.
func (s *Store) Delete(session string) {
	s.mem.Delete(session)
	s.queue(op{deleted: session})
}

// Stats returns the counters.
func (s *Store) Stats() Stats {
	return Stats{Written: s.written.Load(), Dropped: s.dropped.Load(), Failed: s.failed.Load()}
}

// Close writes what is queued and closes the database. Results published
// afterwards are dropped. It may be called more than once.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.ops)
	s.mu.Unlock()
	<-s.done
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("sqlite store: close: %w", err)
	}
	return nil
}

// queue hands o to the writer and waits until it and everything before it
// is done. It reports false when the store is closed.
func (s *Store) queue(o op) bool {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return false
	}
	o.done = make(chan struct{})
	s.ops <- o
	s.mu.RUnlock()
	<-o.done
	return true
}

// run is the writer: it commits the queued operations in batches and
// sweeps expired results, until Close closes ops.
func (s *Store) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.cfg.Sweep)
	defer ticker.Stop()
	for {
		select {
		case o, ok := <-s.ops:
			if !ok {
				return
			}
			s.batch(o)
		case <-ticker.C:
			if err := s.sweep(); err != nil {
				s.warn("sweep", "sqlite store: retention sweep failed", "err", err)
			}
		}
	}
}

// batch applies first and whatever else is queued, up to batchSize, in one
// transaction, then wakes the callers waiting on them.
func (s *Store) batch(first op) {
	ops := []op{first}
loop:
	for len(ops) < batchSize {
		select {
		case o, ok := <-s.ops:
			if !ok {
				break loop
			}
			ops = append(ops, o)
		default:
			break loop
		}
	}
	if err := s.apply(ops); err != nil {
		s.warn("write", "sqlite store: write failed; results not kept", "err", err)
	}
	for _, o := range ops {
		if o.done != nil {
			close(o.done)
		}
	}
}

// apply writes ops in one transaction. A result that cannot be encoded or
// inserted is counted and skipped; a failed commit loses the batch.
func (s *Store) apply(ops []op) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.countFailed(ops)
		return err
	}
	var (
		written uint64
		errs    []error
		// The descriptions this transaction writes are known to the
		// caches only once it commits (Codex M12).
		pending = map[weak.Pointer[store.SessionInfo]]int64{}
	)
	for _, o := range ops {
		switch {
		case o.event != nil:
			if err := s.insert(ctx, tx, o.event, pending); err != nil {
				s.failed.Add(1)
				errs = append(errs, err)
				continue
			}
			written++
		case o.deleted != "":
			if _, err := tx.ExecContext(ctx, `DELETE FROM results WHERE session = ?`, o.deleted); err != nil {
				errs = append(errs, err)
			}
		case o.sweep:
			// Done after the commit, outside the transaction.
		}
	}
	if err := tx.Commit(); err != nil {
		s.failed.Add(written)
		return errors.Join(append(errs, err)...)
	}
	s.written.Add(written)
	s.remember(pending)
	for _, o := range ops {
		if o.sweep {
			errs = append(errs, s.sweep())
		}
	}
	return errors.Join(errs...)
}

func (s *Store) countFailed(ops []op) {
	for _, o := range ops {
		if o.event != nil {
			s.failed.Add(1)
		}
	}
}

// insert writes one result, with its session description.
func (s *Store) insert(ctx context.Context, tx *sql.Tx, e *store.Event, pending map[weak.Pointer[store.SessionInfo]]int64) error {
	infoID, err := s.infoID(ctx, tx, e.Session, pending)
	if err != nil {
		return err
	}
	data, err := encodePayload(s.payload, e)
	if err != nil {
		return fmt.Errorf("encode result of %q: %w", e.Session.Name, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO results (session, kind, time_ns, run_id, sid, info_id, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Session.Name, int(e.Kind), e.Time.UnixNano(), e.RunID, e.SID, infoID, data)
	return err
}

// infoID returns the row of a session description, writing it the first
// time. Equal descriptions share a row. A row written now goes in pending,
// which the caches take over once the transaction commits.
func (s *Store) infoID(ctx context.Context, tx *sql.Tx, info *store.SessionInfo, pending map[weak.Pointer[store.SessionInfo]]int64) (int64, error) {
	key := weak.Make(info)
	if id, ok := s.infoIDs[key]; ok {
		return id, nil
	}
	if id, ok := pending[key]; ok {
		return id, nil
	}
	data, digest, err := encodeInfo(info, s.cfg.Definitions)
	if err != nil {
		s.warn("definition", "sqlite store: session definition not kept", "err", err)
		if data == nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO infos (digest, data) VALUES (?, ?)`, digest, data); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM infos WHERE digest = ?`, digest).Scan(&id); err != nil {
		return 0, err
	}
	pending[key] = id
	return id, nil
}

// remember takes the descriptions of a committed transaction into the
// caches: the writer's, and the readers', where the publisher's description
// is the one readers see too.
func (s *Store) remember(written map[weak.Pointer[store.SessionInfo]]int64) {
	if len(written) == 0 {
		return
	}
	if len(s.infoIDs) > maxCached {
		pruneWriterCache(s.infoIDs)
	}
	maps.Copy(s.infoIDs, written)
	s.infosMu.Lock()
	defer s.infosMu.Unlock()
	for key, id := range written {
		if cur, ok := s.infos[id]; !ok || cur.Value() == nil {
			s.infos[id] = key
		}
	}
	if len(s.infos) > maxCached {
		pruneReaderCache(s.infos)
	}
}

// maxCached is the size of a description cache above which its entries of
// collected descriptions are dropped.
const maxCached = 1024

// pruneWriterCache drops the entries of collected descriptions.
func pruneWriterCache(m map[weak.Pointer[store.SessionInfo]]int64) {
	maps.DeleteFunc(m, func(k weak.Pointer[store.SessionInfo], _ int64) bool { return k.Value() == nil })
}

// pruneReaderCache drops the entries of collected descriptions.
func pruneReaderCache(m map[int64]weak.Pointer[store.SessionInfo]) {
	maps.DeleteFunc(m, func(_ int64, v weak.Pointer[store.SessionInfo]) bool { return v.Value() == nil })
}

// columns are the columns query reads, in its order.
const columns = `kind, time_ns, run_id, sid, info_id, data, session`

// row is one result as read, before its description is resolved.
type row struct {
	e       store.Event
	infoID  int64
	data    []byte
	session string
}

// query reads results, sharing one SessionInfo per description. The rows
// are read to the end first: the one connection serves the descriptions
// next.
func (s *Store) query(q string, args ...any) ([]store.Event, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	var read []row
	for rows.Next() {
		var (
			r    row
			kind int
			ns   int64
		)
		if err := rows.Scan(&kind, &ns, &r.e.RunID, &r.e.SID, &r.infoID, &r.data, &r.session); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.e.Kind, r.e.Time = store.EventKind(kind), time.Unix(0, ns) //nolint:gosec // G115: kinds written from store.EventKind
		read = append(read, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var out []store.Event
	for _, r := range read {
		if r.e.Session, err = s.info(r.infoID); err != nil {
			return nil, err
		}
		if err := decodePayload(&r.e, r.data); err != nil {
			s.warn("decode", "sqlite store: unreadable result skipped", "session", r.session, "err", err)
			continue
		}
		out = append(out, r.e)
	}
	return out, nil
}

// info returns the shared description of row id, reading it while no one
// holds it. The database is read without infosMu: the writer, holding the
// one connection, may need it (Codex M10).
func (s *Store) info(id int64) (*store.SessionInfo, error) {
	if info := s.cached(id); info != nil {
		return info, nil
	}
	var data []byte
	if err := s.db.QueryRow(`SELECT data FROM infos WHERE id = ?`, id).Scan(&data); err != nil {
		return nil, fmt.Errorf("session description %d: %w", id, err)
	}
	info, err := decodeInfo(data, s.cfg.Definitions)
	if err != nil {
		s.warn("definition", "sqlite store: session definition not restored", "err", err)
		if info == nil {
			return nil, err
		}
	}
	s.infosMu.Lock()
	defer s.infosMu.Unlock()
	// Another reader or the writer may have shared one meanwhile.
	if cur := s.infos[id].Value(); cur != nil {
		return cur, nil
	}
	s.infos[id] = weak.Make(info)
	if len(s.infos) > maxCached {
		pruneReaderCache(s.infos)
	}
	return info, nil
}

// cached returns the shared description of row id, or nil.
func (s *Store) cached(id int64) *store.SessionInfo {
	s.infosMu.Lock()
	defer s.infosMu.Unlock()
	return s.infos[id].Value()
}

// sweep deletes the results older than the retention and the descriptions
// no result uses, and warns when the files outgrow Config.MaxSize.
func (s *Store) sweep() error {
	var errs []error
	if s.cfg.Retention > 0 {
		cutoff := s.cfg.Now().Add(-s.cfg.Retention).UnixNano()
		if _, err := s.db.Exec(`DELETE FROM results WHERE time_ns < ?`, cutoff); err != nil {
			errs = append(errs, err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM infos WHERE id NOT IN (SELECT info_id FROM results)`); err != nil {
		errs = append(errs, err)
	}
	// The writer's cache may name deleted descriptions; ids are never reused.
	clear(s.infoIDs)
	s.infosMu.Lock()
	pruneReaderCache(s.infos)
	s.infosMu.Unlock()
	if size := s.fileSize(); s.cfg.MaxSize > 0 && size > s.cfg.MaxSize {
		s.cfg.Logger.Warn("sqlite store: result database above its size limit; shorten store.sqlite.retention",
			"path", s.cfg.Path, "size", size, "max_size", s.cfg.MaxSize)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("sqlite store: sweep: %w", err)
	}
	return nil
}

// fileSize returns the size of the database and its WAL.
func (s *Store) fileSize() int64 {
	var size int64
	for _, p := range []string{s.cfg.Path, s.cfg.Path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			size += fi.Size()
		}
	}
	return size
}

// warn logs msg at most once per warnEvery for each topic.
func (s *Store) warn(topic, msg string, args ...any) {
	s.warnMu.Lock()
	now := time.Now()
	if last, ok := s.lastWarn[topic]; ok && now.Sub(last) < warnEvery {
		s.warnMu.Unlock()
		return
	}
	s.lastWarn[topic] = now
	s.warnMu.Unlock()
	s.cfg.Logger.Warn(msg, args...)
}
