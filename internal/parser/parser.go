package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	ContextBlockMaxBytes = 2048

	SectionStack          = "stack"
	SectionConventions    = "conventions"
	SectionTaxonomy       = "taxonomy"
	SectionReviewDiscipl  = "review-discipline"
	SectionTrustBoundary  = "trust-boundaries"
)

var RequiredProjectSections = []string{
	SectionStack,
	SectionConventions,
	SectionTaxonomy,
	SectionReviewDiscipl,
	SectionTrustBoundary,
}

var SectionLabels = map[string]string{
	SectionStack:         "Stack",
	SectionConventions:   "Conventions",
	SectionTaxonomy:      "Taxonomy",
	SectionReviewDiscipl: "Review discipline",
	SectionTrustBoundary: "Trust boundaries",
}

type ProjectMd struct {
	SchemaVersion int               `json:"schema_version"`
	ProjectName   string            `json:"project_name"`
	Sections      map[string]string `json:"sections"`
	ContextBlock  string            `json:"context_block"`
	Warnings      []string          `json:"warnings,omitempty"`
}

type TeamMd struct {
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	Members      []string            `json:"members"`
	Policy       TeamPolicy          `json:"policy"`
	Persistence  TeamPersistence     `json:"persistence"`
	Sharing      TeamSharing         `json:"sharing"`
	OrchBody     string              `json:"orchestration_body"`
	TemplateHash string              `json:"template_hash"`
}

type TeamPolicy struct {
	MaxRounds           int  `json:"max_rounds"`
	MaxRoundsCeiling    int  `json:"max_rounds_ceiling"`
	MaxAgentsParallel   int  `json:"max_agents_parallel"`
	PerAgentTokenBudget int  `json:"per_agent_token_budget"`
	RoundTimeoutSeconds int  `json:"round_timeout_seconds"`
	AbortOnAgentFailure bool `json:"abort_on_agent_failure"`
}

type TeamPersistence struct {
	Mode        string `json:"mode"`
	FindingsLog string `json:"findings_log"`
}

type TeamSharing struct {
	Scope string `json:"scope"`
}

type AgentMd struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tools       []string `json:"tools"`
}

var (
	ErrNoFrontmatter       = errors.New("missing YAML frontmatter opener `---`")
	ErrFrontmatterUnclosed = errors.New("unterminated YAML frontmatter (missing closing `---`)")
	ErrDuplicateSection    = errors.New("duplicate section")
	ErrMissingRequired     = errors.New("missing required section")
	ErrUnclosedSection     = errors.New("unterminated section (missing `<!-- loom:end -->`)")
	ErrSchemaVersion       = errors.New("unsupported schema_version")
)

var sectionOpenRe = regexp.MustCompile(`(?m)^<!--\s*loom:section=([a-z0-9_-]+)\s*-->\s*$`)
var sectionCloseRe = regexp.MustCompile(`(?m)^<!--\s*loom:end\s*-->\s*$`)

// SplitFrontmatter returns (frontmatter YAML body, document body, error).
func SplitFrontmatter(text string) (string, string, error) {
	if !strings.HasPrefix(text, "---\n") {
		return "", "", ErrNoFrontmatter
	}
	rest := text[4:]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		// also accept trailing close without newline (EOF after ---)
		if strings.HasSuffix(rest, "\n---") {
			return rest[:len(rest)-4], "", nil
		}
		return "", "", ErrFrontmatterUnclosed
	}
	return rest[:idx], rest[idx+5:], nil
}

// parseSimpleYAML is a narrow hand-rolled parser that covers the flat
// key:value / key:[a,b] / key:\n  - item / key:\n  subkey:value shapes
// this project's frontmatter uses. Avoids pulling in yaml.v3 for parse
// operations that must run anywhere the binary runs.
func parseSimpleYAML(s string) map[string]any {
	out := map[string]any{}
	lines := strings.Split(s, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			i++
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			i++
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			i++
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		if val == "" {
			// collect indented block — could be list or map
			block := []string{}
			i++
			for i < len(lines) && (strings.HasPrefix(lines[i], "  ") || strings.HasPrefix(lines[i], "\t") || strings.TrimSpace(lines[i]) == "") {
				block = append(block, lines[i])
				i++
			}
			out[key] = parseIndentedBlock(block)
			continue
		}
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			items := []string{}
			inner := strings.TrimSpace(val[1 : len(val)-1])
			if inner != "" {
				for _, it := range strings.Split(inner, ",") {
					items = append(items, unquoteScalar(strings.TrimSpace(it)))
				}
			}
			out[key] = items
		} else {
			out[key] = unquoteScalar(val)
		}
		i++
	}
	return out
}

