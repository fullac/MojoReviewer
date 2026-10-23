package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put("acme/web#1", Record{Repo: "acme/web", Number: 1, HeadSHA: "abc", SessionID: "sess"}); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.Get("acme/web#1")
	if !ok || got.SessionID != "sess" || got.HeadSHA != "abc" {
		t.Fatalf("记录错误: %+v %v", got, ok)
	}
}

func TestWorkerIDSurvivesSessionUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme/web#7", Record{Repo: "acme/web", Number: 7, SessionID: "sess", WorkerID: PinWorker(2)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme/web#7", Record{Repo: "acme/web", Number: 7, SessionID: "sess-2"}); err != nil {
		t.Fatal(err)
	}
	rec, ok := s.Get("acme/web#7")
	if !ok || rec.SessionID != "sess-2" {
		t.Fatalf("record=%+v ok=%v", rec, ok)
	}
	if worker, set := rec.Worker(); !set || worker != 2 {
		t.Fatalf("worker pin lost: %d %v", worker, set)
	}
}

// worker 0 是合法值：读取时不能被当成「没写」。
func TestWorkerZeroIsARecordedPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme/web#7", Record{Repo: "acme/web", Number: 7, SessionID: "sess", WorkerID: PinWorker(0)}); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := s.Get("acme/web#7")
	if worker, set := rec.Worker(); !set || worker != 0 {
		t.Fatalf("worker 0 was read back as unset: %d %v", worker, set)
	}
	if err := s.Put("acme/web#7", Record{Repo: "acme/web", Number: 7, SessionID: "sess-2", WorkerID: PinWorker(3)}); err != nil {
		t.Fatal(err)
	}
	if worker, _ := mustWorker(t, s, "acme/web#7"); worker != 3 {
		t.Fatalf("re-pin to another worker was ignored: %d", worker)
	}
}

// 会话字段以传入值为准：worker 换了以后必须能把旧会话清掉。
func TestPutClearsSessionsExplicitly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme/web#7", Record{
		Repo: "acme/web", Number: 7, SessionID: "main", WorkerID: PinWorker(2),
		RoleSessions: map[string]string{"map": "role"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme/web#7", Record{Repo: "acme/web", Number: 7, WorkerID: PinWorker(0)}); err != nil {
		t.Fatal(err)
	}
	rec, _ := s.Get("acme/web#7")
	if rec.SessionID != "" || len(rec.RoleSessions) != 0 {
		t.Fatalf("stale sessions survived an explicit reset: %+v", rec)
	}
	if worker, _ := rec.Worker(); worker != 0 {
		t.Fatalf("worker=%d", worker)
	}
}

func mustWorker(t *testing.T, s *Store, key string) (int, bool) {
	t.Helper()
	rec, ok := s.Get(key)
	if !ok {
		t.Fatalf("record %s missing", key)
	}
	return rec.Worker()
}

func TestAutomaticHeadsSurviveSessionUpdatesAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, head := range []string{"A", "B"} {
		reserved, err := s.ReserveAutoHead("acme/web#1", Record{Repo: "acme/web", Number: 1, HeadSHA: head})
		if err != nil || !reserved {
			t.Fatalf("reserve %s=%v err=%v", head, reserved, err)
		}
	}
	// An explicit review of C keeps the session current but doesn't count as an
	// automatic attempt and must preserve the reservations for A and B.
	if err := s.Put("acme/web#1", Record{Repo: "acme/web", Number: 1, HeadSHA: "C", SessionID: "continued"}); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasAutoHead("acme/web#1", "A") || !s.HasAutoHead("acme/web#1", "B") || s.HasAutoHead("acme/web#1", "C") {
		t.Fatal("automatic attempts were confused with the latest session head")
	}
	if reserved, err := s.ReserveAutoHead("acme/web#1", Record{HeadSHA: "A"}); err != nil || reserved {
		t.Fatal("returning to A should not start another automatic attempt", err)
	}
	rec, _ := s.Get("acme/web#1")
	if rec.HeadSHA != "C" || rec.SessionID != "continued" || !slices.Equal(rec.AutoHeads, []string{"A", "B"}) {
		t.Fatalf("record changed: %+v", rec)
	}
	rec.AutoHeads[0] = "caller mutation"
	if !s.HasAutoHead("acme/web#1", "A") {
		t.Fatal("Get exposed mutable store state")
	}
}

func TestOpenMigratesLegacyHeadAndRejectsReadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	if err := os.WriteFile(path, []byte(`{"acme/web#1":{"repo":"acme/web","number":1,"head_sha":"old","session_id":"continued"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil || !s.HasAutoHead("acme/web#1", "old") {
		t.Fatal("legacy reservation lost", err)
	}
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("storage read failure treated as an empty database")
	}
}

func TestExplicitSessionHeadIsNotMigratedAsAutomaticAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme/web#1", Record{HeadSHA: "explicit-head", SessionID: "continued"}); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil || s.HasAutoHead("acme/web#1", "explicit-head") {
		t.Fatal("new session record was mistaken for a legacy automatic reservation", err)
	}
}

func TestFailedAtomicSaveRollsBackMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme/web#1", Record{HeadSHA: "old", SessionID: "original"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A directory destination forces rename to fail after the temp file is written.
	s.path = filepath.Join(filepath.Dir(path), "blocked")
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme/web#1", Record{HeadSHA: "new"}); err == nil {
		t.Fatal("failed rename was accepted")
	}
	if _, err := s.ReserveAutoHead("acme/web#1", Record{HeadSHA: "new"}); err == nil {
		t.Fatal("failed automatic reservation was accepted")
	}
	rec, _ := s.Get("acme/web#1")
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) || rec.HeadSHA != "old" || rec.SessionID != "original" || s.HasAutoHead("acme/web#1", "new") {
		t.Fatalf("failed save changed durable or in-memory record: %+v err=%v", rec, err)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".blocked-*"))
	if err != nil || len(temps) != 0 {
		t.Fatal("temporary snapshot leaked", temps, err)
	}
}

func TestReadersAlwaysSeeCompletePrivateSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("acme/web#1", Record{HeadSHA: "initial"}); err != nil {
		t.Fatal(err)
	}
	done, readDone, readErr := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		defer close(readDone)
		for {
			select {
			case <-done:
				return
			default:
			}
			raw, err := os.ReadFile(path)
			var snapshot map[string]Record
			if err == nil {
				err = json.Unmarshal(raw, &snapshot)
			}
			if err != nil || snapshot["acme/web#1"].HeadSHA == "" {
				readErr <- fmt.Errorf("incomplete snapshot: %s: %v", raw, err)
				return
			}
		}
	}()
	for i := 0; i < 30; i++ {
		if err := s.Put("acme/web#1", Record{HeadSHA: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	<-readDone
	select {
	case err := <-readErr:
		t.Fatal(err)
	default:
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("review records are not private", err)
	}
}
