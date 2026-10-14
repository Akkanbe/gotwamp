// Package jsonl writes results as JSON Lines (SPEC §9.7): one record per
// line, written by one goroutine behind a buffered channel, with rotation by
// size and by day.
package jsonl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Akkanbe/gotwamp/internal/store"
)

// Defaults of SPEC §9.2.
const (
	defaultMaxSize  = 100 << 20
	defaultMaxFiles = 10
	defaultBuffer   = 4096
)

// reopenDelay is the pause between attempts to open the file again.
const reopenDelay = time.Second

// openFile is os.OpenFile; tests replace it to make opening fail.
var openFile = os.OpenFile

// Records selects the record types to write.
type Records uint8

// Record types, combinable.
//
//declscope:ignore overexported // an enum the daemon builds with ParseRecords; the values document Config.Records
const (
	RunSummary Records = 1 << iota
	WindowSummary
	RunError
	Packet
)

// DefaultRecords is the records list of SPEC §9.2.
const DefaultRecords = RunSummary | WindowSummary | RunError

// ParseRecords parses the export.jsonl.records list.
func ParseRecords(names []string) (Records, error) {
	var r Records
	for _, n := range names {
		switch n {
		case "run_summary":
			r |= RunSummary
		case "window_summary":
			r |= WindowSummary
		case "run_error":
			r |= RunError
		case "packet":
			r |= Packet
		default:
			return 0, fmt.Errorf("jsonl: unknown record type %q", n)
		}
	}
	return r, nil
}

func (r Records) has(k store.EventKind) bool {
	switch k {
	case store.EventRunSummary:
		return r&RunSummary != 0
	case store.EventWindowSummary:
		return r&WindowSummary != 0
	case store.EventRunError:
		return r&RunError != 0
	case store.EventPacket:
		return r&Packet != 0
	default:
		return false
	}
}

// Config parameterizes a Writer.
type Config struct {
	// Path is the file written to. Rotated files are Path.1 (the newest)
	// to Path.MaxFiles.
	Path string
	// Records selects what is written. Zero means DefaultRecords.
	Records Records
	// MaxSize rotates the file before a record would take it beyond this
	// many octets. Zero means 100 MiB; negative disables it.
	MaxSize int64
	// MaxFiles is how many rotated files are kept. Zero means 10.
	MaxFiles int
	// Daily also rotates the file at the first record of each day.
	Daily bool
	// Buffer is the capacity of the record channel. Zero means 4096.
	Buffer int
	// Location is the time zone of the record times and of the day
	// boundary. Nil means time.Local.
	Location *time.Location
	// Now returns the current time for the day boundary. Nil means
	// time.Now.
	Now func() time.Time
	// Logger receives a warning when writing starts failing and a notice
	// when it recovers. Nil discards them.
	Logger *slog.Logger
}

// Writer writes events as JSON Lines. It implements store.ResultSink.
type Writer struct {
	cfg Config

	mu     sync.RWMutex
	closed bool
	ch     chan store.Event
	done   chan struct{}
	// abort is closed when Shutdown's deadline passes; cur is the open
	// file, which Shutdown then closes under the writer goroutine.
	abort     chan struct{}
	abortOnce sync.Once
	cur       atomic.Pointer[os.File]

	dropped atomic.Uint64
	written atomic.Uint64
	lastErr atomic.Pointer[error]

	// The fields below belong to the writer goroutine. f is nil while a
	// reopen is pending.
	retryAt time.Time
	// failing is set from a write, open or rotation failure until the file
	// is open again.
	failing bool
	// pending counts the records in buf, not yet known to be written.
	pending uint64
	f       *os.File
	buf     *bufio.Writer
	size    int64
	day     time.Time
}

