package dispatch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/skillweave/treadle/internal/parser"
)

// SpecReviewPrepOpts are the flag-level inputs to SpecReviewPrep.
type SpecReviewPrepOpts struct {
	SpecPath        string // user-supplied spec path (absolute or relative to cwd)
	ProjectRoot     string // if empty: walk up from cwd looking for .loom/project.md
	Team            string // default: "four-round-reviewers"
	PolicyOverrides map[string]any
}

// SpecReviewPrepArgs is the full JSON body that the loom dispatch-team
// skill expects. It doubles as the embedded `args` field in the result.
type SpecReviewPrepArgs struct {
	Team            string          `json:"team"`
	StateKey        string          `json:"state_key"`
	ContextBlock    string          `json:"context_block"`
	Subject         DispatchSubject `json:"subject"`
	PolicyOverrides map[string]any  `json:"policy_overrides"`
}

// SpecReviewPrepResult is the JSON blob written to stdout. The
// SKILL.md extracts `args` and passes it directly as the Skill tool's
// `args` parameter when invoking loom:dispatch-team.
type SpecReviewPrepResult struct {
	OK           bool                `json:"ok"`
	Error        string              `json:"error,omitempty"`
	Kind         string              `json:"kind,omitempty"`
	ProjectRoot  string              `json:"project_root,omitempty"`
	SpecAbs      string              `json:"spec_abs,omitempty"`
	SpecRel      string              `json:"spec_rel,omitempty"`
	StateKey     string              `json:"state_key,omitempty"`
	ContextBlock string              `json:"context_block,omitempty"`
	Warnings     []string            `json:"warnings,omitempty"`
	Args         *SpecReviewPrepArgs `json:"args,omitempty"`
}

// SpecReviewPrep folds the per-call prep work loom:spec-review used to
// do in shell into one subcommand call:
//   - walk up from cwd (or use the caller's --project-root) to find
//     `.loom/project.md`
//   - canonicalize the user-supplied spec path; refuse paths outside
//     the project root, refuse symlinks on the path
//   - parse .loom/project.md -> context_block
//   - read spec content
//   - compute state_key = sha256(repo-relative spec path)[:16]
//   - build the full dispatch-team args JSON and return it
//
// Recoverable errors (missing project.md, spec not found, spec escape,
// symlink on path, project parse failure) surface as ok:false rather
// than non-zero exits so the SKILL.md only needs one Bash call to
// discover them.
func SpecReviewPrep(opts SpecReviewPrepOpts) (*SpecReviewPrepResult, error) {
	if strings.TrimSpace(opts.SpecPath) == "" {
		return &SpecReviewPrepResult{OK: false, Error: "spec-path is required", Kind: "validation_error"}, nil
	}
	team := opts.Team
	if team == "" {
		team = "four-round-reviewers"
	}
	policy := opts.PolicyOverrides
	if policy == nil {
		policy = map[string]any{}
	}

	// 1. Resolve project root: explicit override -> walk-up from cwd
	// for .loom/ -> git toplevel if it has .loom/ -> cwd.
	projectRoot := opts.ProjectRoot
	if projectRoot == "" {
		projectRoot = discoverProjectRootWithGit()
	}
	projectMd := filepath.Join(projectRoot, ".loom", "project.md")
	if _, err := os.Stat(projectMd); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &SpecReviewPrepResult{
				OK:    false,
				Error: "no .loom/project.md at " + projectMd + "; run /loom:init first",
				Kind:  "missing_project_md",
			}, nil
		}
		return nil, err
	}

	// 2. Canonicalize the spec path.
	specAbs, err := resolveSpecPath(opts.SpecPath)
	if err != nil {
		return &SpecReviewPrepResult{
			OK:    false,
			Error: err.Error(),
			Kind:  "spec_not_found",
		}, nil
	}

	// 3. Refuse paths outside the project root.
	relFromRoot, err := filepath.Rel(projectRoot, specAbs)
	if err != nil || strings.HasPrefix(relFromRoot, "..") || filepath.IsAbs(relFromRoot) {
		return &SpecReviewPrepResult{
			OK:    false,
			Error: "spec path " + specAbs + " is outside project root " + projectRoot,
			Kind:  "spec_escape",
		}, nil
	}

	// 4. Refuse if any parent of the spec inside the project root is a symlink.
	if err := refuseSymlinkOnPath(projectRoot, specAbs); err != nil {
		return &SpecReviewPrepResult{
			OK:    false,
			Error: err.Error(),
			Kind:  "symlink_on_path",
		}, nil
	}

	// 5. Parse project.md.
	project, err := parser.ParseProjectMd(projectMd)
	if err != nil {
		return &SpecReviewPrepResult{
			OK:    false,
			Error: "parse project.md: " + err.Error(),
			Kind:  "parse_error",
		}, nil
	}

	// 6. Read spec content.
	specBytes, err := os.ReadFile(specAbs)
	if err != nil {
		return &SpecReviewPrepResult{
			OK:    false,
			Error: "read spec: " + err.Error(),
			Kind:  "spec_not_found",
		}, nil
	}

	// 7. Compute state_key from repo-relative spec path.
	stateKey := ComputeStateKey(relFromRoot)

	// 8. Build the dispatch-team args payload.
	args := &SpecReviewPrepArgs{
		Team:            team,
		StateKey:        stateKey,
		ContextBlock:    project.ContextBlock,
		Subject:         DispatchSubject{Path: relFromRoot, Content: string(specBytes)},
		PolicyOverrides: policy,
	}

	return &SpecReviewPrepResult{
		OK:           true,
		ProjectRoot:  projectRoot,
		SpecAbs:      specAbs,
		SpecRel:      relFromRoot,
		StateKey:     stateKey,
		ContextBlock: project.ContextBlock,
		Warnings:     project.Warnings,
		Args:         args,
	}, nil
}