func parseIndentedBlock(lines []string) any {
	trimmed := []string{}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		trimmed = append(trimmed, l)
	}
	if len(trimmed) == 0 {
		return nil
	}
	isList := true
	for _, l := range trimmed {
		s := strings.TrimLeft(l, " \t")
		if !strings.HasPrefix(s, "- ") && s != "-" {
			isList = false
			break
		}
	}
	if isList {
		items := []string{}
		for _, l := range trimmed {
			s := strings.TrimLeft(l, " \t")
			items = append(items, unquoteScalar(strings.TrimPrefix(strings.TrimPrefix(s, "-"), " ")))
		}
		return items
	}
	// treat as nested map: strip leading indent consistently
	// find common leading whitespace
	minPrefix := -1
	for _, l := range trimmed {
		count := 0
		for _, r := range l {
			if r == ' ' || r == '\t' {
				count++
			} else {
				break
			}
		}
		if minPrefix < 0 || count < minPrefix {
			minPrefix = count
		}
	}
	if minPrefix < 0 {
		minPrefix = 0
	}
	unindented := make([]string, 0, len(trimmed))
	for _, l := range trimmed {
		if len(l) >= minPrefix {
			unindented = append(unindented, l[minPrefix:])
		} else {
			unindented = append(unindented, l)
		}
	}
	return parseSimpleYAML(strings.Join(unindented, "\n"))
}

