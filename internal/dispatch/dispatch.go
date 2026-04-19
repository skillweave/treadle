package dispatch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var (
	ErrInvalidStateKey     = errors.New("state_key is not valid")
	ErrLockHeld            = errors.New("lock held by another session")
	ErrLockNotOwned        = errors.New("lock is not owned by the provided session id")
	ErrTemplateHashMismatch = errors.New("template hash mismatch — prior state quarantined")
)

var stateKeyRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// ValidateStateKey returns nil iff s matches [a-z0-9_-]+ with no dots.
// Dots are explicitly rejected to prevent `..` path traversal.
func ValidateStateKey(s string) error {
	if s == "" {
		return fmt.Errorf("%w: empty", ErrInvalidStateKey)
	}
	if strings.Contains(s, ".") {
		return fmt.Errorf("%w: contains dot", ErrInvalidStateKey)
	}
	if !stateKeyRe.MatchString(s) {
		return fmt.Errorf("%w: must match [a-z0-9_-]+ (got %q)", ErrInvalidStateKey, s)
	}
	return nil
}

// ComputeStateKey returns sha256(repoRelativePath)[:16] as a hex string.
// The input is used verbatim — callers that need path canonicalization
// should do it before calling.
func ComputeStateKey(repoRelPath string) string {
	h := sha256.Sum256([]byte(repoRelPath))
	return hex.EncodeToString(h[:])[:16]
}

// AtomicWrite writes content to dest by first writing to a sibling tmp
// file, fsyncing, and renaming into place. Destination's parent dir is
// created if missing.
func AtomicWrite(dest string, content []byte) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-atomicwrite-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // best-effort cleanup on error paths

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dest)
}

// AtomicWriteReader streams from r into a tmp file, then renames.
func AtomicWriteReader(dest string, r io.Reader) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-atomicwrite-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dest)
}

// AppendFindingsLog atomically appends block to findings.log.md under
// stateDir. On concurrent writes the outer file is always consistent
// because each append reads → compose → rename.
func AppendFindingsLog(stateDir string, block []byte) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(stateDir, "findings.log.md")

	existing, err := os.ReadFile(logPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.Write(block)
	if !bytes.HasSuffix(block, []byte("\n")) {
		buf.WriteByte('\n')
	}
	return AtomicWrite(logPath, buf.Bytes())
}

// CheckFsLocalityResult describes whether the path is on a filesystem
// that supports atomic rename / advisory locks safely. Non-local =
// warning only; callers decide whether to continue.
type CheckFsLocalityResult struct {
	Path      string `json:"path"`
	IsLocal   bool   `json:"is_local"`
	FsType    string `json:"fs_type"`
	Mountpoint string `json:"mountpoint,omitempty"`
	Note      string `json:"note,omitempty"`
}

// CheckFsLocality inspects the filesystem backing `path`. On Linux it
// parses /proc/self/mountinfo and matches the longest mountpoint prefix.
// On Darwin it currently returns a conservative "unknown" result.
// Non-Linux / non-Darwin platforms report unknown-but-proceed with a
// note.
func CheckFsLocality(path string) (*CheckFsLocalityResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	res := &CheckFsLocalityResult{Path: abs, IsLocal: true, FsType: "unknown"}

	if runtime.GOOS != "linux" {
		res.Note = fmt.Sprintf("locality detection not implemented for %s; assuming local", runtime.GOOS)
		return res, nil
	}

	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		res.Note = "unable to read /proc/self/mountinfo; assuming local"
		return res, nil
	}
	type mount struct {
		mountpoint string
		fstype     string
	}
	mounts := []mount{}
	for _, line := range strings.Split(string(data), "\n") {
		// mountinfo format (7+): id parent major:minor root mp opts dash fstype src ...
		parts := strings.Fields(line)
		if len(parts) < 10 {
			continue
		}
		dash := -1
		for i, p := range parts {
			if p == "-" {
				dash = i
				break
			}
		}
		if dash < 0 || dash+1 >= len(parts) {
			continue
		}
		mp := parts[4]
		fstype := parts[dash+1]
		mounts = append(mounts, mount{mountpoint: mp, fstype: fstype})
	}
	best := mount{}
	for _, m := range mounts {
		if strings.HasPrefix(abs, m.mountpoint) && len(m.mountpoint) > len(best.mountpoint) {
			best = m
		}
	}
	if best.mountpoint == "" {
		res.Note = "no matching mountpoint; assuming local"
		return res, nil
	}
	res.Mountpoint = best.mountpoint
	res.FsType = best.fstype

	switch best.fstype {
	case "ext2", "ext3", "ext4", "btrfs", "xfs", "zfs", "tmpfs", "f2fs":
		res.IsLocal = true
	case "nfs", "nfs4", "cifs", "smbfs", "smb3", "9p", "fuse.sshfs", "virtiofs":
		res.IsLocal = false
		res.Note = "non-local filesystem — atomic rename and advisory locks may not be reliable"
	case "overlay", "aufs":
		res.IsLocal = false
		res.Note = "overlay filesystem — atomic rename semantics may not be preserved across lower/upper layers"
	default:
		// unknown fstypes: warn but don't hard-fail
		res.IsLocal = false
		res.Note = fmt.Sprintf("unrecognized filesystem type %q — cannot guarantee atomicity", best.fstype)
	}
	return res, nil
}

