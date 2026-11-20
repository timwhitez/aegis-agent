package main

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
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
	sessions, err := store.ListAvailable(maxSessions + 1)
	if err != nil {
		r.Category = "unavailable"
		r.Partial = true
		return r
	}
	if len(sessions) > maxSessions {
		r.Partial = true
		// An older root can fall outside the recent window. Reserve room for roots
		// before filling the remaining slots with the most recent children.
		roots, err := store.ListChildren("", maxSessions)
		if err != nil {
			r.Category = "unavailable"
			return r
		}
		for _, row := range sessions {
			if len(roots) == maxSessions {
				break
			}
			if row.ParentSessionID != "" {
				roots = append(roots, row)
			}
		}
		sessions = roots
	}
	rootCount := 0
	for _, row := range sessions {
		if row.ParentSessionID == "" {
			rootCount++
		}
		if !sessionID.MatchString(row.ID) || !allowed(row.Status, statuses) {
			r.Partial = true
			continue
		}
		se := sessionEvidence{ID: row.ID, Status: row.Status, Parent: row.ParentSessionID != ""}
		positions := map[string]int{}
		var lastMain requestFact
		validTrace := true
		err = store.VisitEvents(row.ID, func(e events.Event) error {
			if e.Type == "tool.before" {
				se.ToolCalls++
				return nil
			}
			if e.Type != "provider.request.prepared" && e.Type != "provider.request.completed" && e.Type != "turn.stopped" && e.Type != "provider.request.failed" {
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
				q.Status = "prepared"
				budget, _ := e.Data["request_budget"].(map[string]any)
				q.MaxOutputTokens = integer(budget["effective_max_output_tokens"])
				q.Source = enum(budget["max_output_tokens_source"], sources)
				q.Effort = enum(budget["output_config_effort"], efforts)
			case "provider.request.failed":
				status, _ := e.Data["status"].(string)
				if allowed(status, "failed rejected cancelled") {
					q.Status = status
				} else {
					validTrace = false
					r.Partial = true
				}
			case "provider.request.completed", "turn.stopped":
				// Prefer the canonical terminal event. Older traces may only
				// have the main turn's stop event.
				if e.Type == "turn.stopped" && q.Status != "" && q.Status != "prepared" {
					return nil
				}
				q.Status = "completed"
				q.Stop = enum(e.Data["stop_reason"], reasons)
				u, _ := e.Data["usage"].(map[string]any)
				if u["reported"] == true {
					counts := u
					if fields, exists := u["reported_counts"]; exists {
						counts, _ = fields.(map[string]any)
					}
					q.Usage = &usageFact{integer(counts["input_tokens"]), integer(counts["output_tokens"]), integer(u["reasoning_tokens"]), integer(counts["cache_read_input_tokens"]), integer(counts["cache_creation_input_tokens"])}
				}
			}
			if e.Phase != "compact" {
				lastMain = *q
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
				case !validTrace:
					r.Category = "incomplete"
				case lastMain.Status == "failed":
					r.Category = "provider_error"
				case lastMain.Status == "rejected":
					r.Category = "request_rejected"
				case lastMain.Status == "cancelled":
					r.Category = "request_cancelled"
				case lastMain.Status == "completed" && lastMain.Stop != "":
					r.Category = "agent_failed"
				default:
					r.Category = "incomplete"
				}
			default:
				r.Category = "incomplete"
			}
		}
	}
	if len(sessions) > 0 && (rootCount != 1 || !r.Started) {
		r.Category = "incomplete"
		r.Partial = true
	}
	return r
}

func encode(r report) ([]byte, error) {
	data, err := json.Marshal(r)
	// The host --check encoder appends a newline to this canonical JSON.
	if err != nil || len(data) < maxReportBytes {
		return data, err
	}
	// Individual sessions are bounded, but all of their histories may exceed
	// the host limit together. Keep every session header and the child exit
	// code. Trim older child history before touching an unambiguous root.
	r.Partial = true
	r.Sessions = slices.Clone(r.Sessions)
	for _, children := range []bool{true, false} {
		for {
			trimmed := false
			for i := range r.Sessions {
				s := &r.Sessions[i]
				if s.Parent == children && (len(s.Requests) != 0 || len(s.Attempts) != 0) {
					s.Requests = s.Requests[(len(s.Requests)+1)/2:]
					s.Attempts = s.Attempts[(len(s.Attempts)+1)/2:]
					trimmed = true
				}
			}
			if !trimmed {
				break
			}
			data, err = json.Marshal(r)
			if err != nil || len(data) < maxReportBytes {
				return data, err
			}
		}
	}
	return nil, errors.New("evidence headers exceed size limit")
}
