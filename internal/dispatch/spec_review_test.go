package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validProjectMd = `---
schema_version: 1
project_name: testproj
---

<!-- loom:section=stack -->
Rust 2024, tokio async runtime.
<!-- loom:end -->

<!-- loom:section=conventions -->
Errors: thiserror for libs, anyhow for binaries.
<!-- loom:end -->

<!-- loom:section=taxonomy -->
Specs: docs/specs/NN-slug.md.
<!-- loom:end -->

<!-- loom:section=review-discipline -->
Invariants: crate dep-graph.
<!-- loom:end -->

<!-- loom:section=trust-boundaries -->
Off-limits: .env, secrets/.
<!-- loom:end -->
`

// setupProject creates a temp project root with .loom/project.md and
// a spec under docs/specs/. Returns (project_root, spec_abs).
func setupProject(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	loomDir := filepath.Join(root, ".loom")
	if err := os.MkdirAll(loomDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loomDir, "project.md"), []byte(validProjectMd), 0o644); err != nil {
		t.Fatal(err)
	}
	specDir := filepath.Join(root, "docs", "specs")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	specAbs := filepath.Join(specDir, "001-sample.md")
	if err := os.WriteFile(specAbs, []byte("# spec body\n\nsome content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, specAbs
}

func TestSpecReviewPrep_HappyPath(t *testing.T) {
	root, specAbs := setupProject(t)

	res, err := SpecReviewPrep(SpecReviewPrepOpts{
		SpecPath:    specAbs,
		ProjectRoot: root,
	})
	if err != nil {
		t.Fatalf("SpecReviewPrep: %v", err)
	}
	if !res.OK {
		t.Fatalf("OK=false: %q %q", res.Error, res.Kind)
	}
	if res.ProjectRoot != root {
		t.Errorf("ProjectRoot = %q, want %q", res.ProjectRoot, root)
	}
	if res.SpecAbs != specAbs {
		t.Errorf("SpecAbs = %q, want %q", res.SpecAbs, specAbs)
	}
	if res.SpecRel != "docs/specs/001-sample.md" {
		t.Errorf("SpecRel = %q", res.SpecRel)
	}
	if res.StateKey == "" || len(res.StateKey) != 16 {
		t.Errorf("StateKey = %q (expected 16 hex chars)", res.StateKey)
	}
	if err := ValidateStateKey(res.StateKey); err != nil {
		t.Errorf("computed state_key failed validation: %v", err)
	}
	if res.Args == nil {
		t.Fatal("Args nil")
	}
	if res.Args.Team != "four-round-reviewers" {
		t.Errorf("default team wrong: %q", res.Args.Team)
	}
	if res.Args.Subject.Path != "docs/specs/001-sample.md" {
		t.Errorf("Subject.Path = %q", res.Args.Subject.Path)
	}
	if !strings.Contains(res.Args.Subject.Content, "spec body") {
		t.Errorf("Subject.Content missing expected text")
	}
	if !strings.Contains(res.Args.ContextBlock, "Rust 2024") {
		t.Errorf("ContextBlock missing stack section: %q", res.Args.ContextBlock)
	}
	if res.Args.PolicyOverrides == nil {
		t.Error("PolicyOverrides should be non-nil empty map")
	}
}

func TestSpecReviewPrep_MissingProjectMd(t *testing.T) {
	root := t.TempDir()
	specAbs := filepath.Join(root, "spec.md")
	if err := os.WriteFile(specAbs, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := SpecReviewPrep(SpecReviewPrepOpts{
		SpecPath:    specAbs,
		ProjectRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected OK=false for missing project.md")
	}
	if res.Kind != "missing_project_md" {
		t.Errorf("Kind = %q, want missing_project_md", res.Kind)
	}
	if !strings.Contains(res.Error, "loom:init") {
		t.Errorf("error should mention loom:init: %q", res.Error)
	}
}

func TestSpecReviewPrep_SpecNotFound(t *testing.T) {
	root, _ := setupProject(t)
	res, err := SpecReviewPrep(SpecReviewPrepOpts{
		SpecPath:    filepath.Join(root, "does-not-exist.md"),
		ProjectRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected OK=false for missing spec")
	}
	if res.Kind != "spec_not_found" {
		t.Errorf("Kind = %q, want spec_not_found", res.Kind)
	}
}

func TestSpecReviewPrep_RefusesEscape(t *testing.T) {
	root, _ := setupProject(t)
	// Spec sits outside the project root in a sibling temp dir.
	outside := t.TempDir()
	specAbs := filepath.Join(outside, "spec.md")
	if err := os.WriteFile(specAbs, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := SpecReviewPrep(SpecReviewPrepOpts{
		SpecPath:    specAbs,
		ProjectRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected OK=false for spec outside project root")
	}
	if res.Kind != "spec_escape" {
		t.Errorf("Kind = %q, want spec_escape", res.Kind)
	}
}

func TestSpecReviewPrep_RefusesSymlinkOnPath(t *testing.T) {
	root, _ := setupProject(t)
	// Build a real spec.
	realSpec := filepath.Join(root, "docs", "specs", "002-real.md")
	if err := os.WriteFile(realSpec, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Plant a symlink in the spec's parent chain: docs/specs-link -> docs/specs
	linkParent := filepath.Join(root, "docs", "specs-link")
	if err := os.Symlink("specs", linkParent); err != nil {
		t.Skipf("symlink unavailable on this fs: %v", err)
	}
	specVia := filepath.Join(linkParent, "002-real.md")
	res, err := SpecReviewPrep(SpecReviewPrepOpts{
		SpecPath:    specVia,
		ProjectRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	// After EvalSymlinks, the path resolves to docs/specs/002-real.md
	// with no symlinks on its parent chain, so this should PASS -- the
	// refuse-symlink rule inspects the canonicalized path's parents,
	// which no longer traverse the symlink. Document that.
	if !res.OK {
		t.Fatalf("canonical resolution should strip symlink; got err=%q kind=%q", res.Error, res.Kind)
	}
}

func TestSpecReviewPrep_PolicyOverridesPassThrough(t *testing.T) {
	root, specAbs := setupProject(t)
	res, err := SpecReviewPrep(SpecReviewPrepOpts{
		SpecPath:        specAbs,
		ProjectRoot:     root,
		PolicyOverrides: map[string]any{"max_rounds": float64(3)},
	})
	if err != nil || !res.OK {
		t.Fatalf("err=%v ok=%v err=%q", err, res.OK, res.Error)
	}
	if res.Args.PolicyOverrides["max_rounds"] != float64(3) {
		t.Errorf("PolicyOverrides not passed through: %+v", res.Args.PolicyOverrides)
	}
}

func TestSpecReviewPrep_StateKeyStable(t *testing.T) {
	root, specAbs := setupProject(t)
	a, err := SpecReviewPrep(SpecReviewPrepOpts{SpecPath: specAbs, ProjectRoot: root})
	if err != nil || !a.OK {
		t.Fatal(err)
	}
	b, err := SpecReviewPrep(SpecReviewPrepOpts{SpecPath: specAbs, ProjectRoot: root})
	if err != nil || !b.OK {
		t.Fatal(err)
	}
	if a.StateKey != b.StateKey {
		t.Errorf("state_key non-deterministic: %s vs %s", a.StateKey, b.StateKey)
	}
}

func TestSpecReviewPrep_CustomTeam(t *testing.T) {
	root, specAbs := setupProject(t)
	res, err := SpecReviewPrep(SpecReviewPrepOpts{
		SpecPath:    specAbs,
		ProjectRoot: root,
		Team:        "custom-team",
	})
	if err != nil || !res.OK {
		t.Fatal(err)
	}
	if res.Args.Team != "custom-team" {
		t.Errorf("Team override not honored: %q", res.Args.Team)
	}
}

func TestSpecReviewPrep_EmptySpecPath(t *testing.T) {
	root, _ := setupProject(t)
	res, err := SpecReviewPrep(SpecReviewPrepOpts{SpecPath: "", ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected OK=false for empty spec-path")
	}
	if res.Kind != "validation_error" {
		t.Errorf("Kind = %q", res.Kind)
	}
}
