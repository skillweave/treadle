package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/skillweave/treadle/internal/dispatch"
	"github.com/skillweave/treadle/internal/parser"
)

// Version is baked in at build time via -ldflags "-X main.Version=0.1.0".
var Version = "dev"

type subcommand struct {
	name    string
	summary string
	run     func(args []string) error
}

var subcommands []subcommand

func init() {
	subcommands = []subcommand{
		{"version", "Print treadle version and exit.", cmdVersion},
		{"parse-project", "Parse a .loom/project.md file; emit JSON to stdout.", cmdParseProject},
		{"parse-team", "Parse a team template; emit JSON to stdout. Input: path to .md.", cmdParseTeam},
		{"parse-agent", "Parse an agent file; emit JSON frontmatter to stdout.", cmdParseAgent},
		{"validate-state-key", "Exit 0 iff <key> matches [a-z0-9_-]+ with no dots.", cmdValidateStateKey},
		{"compute-state-key", "Print sha256(<repo-relative-path>)[:16] to stdout.", cmdComputeStateKey},
		{"check-fs-locality", "Check whether <path>'s filesystem supports atomic rename; emit JSON to stdout.", cmdCheckFsLocality},
		{"atomic-write", "Read stdin, write atomically to <dest-path>.", cmdAtomicWrite},
		{"append-findings-log", "Append stdin to findings.log.md under <state-dir>.", cmdAppendFindingsLog},
		{"acquire-lock", "Acquire advisory lock in <state-dir>; --session-id=<id>.", cmdAcquireLock},
		{"release-lock", "Release advisory lock in <state-dir>; takes <session-id>.", cmdReleaseLock},
		{"load-state", "Read meta.json under <state-dir>; emit JSON to stdout.", cmdLoadState},
		{"save-meta", "Read stdin (JSON with rounds + template_hash); atomically save to <state-dir>/meta.json.", cmdSaveMeta},
		{"trace", "Append JSONL event to <state-dir>/trace/<session-id>.jsonl. Positional: <state-dir> <session-id> <event-type> [--json-fields=<json>].", cmdTrace},
		{"new-session-id", "Print a fresh time-prefixed session id to stdout.", cmdNewSessionID},
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	name := os.Args[1]
	if name == "-h" || name == "--help" || name == "help" {
		usage()
		os.Exit(0)
	}
	for _, c := range subcommands {
		if c.name == name {
			if err := c.run(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "treadle: "+err.Error())
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "treadle: unknown command %q\n\n", name)
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprintf(os.Stderr, "treadle v%s — helper binary for the loom plugin\n\n", Version)
	fmt.Fprintln(os.Stderr, "Usage: treadle <command> [args...]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	for _, c := range subcommands {
		fmt.Fprintf(os.Stderr, "  %-22s %s\n", c.name, c.summary)
	}
}

func cmdVersion(args []string) error {
	fmt.Println(Version)
	return nil
}

func cmdParseProject(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: treadle parse-project <path>")
	}
	r, err := parser.ParseProjectMd(args[0])
	if err != nil {
		return err
	}
	return writeJSON(r)
}

func cmdParseTeam(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: treadle parse-team <path>")
	}
	t, err := parser.ParseTeamMd(args[0])
	if err != nil {
		return err
	}
	return writeJSON(t)
}

func cmdParseAgent(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: treadle parse-agent <path>")
	}
	a, err := parser.ParseAgentMd(args[0])
	if err != nil {
		return err
	}
	return writeJSON(a)
}

func cmdValidateStateKey(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: treadle validate-state-key <key>")
	}
	return dispatch.ValidateStateKey(args[0])
}

func cmdComputeStateKey(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: treadle compute-state-key <repo-relative-path>")
	}
	fmt.Println(dispatch.ComputeStateKey(args[0]))
	return nil
}

func cmdCheckFsLocality(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: treadle check-fs-locality <path>")
	}
	r, err := dispatch.CheckFsLocality(args[0])
	if err != nil {
		return err
	}
	return writeJSON(r)
}

func cmdAtomicWrite(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: treadle atomic-write <dest-path>   (content on stdin)")
	}
	return dispatch.AtomicWriteReader(args[0], os.Stdin)
}

func cmdAppendFindingsLog(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: treadle append-findings-log <state-dir>   (block on stdin)")
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	return dispatch.AppendFindingsLog(args[0], body)
}

func cmdAcquireLock(args []string) error {
	fs := flag.NewFlagSet("acquire-lock", flag.ContinueOnError)
	sessionID := fs.String("session-id", "", "session id to record; auto-generated if absent")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return errors.New("usage: treadle acquire-lock [--session-id=<id>] <state-dir>")
	}
	sid := *sessionID
	if sid == "" {
		sid = dispatch.NewSessionID()
	}
	reclaimed, err := dispatch.AcquireLock(rest[0], sid)
	if err != nil {
		if errors.Is(err, dispatch.ErrLockHeld) {
			fmt.Fprintln(os.Stderr, "lock held by another session")
			os.Exit(1)
		}
		return err
	}
	fmt.Println(sid)
	if reclaimed {
		fmt.Fprintln(os.Stderr, "note: reclaimed stale lock")
	}
	return nil
}

func cmdReleaseLock(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: treadle release-lock <state-dir> <session-id>")
	}
	return dispatch.ReleaseLock(args[0], args[1])
}

func cmdLoadState(args []string) error {
	fs := flag.NewFlagSet("load-state", flag.ContinueOnError)
	expectedHash := fs.String("expected-template-hash", "", "if set, reject+quarantine when stored hash differs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return errors.New("usage: treadle load-state [--expected-template-hash=<hash>] <state-dir>")
	}
	s, err := dispatch.LoadPriorState(rest[0], *expectedHash)
	if err != nil {
		return err
	}
	return writeJSON(s)
}

func cmdSaveMeta(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: treadle save-meta <state-dir>   (stdin: {\"rounds\":[...], \"template_hash\":\"...\"})")
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	var payload struct {
		Rounds       []json.RawMessage `json:"rounds"`
		TemplateHash string            `json:"template_hash"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("stdin is not valid JSON: %w", err)
	}
	return dispatch.SaveMeta(args[0], payload.TemplateHash, payload.Rounds)
}

func cmdTrace(args []string) error {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	jsonFields := fs.String("json-fields", "", "JSON object of extra fields to attach to the event")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 3 {
		return errors.New("usage: treadle trace [--json-fields=<json>] <state-dir> <session-id> <event-type>")
	}
	fields := map[string]any{}
	if *jsonFields != "" {
		if err := json.Unmarshal([]byte(*jsonFields), &fields); err != nil {
			return fmt.Errorf("--json-fields is not valid JSON: %w", err)
		}
	}
	return dispatch.TraceEvent(rest[0], rest[1], rest[2], fields)
}

func cmdNewSessionID(args []string) error {
	fmt.Println(dispatch.NewSessionID())
	return nil
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return nil
}

// ensureAbs resolves relative paths against the current working dir
// before handing them off — important because treadle is invoked from
// inside a skill's Bash tool, which may chdir before exec.
func ensureAbs(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, p), nil
}

// keep ensureAbs referenced from at least one place so `go vet` doesn't
// warn about unused helper during migration periods
var _ = strings.Split
var _ = ensureAbs
