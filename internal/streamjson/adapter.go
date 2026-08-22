package streamjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"aegis-agent/internal/events"
)

type Adapter struct {
	mu     sync.Mutex
	w      io.Writer
	usage  StreamUsage
	writeE error
}

func NewAdapter(w io.Writer) *Adapter {
	return &Adapter{w: w}
}

func (a *Adapter) Handle(evt events.Event) {
	msg := a.convert(evt)
	if msg == nil {
		return
	}
	if err := a.writeLine(msg); err != nil {
		a.mu.Lock()
		if a.writeE == nil {
			a.writeE = err
		}
		a.mu.Unlock()
	}
}

func (a *Adapter) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.writeE
}

func (a *Adapter) Usage() StreamUsage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usage
}

func (a *Adapter) WriteResult(sessionID, finalText, status, lastError string, exitCode int) error {
	resultText := finalText
	if strings.TrimSpace(resultText) == "" && exitCode != 0 {
		resultText = lastError
	}
	if strings.TrimSpace(status) == "" {
		if exitCode == 0 {
			status = "completed"
		} else {
			status = "failed"
		}
	}
	isError := exitCode != 0
	a.mu.Lock()
	usage := a.usage
	a.mu.Unlock()
	msg := &StreamOutputMessage{
		Type:      "result",
		SessionID: sessionID,
		Result:    resultText,
		Status:    status,
		IsError:   &isError,
		Usage:     &usage,
	}
	return a.writeLine(msg)
}

func (a *Adapter) convert(evt events.Event) *StreamOutputMessage {
	switch evt.Type {
	case "session.started":
		return &StreamOutputMessage{
			Type:            "system",
			Protocol:        ProtocolName,
			ProtocolVersion: ProtocolVersion,
			SessionID:       evt.SessionID,
			Message: &StreamContentMessage{
				Role: "system",
				Content: []StreamContentBlock{{
					Type: "text",
					Text: "Session started",
				}},
			},
		}
	case "session.created":
		return nil
	case "assistant.message":
		blocks := []StreamContentBlock{}
		if thinking, _ := evt.Data["thinking"].(string); strings.TrimSpace(thinking) != "" {
			blocks = append(blocks, StreamContentBlock{Type: "thinking", Text: thinking})
		}
		if text, _ := evt.Data["text"].(string); strings.TrimSpace(text) != "" {
			blocks = append(blocks, StreamContentBlock{Type: "text", Text: text})
		}
		if len(blocks) == 0 {
			return nil
		}
		return &StreamOutputMessage{
			Type:      "assistant",
			SessionID: evt.SessionID,
			Message: &StreamContentMessage{
				Role:    "assistant",
				Content: blocks,
			},
		}
	case "tool.before":
		callID, _ := evt.Data["call_id"].(string)
		name, _ := evt.Data["tool_name"].(string)
		argsText, _ := evt.Data["arguments"].(string)
		return &StreamOutputMessage{
			Type:      "assistant",
			SessionID: evt.SessionID,
			Message: &StreamContentMessage{
				Role: "assistant",
				Content: []StreamContentBlock{{
					Type:  "tool_use",
					ID:    callID,
					Name:  name,
					Input: decodeObject(argsText),
				}},
			},
		}
	case "tool.after":
		callID, _ := evt.Data["call_id"].(string)
		output, _ := evt.Data["display_output"].(string)
		isErr, _ := evt.Data["is_error"].(bool)
		return &StreamOutputMessage{
			Type:      "user",
			SessionID: evt.SessionID,
			Message: &StreamContentMessage{
				Role: "user",
				Content: []StreamContentBlock{{
					Type:      "tool_result",
					ToolUseID: callID,
					Content:   output,
					IsError:   isErr,
				}},
			},
		}
	case "turn.stopped":
		a.accumulateUsage(evt.Data["usage"])
		return nil
	case "provider.error":
		return logMessage(evt, "error", eventLogText(evt))
	case "provider.retry", "provider.auto_resume":
		return logMessage(evt, "warn", eventLogText(evt))
	case events.EventEventsDropped:
		// Surface bus back-pressure loss so consumers can tell an incomplete
		// stream apart from a run that simply produced nothing.
		return logMessage(evt, "warn", fmt.Sprintf("%s: %d event(s) dropped because the stream-json consumer fell behind", evt.Type, intValue(evt.Data["dropped"])))
	default:
		return nil
	}
}

func (a *Adapter) writeLine(msg *StreamOutputMessage) error {
	if err := a.ensureWriter(); err != nil {
		return err
	}
	line, err := MarshalLine(msg)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.writeE != nil {
		return a.writeE
	}
	if _, err := a.w.Write(append(line, '\n')); err != nil {
		a.writeE = err
		return err
	}
	return nil
}

func (a *Adapter) accumulateUsage(raw any) {
	usage, ok := raw.(map[string]any)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usage.InputTokens += intValue(usage["input_tokens"])
	a.usage.OutputTokens += intValue(usage["output_tokens"])
	a.usage.CacheCreationInputTokens += intValue(usage["cache_creation_input_tokens"])
	a.usage.CacheReadInputTokens += intValue(usage["cache_read_input_tokens"])
}

func decodeObject(value string) map[string]any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return nil
	}
	return out
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
		if f, err := v.Float64(); err == nil {
			return int(f)
		}
		return 0
	default:
		return 0
	}
}

func logMessage(evt events.Event, level, text string) *StreamOutputMessage {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return &StreamOutputMessage{
		Type:      "log",
		SessionID: evt.SessionID,
		Log: &StreamLogEntry{
			Level:   level,
			Message: text,
		},
	}
}

func eventLogText(evt events.Event) string {
	for _, key := range []string{"error", "message", "reason"} {
		if value, _ := evt.Data[key].(string); strings.TrimSpace(value) != "" {
			return value
		}
	}
	if len(evt.Data) == 0 {
		return evt.Type
	}
	data, err := json.Marshal(evt.Data)
	if err != nil {
		return evt.Type
	}
	return fmt.Sprintf("%s: %s", evt.Type, string(data))
}

var errNilWriter = errors.New("stream-json writer is nil")

func (a *Adapter) ensureWriter() error {
	if a == nil || a.w == nil {
		return errNilWriter
	}
	return nil
}
