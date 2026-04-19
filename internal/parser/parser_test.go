package parser

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validProject = `---
schema_version: 1
project_name: demo
---

# demo

<!-- loom:section=stack -->
Rust 2024 edition, tokio, mlua.
<!-- loom:end -->

<!-- loom:section=conventions -->
Errors: thiserror for libs, anyhow for bins.
<!-- loom:end -->

<!-- loom:section=taxonomy -->
Specs at docs/specs/NN-slug.md.
<!-- loom:end -->

<!-- loom:section=review-discipline -->
Host-authoritative state rule. api_version bump for mod-API changes.
<!-- loom:end -->

<!-- loom:section=trust-boundaries -->
Agents read src/, docs/. Off-limits: .env, secrets/.
<!-- loom:end -->
`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseProject_HappyPath(t *testing.T) {
	p := writeTemp(t, "project.md", validProject)
	r, err := ParseProjectMd(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", r.SchemaVersion)
	}
	if r.ProjectName != "demo" {
		t.Errorf("project_name = %q, want demo", r.ProjectName)
	}
	for _, req := range RequiredProjectSections {
		if _, ok := r.Sections[req]; !ok {
			t.Errorf("missing section %q", req)
		}
	}
	if !strings.Contains(r.ContextBlock, "Stack") || !strings.Contains(r.ContextBlock, "Rust 2024") {
		t.Errorf("context block missing Stack label or Rust content: %q", r.ContextBlock)
	}
}

func TestParseProject_MissingRequiredSection(t *testing.T) {
	src := strings.Replace(validProject, "<!-- loom:section=trust-boundaries -->\nAgents read src/, docs/. Off-limits: .env, secrets/.\n<!-- loom:end -->\n", "", 1)
	p := writeTemp(t, "project.md", src)
	_, err := ParseProjectMd(p)
	if err == nil || !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("expected ErrMissingRequired, got %v", err)
	}
}

func TestParseProject_DuplicateSection(t *testing.T) {
	src := validProject + "\n<!-- loom:section=stack -->\nDuplicate.\n<!-- loom:end -->\n"
	p := writeTemp(t, "project.md", src)
	_, err := ParseProjectMd(p)
	if err == nil || !errors.Is(err, ErrDuplicateSection) {
		t.Fatalf("expected ErrDuplicateSection, got %v", err)
	}
}

func TestParseProject_UnclosedSection(t *testing.T) {
	src := strings.Replace(validProject, "<!-- loom:section=stack -->\nRust 2024 edition, tokio, mlua.\n<!-- loom:end -->\n", "<!-- loom:section=stack -->\nRust 2024 edition, tokio, mlua.\n", 1)
	p := writeTemp(t, "project.md", src)
	_, err := ParseProjectMd(p)
	if err == nil || !errors.Is(err, ErrUnclosedSection) {
		t.Fatalf("expected ErrUnclosedSection, got %v", err)
	}
}

func TestParseProject_MissingSchemaVersion(t *testing.T) {
	src := strings.Replace(validProject, "schema_version: 1\n", "", 1)
	p := writeTemp(t, "project.md", src)
	_, err := ParseProjectMd(p)
	if err == nil || !errors.Is(err, ErrSchemaVersion) {
		t.Fatalf("expected ErrSchemaVersion, got %v", err)
	}
}

func TestParseProject_NoFrontmatter(t *testing.T) {
	src := "# no frontmatter here\n<!-- loom:section=stack -->\n\n<!-- loom:end -->\n"
	p := writeTemp(t, "project.md", src)
	_, err := ParseProjectMd(p)
	if err == nil || !errors.Is(err, ErrNoFrontmatter) {
		t.Fatalf("expected ErrNoFrontmatter, got %v", err)
	}
}

func TestParseProject_UnknownSectionWarning(t *testing.T) {
	src := validProject + "\n<!-- loom:section=future-thing -->\ncontent\n<!-- loom:end -->\n"
	p := writeTemp(t, "project.md", src)
	r, err := ParseProjectMd(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("expected at least one warning for unknown section")
	}
	if _, ok := r.Sections["future-thing"]; !ok {
		t.Error("unknown section should still be captured")
	}
}