// resolveSpecPath returns the canonical absolute path of specPath,
// resolving relative paths against cwd. Returns an error if the file
// doesn't exist.
func resolveSpecPath(specPath string) (string, error) {
	abs := specPath
	if !filepath.IsAbs(abs) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		abs = filepath.Join(cwd, abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Fall back to the unresolved absolute path if EvalSymlinks
		// chokes on an intermediate component; the symlink-on-path
		// check below will still refuse if a symlink is present.
		if _, statErr := os.Stat(abs); statErr != nil {
			return "", fmt.Errorf("spec not found at %s", specPath)
		}
		resolved = abs
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("spec not found at %s", specPath)
	}
	if info.IsDir() {
		return "", fmt.Errorf("spec path %s is a directory, not a file", specPath)
	}
	return resolved, nil
}

// refuseSymlinkOnPath walks each directory component of specAbs that
// sits at or below projectRoot and refuses if any component is a
// symlink. Prevents reviewer writes from escaping via a planted symlink
// under .loom/ or docs/.
func refuseSymlinkOnPath(projectRoot, specAbs string) error {
	cur := filepath.Dir(specAbs)
	for {
		if cur == projectRoot || cur == "/" || cur == "." {
			break
		}
		info, err := os.Lstat(cur)
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink on path: %s", cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return nil
}

// gitToplevel returns the output of `git rev-parse --show-toplevel`
// for cwd, or empty string if cwd isn't inside a git repo. Used as a
// secondary fallback to discoverProjectRoot when `.loom/` isn't on
// the cwd chain but sits at the git repo root.
func gitToplevel() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// discoverProjectRootWithGit extends discoverProjectRoot with a git
// toplevel fallback. If neither yields .loom/, returns cwd.
func discoverProjectRootWithGit() string {
	root := discoverProjectRoot()
	cwd, _ := os.Getwd()
	if root == cwd {
		// discoverProjectRoot returned cwd as a fallback. Try git.
		top := gitToplevel()
		if top != "" {
			if _, err := os.Stat(filepath.Join(top, ".loom", "project.md")); err == nil {
				return top
			}
		}
	}
	return root
}

