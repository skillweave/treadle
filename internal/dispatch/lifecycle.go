package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/skillweave/treadle/internal/parser"
)

// Finding is the structured per-finding shape used across round-finalize
// and dispatch-end. LLM-produced by the SKILL.md; treadle only sorts
// and renders.
type Finding struct {
	Severity      string   `json:"severity"`
	Location      string   `json:"location"`
	Claim         string   `json:"claim"`
	Reasoning     string   `json:"reasoning,omitempty"`
	Sources       []string `json:"sources,omitempty"`
	Contradiction bool     `json:"contradiction,omitempty"`
	// FirstSurfacedRound is only set by dispatch-end's cross-round
	// annotation. Ignored on input.
	FirstSurfacedRound int `json:"first_surfaced_round,omitempty"`
}

// DegradedMember records why a reviewer didn't complete its round.
type DegradedMember struct {
	Member string `json:"member"`
	Reason string `json:"reason"`
}

// RoundEntry is the structured record stored in meta.json['rounds'].
type RoundEntry struct {
	Round              int              `json:"round"`
	Timestamp          string           `json:"timestamp"`
	TeamName           string           `json:"team_name,omitempty"`
	Members            []string         `json:"members"`
	MembersSucceeded   []string         `json:"members_succeeded"`
	DegradedMembers    []DegradedMember `json:"degraded_members"`
	PeerMessagesCount  int              `json:"peer_messages_count"`
	FindingCount       int              `json:"finding_count"`
	FindingCounts      map[string]int   `json:"finding_counts"`
	SynthesisMD        string           `json:"synthesis_md"`
	Findings           []Finding        `json:"findings"`
}

// =============================================================================
// dispatch-init
// =============================================================================

// DispatchInitArgs is the stdin payload the SKILL.md passes through.
// Mirrors the args-JSON the calling skill (loom:spec-review) sends.
type DispatchInitArgs struct {
	Team            string          `json:"team"`
	StateKey        string          `json:"state_key"`
	ContextBlock    string          `json:"context_block"`
	Subject         DispatchSubject `json:"subject"`
	PolicyOverrides map[string]any  `json:"policy_overrides,omitempty"`
}

// DispatchSubject is the artifact under review. Content is carried here
// only so dispatch-init can validate the path; the SKILL.md composes
// reviewer prompts from its own context, not from the binary.
type DispatchSubject struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// DispatchInitOpts carries flag values (paths that the caller resolves
// from its own environment).
type DispatchInitOpts struct {
	PluginRoot  string // where <plugin>/teams/<team>.md lives; required
	ProjectRoot string // where .loom/ lives; walked-up from cwd if empty
}

// DispatchInitResult is the JSON blob written to stdout.
type DispatchInitResult struct {
	OK                bool                    `json:"ok"`
	Error             string                  `json:"error,omitempty"`
	Kind              string                  `json:"kind,omitempty"`
	SessionID         string                  `json:"session_id,omitempty"`
	StateDir          string                  `json:"state_dir,omitempty"`
	PluginRoot        string                  `json:"plugin_root,omitempty"`
	ProjectRoot       string                  `json:"project_root,omitempty"`
	TeamFile          string                  `json:"team_file,omitempty"`
	TeamName          string                  `json:"team,omitempty"`
	TemplateHash      string                  `json:"template_hash,omitempty"`
	Members           []string                `json:"members,omitempty"`
	OrchestrationBody string                  `json:"orchestration_body,omitempty"`
	ResolvedPolicy    *parser.TeamPolicy      `json:"resolved_policy,omitempty"`
	PriorRounds       []json.RawMessage       `json:"prior_rounds,omitempty"`
	IsLocalFS         bool                    `json:"is_local_fs,omitempty"`
	FsNote            string                  `json:"fs_note,omitempty"`
	FsType            string                  `json:"fs_type,omitempty"`
	LockReclaimed     bool                    `json:"lock_reclaimed,omitempty"`
	QuarantinedPrior  bool                    `json:"quarantined_prior,omitempty"`
	TracePath         string                  `json:"trace_path,omitempty"`
	Warnings          []string                `json:"warnings,omitempty"`
}

