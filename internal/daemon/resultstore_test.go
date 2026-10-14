package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// TestSQLiteStore: with store.backend sqlite the summaries outlast a
// restart, which writes the queue out, and GetStatus reports the store.
func TestSQLiteStore(t *testing.T) {
	db := filepath.Join(t.TempDir(), "results.db")
	c := testConfig(t)
	c.Store.Backend, c.Store.SQLite.Path = "sqlite", db
	r := start(t, c, Options{})
	session := &gotwampv1.SessionSpec{
		Name: "kept",
		Spec: &gotwampv1.TestSpec{
			Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_LIGHT, ReceiverPort: proto.Uint32(uint32(r.d.LightAddrs()[0].Port())),
			Count: proto.Uint32(3), Interval: durationpb.New(10 * time.Millisecond), Timeout: durationpb.New(time.Second),
		},
		Schedule: &gotwampv1.Schedule{Frequency: durationpb.New(time.Hour)},
	}
	sender := gotwampv1.NewSenderServiceClient(r.conn)
	if _, err := sender.CreateSession(t.Context(), &gotwampv1.CreateSessionRequest{Spec: session}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first summary", func() bool {
		st, err := sender.GetSessionStats(t.Context(), &gotwampv1.GetSessionStatsRequest{Name: "kept"})
		return err == nil && len(st.GetSummaries()) == 1
	})
	r.stop(t)

	r = start(t, c, Options{})
	defer r.stop(t)
	// The API session is gone with the restart; its results are not.
	st, err := gotwampv1.NewSenderServiceClient(r.conn).GetSessionStats(t.Context(), &gotwampv1.GetSessionStatsRequest{Name: "kept"})
	if err != nil || len(st.GetSummaries()) != 1 || st.GetSummaries()[0].GetReceived() != 3 || st.GetSummaries()[0].GetSpec().GetCount() != 3 {
		t.Fatalf("after a restart: %v, %v", st, err)
	}
	status, err := gotwampv1.NewDaemonServiceClient(r.conn).GetStatus(t.Context(), &gotwampv1.GetStatusRequest{})
	if err != nil || status.GetStore().GetBackend() != "sqlite" || status.GetStore().GetPath() != db {
		t.Errorf("GetStatus store = %v, %v", status.GetStore(), err)
	}
}

// TestMemoryStoreStatus: the default store is reported as memory.
func TestMemoryStoreStatus(t *testing.T) {
	r := start(t, testConfig(t), Options{})
	defer r.stop(t)
	status, err := gotwampv1.NewDaemonServiceClient(r.conn).GetStatus(t.Context(), &gotwampv1.GetStatusRequest{})
	if err != nil || status.GetStore().GetBackend() != "memory" || status.GetStore().GetPath() != "" {
		t.Errorf("GetStatus store = %v, %v", status.GetStore(), err)
	}
	if (&resultStore{backend: "memory"}).stats() != nil {
		t.Error("memory exports database counters")
	}
}

// TestSQLiteStoreStartup: a database that cannot be opened stops the start.
func TestSQLiteStoreStartup(t *testing.T) {
	c := testConfig(t)
	c.Store.Backend, c.Store.SQLite.Path = "sqlite", filepath.Join(t.TempDir(), "missing", "dir", "results.db")
	d, err := Start(t.Context(), c, Options{})
	if err == nil {
		_ = d.Wait()
		t.Fatal("started without a database")
	}
}