// Lockfile describes an active advisory lock.
type Lockfile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"session_id"`
	StartedAt string `json:"started_at"`
}

// StaleLockTTL is how old an existing lock can be before AcquireLock
// reclaims it. One hour by default; spec §Teams primitive step 5.
var StaleLockTTL = 1 * time.Hour

// AcquireLock places a .lock file in stateDir containing the given
// session_id + current pid + timestamp. If a lock already exists:
//   - If its mtime is newer than now - StaleLockTTL, return ErrLockHeld.
//   - If stale, reclaim with a note via the returned `reclaimed` bool.
func AcquireLock(stateDir, sessionID string) (reclaimed bool, err error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return false, err
	}
	lockPath := filepath.Join(stateDir, ".lock")

	info, statErr := os.Stat(lockPath)
	if statErr == nil {
		if time.Since(info.ModTime()) < StaleLockTTL {
			return false, ErrLockHeld
		}
		reclaimed = true
		// fall through to overwrite
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, statErr
	}

	body := Lockfile{
		PID:       os.Getpid(),
		SessionID: sessionID,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(&body, "", "  ")
	if err != nil {
		return false, err
	}
	if err := AtomicWrite(lockPath, data); err != nil {
		return false, err
	}
	return reclaimed, nil
}

// ReleaseLock removes the .lock file in stateDir only if the stored
// session_id matches the provided id.
func ReleaseLock(stateDir, sessionID string) error {
	lockPath := filepath.Join(stateDir, ".lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // nothing to release
		}
		return err
	}
	var body Lockfile
	if err := json.Unmarshal(data, &body); err != nil {
		// corrupt lock — remove it and warn
		_ = os.Remove(lockPath)
		return fmt.Errorf("lock file corrupt, removed: %w", err)
	}
	if body.SessionID != sessionID {
		return ErrLockNotOwned
	}
	return os.Remove(lockPath)
}

// PriorState represents what a prior dispatch persisted.
type PriorState struct {
	Rounds       []json.RawMessage `json:"rounds"`
	TemplateHash string            `json:"template_hash"`
	FindingsLogBytes int64         `json:"findings_log_bytes"`
	HasMeta      bool              `json:"has_meta"`
}

// LoadPriorState reads meta.json + findings.log.md under stateDir. If
// meta.json is missing it returns an empty PriorState with HasMeta=false.
// If template hash mismatches the provided expectedHash, the stateDir is
// quarantined (renamed to .quarantined-<timestamp>/) and ErrTemplateHashMismatch
// is returned.
func LoadPriorState(stateDir, expectedHash string) (*PriorState, error) {
	s := &PriorState{Rounds: []json.RawMessage{}}

	metaPath := filepath.Join(stateDir, "meta.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(metaData, s); err != nil {
		return nil, fmt.Errorf("meta.json corrupt in %s: %w", stateDir, err)
	}
	s.HasMeta = true

	if expectedHash != "" && s.TemplateHash != "" && s.TemplateHash != expectedHash {
		if err := quarantineStateDir(stateDir); err != nil {
			return nil, fmt.Errorf("%w, and quarantine failed: %v", ErrTemplateHashMismatch, err)
		}
		return nil, ErrTemplateHashMismatch
	}

	logPath := filepath.Join(stateDir, "findings.log.md")
	if info, err := os.Stat(logPath); err == nil {
		s.FindingsLogBytes = info.Size()
	}
	return s, nil
}

func quarantineStateDir(stateDir string) error {
	parent := filepath.Dir(stateDir)
	base := filepath.Base(stateDir)
	target := filepath.Join(parent, fmt.Sprintf(".quarantined-%s-%s", time.Now().UTC().Format("20060102T150405Z"), base))
	return os.Rename(stateDir, target)
}

// SaveMeta atomically writes meta.json under stateDir with the provided
// rounds payload and template hash.
func SaveMeta(stateDir, templateHash string, rounds []json.RawMessage) error {
	meta := struct {
		Rounds       []json.RawMessage `json:"rounds"`
		TemplateHash string            `json:"template_hash"`
	}{
		Rounds:       rounds,
		TemplateHash: templateHash,
	}
	data, err := json.MarshalIndent(&meta, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(stateDir, "meta.json"), data)
}

// TraceEvent appends a single JSONL line to
// stateDir/trace/<sessionID>.jsonl. Fields are serialized alongside the
// provided event_type and an ISO8601 UTC timestamp.
func TraceEvent(stateDir, sessionID, eventType string, fields map[string]any) error {
	traceDir := filepath.Join(stateDir, "trace")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		return err
	}
	tracePath := filepath.Join(traceDir, sessionID+".jsonl")

	payload := map[string]any{
		"ts":         time.Now().UTC().Format(time.RFC3339),
		"event_type": eventType,
	}
	for k, v := range fields {
		payload[k] = v
	}
	line, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	// Trace lines are appended with O_APPEND — not strict atomic, but
	// each line is POSIX-atomic-small-write if under PIPE_BUF. For
	// durability we fsync after each append.
	f, err := os.OpenFile(tracePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return err
	}
	return f.Sync()
}

// NewSessionID returns a time-prefixed, hash-suffixed session id that
// sorts chronologically lexicographically. Format: 20260418T143012Z-<8hex>.
func NewSessionID() string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	seed := fmt.Sprintf("%s-%d-%d", ts, os.Getpid(), time.Now().UnixNano())
	h := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%s-%s", ts, hex.EncodeToString(h[:4]))
}