// DispatchInit performs alpha.5 SKILL.md Steps 1-7 in one call. It never
// returns an error for caller-recoverable conditions (lock held, hash
// mismatch, validation errors) — those are surfaced via OK=false so the
// SKILL.md only makes one Bash call to discover them.
func DispatchInit(args *DispatchInitArgs, opts DispatchInitOpts) (*DispatchInitResult, error) {
	// 1. Validate required args.
	if args.Team == "" {
		return &DispatchInitResult{OK: false, Error: "args.team is required", Kind: "validation_error"}, nil
	}
	if err := ValidateStateKey(args.StateKey); err != nil {
		return &DispatchInitResult{OK: false, Error: err.Error(), Kind: "validation_error"}, nil
	}
	if args.Subject.Path == "" {
		return &DispatchInitResult{OK: false, Error: "args.subject.path is required", Kind: "validation_error"}, nil
	}
	if filepath.IsAbs(args.Subject.Path) {
		return &DispatchInitResult{OK: false, Error: "subject.path must be repo-relative, not absolute", Kind: "validation_error"}, nil
	}
	cleaned := filepath.Clean(args.Subject.Path)
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") {
		return &DispatchInitResult{OK: false, Error: "subject.path must not traverse parents", Kind: "validation_error"}, nil
	}

	// 2. Resolve plugin root.
	pluginRoot := opts.PluginRoot
	if pluginRoot == "" {
		return &DispatchInitResult{OK: false, Error: "plugin_root is required (pass --plugin-root=<abs> or set LOOM_PLUGIN_ROOT)", Kind: "validation_error"}, nil
	}
	teamFile := filepath.Join(pluginRoot, "teams", args.Team+".md")
	if _, err := os.Stat(teamFile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DispatchInitResult{OK: false, Error: "team template not found: " + teamFile, Kind: "parse_error"}, nil
		}
		return nil, err
	}

	// 3. Parse team.
	team, err := parser.ParseTeamMd(teamFile)
	if err != nil {
		return &DispatchInitResult{OK: false, Error: "parse team: " + err.Error(), Kind: "parse_error"}, nil
	}

	// 4. Resolve policy (caller overrides win, ceiling enforced).
	policy, err := resolvePolicy(team.Policy, args.PolicyOverrides)
	if err != nil {
		return &DispatchInitResult{OK: false, Error: err.Error(), Kind: "validation_error"}, nil
	}

	// 5. Resolve project root (walk up from cwd if not supplied).
	projectRoot := opts.ProjectRoot
	if projectRoot == "" {
		projectRoot = discoverProjectRoot()
	}

	// 6. Compute + create state dir.
	stateDir := filepath.Join(projectRoot, ".loom", "teams", "state", args.Team, args.StateKey)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir state_dir: %w", err)
	}

	// 7. Filesystem locality check (advisory only).
	locality, err := CheckFsLocality(stateDir)
	if err != nil {
		return nil, fmt.Errorf("check fs locality: %w", err)
	}

	// 8. Mint session id + acquire advisory lock.
	sessionID := NewSessionID()
	reclaimed, err := AcquireLock(stateDir, sessionID)
	if err != nil {
		if errors.Is(err, ErrLockHeld) {
			return &DispatchInitResult{OK: false, Error: "lock held by another session; re-run when it finishes", Kind: "lock_held", StateDir: stateDir}, nil
		}
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	// 9. Load prior state (quarantine on template-hash mismatch).
	quarantined := false
	prior, err := LoadPriorState(stateDir, team.TemplateHash)
	if err != nil {
		if errors.Is(err, ErrTemplateHashMismatch) {
			// LoadPriorState already quarantined; recreate the dir and load fresh.
			if mkErr := os.MkdirAll(stateDir, 0o755); mkErr != nil {
				return nil, fmt.Errorf("recreate state_dir post-quarantine: %w", mkErr)
			}
			prior = &PriorState{Rounds: []json.RawMessage{}}
			quarantined = true
		} else {
			// Release the lock before surfacing; the caller can retry.
			_ = ReleaseLock(stateDir, sessionID)
			return &DispatchInitResult{OK: false, Error: "load prior state: " + err.Error(), Kind: "parse_error"}, nil
		}
	}

	// 10. Trace dispatch-start with resolved policy.
	traceFields := map[string]any{
		"team":           args.Team,
		"state_key":      args.StateKey,
		"template_hash":  team.TemplateHash,
		"policy":         policy,
		"prior_rounds":   len(prior.Rounds),
		"quarantined":    quarantined,
		"lock_reclaimed": reclaimed,
		"is_local_fs":    locality.IsLocal,
	}
	if err := TraceEvent(stateDir, sessionID, "dispatch-start", traceFields); err != nil {
		return nil, fmt.Errorf("trace dispatch-start: %w", err)
	}

	tracePath := filepath.Join(stateDir, "trace", sessionID+".jsonl")

	res := &DispatchInitResult{
		OK:                true,
		SessionID:         sessionID,
		StateDir:          stateDir,
		PluginRoot:        pluginRoot,
		ProjectRoot:       projectRoot,
		TeamFile:          teamFile,
		TeamName:          args.Team,
		TemplateHash:      team.TemplateHash,
		Members:           team.Members,
		OrchestrationBody: team.OrchBody,
		ResolvedPolicy:    policy,
		PriorRounds:       prior.Rounds,
		IsLocalFS:         locality.IsLocal,
		FsNote:            locality.Note,
		FsType:            locality.FsType,
		LockReclaimed:     reclaimed,
		QuarantinedPrior:  quarantined,
		TracePath:         tracePath,
	}
	return res, nil
}

