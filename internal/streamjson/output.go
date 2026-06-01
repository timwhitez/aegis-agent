package streamjson

import "encoding/json"

const (
	ProtocolName    = "gocli-stream-json"
	ProtocolVersion = 1
)

type StreamOutputMessage struct {
	Type            string                `json:"type"`
	Protocol        string                `json:"protocol,omitempty"`
	ProtocolVersion int                   `json:"protocol_version,omitempty"`
	RunRole         string                `json:"run_role,omitempty"`
	Metadata        map[string]any        `json:"metadata,omitempty"`
	SessionID       string                `json:"session_id,omitempty"`
	Message         *StreamContentMessage `json:"message,omitempty"`
	Result          string                `json:"result,omitempty"`
	Status          string                `json:"status,omitempty"`
	IsError         *bool                 `json:"is_error,omitempty"`
	Usage           *StreamUsage          `json:"usage,omitempty"`
	Handoff         *StreamHandoff        `json:"handoff,omitempty"`
	Log             *StreamLogEntry       `json:"log,omitempty"`
}

type StreamContentMessage struct {
	Role    string               `json:"role"`
	Content []StreamContentBlock `json:"content"`
}

type StreamContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type StreamUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type StreamLogEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type StreamHandoff struct {
	Summary    string                    `json:"summary,omitempty"`
	Completed  []string                  `json:"completed,omitempty"`
	Remaining  []string                  `json:"remaining,omitempty"`
	Commands   []StreamHandoffCommand    `json:"commands,omitempty"`
	Artifacts  []StreamArtifactRef       `json:"artifacts,omitempty"`
	Risks      []string                  `json:"risks,omitempty"`
	Validation []StreamHandoffValidation `json:"validation,omitempty"`
}

type StreamHandoffCommand struct {
	Command  string             `json:"command,omitempty"`
	ExitCode int                `json:"exit_code,omitempty"`
	Status   string             `json:"status,omitempty"`
	Artifact *StreamArtifactRef `json:"artifact,omitempty"`
}

type StreamHandoffValidation struct {
	AssertionID string             `json:"assertion_id,omitempty"`
	Status      string             `json:"status,omitempty"`
	Evidence    string             `json:"evidence,omitempty"`
	Artifact    *StreamArtifactRef `json:"artifact,omitempty"`
}

type StreamArtifactRef struct {
	Kind        string `json:"kind,omitempty"`
	Path        string `json:"path,omitempty"`
	URI         string `json:"uri,omitempty"`
	Description string `json:"description,omitempty"`
}

func MarshalLine(msg *StreamOutputMessage) ([]byte, error) {
	return json.Marshal(msg)
}
