package multica

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"ngen/internal/version"
)

const (
	ProtocolName    = version.Protocol
	ProtocolVersion = version.ProtocolVersion
)

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type ContentMessage struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type StreamInputMessage struct {
	Protocol        string            `json:"protocol"`
	ProtocolVersion int               `json:"protocol_version"`
	Type            string            `json:"type"`
	ID              string            `json:"id,omitempty"`
	Role            string            `json:"role"`
	Content         []ContentBlock    `json:"content"`
	SystemPrompt    string            `json:"system_prompt,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type StreamOutputMessage struct {
	Type            string             `json:"type"`
	Protocol        string             `json:"protocol,omitempty"`
	ProtocolVersion int                `json:"protocol_version,omitempty"`
	ID              string             `json:"id,omitempty"`
	TaskID          string             `json:"task_id,omitempty"`
	SessionID       string             `json:"session_id,omitempty"`
	RunRole         string             `json:"run_role,omitempty"`
	ModelRoute      string             `json:"model_route,omitempty"`
	ProviderMode    string             `json:"provider_mode,omitempty"`
	ProviderModel   string             `json:"provider_model,omitempty"`
	Status          string             `json:"status,omitempty"`
	IsError         bool               `json:"is_error,omitempty"`
	Message         *ContentMessage    `json:"message,omitempty"`
	Tool            *ToolProjection    `json:"tool,omitempty"`
	Log             *LogEntry          `json:"log,omitempty"`
	Usage           map[string]Usage   `json:"usage,omitempty"`
	Handoff         *StructuredHandoff `json:"handoff,omitempty"`
	Metadata        map[string]any     `json:"metadata,omitempty"`
}

type ToolProjection struct {
	Name   string         `json:"name"`
	CallID string         `json:"call_id"`
	Input  map[string]any `json:"input,omitempty"`
	Output string         `json:"output,omitempty"`
	Status string         `json:"status,omitempty"`
}

type LogEntry struct {
	Level   string `json:"level,omitempty"`
	Message string `json:"message"`
}

type Usage struct {
	InputTokens      int64 `json:"input_tokens,omitempty"`
	OutputTokens     int64 `json:"output_tokens,omitempty"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

type StructuredHandoff struct {
	Summary             string            `json:"summary"`
	TaskID              string            `json:"task_id"`
	State               string            `json:"state"`
	Phase               string            `json:"phase"`
	StatusReasonCode    string            `json:"status_reason_code,omitempty"`
	ModelRoute          string            `json:"model_route,omitempty"`
	ProviderMode        string            `json:"provider_mode,omitempty"`
	ProviderModel       string            `json:"provider_model,omitempty"`
	HandoffRef          string            `json:"handoff_ref,omitempty"`
	CompletionRef       string            `json:"completion_ref,omitempty"`
	VerificationRef     string            `json:"verification_ref,omitempty"`
	ReviewRef           string            `json:"review_ref,omitempty"`
	CriteriaRef         string            `json:"criteria_ref,omitempty"`
	RestoreRefs         []ArtifactRef     `json:"restore_refs,omitempty"`
	OpenCriteria        []CriterionDigest `json:"open_criteria,omitempty"`
	MetCriteria         []CriterionDigest `json:"met_criteria,omitempty"`
	WorkerResults       []WorkerDigest    `json:"worker_results,omitempty"`
	Mission             *MissionDigest    `json:"mission,omitempty"`
	RecommendedCommands []HandoffCommand  `json:"recommended_commands,omitempty"`
}

type ArtifactRef struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type CriterionDigest struct {
	ID           string   `json:"id"`
	Statement    string   `json:"statement,omitempty"`
	Status       string   `json:"status"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

type WorkerDigest struct {
	WorkerID                   string   `json:"worker_id"`
	ChildTaskID                string   `json:"child_task_id"`
	Role                       string   `json:"role"`
	Status                     string   `json:"status"`
	ChildState                 string   `json:"child_state,omitempty"`
	CompletionStatus           string   `json:"completion_status,omitempty"`
	ReviewStatus               string   `json:"review_status,omitempty"`
	VerificationStatus         string   `json:"verification_status,omitempty"`
	BlockedReasonCode          string   `json:"blocked_reason_code,omitempty"`
	BlockedDetailRef           string   `json:"blocked_detail_ref,omitempty"`
	Summary                    string   `json:"summary,omitempty"`
	EvidenceGrade              string   `json:"evidence_grade,omitempty"`
	EvidenceScore              int      `json:"evidence_score,omitempty"`
	MissingEvidence            []string `json:"missing_evidence,omitempty"`
	TrustedForParentCompletion bool     `json:"trusted_for_parent_completion,omitempty"`
	RequiresParentAction       bool     `json:"requires_parent_action,omitempty"`
	ParentActionType           string   `json:"parent_action_type,omitempty"`
	ParentActionOptions        []string `json:"parent_action_options,omitempty"`
	ParentActionSummary        string   `json:"parent_action_summary,omitempty"`
	ParentActionUnresolved     bool     `json:"parent_action_unresolved,omitempty"`
	ConflictCount              int      `json:"conflict_count,omitempty"`
	EvidenceRefs               []string `json:"evidence_refs,omitempty"`
}

type MissionDigest struct {
	MissionID           string `json:"mission_id,omitempty"`
	Status              string `json:"status,omitempty"`
	CurrentMilestoneID  string `json:"current_milestone_id,omitempty"`
	LatestValidationRef string `json:"latest_validation_ref,omitempty"`
}

type HandoffCommand struct {
	Kind    string   `json:"kind,omitempty"`
	Command []string `json:"command"`
	Reason  string   `json:"reason,omitempty"`
}

var usageTokenPattern = regexp.MustCompile(`([A-Za-z_]+)=([0-9]+)`)

func ParseUsageSummary(tokenUsage, promptCacheUsage string) (Usage, bool) {
	var usage Usage
	apply := func(summary string) {
		for _, match := range usageTokenPattern.FindAllStringSubmatch(summary, -1) {
			n, err := strconv.ParseInt(match[2], 10, 64)
			if err != nil {
				continue
			}
			switch match[1] {
			case "input_tokens":
				usage.InputTokens = n
			case "output_tokens":
				usage.OutputTokens = n
			case "cache_creation_input_tokens":
				usage.CacheWriteTokens = n
			case "cache_read_input_tokens":
				usage.CacheReadTokens = n
			}
		}
	}
	if !strings.EqualFold(strings.TrimSpace(tokenUsage), "unknown") {
		apply(tokenUsage)
	}
	if !strings.EqualFold(strings.TrimSpace(promptCacheUsage), "unknown") {
		apply(promptCacheUsage)
	}
	return usage, usage != Usage{}
}

func SplitModelRoute(route string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(route), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("model route must be provider/model: %q", route)
	}
	return parts[0], parts[1], nil
}
