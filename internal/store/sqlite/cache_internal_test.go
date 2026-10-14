//declscope:namespace sqlite

package sqlite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
)

// within fails t unless done closes within 5 s.
func within(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish: deadlock", what)
	}
}

func resultOf(info *store.SessionInfo, at time.Time, runID string) store.Event {
	return store.Event{Kind: store.EventRunSummary, Time: at, Session: info, RunID: runID, Summary: &sender.Summary{}}
}

// TestReaderWriterLockOrder (Codex M10): a reader looking up a description
// no one holds waits for the one connection while the writer, holding it
// in a transaction, writes a new description. Both, and the store after
// them, finish.
func TestReaderWriterLockOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	plain := DefinitionCodec{Encode: func(d any) ([]byte, error) { return json.Marshal(d) }, Decode: func(b []byte) (any, error) {
		var s string
		return s, json.Unmarshal(b, &s)
	}}
	s, err := Open(Config{Path: path, History: 1, Retention: -1, Definitions: plain})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_790_985_600, 0)
	s.Publish(resultOf(&store.SessionInfo{Name: "a", Target: "old", Definition: "old"}, t0, "1"))
	s.Publish(resultOf(&store.SessionInfo{Name: "a", Target: "new", Definition: "new"}, t0.Add(time.Minute), "2"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	entered, release := make(chan struct{}), make(chan struct{})
	gated := plain
	gated.Encode = func(d any) ([]byte, error) {
		if d == "third" {
			close(entered)
			<-release // the writer holds the connection in its transaction
		}
		return json.Marshal(d)
	}
	s, err = Open(Config{Path: path, History: 1, Retention: -1, Definitions: gated})
	if err != nil {
		t.Fatal(err)
	}
	var oldID int64
	if err := s.db.QueryRow(`SELECT info_id FROM results WHERE run_id = '1'`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if s.cached(oldID) != nil {
		t.Fatal("the old description is cached: the test needs it cold")
	}
	s.Publish(resultOf(&store.SessionInfo{Name: "a", Target: "third", Definition: "third"}, t0.Add(2*time.Minute), "3"))
	<-entered
	read := make(chan struct{})
	go func() {
		defer close(read)
		if info, err := s.info(oldID); err != nil || info.Target != "old" {
			t.Errorf("info = %+v, %v", info, err)
		}
	}()
	time.Sleep(100 * time.Millisecond) // the reader waits for the connection
	close(release)
	within(t, "the reader", read)
	ranged := make(chan struct{})
	go func() {
		defer close(ranged)
		if got := s.Range("a", t0, t0.Add(time.Hour)); len(got) != 3 {
			t.Errorf("Range = %d results, want 3", len(got))
		}
	}()
	within(t, "Range behind the writer", ranged)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		_ = s.Close()
	}()
	within(t, "Close", closed)
}

// TestDeletedDefinitionsCollected (Codex M11): the description caches keep
// no generation alive once no event holds it: after 100 generations are
// published and deleted, all of them are collected with the store open.
func TestDeletedDefinitionsCollected(t *testing.T) {
	s, err := Open(Config{
		Path: filepath.Join(t.TempDir(), "r.db"), History: 1, Retention: -1,
		Definitions: DefinitionCodec{Encode: func(d any) ([]byte, error) { return json.Marshal(d) }},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var collected atomic.Int32
	t0 := time.Unix(1_790_985_600, 0)
	for i := range 100 {
		info := &store.SessionInfo{Name: "gen", Target: fmt.Sprint(i), Definition: map[string]int{"generation": i}}
		runtime.AddCleanup(info, func(int) { collected.Add(1) }, 0)
		s.Publish(resultOf(info, t0.Add(time.Duration(i)*time.Second), fmt.Sprint(i)))
	}
	_ = s.Range("gen", t0, t0.Add(time.Hour)) // read them back: the reader cache takes them too
	s.Delete("gen")
	s.queue(op{sweep: true})
	for range 50 {
		if runtime.GC(); collected.Load() == 100 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := collected.Load(); n != 100 {
		t.Errorf("%d of 100 deleted generations collected", n)
	}
	var results, infos int
	_ = s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM results), (SELECT COUNT(*) FROM infos)`).Scan(&results, &infos)
	if results != 0 || infos != 0 {
		t.Errorf("database keeps %d results and %d descriptions", results, infos)
	}
}

// TestCommitFailureRecovery (Codex M12): a commit that fails takes back the
// descriptions it wrote; once the cause is gone, the next results of the
// same description are kept, without waiting for a sweep.
func TestCommitFailureRecovery(t *testing.T) {
	s, err := Open(Config{Path: filepath.Join(t.TempDir(), "r.db"), Retention: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// A deferred foreign key that the results of run "boom" break: the
	// insert succeeds, the commit fails.
	for _, q := range []string{
		`CREATE TABLE boom (x INTEGER REFERENCES infos(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER boom AFTER INSERT ON results WHEN NEW.run_id = 'boom' BEGIN INSERT INTO boom VALUES (-1); END`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	info := &store.SessionInfo{Name: "a", Target: "x"}
	t0 := time.Unix(1_790_985_600, 0)
	s.Publish(resultOf(info, t0, "boom"))
	s.queue(op{})
	if st := s.Stats(); st.Written != 0 || st.Failed != 1 {
		t.Fatalf("after the failed commit: %+v", st)
	}
	if _, err := s.db.Exec(`DROP TRIGGER boom`); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		s.Publish(resultOf(info, t0.Add(time.Duration(i+1)*time.Second), fmt.Sprint(i)))
	}
	if got := s.Range("a", t0, t0.Add(time.Hour)); len(got) != 3 {
		t.Errorf("Range = %d results after the cause is gone, want 3", len(got))
	}
	if st := s.Stats(); st.Written != 3 || st.Failed != 1 {
		t.Errorf("stats %+v, want 3 written and the 1 failure", st)
	}
}
