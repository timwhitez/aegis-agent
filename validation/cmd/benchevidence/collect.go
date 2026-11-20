package main

import (
	"encoding/json"
	"errors"
	"math"
	"strings"

	"aegis-agent/internal/events"
	"aegis-agent/internal/session"
)

func integer(value any) *int {
	v, ok := value.(float64)
	if !ok || v < 0 || v > math.MaxInt32 || v != math.Trunc(v) {
		return nil
	}
	n := int(v)
	return &n
}

func collect(dir string, exitCode *int) report {
	r := report{Schema: 1, Category: "not_started", ExitCode: exitCode, Sessions: []sessionEvidence{}}
	store := session.NewStore(dir)
	sessions, err := store.ListAvailable(33)
	if err != nil {
		r.Category = "unavailable"
		r.Partial = true
		return r
	}
	if len(sessions) > 32 {
		r.Partial = true
		sessions = sessions[:32]
	}
	for _, row := range sessions {
		if !sessionID.MatchString(row.ID) || !allowed(row.Status, statuses) {
			r.Partial = true
			continue
		}
		se := sessionEvidence{ID: row.ID, Status: row.Status, Parent: row.ParentSessionID != ""}
		positions := map[string]int{}
		lastStop := ""
		validTrace := true
		err = store.VisitEvents(row.ID, func(e events.Event) error {
			if e.Type == "tool.before" {
				se.ToolCalls++
				return nil
			}
			if e.Type != "provider.request.prepared" && e.Type != "turn.stopped" && e.Type != "provider.request.failed" {
				return nil
			}
			id, _ := e.Data["request_id"].(string)
			suffix, ok := strings.CutPrefix(id, row.ID+":")
			if !ok || !requestSuffix.MatchString(suffix) {
				validTrace = false
				r.Partial = true
				return nil
			}
			i, exists := positions[id]
			if !exists {
				if len(se.Requests) >= maxRequests {
					return errors.New("request limit")
				}
				i = len(se.Requests)
				positions[id] = i
				q := requestFact{ID: id}
				if n := integer(e.Data["turn"]); n != nil {
					q.Turn = *n
				}
				if n := integer(e.Data["request_sequence"]); n != nil {
					q.Sequence = *n
				}
				se.Requests = append(se.Requests, q)
			}
			q := &se.Requests[i]
			switch e.Type {
			case "provider.request.prepared":
				lastStop = ""
				budget, _ := e.Data["request_budget"].(map[string]any)
				q.MaxOutputTokens = integer(budget["effective_max_output_tokens"])
				q.Source = enum(budget["max_output_tokens_source"], sources)
				q.Effort = enum(budget["output_config_effort"], efforts)
			case "provider.request.failed":
				q.Stop = "failed"
				lastStop = "failed"
			case "turn.stopped":
				q.Stop = enum(e.Data["stop_reason"], reasons)
				lastStop = q.Stop
				u, _ := e.Data["usage"].(map[string]any)
				if u["reported"] == true {
					q.Usage = &usageFact{integer(u["input_tokens"]), integer(u["output_tokens"]), integer(u["reasoning_tokens"]), integer(u["cache_read_input_tokens"]), integer(u["cache_creation_input_tokens"])}
				}
			}
			return nil
		})
		if err != nil {
			validTrace = false
			r.Partial = true
		}
		attempts, more, err := store.LoadProviderAttemptsTail(row.ID, maxRequests)
		if err != nil || more {
			r.Partial = true
		}
		for _, a := range attempts {
			outcome := enum(a.Outcome, outcomes)
			if outcome == "" {
				outcome = "other"
			}
			se.Attempts = append(se.Attempts, attemptFact{a.Turn, a.Attempt, a.StatusCode, a.Retryable, outcome})
		}
		r.Sessions = append(r.Sessions, se)
		if !se.Parent {
			r.Started = true
			switch row.Status {
			case "completed", "cancelled":
				r.Category = row.Status
			case "failed":
				switch {
				case !validTrace || lastStop == "":
					r.Category = "incomplete"
				case lastStop == "failed":
					r.Category = "provider_error"
				default:
					r.Category = "agent_failed"
				}
			default:
				r.Category = "incomplete"
			}
		}
	}
	return r
}

func encode(r report) ([]byte, error) {
	data, err := json.Marshal(r)
	if err == nil && len(data) > maxReportBytes {
		return nil, errors.New("evidence size limit")
	}
	return data, err
}