// Open opens or creates cfg.Path for appending and starts the writer
// goroutine.
func Open(cfg Config) (*Writer, error) {
	if cfg.Path == "" {
		return nil, errors.New("jsonl: path is empty")
	}
	if cfg.Records == 0 {
		cfg.Records = DefaultRecords
	}
	if cfg.MaxSize == 0 {
		cfg.MaxSize = defaultMaxSize
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = defaultMaxFiles
	}
	if cfg.Buffer <= 0 {
		cfg.Buffer = defaultBuffer
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	w := &Writer{cfg: cfg, ch: make(chan store.Event, cfg.Buffer), done: make(chan struct{}), abort: make(chan struct{})}
	if err := w.open(); err != nil {
		return nil, err
	}
	go w.loop()
	return w, nil
}

// Publish queues e if its type is selected. It never blocks: when the
// buffer is full, or the Writer is closed, e is dropped and counted
// (jsonl_dropped_total).
func (w *Writer) Publish(e store.Event) {
	if !w.cfg.Records.has(e.Kind) {
		return
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		w.dropped.Add(1)
		return
	}
	select {
	case w.ch <- e:
	default:
		w.dropped.Add(1)
	}
}

// Dropped returns the number of records dropped.
func (w *Writer) Dropped() uint64 { return w.dropped.Load() }

// Written returns the number of records written out to the file: counted
// when a flush of the buffer that held them succeeds.
func (w *Writer) Written() uint64 { return w.written.Load() }

// Err returns the last write or rotation error, or nil.
func (w *Writer) Err() error {
	if p := w.lastErr.Load(); p != nil {
		return *p
	}
	return nil
}

// shutdownGrace is how long Shutdown waits for the writer goroutine after
// closing the file under it.
const shutdownGrace = 100 * time.Millisecond

// Close is Shutdown(context.Background()): it writes every queued record and
// closes the file.
func (w *Writer) Close() error {
	return w.Shutdown(context.Background())
}

// Shutdown stops the Writer within ctx (SPEC §9.1: gotwampd stops within
// its shutdown budget). It writes the queued records until ctx is done,
// then drops the rest (counted in Dropped), closes the file at once, which
// interrupts a write blocked on a pipe, a FIFO or a socket, and returns.
// It returns nil when every queued record was written, ctx.Err (wrapped)
// when the deadline cut it short, or else the last write error. Records
// published afterwards are dropped. It may be called more than once.
//
// The writer goroutine ends as soon as its blocked write fails. Only a
// write the kernel does not interrupt (a regular file on a hung network
// file system) keeps it until the system call returns; Shutdown waits for
// it no longer than shutdownGrace.
func (w *Writer) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		return w.Err()
	case <-ctx.Done():
	}
	w.abortOnce.Do(func() { close(w.abort) })
	if f := w.cur.Load(); f != nil {
		_ = f.Close()
	}
	t := time.NewTimer(shutdownGrace)
	defer t.Stop()
	select {
	case <-w.done:
	case <-t.C:
	}
	return fmt.Errorf("jsonl: shutdown: %w", ctx.Err())
}

func (w *Writer) aborted() bool {
	select {
	case <-w.abort:
		return true
	default:
		return false
	}
}

// fail records err as the last error, and logs it when writing was fine
// until now: a failing output fails for every record, and one line says it.
func (w *Writer) fail(err error) {
	w.lastErr.Store(&err)
	if !w.failing {
		w.failing = true
		w.cfg.Logger.Warn("JSON Lines output failing; records are dropped until it recovers",
			"path", w.cfg.Path, "error", err)
	}
}

// recovered clears the last error once the file could be opened again or
// rotated.
func (w *Writer) recovered() {
	w.lastErr.Store(nil)
	if w.failing {
		w.failing = false
		w.cfg.Logger.Info("JSON Lines output recovered", "path", w.cfg.Path, "dropped_total", w.dropped.Load())
	}
}

// discard gives the file up after a failed write: the buffer would return
// the same error for every later record, and the file never grow to its
// rotation. The next record after reopenDelay opens it again (SPEC §12.2).
func (w *Writer) discard() {
	_ = w.f.Close()
	w.f = nil
	w.cur.Store(nil)
	w.retryAt = w.cfg.Now().Add(reopenDelay)
}

func (w *Writer) loop() {
	defer close(w.done)
	for e := range w.ch {
		if w.aborted() {
			w.dropped.Add(1) // past Shutdown's deadline
			continue
		}
		w.write(e)
		if len(w.ch) == 0 && w.f != nil {
			if err := w.flush(); err != nil {
				w.fail(fmt.Errorf("jsonl: write: %w", err))
				w.discard()
			}
		}
	}
	if w.f == nil || w.aborted() {
		// Shutdown closed the file: what the buffer held is lost.
		w.dropped.Add(w.pending)
		w.pending = 0
		return
	}
	if err := w.flush(); err != nil {
		w.fail(fmt.Errorf("jsonl: write: %w", err))
	}
	if err := w.f.Close(); err != nil {
		w.fail(fmt.Errorf("jsonl: close: %w", err))
	}
}

