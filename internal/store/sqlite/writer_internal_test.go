//declscope:namespace sqlite

package sqlite

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
)

var info = &store.SessionInfo{Name: "alpha", Mode: "light"}

func summary(at time.Time) store.Event {
	return store.Event{Kind: store.EventRunSummary, Time: at, Session: info, RunID: "r", Summary: &sender.Summary{}}
}

// TestWriterBehind holds the database so that the writer cannot commit:
// Publish drops what does not fit the buffer, warns once, and never blocks.
func TestWriterBehind(t *testing.T) {
	var log bytes.Buffer
	s, err := Open(Config{Path: filepath.Join(t.TempDir(), "r.db"), Buffer: 2, Logger: slog.New(slog.NewTextHandler(&log, nil))})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := s.db.Conn(context.Background()) // the only connection
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 10 {
			s.Publish(summary(now.Add(time.Duration(i))))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked behind the writer")
	}
	_ = conn.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	st := s.Stats()
	if st.Dropped == 0 || st.Written+st.Dropped != 10 || strings.Count(log.String(), "writer behind") != 1 {
		t.Errorf("stats %+v; log %s", st, log.String())
	}
}

// TestSizeWarning checks the warning past MaxSize and a sweep asked through
// the queue.
func TestSizeWarning(t *testing.T) {
	var log bytes.Buffer
	s, err := Open(Config{Path: filepath.Join(t.TempDir(), "r.db"), MaxSize: 1, Logger: slog.New(slog.NewTextHandler(&log, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !strings.Contains(log.String(), "above its size limit") {
		t.Errorf("no warning at Open: %s", log.String())
	}
	log.Reset()
	s.Publish(summary(time.Now()))
	if !s.queue(op{sweep: true}) || !strings.Contains(log.String(), "above its size limit") {
		t.Errorf("no warning after a sweep: %s", log.String())
	}
}

// TestUnwritable counts results that cannot be encoded, and keeps a session
// whose definition cannot be encoded without it.
func TestUnwritable(t *testing.T) {
	codec := DefinitionCodec{
		Encode: func(any) ([]byte, error) { return nil, errors.New("no codec for this") },
		Decode: func([]byte) (any, error) { return nil, errors.New("unused") },
	}
	s, err := Open(Config{Path: filepath.Join(t.TempDir(), "r.db"), Definitions: codec, Retention: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bad := summary(time.Now())
	bad.Summary = nil
	s.Publish(bad)
	defined := &store.SessionInfo{Name: "beta", Definition: struct{}{}}
	s.Publish(store.Event{Kind: store.EventRunSummary, Time: time.Now(), Session: defined, Summary: &sender.Summary{}})
	got := s.Range("beta", time.Time{}, time.Now().Add(time.Hour))
	if st := s.Stats(); st.Failed != 1 || st.Written != 1 || len(got) != 1 {
		t.Errorf("stats %+v, beta %d results", st, len(got))
	}
	// Octets that are not JSON are kept as a string.
	raw := DefinitionCodec{Encode: func(any) ([]byte, error) { return []byte{0xff, 'x'}, nil }}
	if data, _, err := encodeInfo(defined, raw); err != nil || !bytes.Contains(data, []byte(`"definition":"`)) {
		t.Errorf("raw definition: %s, %v", data, err)
	}
	if _, err := decodeInfo([]byte("{"), codec); err == nil {
		t.Error("broken description decoded")
	}
	if info, err := decodeInfo([]byte(`{"name":"x","definition":{}}`), codec); err == nil || info == nil {
		t.Error("an undecodable definition should keep the description and report")
	}
	if err := decodePayload(&store.Event{Kind: store.EventPacket}, nil); err == nil {
		t.Error("packet payload decoded")
	}
	if _, err := encodePayload(newCompactPayload(), &store.Event{Kind: store.EventRunError}); err == nil {
		t.Error("error event without an error encoded")
	}
}
