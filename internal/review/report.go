package review

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"aegis-agent/internal/fileutil"
	"aegis-agent/internal/tools"
)

var (
	findingsHeadingPattern   = regexp.MustCompile(`(?im)^#{1,6}\s+.*\b(findings|finding records|validated findings)\b`)
	unresolvedHeadingPattern = regexp.MustCompile(`(?im)^#{1,6}\s+.*\b(unresolved|remaining risks?)\b`)
	noFindingsPattern        = regexp.MustCompile(`(?im)\bno (?:(?:validated|confirmed|directly|new|live-validated)\s+)?(?:[a-z0-9._/-]+\s+){0,6}(?:findings?|issues?|drifts?|breaks?|mismatches?|blockers?)\b`)
	headingLinePattern       = regexp.MustCompile(`(?m)^(#{1,6})\s+`)
	fieldLinePattern         = regexp.MustCompile(`(?im)^(?:[-*+]\s+|\d+\.\s+)?([a-z][a-z0-9 _-]+?)\s*(?::|-)\s*(.*)$`)
	findingBoundaryPattern   = regexp.MustCompile(`(?im)^(?:(?:#{2,6}\s+.*\b(?:finding|issue|record)\b.*)|(?:(?:finding|issue|record)\s+\d+\b.*))$`)
	evidencePathLinePattern  = regexp.MustCompile("(?i)(?:^|[\\s\\[(`])(?:[a-z0-9._-]+/)*[a-z0-9._-]+(?::\\d+(?::\\d+)?(?:-\\d+(?::\\d+)?)?)")
	evidenceReferencePattern = regexp.MustCompile("(?i)(?:^|[\\s\\[(`;])((?:[a-z0-9._-]+/)*[a-z0-9._-]+):(\\d+)(?::\\d+)?(?:-(\\d+)(?::\\d+)?)?")
	evidenceRangeTailPattern = regexp.MustCompile(`^\s*,\s*(\d+)(?::\d+)?(?:-(\d+)(?::\d+)?)?`)
	evidenceSnippetPattern   = regexp.MustCompile("`([^`]{2,})`|\"([^\"]{2,})\"|'([^']{2,})'")
	unresolvedNonePattern    = regexp.MustCompile(`(?im)\b(?:none|no unresolved(?: questions| issues)?|n/?a)\b`)
)

type ValidationResult struct {
	Valid                 bool
	NoFindings            bool
	FindingCount          int
	SeverityCount         int
	ConfidenceCount       int
	EvidenceCount         int
	VerifiedEvidenceCount int
	WhyItMattersCount     int
	Issues                []string
}

type findingRecord struct {
	Severity     string
	Confidence   string
	Evidence     string
	Snippet      string
	WhyItMatters string
}

func ValidateMarkdownArtifact(content string) ValidationResult {
	return validateMarkdownArtifact(content, "")
}

func ValidateMarkdownArtifactWithWorkspace(workdir, content string) ValidationResult {
	return validateMarkdownArtifact(content, workdir)
}