func TestContextBlock_Cap(t *testing.T) {
	long := strings.Repeat("abcdefghij", 500)
	sections := map[string]string{
		SectionStack:         long,
		SectionConventions:   long,
		SectionTaxonomy:      "small",
		SectionReviewDiscipl: "small",
		SectionTrustBoundary: "small",
	}
	out := buildContextBlock(sections, 2048)
	if len(out) > 2048 {
		t.Errorf("context block %d bytes > 2048 cap", len(out))
	}
	if !strings.Contains(out, "…[truncated]") {
		t.Error("expected truncation marker in oversized block")
	}
}

func TestContextBlock_Order(t *testing.T) {
	sections := map[string]string{
		SectionStack:         "s",
		SectionConventions:   "c",
		SectionTaxonomy:      "t",
		SectionReviewDiscipl: "r",
		SectionTrustBoundary: "b",
	}
	out := buildContextBlock(sections, 2048)
	posStack := strings.Index(out, "### Stack")
	posConv := strings.Index(out, "### Conventions")
	posTax := strings.Index(out, "### Taxonomy")
	if !(posStack < posConv && posConv < posTax) {
		t.Errorf("required sections not in order: stack=%d conv=%d tax=%d", posStack, posConv, posTax)
	}
}

const validTeam = `---
name: test-team
description: test
members:
  - alice
  - bob
policy:
  max_rounds: 2
  max_rounds_ceiling: 5
  max_agents_parallel: 4
  abort_on_agent_failure: false
persistence:
  mode: persistent
  findings_log: appendable
sharing:
  scope: local
---

# Orchestration

Round 1: both members speak.
`

func TestParseTeam_HappyPath(t *testing.T) {
	p := writeTemp(t, "team.md", validTeam)
	tm, err := ParseTeamMd(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tm.Name != "test-team" {
		t.Errorf("name = %q, want test-team", tm.Name)
	}
	if len(tm.Members) != 2 || tm.Members[0] != "alice" || tm.Members[1] != "bob" {
		t.Errorf("members = %v, want [alice bob]", tm.Members)
	}
	if tm.Policy.MaxRounds != 2 || tm.Policy.MaxRoundsCeiling != 5 {
		t.Errorf("policy rounds = %d/%d, want 2/5", tm.Policy.MaxRounds, tm.Policy.MaxRoundsCeiling)
	}
	if tm.Persistence.Mode != "persistent" {
		t.Errorf("persistence.mode = %q", tm.Persistence.Mode)
	}
	if tm.Sharing.Scope != "local" {
		t.Errorf("sharing.scope = %q", tm.Sharing.Scope)
	}
	if len(tm.TemplateHash) != 16 {
		t.Errorf("template_hash len = %d, want 16", len(tm.TemplateHash))
	}
	if !strings.Contains(tm.OrchBody, "Round 1") {
		t.Errorf("orchestration body missing expected text")
	}
}

func TestParseTeam_CeilingBreach(t *testing.T) {
	src := strings.Replace(validTeam, "max_rounds: 2", "max_rounds: 10", 1)
	p := writeTemp(t, "team.md", src)
	_, err := ParseTeamMd(p)
	if err == nil {
		t.Fatal("expected error on max_rounds > max_rounds_ceiling")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention ceiling breach: %v", err)
	}
}

func TestParseTeam_HashChangesWhenBodyChanges(t *testing.T) {
	p1 := writeTemp(t, "t1.md", validTeam)
	p2 := writeTemp(t, "t2.md", validTeam+"\nextra body text.\n")
	tm1, _ := ParseTeamMd(p1)
	tm2, _ := ParseTeamMd(p2)
	if tm1.TemplateHash == tm2.TemplateHash {
		t.Errorf("hashes should differ: %s == %s", tm1.TemplateHash, tm2.TemplateHash)
	}
}

const validAgent = `---
name: test-agent
description: does stuff
tools: [Read, Grep, Glob]
---

You are a test agent.
`

func TestParseAgent_HappyPath(t *testing.T) {
	p := writeTemp(t, "agent.md", validAgent)
	a, err := ParseAgentMd(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Name != "test-agent" {
		t.Errorf("name = %q", a.Name)
	}
	if len(a.Tools) != 3 || a.Tools[0] != "Read" {
		t.Errorf("tools = %v", a.Tools)
	}
}