func unquoteScalar(s string) string {
	if (strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`)) ||
		(strings.HasPrefix(s, `'`) && strings.HasSuffix(s, `'`)) {
		if len(s) >= 2 {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func atoiOrZero(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case string:
		n := 0
		fmt.Sscanf(x, "%d", &n)
		return n
	}
	return 0
}

func boolOrFalse(v any) bool {
	if s, ok := v.(string); ok {
		return s == "true" || s == "True" || s == "TRUE"
	}
	return false
}

func stringOr(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func stringSlice(v any) []string {
	if list, ok := v.([]string); ok {
		return list
	}
	return nil
}

func mapOr(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// ParseProjectMd reads and parses a .loom/project.md file, honoring
// section-marker semantics and building the context block (capped at
// ContextBlockMaxBytes, truncated longest-section-first).
func ParseProjectMd(path string) (*ProjectMd, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseProjectFromBytes(raw)
}

func parseProjectFromBytes(raw []byte) (*ProjectMd, error) {
	text := string(raw)
	fmBody, body, err := SplitFrontmatter(text)
	if err != nil {
		return nil, err
	}
	fm := parseSimpleYAML(fmBody)
	schemaVer := atoiOrZero(fm["schema_version"])
	projName := stringOr(fm["project_name"], "")
	if schemaVer == 0 {
		return nil, fmt.Errorf("%w: schema_version missing or not an integer", ErrSchemaVersion)
	}

	sections, warnings, err := extractSections(body)
	if err != nil {
		return nil, err
	}

	for _, req := range RequiredProjectSections {
		if _, ok := sections[req]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrMissingRequired, req)
		}
	}

	ctx := buildContextBlock(sections, ContextBlockMaxBytes)

	return &ProjectMd{
		SchemaVersion: schemaVer,
		ProjectName:   projName,
		Sections:      sections,
		ContextBlock:  ctx,
		Warnings:      warnings,
	}, nil
}

func extractSections(body string) (map[string]string, []string, error) {
	sections := map[string]string{}
	warnings := []string{}
	pos := 0
	for pos < len(body) {
		m := sectionOpenRe.FindStringSubmatchIndex(body[pos:])
		if m == nil {
			break
		}
		absOpenStart := pos + m[0]
		absOpenEnd := pos + m[1]
		name := body[pos+m[2] : pos+m[3]]

		closeM := sectionCloseRe.FindStringSubmatchIndex(body[absOpenEnd:])
		if closeM == nil {
			return nil, nil, fmt.Errorf("%w: section %q opened at offset %d", ErrUnclosedSection, name, absOpenStart)
		}
		absCloseStart := absOpenEnd + closeM[0]
		absCloseEnd := absOpenEnd + closeM[1]

		// Refuse if a new section opens before the matching close —
		// that means the current section was never terminated.
		nextOpen := sectionOpenRe.FindStringSubmatchIndex(body[absOpenEnd:absCloseStart])
		if nextOpen != nil {
			return nil, nil, fmt.Errorf("%w: section %q opened at offset %d", ErrUnclosedSection, name, absOpenStart)
		}

		content := strings.TrimSpace(body[absOpenEnd:absCloseStart])

		if _, dup := sections[name]; dup {
			return nil, nil, fmt.Errorf("%w: %q", ErrDuplicateSection, name)
		}
		if _, known := SectionLabels[name]; !known {
			warnings = append(warnings, fmt.Sprintf("unknown section %q ignored (forward-compat)", name))
		}
		sections[name] = content
		pos = absCloseEnd
	}
	return sections, warnings, nil
}

func buildContextBlock(sections map[string]string, limit int) string {
	type kv struct {
		key   string
		label string
		value string
	}
	items := []kv{}
	for _, req := range RequiredProjectSections {
		if v, ok := sections[req]; ok {
			items = append(items, kv{req, SectionLabels[req], v})
		}
	}
	// append unknown sections in sorted order so output is deterministic
	knownSet := map[string]struct{}{}
	for k := range SectionLabels {
		knownSet[k] = struct{}{}
	}
	unknown := []string{}
	for k := range sections {
		if _, ok := knownSet[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		label := strings.ReplaceAll(k, "-", " ")
		if label != "" {
			label = strings.ToUpper(label[:1]) + label[1:]
		}
		items = append(items, kv{k, label, sections[k]})
	}

	render := func(it []kv) string {
		var b strings.Builder
		for _, x := range it {
			b.WriteString("### ")
			b.WriteString(x.label)
			b.WriteString("\n\n")
			b.WriteString(x.value)
			b.WriteString("\n\n")
		}
		return strings.TrimRight(b.String(), "\n") + "\n"
	}

	out := render(items)
	if len(out) <= limit {
		return out
	}

	// Truncate longest-section-first: shrink the longest section's value
	// by half until total fits under the limit.
	trimmed := make([]kv, len(items))
	copy(trimmed, items)
	for len(render(trimmed)) > limit {
		longestIdx := 0
		for i := range trimmed {
			if len(trimmed[i].value) > len(trimmed[longestIdx].value) {
				longestIdx = i
			}
		}
		v := trimmed[longestIdx].value
		if len(v) <= 32 {
			// stop shrinking; truncate the rendered output hard
			break
		}
		cut := len(v) / 2
		trimmed[longestIdx].value = strings.TrimRight(v[:cut], " \n") + " …[truncated]"
	}
	out = render(trimmed)
	if len(out) > limit {
		out = out[:limit-1] + "\n"
	}
	return out
}

// ParseTeamMd reads a team template, validates the frontmatter against
// required fields, and computes template_hash = sha256(orch_body_bytes)[:16]
// for the template-evolution guard.
func ParseTeamMd(path string) (*TeamMd, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	fmBody, body, err := SplitFrontmatter(text)
	if err != nil {
		return nil, err
	}
	fm := parseSimpleYAML(fmBody)

	policy := mapOr(fm["policy"])
	persist := mapOr(fm["persistence"])
	sharing := mapOr(fm["sharing"])

	t := &TeamMd{
		Name:        stringOr(fm["name"], ""),
		Description: stringOr(fm["description"], ""),
		Members:     stringSlice(fm["members"]),
		Policy: TeamPolicy{
			MaxRounds:           atoiOrZero(policy["max_rounds"]),
			MaxRoundsCeiling:    atoiOrZero(policy["max_rounds_ceiling"]),
			MaxAgentsParallel:   atoiOrZero(policy["max_agents_parallel"]),
			PerAgentTokenBudget: atoiOrZero(policy["per_agent_token_budget"]),
			RoundTimeoutSeconds: atoiOrZero(policy["round_timeout_seconds"]),
			AbortOnAgentFailure: boolOrFalse(policy["abort_on_agent_failure"]),
		},
		Persistence: TeamPersistence{
			Mode:        stringOr(persist["mode"], "ephemeral"),
			FindingsLog: stringOr(persist["findings_log"], "off"),
		},
		Sharing: TeamSharing{
			Scope: stringOr(sharing["scope"], "local"),
		},
		OrchBody: strings.TrimSpace(body),
	}
	if t.Name == "" {
		return nil, fmt.Errorf("team template %q: name missing from frontmatter", path)
	}
	if len(t.Members) == 0 {
		return nil, fmt.Errorf("team template %q: members list missing from frontmatter", path)
	}
	if t.Policy.MaxRoundsCeiling > 0 && t.Policy.MaxRounds > t.Policy.MaxRoundsCeiling {
		return nil, fmt.Errorf("team template %q: max_rounds (%d) exceeds max_rounds_ceiling (%d)",
			path, t.Policy.MaxRounds, t.Policy.MaxRoundsCeiling)
	}
	h := sha256.Sum256([]byte(t.OrchBody))
	t.TemplateHash = hex.EncodeToString(h[:])[:16]
	return t, nil
}

// ParseAgentMd reads an agent file's frontmatter.
func ParseAgentMd(path string) (*AgentMd, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fmBody, _, err := SplitFrontmatter(string(raw))
	if err != nil {
		return nil, err
	}
	fm := parseSimpleYAML(fmBody)
	a := &AgentMd{
		Name:        stringOr(fm["name"], ""),
		Description: stringOr(fm["description"], ""),
		Tools:       stringSlice(fm["tools"]),
	}
	if a.Name == "" {
		return nil, fmt.Errorf("agent %q: name missing from frontmatter", path)
	}
	return a, nil
}
