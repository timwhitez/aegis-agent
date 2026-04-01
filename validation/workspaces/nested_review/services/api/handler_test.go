package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateTicketRejectsMissingTitle(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/tickets", strings.NewReader(`{"external_id":"EXT-1"}`))
	rec := httptest.NewRecorder()
	CreateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d", rec.Code)
	}
}

func TestCreateTicketReturnsJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/tickets", strings.NewReader(`{"external_id":"EXT-1","title":"hello"}`))
	rec := httptest.NewRecorder()
	CreateTicket(rec, req)
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected json content type, got %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if _, ok := body["id"]; ok {
		t.Fatalf("expected internal id to stay private, got %#v", body)
	}
	if body["external_id"] != "EXT-1" || body["title"] != "hello" {
		t.Fatalf("unexpected response body: %#v", body)
	}
}

func TestCreateTicketRejectsMalformedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/tickets", strings.NewReader(`{"external_id":"EXT-1",`))
	rec := httptest.NewRecorder()
	CreateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for malformed json, got %d", rec.Code)
	}
}

func TestCreateTicketRejectsUnknownIDField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/tickets", strings.NewReader(`{"id":12,"external_id":"EXT-1","title":"hello"}`))
	rec := httptest.NewRecorder()
	CreateTicket(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for unknown id field, got %d", rec.Code)
	}
}