// flush writes the buffer out and settles the records it held: written
// when the flush succeeds, dropped when it fails. A record the buffer wrote
// out on its own before a later failure is counted as dropped too.
func (w *Writer) flush() error {
	err := w.buf.Flush()
	if err != nil {
		w.dropped.Add(w.pending)
	} else {
		w.written.Add(w.pending)
	}
	w.pending = 0
	return err
}

// reopen tries to open the file again after a failed rotation or write, at
// most once per reopenDelay. It reports whether there is a file to write to; a
// successful reopen clears Err.
func (w *Writer) reopen() bool {
	now := w.cfg.Now()
	if now.Before(w.retryAt) {
		return false
	}
	if err := w.open(); err != nil {
		w.fail(err)
		w.retryAt = now.Add(reopenDelay)
		return false
	}
	w.recovered()
	return true
}

func (w *Writer) write(e store.Event) {
	line, err := encode(e, w.cfg.Location)
	if err != nil {
		// A record that cannot be encoded is a bug, not a failing output.
		w.dropped.Add(1)
		w.lastErr.Store(&err)
		w.cfg.Logger.Error("JSON Lines record not encodable; dropped", "kind", e.Kind.String(),
			"session", e.Session.Name, "run_id", e.RunID, "error", err)
		return
	}
	if w.f != nil && w.dueRotation(int64(len(line))) {
		if err := w.rotate(); err != nil {
			w.fail(err)
		} else {
			w.recovered()
		}
	}
	// Without a file, after a rotation that could not open the new one,
	// records are dropped until a reopen succeeds (SPEC §12.2).
	if w.f == nil && !w.reopen() {
		w.dropped.Add(1)
		return
	}
	n, err := w.buf.Write(line)
	w.size += int64(n)
	if err != nil {
		// The buffer keeps the error: everything it held is lost.
		w.dropped.Add(w.pending + 1)
		w.pending = 0
		w.fail(fmt.Errorf("jsonl: write: %w", err))
		w.discard()
		return
	}
	w.pending++
}

func (w *Writer) dueRotation(next int64) bool {
	if w.cfg.MaxSize > 0 && w.size > 0 && w.size+next > w.cfg.MaxSize {
		return true
	}
	return w.cfg.Daily && w.size > 0 && !w.day.Equal(w.today())
}

func (w *Writer) today() time.Time {
	y, m, d := w.cfg.Now().In(w.cfg.Location).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, w.cfg.Location)
}

// open opens the file for appending. An existing file keeps the day of its
// last modification.
func (w *Writer) open() error {
	f, err := openFile(w.cfg.Path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640) //nolint:gosec // G302: log shippers in the daemon's group read the results
	if err != nil {
		return fmt.Errorf("jsonl: open: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("jsonl: open: %w", err)
	}
	w.f, w.size, w.day = f, st.Size(), w.today()
	w.cur.Store(f)
	if w.size > 0 {
		y, m, d := st.ModTime().In(w.cfg.Location).Date()
		w.day = time.Date(y, m, d, 0, 0, 0, 0, w.cfg.Location)
	}
	if w.buf == nil {
		w.buf = bufio.NewWriterSize(f, 64<<10)
	} else {
		w.buf.Reset(f)
	}
	return nil
}

// rotate shifts Path.i to Path.i+1, drops the oldest, moves Path to Path.1
// and opens a new Path. The file is reopened even when a rename fails, so
// that writing goes on.
func (w *Writer) rotate() error {
	err := w.flush()
	err = errors.Join(err, w.f.Close())
	w.f = nil
	w.cur.Store(nil)
	name := func(i int) string { return w.cfg.Path + "." + strconv.Itoa(i) }
	if rerr := os.Remove(name(w.cfg.MaxFiles)); !errors.Is(rerr, os.ErrNotExist) {
		err = errors.Join(err, rerr)
	}
	for i := w.cfg.MaxFiles - 1; i >= 1; i-- {
		if rerr := os.Rename(name(i), name(i+1)); !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}
	err = errors.Join(err, os.Rename(w.cfg.Path, name(1)), w.open())
	if err != nil {
		return fmt.Errorf("jsonl: rotate: %w", err)
	}
	return nil
}