func validateMarkdownArtifact(content, workdir string) ValidationResult {
	normalized := normalizeMarkdown(content)
	result := ValidationResult{}

	if !findingsHeadingPattern.MatchString(normalized) {
		result.Issues = append(result.Issues, "missing findings section")
	}
	if !unresolvedHeadingPattern.MatchString(normalized) {
		result.Issues = append(result.Issues, "missing unresolved or remaining-risks section")
	}

	findingsBody := extractSection(normalized, findingsHeadingPattern)
	unresolvedBody := extractSection(normalized, unresolvedHeadingPattern)
	result.NoFindings = noFindingsPattern.MatchString(findingsBody)

	if !result.NoFindings {
		records := parseFindingRecords(findingsBody)
		result.FindingCount = len(records)
		if len(records) == 0 {
			result.Issues = append(result.Issues, "missing finding records")
		}
		for i, record := range records {
			if strings.TrimSpace(record.Severity) != "" {
				result.SeverityCount++
			}
			if strings.TrimSpace(record.Confidence) != "" {
				result.ConfidenceCount++
			}
			if strings.TrimSpace(record.Evidence) != "" {
				result.EvidenceCount++
			}
			if strings.TrimSpace(record.WhyItMatters) != "" {
				result.WhyItMattersCount++
			}
			if !record.hasSeverity() {
				result.Issues = append(result.Issues, "finding "+ordinalLabel(i+1)+" is missing severity")
			}
			if !record.hasConfidence() {
				result.Issues = append(result.Issues, "finding "+ordinalLabel(i+1)+" is missing confidence")
			}
			if !record.hasEvidence() {
				result.Issues = append(result.Issues, "finding "+ordinalLabel(i+1)+" is missing evidence")
			}
			if !record.hasWhyItMatters() {
				result.Issues = append(result.Issues, "finding "+ordinalLabel(i+1)+" is missing why it matters")
			}
			if record.hasEvidence() && !evidencePathLinePattern.MatchString(record.Evidence) {
				result.Issues = append(result.Issues, "finding "+ordinalLabel(i+1)+" evidence must include concrete repo path:line support")
			}
			if strings.TrimSpace(workdir) != "" && record.hasEvidence() {
				validation := validateEvidenceAgainstWorkspace(workdir, record.Evidence, record.Snippet)
				if !validation.readable {
					result.Issues = append(result.Issues, "finding "+ordinalLabel(i+1)+" evidence must resolve to readable in-workspace files and line ranges; use explicit workspace-relative repo paths like internal/app/app.go:42-44 instead of omitted-path or ellipsis shorthand")
				}
				if !validation.hasSnippet {
					result.Issues = append(result.Issues, "finding "+ordinalLabel(i+1)+" evidence must include a quoted snippet or identifier from the cited lines")
				}
				if validation.hasSnippet && !validation.verifiedSnippet {
					result.Issues = append(result.Issues, "finding "+ordinalLabel(i+1)+" evidence snippets must match the cited lines; correct the cited line numbers or widen the cited line range so the quoted text or identifier appears within those exact lines")
				}
				if validation.readable && validation.hasSnippet && validation.verifiedSnippet {
					result.VerifiedEvidenceCount++
				}
			}
		}
		if result.SeverityCount == 0 {
			result.Issues = append(result.Issues, "missing per-finding severity fields")
		}
		if result.ConfidenceCount == 0 {
			result.Issues = append(result.Issues, "missing per-finding confidence fields")
		}
		if result.EvidenceCount == 0 {
			result.Issues = append(result.Issues, "missing per-finding evidence fields")
		}
		if result.WhyItMattersCount == 0 {
			result.Issues = append(result.Issues, "missing per-finding why it matters fields")
		}
		if result.FindingCount > 0 && (result.SeverityCount != result.FindingCount || result.ConfidenceCount != result.FindingCount || result.EvidenceCount != result.FindingCount || result.WhyItMattersCount != result.FindingCount) {
			result.Issues = append(result.Issues, "finding field counts do not match across severity, confidence, evidence, and why it matters")
		}
	}

	if unresolvedHeadingPattern.MatchString(normalized) && !hasMeaningfulUnresolvedContent(unresolvedBody) {
		result.Issues = append(result.Issues, "unresolved or remaining-risks section must contain at least one question, bullet, or explicit none statement")
	}

	result.Valid = len(result.Issues) == 0
	return result
}

func normalizeMarkdown(content string) string {
	replacer := strings.NewReplacer("*", "")
	return replacer.Replace(content)
}

func extractSection(content string, headingPattern *regexp.Regexp) string {
	loc := headingPattern.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	section := content[loc[0]:]
	matches := headingLinePattern.FindAllStringSubmatchIndex(section, -1)
	if len(matches) <= 1 {
		return section
	}
	currentLevel := matches[0][3] - matches[0][2]
	for _, match := range matches[1:] {
		level := match[3] - match[2]
		if level <= currentLevel {
			return section[:match[0]]
		}
	}
	return section
}

func parseFindingRecords(section string) []findingRecord {
	lines := strings.Split(section, "\n")
	records := make([]findingRecord, 0, 4)
	var current findingRecord
	var hasCurrent bool
	lastField := ""

	flush := func() {
		if hasCurrent && current.hasAnyField() {
			records = append(records, current)
		}
		current = findingRecord{}
		hasCurrent = false
		lastField = ""
	}

	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			lastField = ""
			continue
		}
		if idx == 0 && findingsHeadingPattern.MatchString(line) {
			continue
		}
		if findingBoundaryPattern.MatchString(line) {
			flush()
			continue
		}
		field, value, ok := parseFindingField(line)
		if ok {
			if !hasCurrent {
				hasCurrent = true
			}
			if field == "severity" && current.hasAnyField() {
				flush()
				hasCurrent = true
			}
			if current.fieldValue(field) != "" {
				if allowsRepeatedFindingField(field) {
					current.appendField(field, value)
					lastField = field
					continue
				}
				flush()
				hasCurrent = true
			}
			current.setField(field, value)
			lastField = field
			continue
		}
		if hasCurrent && lastField != "" {
			current.appendField(lastField, line)
		}
	}
	flush()
	return records
}

