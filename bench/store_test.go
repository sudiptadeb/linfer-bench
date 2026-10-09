package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stored(t *testing.T, s Store, id string, started time.Time, tags []string, rowTags ...string) Results {
	t.Helper()
	r := sampleResults()
	r.ID, r.Tags, r.Started = id, tags, started
	r.Rows = r.Rows[:1]
	r.Rows[0].Tags = rowTags
	if _, err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	return r
}

// A run's id has its start time and a hash that tells apart two runs
// started in the same second.
func TestNewID(t *testing.T) {
	at := time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)
	a := NewID(at, "x")
	if !strings.HasPrefix(a, "20261009-143000-") || len(a) != len("20261009-143000-")+6 {
		t.Errorf("id %q", a)
	}
	if NewID(at, "y") == a || NewID(at.Add(time.Millisecond), "x") == a {
		t.Error("ids collide")
	}
}

// The store keeps a run under its id; list is newest first; a run is found
// by id or by a unique prefix; tags are edited in place; a tag on a row
// counts as the run's for listing and finding.
func TestStore(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "runs")}
	if rs, err := s.List(); err != nil || len(rs) != 0 {
		t.Fatalf("empty store: %v %v", rs, err)
	}
	t0 := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	stored(t, s, "20261009-140000-aaaaaa", t0, []string{"baseline"})
	stored(t, s, "20261009-150000-bbbbbb", t0.Add(time.Hour), []string{"after"}, "backend:llama")
	stored(t, s, "20261009-160000-cccccc", t0.Add(2*time.Hour), nil, "backend:llama")

	rs, err := s.List()
	if err != nil || len(rs) != 3 || rs[0].ID != "20261009-160000-cccccc" || rs[2].ID != "20261009-140000-aaaaaa" {
		t.Fatalf("list: %v %d", err, len(rs))
	}
	if r, err := s.Find("20261009-15"); err != nil || r.ID != "20261009-150000-bbbbbb" {
		t.Errorf("prefix find: %v %q", err, r.ID)
	}
	if _, err := s.Find("20261009"); err == nil || !strings.Contains(err.Error(), "matches 3 runs") {
		t.Errorf("ambiguous prefix: %v", err)
	}
	if _, err := s.Find("nope"); err == nil || !strings.Contains(err.Error(), "no stored run") {
		t.Errorf("missing: %v", err)
	}

	r, err := s.SetTags("20261009-140000-aaaaaa", []string{"mlx-mtp", "baseline"}, nil)
	if err != nil || strings.Join(r.Tags, ",") != "baseline,mlx-mtp" {
		t.Errorf("add: %v %v", err, r.Tags)
	}
	r, err = s.SetTags("20261009-140000-aaaaaa", nil, []string{"baseline"})
	if err != nil || strings.Join(r.Tags, ",") != "mlx-mtp" {
		t.Errorf("remove: %v %v", err, r.Tags)
	}
	if back, _ := s.Find("20261009-140000-aaaaaa"); strings.Join(back.Tags, ",") != "mlx-mtp" {
		t.Errorf("tags not saved: %v", back.Tags)
	}

	with, err := s.WithTag("backend:llama")
	if err != nil || len(with) != 2 {
		t.Errorf("row tag: %v %d", err, len(with))
	}
	with, _ = s.WithTag("after")
	if len(with) != 1 || with[0].ID != "20261009-150000-bbbbbb" {
		t.Fatalf("run tag: %+v", with)
	}
	if tags := AllTags(with[0]); strings.Join(tags, ",") != "after,backend:llama" {
		t.Errorf("all tags %v", tags)
	}

	// A file that does not read is reported, and the rest still list.
	os.WriteFile(filepath.Join(s.Dir, "junk.json"), []byte("{"), 0o644)
	rs, err = s.List()
	if err == nil || len(rs) != 3 {
		t.Errorf("junk: %v %d", err, len(rs))
	}
	// A run without an id cannot be stored.
	if _, err := s.Put(Results{}); err == nil {
		t.Error("stored a run with no id")
	}
}

// The default store is under XDG_DATA_HOME when that is set.
func TestDefaultStoreDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/x/data")
	if got := DefaultStoreDir(); got != filepath.Join("/x/data", "linfer-bench", "runs") {
		t.Errorf("got %q", got)
	}
}
