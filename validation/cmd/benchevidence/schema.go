// Command benchevidence projects private Aegis session facts into a benchmark report.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
)

const maxReportBytes = 4 << 20
const maxSessions = 32
const maxRequests = 2048

type report struct {
	Schema   int               `json:"schema_version"`
	Started  bool              `json:"started"`
	Category string            `json:"category"`
	ExitCode *int              `json:"exit_code"`
	Sessions []sessionEvidence `json:"sessions"`
	Partial  bool              `json:"partial"`
}

type sessionEvidence struct {
	ID        string        `json:"id"`
	Status    string        `json:"status"`
	Parent    bool          `json:"child"`
	Requests  []requestFact `json:"requests"`
	Attempts  []attemptFact `json:"attempts"`
	ToolCalls int           `json:"tool_calls"`
}

type requestFact struct {
	ID              string     `json:"request_id"`
	Status          string     `json:"status,omitempty"`
	Turn            int        `json:"turn"`
	Sequence        int        `json:"sequence"`
	MaxOutputTokens *int       `json:"max_output_tokens"`
	Source          string     `json:"source,omitempty"`
	Effort          string     `json:"effort,omitempty"`
	Stop            string     `json:"stop_reason,omitempty"`
	Usage           *usageFact `json:"usage"`
}

type usageFact struct {
	Input     *int `json:"input_tokens"`
	Output    *int `json:"output_tokens"`
	Reasoning *int `json:"reasoning_tokens"`
	Read      *int `json:"cache_read_input_tokens"`
	Write     *int `json:"cache_creation_input_tokens"`
}

type attemptFact struct {
	Turn       int    `json:"turn"`
	Attempt    int    `json:"attempt"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Retryable  bool   `json:"retryable"`
	Outcome    string `json:"outcome"`
}

var sessionID = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-([a-f0-9]{6}|fallback)$`)
var requestSuffix = regexp.MustCompile(`^[0-9]{1,9}:(main|semantic_summary|probe):[0-9]{1,9}$`)

const statuses = "running completed cancelled paused failed awaiting_input"
const categories = "completed cancelled provider_error request_rejected request_cancelled agent_failed not_started unavailable incomplete"
const requestStatuses = "prepared completed failed rejected cancelled"
const reasons = "done_candidate tool_call tool_use end_turn max_tokens stop_sequence refusal pause_turn length tool_calls stop content_filter blocked cancelled error failed other"
const sources = "adapter_default configured thinking_budget semantic_summary"
const efforts = "none minimal low medium high xhigh max other"
const outcomes = "success failure auto_resume max_tokens_resume retry other"

func allowed(s, choices string) bool { return slices.Contains(strings.Fields(choices), s) }
func enum(v any, choices string) string {
	s, _ := v.(string)
	if s == "" || allowed(s, choices) {
		return s
	}
	return "other"
}

// Re-validate at the host boundary. A task can write to its own container files.
func checked(r io.Reader) (report, error) {
	var result report
	raw, err := io.ReadAll(io.LimitReader(r, maxReportBytes+1))
	if err != nil || len(raw) > maxReportBytes || json.Unmarshal(raw, &result) != nil {
		return result, errors.New("invalid evidence")
	}
	if result.Schema != 1 || !allowed(result.Category, categories) || len(result.Sessions) > maxSessions || (result.ExitCode != nil && (*result.ExitCode < 0 || *result.ExitCode > 255)) {
		return result, errors.New("invalid evidence header")
	}
	roots := 0
	for _, s := range result.Sessions {
		if !sessionID.MatchString(s.ID) || !allowed(s.Status, statuses) || len(s.Requests) > maxRequests || len(s.Attempts) > maxRequests || s.ToolCalls < 0 {
			return result, errors.New("invalid session evidence")
		}
		if !s.Parent {
			roots++
		}
		for _, q := range s.Requests {
			suffix, ok := strings.CutPrefix(q.ID, s.ID+":")
			if !ok || !requestSuffix.MatchString(suffix) || q.Turn < 0 || q.Sequence < 0 || (q.MaxOutputTokens != nil && *q.MaxOutputTokens <= 0) || (q.Source != "" && !allowed(q.Source, sources)) || (q.Effort != "" && !allowed(q.Effort, efforts)) || (q.Stop != "" && !allowed(q.Stop, reasons)) {
				return result, errors.New("invalid request evidence")
			}
			if q.Status != "" && (!allowed(q.Status, requestStatuses) || (q.Status != "completed" && (q.Stop != "" || q.Usage != nil))) {
				return result, errors.New("inconsistent request lifecycle")
			}
			if q.Usage != nil {
				for _, n := range []*int{q.Usage.Input, q.Usage.Output, q.Usage.Reasoning, q.Usage.Read, q.Usage.Write} {
					if n != nil && *n < 0 {
						return result, errors.New("invalid usage")
					}
				}
			}
		}
		for _, a := range s.Attempts {
			if a.Turn < 0 || a.Attempt < 0 || a.HTTPStatus < 0 || a.HTTPStatus > 599 || !allowed(a.Outcome, outcomes) {
				return result, errors.New("invalid attempt evidence")
			}
		}
	}
	ambiguous := len(result.Sessions) > 0 && roots != 1
	if (roots > 0) != result.Started || (ambiguous && (result.Category != "incomplete" || !result.Partial)) {
		return result, errors.New("inconsistent session evidence")
	}
	return result, nil
}
