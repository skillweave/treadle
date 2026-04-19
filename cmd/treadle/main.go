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
		{"dispatch-init", "Read dispatch args JSON on stdin; bundle parse+validate+policy+state+lock+prior-state+trace-start; emit JSON to stdout.", cmdDispatchInit},
		{"round-init", "Read {members, team_key} on stdin; mint team_name + atomically trace round-start/dispatches/kickoffs; emit JSON to stdout.", cmdRoundInit},
		{"round-finalize", "Read {round, findings, members_succeeded, members_degraded, ...} on stdin; sort+render synthesis, append findings log, update meta, trace round-end.", cmdRoundFinalize},
		{"dispatch-end", "Read {outcome, error_reason} on stdin; flatten findings across rounds, render final synthesis, release lock, trace dispatch-end.", cmdDispatchEnd},
		{"spec-review-prep", "Resolve project_root + canonicalize spec path + parse .loom/project.md + read spec + compute state_key + build dispatch-team args; emit JSON to stdout.", cmdSpecReviewPrep},
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
	if err := fs.Parse(reorderFlags(args)); err != nil {
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
	if err := fs.Parse(reorderFlags(args)); err != nil {
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
	if err := fs.Parse(reorderFlags(args)); err != nil {
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

func cmdDispatchInit(args []string) error {
	fs := flag.NewFlagSet("dispatch-init", flag.ContinueOnError)
	pluginRoot := fs.String("plugin-root", os.Getenv("LOOM_PLUGIN_ROOT"), "absolute path to the plugin root (where teams/ lives); defaults to $LOOM_PLUGIN_ROOT")
	projectRoot := fs.String("project-root", "", "absolute path to the project root (where .loom/ lives); walks up from cwd if empty")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var in dispatch.DispatchInitArgs
	dec := json.NewDecoder(strings.NewReader(string(stdin)))
	dec.UseNumber()
	if err := dec.Decode(&in); err != nil {
		return fmt.Errorf("stdin is not valid JSON: %w", err)
	}
	res, err := dispatch.DispatchInit(&in, dispatch.DispatchInitOpts{
		PluginRoot:  *pluginRoot,
		ProjectRoot: *projectRoot,
	})
	if err != nil {
		return err
	}
	return writeJSON(res)
}

func cmdRoundInit(args []string) error {
	fs := flag.NewFlagSet("round-init", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "absolute path to the state dir (required)")
	sessionID := fs.String("session-id", "", "session id from dispatch-init (required)")
	round := fs.Int("round", 0, "round number, 1-indexed (required)")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var in dispatch.RoundInitArgs
	if err := json.Unmarshal(stdin, &in); err != nil {
		return fmt.Errorf("stdin is not valid JSON: %w", err)
	}
	res, err := dispatch.RoundInit(&in, dispatch.RoundInitOpts{
		StateDir:  *stateDir,
		SessionID: *sessionID,
		Round:     *round,
	})
	if err != nil {
		return err
	}
	return writeJSON(res)
}

func cmdRoundFinalize(args []string) error {
	fs := flag.NewFlagSet("round-finalize", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "absolute path to the state dir (required)")
	sessionID := fs.String("session-id", "", "session id from dispatch-init (required)")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var in dispatch.RoundFinalizeArgs
	if err := json.Unmarshal(stdin, &in); err != nil {
		return fmt.Errorf("stdin is not valid JSON: %w", err)
	}
	res, err := dispatch.RoundFinalize(&in, dispatch.RoundFinalizeOpts{
		StateDir:  *stateDir,
		SessionID: *sessionID,
	})
	if err != nil {
		return err
	}
	return writeJSON(res)
}

func cmdSpecReviewPrep(args []string) error {
	fs := flag.NewFlagSet("spec-review-prep", flag.ContinueOnError)
	projectRoot := fs.String("project-root", "", "absolute path to the project root (where .loom/ lives); walks up from cwd (with git-toplevel fallback) if empty")
	team := fs.String("team", "four-round-reviewers", "team-template name to target")
	policyJSON := fs.String("policy-overrides-json", "", "JSON object of policy overrides to pass through to dispatch-team")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 {
		return errors.New("usage: treadle spec-review-prep [--project-root=<abs>] [--team=<name>] [--policy-overrides-json=<json>] <spec-path>")
	}
	opts := dispatch.SpecReviewPrepOpts{
		SpecPath:    rest[0],
		ProjectRoot: *projectRoot,
		Team:        *team,
	}
	if strings.TrimSpace(*policyJSON) != "" {
		var overrides map[string]any
		if err := json.Unmarshal([]byte(*policyJSON), &overrides); err != nil {
			return fmt.Errorf("--policy-overrides-json is not valid JSON: %w", err)
		}
		opts.PolicyOverrides = overrides
	}
	res, err := dispatch.SpecReviewPrep(opts)
	if err != nil {
		return err
	}
	return writeJSON(res)
}

func cmdDispatchEnd(args []string) error {
	fs := flag.NewFlagSet("dispatch-end", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "absolute path to the state dir (required)")
	sessionID := fs.String("session-id", "", "session id from dispatch-init (required)")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var in dispatch.DispatchEndArgs
	// stdin is optional — empty body = default outcome.
	if len(strings.TrimSpace(string(stdin))) > 0 {
		if err := json.Unmarshal(stdin, &in); err != nil {
			return fmt.Errorf("stdin is not valid JSON: %w", err)
		}
	}
	res, err := dispatch.DispatchEnd(&in, dispatch.DispatchEndOpts{
		StateDir:  *stateDir,
		SessionID: *sessionID,
	})
	if err != nil {
		return err
	}
	return writeJSON(res)
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return nil
}

// reorderFlags moves tokens that look like flags (leading "-") to the
// front of the slice. The Go stdlib flag package stops parsing at the
// first positional argument, so callers that place flags after
// positionals silently lose the flag. Reordering before Parse lets
// skills invoke `treadle trace <dir> <sid> <event> --json-fields=<j>`
// or `--json-fields=<j> <dir> <sid> <event>` interchangeably.
//
// Only handles the --flag=value form. Two-token --flag value form is
// not preserved (the value would be misclassified as positional). All
// treadle subcommands use the = form; keep it that way.
func reorderFlags(args []string) []string {
	flags := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))
	for _, a := range args {
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	return append(flags, positional...)
}

// keep filepath referenced even when nothing below uses it (some
// platforms' build tags may prune helpers).
var _ = filepath.Join
var _ = strings.Split
