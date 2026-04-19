package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTeamFixture creates a minimal team template under
// <pluginRoot>/teams/<name>.md suitable for DispatchInit's parse step.
func writeTeamFixture(t *testing.T, pluginRoot, name string) {
	t.Helper()
	dir := filepath.Join(pluginRoot, "teams")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
name: ` + name + `
description: Test team.
members:
  - tech-reviewer
  - coverage-reviewer
policy:
  max_rounds: 2
  max_rounds_ceiling: 3
  max_agents_parallel: 4
  abort_on_agent_failure: false
persistence:
  mode: persistent
  findings_log: appendable
sharing:
  scope: local
---

## Orchestration

Fresh dispatch per round. Sentinel ends each member's output.
`
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newDispatchArgs returns a valid DispatchInitArgs blob for the given state key.
func newDispatchArgs(teamName, stateKey, subjectRel string, overrides map[string]any) *DispatchInitArgs {
	return &DispatchInitArgs{
		Team:            teamName,
		StateKey:        stateKey,
		ContextBlock:    "### Stack\n\ntest stack\n",
		Subject:         DispatchSubject{Path: subjectRel, Content: "# spec\n\nbody."},
		PolicyOverrides: overrides,
	}
}

func TestDispatchInit_HappyPath(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "abc123", "docs/specs/spec.md", nil)
	res, err := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("DispatchInit: %v", err)
	}
	if !res.OK {
		t.Fatalf("OK=false, error=%q kind=%q", res.Error, res.Kind)
	}
	if res.SessionID == "" {
		t.Error("SessionID empty")
	}
	if res.StateDir == "" || !strings.Contains(res.StateDir, "four-round-reviewers/abc123") {
		t.Errorf("StateDir unexpected: %q", res.StateDir)
	}
	if len(res.Members) != 2 {
		t.Errorf("Members = %v, want [tech, coverage]", res.Members)
	}
	if res.ResolvedPolicy == nil || res.ResolvedPolicy.MaxRounds != 2 {
		t.Errorf("ResolvedPolicy.MaxRounds = %v", res.ResolvedPolicy)
	}
	// lock file should exist
	if _, err := os.Stat(filepath.Join(res.StateDir, ".lock")); err != nil {
		t.Errorf(".lock missing: %v", err)
	}
	// trace file should exist
	if _, err := os.Stat(res.TracePath); err != nil {
		t.Errorf("trace missing: %v", err)
	}
}

func TestDispatchInit_RejectsInvalidStateKey(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "bad.key", "docs/spec.md", nil)
	res, err := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("expected OK=false for invalid state_key")
	}
	if res.Kind != "validation_error" {
		t.Errorf("Kind = %q, want validation_error", res.Kind)
	}
}

func TestDispatchInit_RejectsAbsoluteSubjectPath(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "abc", "/etc/passwd", nil)
	res, err := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Kind != "validation_error" {
		t.Errorf("expected validation_error for absolute path, got OK=%v kind=%q", res.OK, res.Kind)
	}
}

func TestDispatchInit_PolicyCeilingEnforced(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "abc", "spec.md", map[string]any{"max_rounds": 99})
	res, err := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("expected ceiling breach to refuse; got OK=true")
	}
	if !strings.Contains(res.Error, "max_rounds") {
		t.Errorf("error text should mention max_rounds: %q", res.Error)
	}
}

func TestDispatchInit_LockHeldSurfacedAsOKFalse(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	// Acquire the lock out-of-band so dispatch-init sees it.
	stateDir := filepath.Join(projectRoot, ".loom", "teams", "state", "four-round-reviewers", "abc")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(stateDir, "held-by-test"); err != nil {
		t.Fatal(err)
	}

	args := newDispatchArgs("four-round-reviewers", "abc", "spec.md", nil)
	res, err := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("expected OK=false when lock held")
	}
	if res.Kind != "lock_held" {
		t.Errorf("Kind = %q, want lock_held", res.Kind)
	}
}

func TestRoundInit_TracesAndTeamName(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "abc123", "spec.md", nil)
	initRes, err := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})
	if err != nil || !initRes.OK {
		t.Fatalf("dispatch-init failed: %v %+v", err, initRes)
	}

	members := []string{"tech-reviewer", "coverage-reviewer"}
	rRes, err := RoundInit(&RoundInitArgs{Members: members, TeamKey: "four-round-reviewers"}, RoundInitOpts{
		StateDir:  initRes.StateDir,
		SessionID: initRes.SessionID,
		Round:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rRes.OK {
		t.Fatalf("round-init OK=false: %q", rRes.Error)
	}
	if !strings.HasPrefix(rRes.TeamName, "loom-four-round-reviewers-") {
		t.Errorf("TeamName prefix unexpected: %q", rRes.TeamName)
	}
	if !strings.Contains(rRes.TeamName, "-r1-") {
		t.Errorf("TeamName should contain -r1-: %q", rRes.TeamName)
	}

	// Verify the trace file has a round-start + 2 dispatch + 2 kickoff events.
	tracePath := filepath.Join(initRes.StateDir, "trace", initRes.SessionID+".jsonl")
	traceData, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(traceData)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		counts[m["event_type"].(string)]++
	}
	if counts["round-start"] != 1 {
		t.Errorf("round-start count = %d, want 1", counts["round-start"])
	}
	if counts["agent-dispatch"] != 2 {
		t.Errorf("agent-dispatch count = %d, want 2", counts["agent-dispatch"])
	}
	if counts["agent-kickoff"] != 2 {
		t.Errorf("agent-kickoff count = %d, want 2", counts["agent-kickoff"])
	}
}

func TestRoundFinalize_RendersAndPersists(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "abc123", "spec.md", nil)
	initRes, err := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})
	if err != nil || !initRes.OK {
		t.Fatal(err)
	}

	findings := []Finding{
		{Severity: "HIGH", Location: "a.md:10", Claim: "missing auth", Sources: []string{"tech-reviewer"}},
		{Severity: "LOW", Location: "b.md:20", Claim: "nitpick", Sources: []string{"coverage-reviewer"}},
		{Severity: "MED", Location: "c.md:30", Claim: "race condition", Sources: []string{"skeptic-reviewer"}},
	}
	rf, err := RoundFinalize(&RoundFinalizeArgs{
		Round:             1,
		MembersSucceeded:  []string{"tech-reviewer", "coverage-reviewer", "skeptic-reviewer"},
		Findings:          findings,
		PeerMessagesCount: 3,
		TeamName:          "loom-four-round-reviewers-abc123-r1-deadbeef",
	}, RoundFinalizeOpts{StateDir: initRes.StateDir, SessionID: initRes.SessionID})
	if err != nil || !rf.OK {
		t.Fatalf("RoundFinalize: %v ok=%v err=%q", err, rf.OK, rf.Error)
	}
	if rf.FindingCounts["HIGH"] != 1 || rf.FindingCounts["MED"] != 1 || rf.FindingCounts["LOW"] != 1 {
		t.Errorf("FindingCounts = %+v", rf.FindingCounts)
	}
	if rf.Degraded {
		t.Error("Degraded should be false")
	}
	if !strings.Contains(rf.SynthesisMD, "HIGH") {
		t.Errorf("synthesis should have HIGH tag: %s", rf.SynthesisMD)
	}
	if !strings.Contains(rf.SynthesisMD, "## Round 1 synthesis") {
		t.Errorf("synthesis missing header: %s", rf.SynthesisMD)
	}
	// findings log populated
	logPath := filepath.Join(initRes.StateDir, "findings.log.md")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "round 1") && !strings.Contains(string(data), "Round 1") {
		t.Errorf("log missing round tag: %s", data)
	}
	// meta.json stored
	metaData, err := os.ReadFile(filepath.Join(initRes.StateDir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metaData), `"round": 1`) {
		t.Errorf("meta missing round 1: %s", metaData)
	}
}

func TestRoundFinalize_DegradedBannerRendered(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "abc123", "spec.md", nil)
	initRes, _ := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})

	rf, err := RoundFinalize(&RoundFinalizeArgs{
		Round:            1,
		MembersSucceeded: []string{"tech-reviewer"},
		MembersDegraded: []DegradedMember{
			{Member: "security-reviewer", Reason: "no sentinel after 60s post-renudge"},
		},
		Findings: []Finding{{Severity: "HIGH", Location: "x", Claim: "y", Sources: []string{"tech-reviewer"}}},
	}, RoundFinalizeOpts{StateDir: initRes.StateDir, SessionID: initRes.SessionID})
	if err != nil || !rf.OK {
		t.Fatalf("err=%v ok=%v", err, rf.OK)
	}
	if !rf.Degraded {
		t.Error("Degraded should be true")
	}
	if !strings.Contains(rf.SynthesisMD, "Round 1 degraded") {
		t.Errorf("expected degraded banner in synthesis: %s", rf.SynthesisMD)
	}
	if !strings.Contains(rf.SynthesisMD, "security-reviewer") {
		t.Errorf("degraded member name missing: %s", rf.SynthesisMD)
	}
}

func TestRoundFinalize_RerunReplacesRound(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "abc123", "spec.md", nil)
	initRes, _ := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})

	// Initial degraded run.
	_, _ = RoundFinalize(&RoundFinalizeArgs{
		Round:            1,
		MembersSucceeded: []string{"tech-reviewer"},
		MembersDegraded:  []DegradedMember{{Member: "security-reviewer", Reason: "silent"}},
		Findings:         []Finding{{Severity: "LOW", Location: "x", Claim: "trivial", Sources: []string{"tech-reviewer"}}},
	}, RoundFinalizeOpts{StateDir: initRes.StateDir, SessionID: initRes.SessionID})

	// Rerun with full membership.
	rf, _ := RoundFinalize(&RoundFinalizeArgs{
		Round:            1,
		MembersSucceeded: []string{"tech-reviewer", "security-reviewer"},
		Findings: []Finding{
			{Severity: "HIGH", Location: "y", Claim: "big one", Sources: []string{"security-reviewer"}},
		},
		Rerun: true,
	}, RoundFinalizeOpts{StateDir: initRes.StateDir, SessionID: initRes.SessionID})

	if rf.TotalRoundsNow != 1 {
		t.Errorf("TotalRoundsNow = %d, want 1 (rerun should replace, not append)", rf.TotalRoundsNow)
	}
	// meta has the rerun round
	metaRaw, _ := os.ReadFile(filepath.Join(initRes.StateDir, "meta.json"))
	if !strings.Contains(string(metaRaw), "big one") {
		t.Errorf("meta should contain new rerun claim: %s", metaRaw)
	}
	if strings.Contains(string(metaRaw), "trivial") {
		t.Errorf("meta should NOT retain old trivial claim after rerun: %s", metaRaw)
	}
}

func TestDispatchEnd_FlattensAndReleasesLock(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "abc123", "spec.md", nil)
	initRes, _ := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})

	// Two rounds of findings.
	_, _ = RoundFinalize(&RoundFinalizeArgs{
		Round:            1,
		MembersSucceeded: []string{"tech-reviewer"},
		Findings:         []Finding{{Severity: "HIGH", Location: "a", Claim: "x", Sources: []string{"tech-reviewer"}}},
	}, RoundFinalizeOpts{StateDir: initRes.StateDir, SessionID: initRes.SessionID})
	_, _ = RoundFinalize(&RoundFinalizeArgs{
		Round:            2,
		MembersSucceeded: []string{"tech-reviewer", "coverage-reviewer"},
		Findings: []Finding{
			{Severity: "MED", Location: "b", Claim: "y", Sources: []string{"coverage-reviewer"}},
			{Severity: "HIGH", Location: "a", Claim: "x", Sources: []string{"tech-reviewer"}}, // duplicate of round 1
		},
	}, RoundFinalizeOpts{StateDir: initRes.StateDir, SessionID: initRes.SessionID})

	endRes, err := DispatchEnd(&DispatchEndArgs{Outcome: "completed"}, DispatchEndOpts{
		StateDir:  initRes.StateDir,
		SessionID: initRes.SessionID,
	})
	if err != nil || !endRes.OK {
		t.Fatalf("err=%v ok=%v err=%q", err, endRes.OK, endRes.Error)
	}
	if endRes.TotalRounds != 2 {
		t.Errorf("TotalRounds = %d, want 2", endRes.TotalRounds)
	}
	// cross-round dedup: the duplicate (a, x) should appear once with first_surfaced_round=1
	highCount := 0
	for _, f := range endRes.Findings {
		if f.Severity == "HIGH" && f.Location == "a" {
			highCount++
			if f.FirstSurfacedRound != 1 {
				t.Errorf("duplicate finding should have FirstSurfacedRound=1, got %d", f.FirstSurfacedRound)
			}
		}
	}
	if highCount != 1 {
		t.Errorf("duplicate finding collapsed wrong: HIGH (a,x) count = %d, want 1", highCount)
	}
	// lock should be released
	if _, err := os.Stat(filepath.Join(initRes.StateDir, ".lock")); err == nil {
		t.Error(".lock should be removed after dispatch-end")
	}
	// final synthesis is non-empty
	if !strings.Contains(endRes.FinalSynthesisMD, "Final synthesis") {
		t.Errorf("expected Final synthesis header: %s", endRes.FinalSynthesisMD)
	}
}

func TestDispatchEnd_SurfacesDegradedRounds(t *testing.T) {
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "abc", "spec.md", nil)
	initRes, _ := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})

	_, _ = RoundFinalize(&RoundFinalizeArgs{
		Round:            1,
		MembersSucceeded: []string{"tech-reviewer"},
		MembersDegraded:  []DegradedMember{{Member: "security-reviewer", Reason: "silent"}},
		Findings:         []Finding{{Severity: "HIGH", Location: "x", Claim: "y", Sources: []string{"tech-reviewer"}}},
	}, RoundFinalizeOpts{StateDir: initRes.StateDir, SessionID: initRes.SessionID})

	endRes, err := DispatchEnd(&DispatchEndArgs{Outcome: "completed"}, DispatchEndOpts{
		StateDir: initRes.StateDir, SessionID: initRes.SessionID,
	})
	if err != nil || !endRes.OK {
		t.Fatalf("err=%v ok=%v", err, endRes.OK)
	}
	if len(endRes.DegradedRounds) != 1 {
		t.Errorf("DegradedRounds count = %d, want 1", len(endRes.DegradedRounds))
	}
	if endRes.DegradedRounds[0].FailedMembers[0] != "security-reviewer" {
		t.Errorf("DegradedRounds[0].FailedMembers[0] = %q", endRes.DegradedRounds[0].FailedMembers[0])
	}
}

func TestSortFindings_SeverityOrder(t *testing.T) {
	f := []Finding{
		{Severity: "LOW", Location: "b", Claim: "a"},
		{Severity: "HIGH", Location: "c", Claim: "a"},
		{Severity: "MED", Location: "a", Claim: "a"},
	}
	SortFindings(f)
	if f[0].Severity != "HIGH" || f[1].Severity != "MED" || f[2].Severity != "LOW" {
		t.Errorf("sort order wrong: %+v", f)
	}
}

func TestLifecycleEndToEnd_BashStyleCount(t *testing.T) {
	// Simulates what the SKILL.md will do end-to-end and asserts the
	// per-dispatch atomic-operation count stays low. The goal is to
	// verify each lifecycle call IS one atomic operation — not that
	// the Go code is fast. Main purpose: catch regressions if someone
	// splits a subcommand into multiple stateDir trips.
	pluginRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeTeamFixture(t, pluginRoot, "four-round-reviewers")

	args := newDispatchArgs("four-round-reviewers", "k1", "spec.md", nil)
	initRes, err := DispatchInit(args, DispatchInitOpts{PluginRoot: pluginRoot, ProjectRoot: projectRoot})
	if err != nil || !initRes.OK {
		t.Fatalf("dispatch-init: %v", err)
	}

	for round := 1; round <= 2; round++ {
		_, err := RoundInit(&RoundInitArgs{
			Members: []string{"tech-reviewer", "coverage-reviewer"},
			TeamKey: "four-round-reviewers",
		}, RoundInitOpts{StateDir: initRes.StateDir, SessionID: initRes.SessionID, Round: round})
		if err != nil {
			t.Fatal(err)
		}
		_, err = RoundFinalize(&RoundFinalizeArgs{
			Round:            round,
			MembersSucceeded: []string{"tech-reviewer", "coverage-reviewer"},
			Findings:         []Finding{{Severity: "HIGH", Location: "x", Claim: "y", Sources: []string{"tech-reviewer"}}},
		}, RoundFinalizeOpts{StateDir: initRes.StateDir, SessionID: initRes.SessionID})
		if err != nil {
			t.Fatal(err)
		}
	}

	endRes, err := DispatchEnd(&DispatchEndArgs{Outcome: "completed"}, DispatchEndOpts{
		StateDir: initRes.StateDir, SessionID: initRes.SessionID,
	})
	if err != nil || !endRes.OK {
		t.Fatal(err)
	}
	if endRes.TotalRounds != 2 {
		t.Errorf("TotalRounds = %d, want 2", endRes.TotalRounds)
	}
}