func allowsRepeatedFindingField(field string) bool {
	switch field {
	case "evidence", "snippet", "why":
		return true
	default:
		return false
	}
}

func headingLevel(line string) int {
	match := headingLinePattern.FindStringSubmatch(line)
	if len(match) != 2 {
		return 0
	}
	return len(match[1])
}

func parseFindingField(line string) (string, string, bool) {
	match := fieldLinePattern.FindStringSubmatch(line)
	if len(match) != 3 {
		return "", "", false
	}
	field := canonicalFieldName(match[1])
	if field == "" {
		return "", "", false
	}
	return field, strings.TrimSpace(match[2]), true
}

func canonicalFieldName(label string) string {
	normalized := strings.ToLower(strings.TrimSpace(label))
	normalized = strings.ReplaceAll(normalized, "_", " ")
	normalized = strings.Join(strings.Fields(normalized), " ")
	switch normalized {
	case "severity", "level":
		return "severity"
	case "confidence", "certainty":
		return "confidence"
	case "evidence", "proof", "file evidence", "repo evidence":
		return "evidence"
	case "snippet", "evidence snippet", "evidence support", "support", "quote", "quoted support":
		return "snippet"
	case "why it matters", "impact", "risk", "implication":
		return "why"
	default:
		return ""
	}
}

func hasMeaningfulUnresolvedContent(section string) bool {
	if strings.TrimSpace(section) == "" {
		return false
	}
	lines := strings.Split(section, "\n")
	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if idx == 0 && unresolvedHeadingPattern.MatchString(line) {
			continue
		}
		if unresolvedNonePattern.MatchString(line) {
			return true
		}
		trimmed := strings.TrimLeft(line, "-*+0123456789. )(")
		if strings.TrimSpace(trimmed) != "" {
			return true
		}
	}
	return false
}

func ordinalLabel(index int) string {
	return strconv.Itoa(index)
}

func (f findingRecord) hasAnyField() bool {
	return f.Severity != "" || f.Confidence != "" || f.Evidence != "" || f.Snippet != "" || f.WhyItMatters != ""
}

func (f findingRecord) hasSeverity() bool {
	return strings.TrimSpace(f.Severity) != ""
}

func (f findingRecord) hasConfidence() bool {
	return strings.TrimSpace(f.Confidence) != ""
}

func (f findingRecord) hasEvidence() bool {
	return strings.TrimSpace(f.Evidence) != ""
}

func (f findingRecord) hasWhyItMatters() bool {
	return strings.TrimSpace(f.WhyItMatters) != ""
}

func (f findingRecord) fieldValue(field string) string {
	switch field {
	case "severity":
		return f.Severity
	case "confidence":
		return f.Confidence
	case "evidence":
		return f.Evidence
	case "snippet":
		return f.Snippet
	case "why":
		return f.WhyItMatters
	default:
		return ""
	}
}

func (f *findingRecord) setField(field, value string) {
	switch field {
	case "severity":
		f.Severity = value
	case "confidence":
		f.Confidence = value
	case "evidence":
		f.Evidence = value
	case "snippet":
		f.Snippet = value
	case "why":
		f.WhyItMatters = value
	}
}

func (f *findingRecord) appendField(field, value string) {
	switch field {
	case "severity":
		f.Severity = appendFieldValue(f.Severity, value)
	case "confidence":
		f.Confidence = appendFieldValue(f.Confidence, value)
	case "evidence":
		f.Evidence = appendFieldValue(f.Evidence, value)
	case "snippet":
		f.Snippet = appendFieldValue(f.Snippet, value)
	case "why":
		f.WhyItMatters = appendFieldValue(f.WhyItMatters, value)
	}
}

func appendFieldValue(current, addition string) string {
	if strings.TrimSpace(current) == "" {
		return strings.TrimSpace(addition)
	}
	return current + " " + strings.TrimSpace(addition)
}

type evidenceReference struct {
	Path      string
	StartLine int
	EndLine   int
}

type evidenceValidation struct {
	readable        bool
	hasSnippet      bool
	verifiedSnippet bool
}

