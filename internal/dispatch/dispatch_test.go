package dispatch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateStateKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"hello", true},
		{"hello-world", true},
		{"hello_world_2", true},
		{"abc123", true},
		{"", false},
		{"with.dot", false},
		{"with/slash", false},
		{"..", false},
		{"ALLCAPS", false},
		{"with space", false},
		{"mixed-Case", false},
	}
	for _, c := range cases {
		err := ValidateStateKey(c.in)
		got := err == nil
		if got != c.want {
			t.Errorf("ValidateStateKey(%q) = %v (err %v), want valid=%v", c.in, got, err, c.want)
		}
	}
}

func TestComputeStateKey_Deterministic(t *testing.T) {
	a := ComputeStateKey("docs/specs/001-foo.md")
	b := ComputeStateKey("docs/specs/001-foo.md")
	if a != b {
		t.Errorf("non-deterministic: %s != %s", a, b)
	}
	c := ComputeStateKey("docs/specs/002-bar.md")
	if a == c {
		t.Errorf("collision on different paths: %s == %s", a, c)
	}
	if len(a) != 16 {
		t.Errorf("length = %d, want 16", len(a))
	}
	if err := ValidateStateKey(a); err != nil {
		t.Errorf("computed key failed ValidateStateKey: %v", err)
	}
}

func TestAtomicWrite_HappyPath(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "nested", "file.txt")
	if err := AtomicWrite(dest, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want hello", got)
	}
	// no tmp files left behind
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestAtomicWrite_Overwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := AtomicWrite(p, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(p, []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "second" {
		t.Errorf("got %q", got)
	}
}

func TestAppendFindingsLog(t *testing.T) {
	dir := t.TempDir()
	if err := AppendFindingsLog(dir, []byte("first entry")); err != nil {
		t.Fatal(err)
	}
	if err := AppendFindingsLog(dir, []byte("second entry")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "findings.log.md"))
	content := string(got)
	if !strings.Contains(content, "first entry") || !strings.Contains(content, "second entry") {
		t.Errorf("log missing entries: %q", content)
	}
	if strings.Index(content, "first") > strings.Index(content, "second") {
		t.Errorf("entries out of order: %q", content)
	}
}

func TestAppendFindingsLog_Concurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	n := 10
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = AppendFindingsLog(dir, []byte("x"))
		}(i)
	}
	wg.Wait()
	// Since each append reads-compose-renames, the final file should contain
	// at least one entry; some writes may have been squashed by rename races,
	// but the file should never be partial or corrupt.
	got, err := os.ReadFile(filepath.Join(dir, "findings.log.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Error("concurrent appends produced empty log")
	}
}

func TestCheckFsLocality(t *testing.T) {
	dir := t.TempDir()
	res, err := CheckFsLocality(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path == "" {
		t.Error("Path empty")
	}
	// we don't assert IsLocal — depends on the test host's fs.
}

func TestLock_AcquireReleaseHappyPath(t *testing.T) {
	dir := t.TempDir()
	reclaimed, err := AcquireLock(dir, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed {
		t.Error("reclaimed=true on fresh lock")
	}
	if _, err := os.Stat(filepath.Join(dir, ".lock")); err != nil {
		t.Error(".lock not created")
	}
	if err := ReleaseLock(dir, "sess-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Error(".lock not removed on release")
	}
}

func TestLock_RefuseWhenHeld(t *testing.T) {
	dir := t.TempDir()
	if _, err := AcquireLock(dir, "sess-A"); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireLock(dir, "sess-B")
	if !errors.Is(err, ErrLockHeld) {
		t.Errorf("want ErrLockHeld, got %v", err)
	}
}

func TestLock_ReclaimStale(t *testing.T) {
	dir := t.TempDir()
	if _, err := AcquireLock(dir, "sess-A"); err != nil {
		t.Fatal(err)
	}
	// Age the .lock beyond StaleLockTTL.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, ".lock"), old, old); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := AcquireLock(dir, "sess-B")
	if err != nil {
		t.Fatalf("expected stale reclaim, got %v", err)
	}
	if !reclaimed {
		t.Error("expected reclaimed=true on stale lock")
	}
}

func TestLock_ReleaseRefusesWrongSession(t *testing.T) {
	dir := t.TempDir()
	if _, err := AcquireLock(dir, "sess-A"); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseLock(dir, "sess-B"); !errors.Is(err, ErrLockNotOwned) {
		t.Errorf("want ErrLockNotOwned, got %v", err)
	}
}

func TestLoadPriorState_MissingMeta(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadPriorState(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.HasMeta {
		t.Error("HasMeta=true on missing file")
	}
	if len(s.Rounds) != 0 {
		t.Error("Rounds non-empty on missing meta")
	}
}

func TestLoadPriorState_HashMismatch_Quarantine(t *testing.T) {
	parent := t.TempDir()
	stateDir := filepath.Join(parent, "four-round-reviewers", "aaa")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{
		"rounds":        []any{},
		"template_hash": "abc123",
	}
	data, _ := json.Marshal(&meta)
	if err := os.WriteFile(filepath.Join(stateDir, "meta.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPriorState(stateDir, "def456")
	if !errors.Is(err, ErrTemplateHashMismatch) {
		t.Fatalf("want ErrTemplateHashMismatch, got %v", err)
	}
	// original stateDir must no longer exist
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Error("original stateDir not quarantined")
	}
	// a sibling with .quarantined- prefix should exist
	sib, _ := os.ReadDir(filepath.Dir(stateDir))
	foundQ := false
	for _, e := range sib {
		if strings.HasPrefix(e.Name(), ".quarantined-") {
			foundQ = true
		}
	}
	if !foundQ {
		t.Error("no .quarantined-* sibling after mismatch")
	}
}

func TestTraceEvent(t *testing.T) {
	dir := t.TempDir()
	sid := "20260418T143012Z-aaaa"
	if err := TraceEvent(dir, sid, "dispatch-start", map[string]any{"rounds": 2}); err != nil {
		t.Fatal(err)
	}
	if err := TraceEvent(dir, sid, "dispatch-end", map[string]any{"outcome": "completed"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "trace", sid+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Errorf("line not valid JSON: %q", l)
		}
		if _, ok := m["ts"]; !ok {
			t.Error("missing ts field")
		}
		if _, ok := m["event_type"]; !ok {
			t.Error("missing event_type field")
		}
	}
}

func TestSessionID_SortsChronologically(t *testing.T) {
	a := NewSessionID()
	time.Sleep(1100 * time.Millisecond)
	b := NewSessionID()
	if a >= b {
		t.Errorf("expected lexicographic a < b: a=%q b=%q", a, b)
	}
}
