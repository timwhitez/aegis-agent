package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"aegis-agent/internal/config"
	"aegis-agent/internal/session"
)

func TestAnthropicThinkingModesDisplayAndValidation(t *testing.T) {
	newServer := func(captured *map[string]any) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer r.Body.Close()
			data, _ := io.ReadAll(r.Body)
			*captured = nil
			_ = json.Unmarshal(data, captured)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"msg_1",
				"stop_reason":"end_turn",
				"content":[{"type":"text","text":"done"}],
				"usage":{"input_tokens":10,"output_tokens":5}
			}`))
		}))
	}

	t.Run("opus 4.7 adaptive thinking with effort sends adaptive without budget_tokens", func(t *testing.T) {
		var body map[string]any
		server := newServer(&body)
		defer server.Close()

		adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
		res, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:          "s1",
			Model:              "claude-opus-4-7",
			Messages:           []session.Message{session.NewMessage("user", "hello")},
			ThinkingMode:       config.ThinkingModeAdaptive,
			OutputConfigEffort: "high",
		}, func(string, map[string]any) {})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "adaptive"}) {
			t.Fatalf("expected adaptive thinking without budget_tokens, got %#v", body["thinking"])
		}
		if !reflect.DeepEqual(body["output_config"], map[string]any{"effort": "high"}) {
			t.Fatalf("expected output_config.effort=high, got %#v", body["output_config"])
		}
		if res.RawProvider["thinking_strategy"] != "adaptive+output_config_effort" {
			t.Fatalf("unexpected thinking_strategy: %#v", res.RawProvider["thinking_strategy"])
		}
	})

	t.Run("opus 5.5 adaptive thinking with summarized display", func(t *testing.T) {
		var body map[string]any
		server := newServer(&body)
		defer server.Close()

		adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
		_, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:          "s1",
			Model:              "claude-opus-5-5",
			Messages:           []session.Message{session.NewMessage("user", "hello")},
			ThinkingMode:       config.ThinkingModeAdaptive,
			ThinkingDisplay:    config.ThinkingDisplaySummarized,
			OutputConfigEffort: "high",
		}, func(string, map[string]any) {})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if !reflect.DeepEqual(body["thinking"], map[string]any{
			"type":    "adaptive",
			"display": "summarized",
		}) {
			t.Fatalf("expected adaptive+summarized thinking, got %#v", body["thinking"])
		}
	})

	t.Run("sonnet 4.5 manual thinking budget continues to work", func(t *testing.T) {
		var body map[string]any
		server := newServer(&body)
		defer server.Close()

		adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
		res, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:       "s1",
			Model:           "claude-sonnet-4-5",
			Messages:        []session.Message{session.NewMessage("user", "hello")},
			ThinkingMode:    config.ThinkingModeEnabled,
			ThinkingBudget:  config.IntPtr(8192),
			ThinkingDisplay: config.ThinkingDisplayOmitted,
		}, func(string, map[string]any) {})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if !reflect.DeepEqual(body["thinking"], map[string]any{
			"type":          "enabled",
			"budget_tokens": float64(8192),
			"display":       "omitted",
		}) {
			t.Fatalf("unexpected manual thinking payload: %#v", body["thinking"])
		}
		if _, ok := body["output_config"]; ok {
			t.Fatalf("unexpected output_config: %#v", body["output_config"])
		}
		if res.RawProvider["thinking_strategy"] != "manual_budget" {
			t.Fatalf("unexpected thinking_strategy: %#v", res.RawProvider["thinking_strategy"])
		}
	})

	t.Run("legacy include_thoughts=false sends explicit disabled instead of omitting", func(t *testing.T) {
		var body map[string]any
		server := newServer(&body)
		defer server.Close()

		off := false
		adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
		res, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:       "s1",
			Model:           "claude-opus-4-7",
			Messages:        []session.Message{session.NewMessage("user", "hello")},
			ThinkingBudget:  config.IntPtr(4096),
			IncludeThoughts: &off,
		}, func(string, map[string]any) {})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "disabled"}) {
			t.Fatalf("expected explicit thinking.type=disabled, got %#v", body["thinking"])
		}
		if res.RawProvider["thinking_strategy"] != "disabled" {
			t.Fatalf("unexpected thinking_strategy: %#v", res.RawProvider["thinking_strategy"])
		}
	})

	t.Run("opus 4.7 rejects manual budget before sending", func(t *testing.T) {
		called := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		adapter := NewAnthropic(server.URL, "key", "2023-06-01", server.Client())
		_, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:      "s1",
			Model:          "claude-opus-4-7",
			Messages:       []session.Message{session.NewMessage("user", "hello")},
			ThinkingBudget: config.IntPtr(8192),
		}, func(string, map[string]any) {})
		if err == nil || !strings.Contains(err.Error(), "requires adaptive thinking") {
			t.Fatalf("expected adaptive-required error, got %v", err)
		}
		if called {
			t.Fatal("request must be rejected before hitting upstream")
		}
	})
}

func TestGoogleThinkingBudgetOffDynamicDisplayAndBounds(t *testing.T) {
	newServer := func(captured *map[string]any) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer r.Body.Close()
			data, _ := io.ReadAll(r.Body)
			*captured = nil
			_ = json.Unmarshal(data, captured)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],
				"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}
			}`))
		}))
	}

	t.Run("2.5 flash legacy include_thoughts=false with positive budget sends thinkingBudget=0", func(t *testing.T) {
		var body map[string]any
		server := newServer(&body)
		defer server.Close()

		off := false
		adapter := NewGoogle(server.URL, "key", server.Client())
		res, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:       "s1",
			Model:           "gemini-2.5-flash",
			Messages:        []session.Message{session.NewMessage("user", "hello")},
			ThinkingBudget:  config.IntPtr(1024),
			IncludeThoughts: &off,
		}, func(string, map[string]any) {})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		genCfg, _ := body["generationConfig"].(map[string]any)
		if !reflect.DeepEqual(genCfg["thinkingConfig"], map[string]any{"thinkingBudget": float64(0)}) {
			t.Fatalf("expected thinkingBudget=0 for legacy off, got %#v", genCfg)
		}
		if res.RawProvider["thinking_strategy"] != "off" {
			t.Fatalf("expected thinking_strategy=off, got %#v", res.RawProvider["thinking_strategy"])
		}
	})

	t.Run("2.5 flash explicit budget=0 sends thinkingBudget=0", func(t *testing.T) {
		var body map[string]any
		server := newServer(&body)
		defer server.Close()

		adapter := NewGoogle(server.URL, "key", server.Client())
		res, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:      "s1",
			Model:          "gemini-2.5-flash",
			Messages:       []session.Message{session.NewMessage("user", "hello")},
			ThinkingBudget: config.IntPtr(0),
		}, func(string, map[string]any) {})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		genCfg, _ := body["generationConfig"].(map[string]any)
		if !reflect.DeepEqual(genCfg["thinkingConfig"], map[string]any{"thinkingBudget": float64(0)}) {
			t.Fatalf("expected thinkingBudget=0, got %#v", genCfg)
		}
		if res.RawProvider["thinking_strategy"] != "off" {
			t.Fatalf("expected thinking_strategy=off, got %#v", res.RawProvider["thinking_strategy"])
		}
	})

	t.Run("2.5 flash-lite budget=-1 sends thinkingBudget=-1", func(t *testing.T) {
		var body map[string]any
		server := newServer(&body)
		defer server.Close()

		adapter := NewGoogle(server.URL, "key", server.Client())
		res, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:      "s1",
			Model:          "gemini-2.5-flash-lite",
			Messages:       []session.Message{session.NewMessage("user", "hello")},
			ThinkingBudget: config.IntPtr(-1),
		}, func(string, map[string]any) {})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		genCfg, _ := body["generationConfig"].(map[string]any)
		if !reflect.DeepEqual(genCfg["thinkingConfig"], map[string]any{"thinkingBudget": float64(-1)}) {
			t.Fatalf("expected thinkingBudget=-1, got %#v", genCfg)
		}
		if res.RawProvider["thinking_strategy"] != "dynamic_budget" {
			t.Fatalf("expected dynamic_budget strategy, got %#v", res.RawProvider["thinking_strategy"])
		}
	})

	t.Run("2.5 flash positive budget with omitted display preserves budget and sends includeThoughts=false", func(t *testing.T) {
		var body map[string]any
		server := newServer(&body)
		defer server.Close()

		adapter := NewGoogle(server.URL, "key", server.Client())
		res, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:       "s1",
			Model:           "gemini-2.5-flash",
			Messages:        []session.Message{session.NewMessage("user", "hello")},
			ThinkingBudget:  config.IntPtr(1024),
			ThinkingDisplay: config.ThinkingDisplayOmitted,
		}, func(string, map[string]any) {})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		genCfg, _ := body["generationConfig"].(map[string]any)
		if !reflect.DeepEqual(genCfg["thinkingConfig"], map[string]any{
			"thinkingBudget":  float64(1024),
			"includeThoughts": false,
		}) {
			t.Fatalf("expected budget=1024 with includeThoughts=false, got %#v", genCfg)
		}
		if res.RawProvider["thinking_strategy"] != "thinking_budget_omitted_thoughts" {
			t.Fatalf("strategy must not report off when only summaries are hidden, got %#v", res.RawProvider["thinking_strategy"])
		}
	})

	t.Run("2.5 flash rejects budget 32000 and 2.5 pro rejects budget 0 before sending", func(t *testing.T) {
		called := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		adapter := NewGoogle(server.URL, "key", server.Client())
		if _, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:      "s1",
			Model:          "gemini-2.5-flash",
			Messages:       []session.Message{session.NewMessage("user", "hello")},
			ThinkingBudget: config.IntPtr(32000),
		}, func(string, map[string]any) {}); err == nil || !strings.Contains(err.Error(), "24576") {
			t.Fatalf("expected 2.5 flash max budget error, got %v", err)
		}

		if _, err := adapter.RunTurn(context.Background(), TurnRequest{
			SessionID:      "s1",
			Model:          "gemini-2.5-pro",
			Messages:       []session.Message{session.NewMessage("user", "hello")},
			ThinkingBudget: config.IntPtr(0),
		}, func(string, map[string]any) {}); err == nil || !strings.Contains(err.Error(), "does not support disabling thinking") {
			t.Fatalf("expected 2.5 pro disable error, got %v", err)
		}

		if called {
			t.Fatal("invalid google thinking requests must be rejected before hitting upstream")
		}
	})
}