func validateEvidenceAgainstWorkspace(workdir, evidence, explicitSnippet string) evidenceValidation {
	segments := splitEvidenceSegments(evidence)
	allReadable := true
	hasSnippet := false
	verified := false
	foundReference := false
	for _, segment := range segments {
		references := extractEvidenceReferences(segment)
		if len(references) == 0 {
			continue
		}
		foundReference = true
		snippets := extractEvidenceSnippets(segment)
		if snippet := strings.TrimSpace(explicitSnippet); snippet != "" {
			explicitSnippets := extractEvidenceSnippets(snippet)
			if len(explicitSnippets) == 0 && !looksLikeCitationSnippet(snippet) {
				explicitSnippets = []string{snippet}
			}
			snippets = append(snippets, explicitSnippets...)
		}
		if len(snippets) > 0 {
			hasSnippet = true
		}
		for _, ref := range references {
			lines, ok := readEvidenceLines(workdir, ref)
			if !ok {
				allReadable = false
				continue
			}
			for _, snippet := range snippets {
				if snippetMatchesEvidenceLines(snippet, lines) {
					verified = true
					break
				}
			}
		}
	}
	if !foundReference {
		return evidenceValidation{}
	}
	return evidenceValidation{
		readable:        allReadable,
		hasSnippet:      hasSnippet,
		verifiedSnippet: verified,
	}
}

func splitEvidenceSegments(evidence string) []string {
	parts := strings.FieldsFunc(evidence, func(r rune) bool {
		return r == ';' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func extractEvidenceReferences(segment string) []evidenceReference {
	matches := evidenceReferencePattern.FindAllStringSubmatchIndex(segment, -1)
	out := make([]evidenceReference, 0, len(matches))
	for _, match := range matches {
		if len(match) < 8 {
			continue
		}
		path := segment[match[2]:match[3]]
		startText := segment[match[4]:match[5]]
		endText := ""
		if match[6] >= 0 && match[7] >= 0 {
			endText = segment[match[6]:match[7]]
		}
		if ref, ok := parseEvidenceReference(path, startText, endText); ok {
			out = append(out, ref)
		}
		remainder := segment[match[1]:]
		offset := 0
		for {
			tail := evidenceRangeTailPattern.FindStringSubmatchIndex(remainder[offset:])
			if tail == nil || tail[0] != 0 {
				break
			}
			startText := remainder[offset+tail[2] : offset+tail[3]]
			endText := ""
			if tail[4] >= 0 && tail[5] >= 0 {
				endText = remainder[offset+tail[4] : offset+tail[5]]
			}
			if ref, ok := parseEvidenceReference(path, startText, endText); ok {
				out = append(out, ref)
			}
			offset += tail[1]
		}
	}
	return out
}

func parseEvidenceReference(path, startText, endText string) (evidenceReference, bool) {
	start, err := strconv.Atoi(strings.TrimSpace(startText))
	if err != nil || start <= 0 {
		return evidenceReference{}, false
	}
	end := start
	if strings.TrimSpace(endText) != "" {
		value, err := strconv.Atoi(strings.TrimSpace(endText))
		if err == nil && value > 0 {
			end = value
		}
	}
	if end < start {
		end = start
	}
	return evidenceReference{
		Path:      path,
		StartLine: start,
		EndLine:   end,
	}, true
}

func extractEvidenceSnippets(segment string) []string {
	matches := evidenceSnippetPattern.FindAllStringSubmatch(segment, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		for _, candidate := range match[1:] {
			snippet := strings.TrimSpace(candidate)
			if snippet == "" || looksLikeCitationSnippet(snippet) {
				continue
			}
			out = append(out, snippet)
		}
	}
	return out
}

func looksLikeCitationSnippet(snippet string) bool {
	return evidencePathLinePattern.MatchString(snippet)
}

func readEvidenceLines(workdir string, ref evidenceReference) (string, bool) {
	path, ok := resolveEvidencePath(workdir, ref.Path)
	if !ok {
		return "", false
	}
	data, _, err := fileutil.ReadRegularFileNoSymlink(path)
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(data), "\n")
	if ref.StartLine <= 0 || ref.StartLine > len(lines) {
		return "", false
	}
	end := ref.EndLine
	if end > len(lines) {
		end = len(lines)
	}
	if end < ref.StartLine {
		end = ref.StartLine
	}
	return strings.Join(lines[ref.StartLine-1:end], "\n"), true
}

func resolveEvidencePath(workdir, rel string) (string, bool) {
	base := filepath.Clean(strings.TrimSpace(workdir))
	if base == "" {
		return "", false
	}
	target := strings.TrimSpace(rel)
	if target == "" {
		return "", false
	}
	resolved, err := tools.ResolveWorkspacePath(base, target)
	if err != nil {
		return "", false
	}
	return resolved, true
}

func snippetMatchesEvidenceLines(snippet, lines string) bool {
	normalizedSnippet := normalizeEvidenceText(snippet)
	if normalizedSnippet == "" {
		return false
	}
	return strings.Contains(normalizeEvidenceText(lines), normalizedSnippet)
}

func normalizeEvidenceText(value string) string {
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
	return value
}