func resolvePolicy(template parser.TeamPolicy, overrides map[string]any) (*parser.TeamPolicy, error) {
	out := template
	if v, ok := overrides["max_rounds"]; ok {
		if n, ok := toInt(v); ok {
			out.MaxRounds = n
		}
	}
	if v, ok := overrides["max_agents_parallel"]; ok {
		if n, ok := toInt(v); ok {
			out.MaxAgentsParallel = n
		}
	}
	if v, ok := overrides["abort_on_agent_failure"]; ok {
		if b, ok := v.(bool); ok {
			out.AbortOnAgentFailure = b
		}
	}
	// Ceiling enforcement.
	if template.MaxRoundsCeiling > 0 && out.MaxRounds > template.MaxRoundsCeiling {
		return nil, fmt.Errorf("max_rounds override %d exceeds max_rounds_ceiling %d", out.MaxRounds, template.MaxRoundsCeiling)
	}
	if out.MaxRounds < 1 {
		out.MaxRounds = 1
	}
	return &out, nil
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}

func discoverProjectRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	cur := cwd
	for {
		if _, err := os.Stat(filepath.Join(cur, ".loom", "project.md")); err == nil {
			return cur
		}
		if _, err := os.Stat(filepath.Join(cur, ".loom")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return cwd
}

// =============================================================================
// round-init
// =============================================================================

// RoundInitArgs is the stdin payload.
type RoundInitArgs struct {
	Members []string `json:"members"`
	TeamKey string   `json:"team_key"` // e.g., "four-round-reviewers"
}

// RoundInitOpts carries flag values.
type RoundInitOpts struct {
	StateDir  string
	SessionID string
	Round     int
}

// RoundInitResult is the JSON blob written to stdout.
type RoundInitResult struct {
	OK                 bool   `json:"ok"`
	Error              string `json:"error,omitempty"`
	TeamName           string `json:"team_name,omitempty"`
	KickoffTs          string `json:"kickoff_ts,omitempty"`
	SilenceDeadlineTs  string `json:"silence_deadline_ts,omitempty"`
	RenudgeDeadlineTs  string `json:"renudge_deadline_ts,omitempty"`
	DegradeDeadlineTs  string `json:"degrade_deadline_ts,omitempty"`
}

const (
	// SilenceRenudgeAfter is the wait before sending a single re-nudge
	// to a silent member (alpha.5 bundle item #3).
	SilenceRenudgeAfter = 60 * time.Second
	// SilenceDegradeAfter is the total silence window (from kickoff)
	// before marking a member degraded. Matches alpha.5's 60s renudge
	// + 60s post-renudge = 120s.
	SilenceDegradeAfter = 120 * time.Second
)

// RoundInit atomically emits the round-start + per-member agent-dispatch
// + per-member agent-kickoff trace events and returns the team_name
// the SKILL.md should use when it calls TeamCreate. Timestamps it
// returns are upper-bounds for the actual kickoff (the SKILL.md's
// TeamCreate+Agent+SendMessage tool calls happen after this).
func RoundInit(args *RoundInitArgs, opts RoundInitOpts) (*RoundInitResult, error) {
	if opts.StateDir == "" || opts.SessionID == "" || opts.Round < 1 {
		return &RoundInitResult{OK: false, Error: "state-dir, session-id, and round>=1 are required"}, nil
	}
	if len(args.Members) == 0 {
		return &RoundInitResult{OK: false, Error: "members list must not be empty"}, nil
	}
	if args.TeamKey == "" {
		// Derive from state_dir layout: .../state/<team_key>/<state_key>/
		stateKey := filepath.Base(opts.StateDir)
		args.TeamKey = filepath.Base(filepath.Dir(opts.StateDir))
		_ = stateKey // retained for potential future use; team_name uses session_id for uniqueness
	}

	stateKey := filepath.Base(opts.StateDir)
	shortSession := opts.SessionID
	if len(shortSession) > 8 {
		// Prefer a short hash suffix over the timestamp prefix so team
		// names don't all share the same prefix within one minute.
		parts := strings.Split(shortSession, "-")
		shortSession = parts[len(parts)-1]
		if len(shortSession) > 8 {
			shortSession = shortSession[:8]
		}
	}
	shortStateKey := stateKey
	if len(shortStateKey) > 8 {
		shortStateKey = shortStateKey[:8]
	}
	teamName := fmt.Sprintf("loom-%s-%s-r%d-%s", args.TeamKey, shortStateKey, opts.Round, shortSession)

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	renudgeDeadline := now.Add(SilenceRenudgeAfter).Format(time.RFC3339)
	degradeDeadline := now.Add(SilenceDegradeAfter).Format(time.RFC3339)

	// Trace in a deterministic order: round-start, then dispatches, then kickoffs.
	if err := TraceEvent(opts.StateDir, opts.SessionID, "round-start", map[string]any{
		"round":     opts.Round,
		"team_name": teamName,
		"members":   args.Members,
	}); err != nil {
		return nil, err
	}
	for _, m := range args.Members {
		if err := TraceEvent(opts.StateDir, opts.SessionID, "agent-dispatch", map[string]any{
			"round":     opts.Round,
			"member":    m,
			"team_name": teamName,
		}); err != nil {
			return nil, err
		}
	}
	for _, m := range args.Members {
		if err := TraceEvent(opts.StateDir, opts.SessionID, "agent-kickoff", map[string]any{
			"round":              opts.Round,
			"member":             m,
			"kickoff_ts":         nowStr,
			"renudge_deadline":   renudgeDeadline,
			"degrade_deadline":   degradeDeadline,
		}); err != nil {
			return nil, err
		}
	}

	return &RoundInitResult{
		OK:                true,
		TeamName:          teamName,
		KickoffTs:         nowStr,
		SilenceDeadlineTs: degradeDeadline,
		RenudgeDeadlineTs: renudgeDeadline,
		DegradeDeadlineTs: degradeDeadline,
	}, nil
}

// =============================================================================
// round-finalize
// =============================================================================

// RoundFinalizeArgs is the stdin payload.
type RoundFinalizeArgs struct {
	Round              int              `json:"round"`
	TeamName           string           `json:"team_name,omitempty"`
	MembersSucceeded   []string         `json:"members_succeeded"`
	MembersDegraded    []DegradedMember `json:"members_degraded,omitempty"`
	PeerMessagesCount  int              `json:"peer_messages_count,omitempty"`
	Findings           []Finding        `json:"findings"`
	Rerun              bool             `json:"rerun,omitempty"`
	TemplateHash       string           `json:"template_hash,omitempty"`
}

// RoundFinalizeOpts carries flag values.
type RoundFinalizeOpts struct {
	StateDir  string
	SessionID string
}

// RoundFinalizeResult is the JSON blob written to stdout.
type RoundFinalizeResult struct {
	OK             bool           `json:"ok"`
	Error          string         `json:"error,omitempty"`
	SynthesisMD    string         `json:"synthesis_md,omitempty"`
	FindingCounts  map[string]int `json:"finding_counts,omitempty"`
	TotalFindings  int            `json:"total_findings,omitempty"`
	Degraded       bool           `json:"degraded,omitempty"`
	RoundEntry     *RoundEntry    `json:"round_entry,omitempty"`
	TotalRoundsNow int            `json:"total_rounds_now,omitempty"`
}

// RoundFinalize bundles alpha.5 SKILL.md Step 8.5 + 8.6:
//   - severity-sort findings, render synthesis markdown
//   - append-findings-log block
//   - read/update/save meta.json (replace on rerun, append otherwise)
//   - trace round-end event
func RoundFinalize(args *RoundFinalizeArgs, opts RoundFinalizeOpts) (*RoundFinalizeResult, error) {
	if opts.StateDir == "" || opts.SessionID == "" || args.Round < 1 {
		return &RoundFinalizeResult{OK: false, Error: "state-dir, session-id, and round>=1 are required"}, nil
	}
	// Accept empty findings (round may produce zero findings).

	findings := append([]Finding(nil), args.Findings...)
	SortFindings(findings)
	counts := countFindings(findings)
	degraded := len(args.MembersDegraded) > 0

	allMembers := append([]string(nil), args.MembersSucceeded...)
	for _, dm := range args.MembersDegraded {
		allMembers = append(allMembers, dm.Member)
	}
	sort.Strings(allMembers)

	entry := &RoundEntry{
		Round:             args.Round,
		Timestamp:         time.Now().UTC().Format(time.RFC3339),
		TeamName:          args.TeamName,
		Members:           allMembers,
		MembersSucceeded:  args.MembersSucceeded,
		DegradedMembers:   args.MembersDegraded,
		PeerMessagesCount: args.PeerMessagesCount,
		FindingCount:      len(findings),
		FindingCounts:     counts,
		Findings:          findings,
	}
	entry.SynthesisMD = RenderRoundSynthesis(entry)

	// Append findings log (historical record; never rewritten).
	var logBlock strings.Builder
	fmt.Fprintf(&logBlock, "## %s — %s (round %d)\n\n", entry.Timestamp, opts.SessionID, entry.Round)
	logBlock.WriteString(entry.SynthesisMD)
	logBlock.WriteString("\n")
	if err := AppendFindingsLog(opts.StateDir, []byte(logBlock.String())); err != nil {
		return nil, fmt.Errorf("append findings log: %w", err)
	}

	// Read prior meta, update/replace this round's entry, save atomically.
	prior, err := LoadPriorState(opts.StateDir, "")
	if err != nil {
		return nil, fmt.Errorf("load prior meta: %w", err)
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	rounds := append([]json.RawMessage(nil), prior.Rounds...)
	replaced := false
	if args.Rerun {
		for i, r := range rounds {
			var probe struct {
				Round int `json:"round"`
			}
			if json.Unmarshal(r, &probe) == nil && probe.Round == entry.Round {
				rounds[i] = entryJSON
				replaced = true
				break
			}
		}
	}
	if !replaced {
		rounds = append(rounds, entryJSON)
	}

	templateHash := args.TemplateHash
	if templateHash == "" {
		templateHash = prior.TemplateHash
	}
	if err := SaveMeta(opts.StateDir, templateHash, rounds); err != nil {
		return nil, fmt.Errorf("save meta: %w", err)
	}

	// Trace round-end.
	if err := TraceEvent(opts.StateDir, opts.SessionID, "round-end", map[string]any{
		"round":     entry.Round,
		"degraded":  degraded,
		"findings":  len(findings),
		"rerun":     args.Rerun,
		"team_name": entry.TeamName,
	}); err != nil {
		return nil, err
	}

	return &RoundFinalizeResult{
		OK:             true,
		SynthesisMD:    entry.SynthesisMD,
		FindingCounts:  counts,
		TotalFindings:  len(findings),
		Degraded:       degraded,
		RoundEntry:     entry,
		TotalRoundsNow: len(rounds),
	}, nil
}

// =============================================================================
// dispatch-end
// =============================================================================

// DispatchEndArgs is the stdin payload.
type DispatchEndArgs struct {
	Outcome     string `json:"outcome"`      // completed | exit_early | error
	ErrorReason string `json:"error_reason,omitempty"`
}

// DispatchEndOpts carries flag values.
type DispatchEndOpts struct {
	StateDir  string
	SessionID string
}

// DegradedRoundSummary is the per-round degradation summary returned to
// the caller.
type DegradedRoundSummary struct {
	Round          int              `json:"round"`
	FailedMembers  []string         `json:"failed_members"`
	DegradedBreakdown []DegradedMember `json:"degraded_breakdown"`
}

// DispatchEndResult is the JSON blob written to stdout.
type DispatchEndResult struct {
	OK                bool                    `json:"ok"`
	Error             string                  `json:"error,omitempty"`
	Outcome           string                  `json:"outcome,omitempty"`
	TotalRounds       int                     `json:"total_rounds,omitempty"`
	FinalSynthesisMD  string                  `json:"final_synthesis_md,omitempty"`
	Findings          []Finding               `json:"findings,omitempty"`
	DegradedRounds    []DegradedRoundSummary  `json:"degraded_rounds,omitempty"`
	TracePath         string                  `json:"trace_path,omitempty"`
	StateDir          string                  `json:"state_dir,omitempty"`
}

// DispatchEnd bundles alpha.5 SKILL.md Steps 9 + 10:
//   - read all persisted rounds from meta.json
//   - flatten findings, annotate first_surfaced_round by earliest (claim,location) match
//   - severity-sort, render final synthesis markdown
//   - release lock
//   - trace dispatch-end
func DispatchEnd(args *DispatchEndArgs, opts DispatchEndOpts) (*DispatchEndResult, error) {
	if opts.StateDir == "" || opts.SessionID == "" {
		return &DispatchEndResult{OK: false, Error: "state-dir and session-id are required"}, nil
	}
	outcome := args.Outcome
	if outcome == "" {
		outcome = "completed"
	}

	prior, err := LoadPriorState(opts.StateDir, "")
	if err != nil {
		return nil, fmt.Errorf("load prior state: %w", err)
	}

	rounds := make([]*RoundEntry, 0, len(prior.Rounds))
	for _, raw := range prior.Rounds {
		var entry RoundEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("parse round entry: %w", err)
		}
		rounds = append(rounds, &entry)
	}

	flat := flattenFindings(rounds)
	SortFindings(flat)

	degraded := make([]DegradedRoundSummary, 0)
	for _, r := range rounds {
		if len(r.DegradedMembers) == 0 {
			continue
		}
		failed := make([]string, 0, len(r.DegradedMembers))
		for _, dm := range r.DegradedMembers {
			failed = append(failed, dm.Member)
		}
		degraded = append(degraded, DegradedRoundSummary{
			Round:             r.Round,
			FailedMembers:     failed,
			DegradedBreakdown: r.DegradedMembers,
		})
	}

	synthesis := RenderFinalSynthesis(rounds, flat, degraded)

	// Release the lock before returning.
	if err := ReleaseLock(opts.StateDir, opts.SessionID); err != nil && !errors.Is(err, ErrLockNotOwned) {
		// Log but don't fail the return — the caller has its synthesis.
		_ = TraceEvent(opts.StateDir, opts.SessionID, "lock-release-warning", map[string]any{
			"error": err.Error(),
		})
	}

	// Final trace.
	if err := TraceEvent(opts.StateDir, opts.SessionID, "dispatch-end", map[string]any{
		"outcome":       outcome,
		"total_rounds":  len(rounds),
		"findings":      len(flat),
		"degraded":      len(degraded) > 0,
		"error_reason":  args.ErrorReason,
	}); err != nil {
		return nil, err
	}

	tracePath := filepath.Join(opts.StateDir, "trace", opts.SessionID+".jsonl")
	return &DispatchEndResult{
		OK:               true,
		Outcome:          outcome,
		TotalRounds:      len(rounds),
		FinalSynthesisMD: synthesis,
		Findings:         flat,
		DegradedRounds:   degraded,
		TracePath:        tracePath,
		StateDir:         opts.StateDir,
	}, nil
}

// =============================================================================
// Shared helpers — severity sorting, synthesis rendering
// =============================================================================

// severityRank returns a sort key: HIGH < MED < LOW so HIGH comes first.
func severityRank(s string) int {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "HIGH":
		return 0
	case "MED", "MEDIUM":
		return 1
	case "LOW":
		return 2
	default:
		return 3
	}
}

// SortFindings sorts in-place by severity (HIGH first), then by location,
// then by claim — producing a stable human-readable ordering.
func SortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if a, b := severityRank(f[i].Severity), severityRank(f[j].Severity); a != b {
			return a < b
		}
		if f[i].Location != f[j].Location {
			return f[i].Location < f[j].Location
		}
		return f[i].Claim < f[j].Claim
	})
}

func countFindings(f []Finding) map[string]int {
	c := map[string]int{"HIGH": 0, "MED": 0, "LOW": 0, "OTHER": 0}
	for _, x := range f {
		switch severityRank(x.Severity) {
		case 0:
			c["HIGH"]++
		case 1:
			c["MED"]++
		case 2:
			c["LOW"]++
		default:
			c["OTHER"]++
		}
	}
	return c
}

// RenderRoundSynthesis renders a round's synthesis block in the format
// the four-round-reviewers template Orchestration section documents.
func RenderRoundSynthesis(entry *RoundEntry) string {
	var b strings.Builder
	// Degraded banner (if any) goes at the top of the synthesis block.
	if len(entry.DegradedMembers) > 0 {
		total := len(entry.Members)
		bad := len(entry.DegradedMembers)
		fmt.Fprintf(&b, "⚠ Round %d degraded: %d/%d members failed\n", entry.Round, bad, total)
		for _, dm := range entry.DegradedMembers {
			fmt.Fprintf(&b, "  - %s: %s\n", dm.Member, dm.Reason)
		}
		fmt.Fprintf(&b, "  Synthesis reflects %d/%d coverage.\n\n", total-bad, total)
	}
	fmt.Fprintf(&b, "## Round %d synthesis\n\n", entry.Round)
	b.WriteString("### Findings (merged, deduplicated, severity-sorted)\n\n")
	if len(entry.Findings) == 0 {
		b.WriteString("_No findings this round._\n\n")
	} else {
		for _, f := range entry.Findings {
			writeFindingLine(&b, f)
		}
		b.WriteString("\n")
	}
	b.WriteString("### Meta\n")
	fmt.Fprintf(&b, "- Members: %s\n", strings.Join(entry.Members, ", "))
	if len(entry.DegradedMembers) > 0 {
		parts := make([]string, 0, len(entry.DegradedMembers))
		for _, dm := range entry.DegradedMembers {
			parts = append(parts, fmt.Sprintf("%s (%s)", dm.Member, dm.Reason))
		}
		fmt.Fprintf(&b, "- Degraded members: %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "- Cross-collaboration exchanges: %d\n", entry.PeerMessagesCount)
	return b.String()
}

func writeFindingLine(b *strings.Builder, f Finding) {
	sev := strings.ToUpper(strings.TrimSpace(f.Severity))
	if sev == "" {
		sev = "OTHER"
	}
	tag := "source"
	if len(f.Sources) > 1 {
		tag = "sources"
	}
	sourceText := strings.Join(f.Sources, ", ")
	if sourceText == "" {
		sourceText = "(unknown)"
	}
	provenance := ""
	if f.FirstSurfacedRound > 0 {
		provenance = fmt.Sprintf("; first surfaced: round %d", f.FirstSurfacedRound)
	}
	contradictionTag := ""
	if f.Contradiction {
		contradictionTag = " [contradiction]"
	}
	fmt.Fprintf(b, "**[%s] %s%s**: %s", sev, f.Location, contradictionTag, f.Claim)
	if f.Reasoning != "" {
		fmt.Fprintf(b, ". %s", strings.TrimSuffix(f.Reasoning, "."))
	}
	fmt.Fprintf(b, ".  (%s: %s%s)\n", tag, sourceText, provenance)
}

// RenderFinalSynthesis renders the cross-round merged synthesis. Takes
// already-flattened, severity-sorted findings plus the per-round
// degradation summaries.
func RenderFinalSynthesis(rounds []*RoundEntry, findings []Finding, degraded []DegradedRoundSummary) string {
	var b strings.Builder
	if len(degraded) > 0 {
		b.WriteString("⚠ One or more rounds ran with reduced coverage.\n")
		for _, d := range degraded {
			parts := make([]string, 0, len(d.DegradedBreakdown))
			for _, dm := range d.DegradedBreakdown {
				parts = append(parts, fmt.Sprintf("%s (%s)", dm.Member, dm.Reason))
			}
			fmt.Fprintf(&b, "  - Round %d: %s\n", d.Round, strings.Join(parts, ", "))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## Final synthesis across %d round(s)\n\n", len(rounds))
	b.WriteString("### Findings (deduplicated, severity-sorted)\n\n")
	if len(findings) == 0 {
		b.WriteString("_No findings._\n\n")
	} else {
		for _, f := range findings {
			writeFindingLine(&b, f)
		}
		b.WriteString("\n")
	}
	b.WriteString("### Round metadata\n\n")
	for _, r := range rounds {
		fmt.Fprintf(&b, "- Round %d: %d findings", r.Round, r.FindingCount)
		if len(r.DegradedMembers) > 0 {
			dm := make([]string, 0, len(r.DegradedMembers))
			for _, d := range r.DegradedMembers {
				dm = append(dm, d.Member)
			}
			fmt.Fprintf(&b, " (degraded: %s)", strings.Join(dm, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// flattenFindings walks rounds in order and emits the union of findings,
// annotating each with first_surfaced_round (earliest round where an
// exact (location, claim) match appeared).
func flattenFindings(rounds []*RoundEntry) []Finding {
	type key struct {
		location string
		claim    string
	}
	firstRound := map[key]int{}
	out := make([]Finding, 0)
	for _, r := range rounds {
		for _, f := range r.Findings {
			k := key{location: f.Location, claim: f.Claim}
			firstR, seen := firstRound[k]
			if !seen {
				firstRound[k] = r.Round
				firstR = r.Round
			}
			if seen {
				// Skip exact duplicates; the first-surfaced version is
				// already in `out`. Merge sources into it.
				for i := range out {
					if out[i].Location == f.Location && out[i].Claim == f.Claim {
						out[i].Sources = mergeStrings(out[i].Sources, f.Sources)
						break
					}
				}
				continue
			}
			copy := f
			copy.FirstSurfacedRound = firstR
			out = append(out, copy)
		}
	}
	return out
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
