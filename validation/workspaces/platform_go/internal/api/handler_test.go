package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/platformgo/internal/config"
	"example.com/platformgo/internal/service"
)

func newTestHandler() Handler {
	return NewHandler(service.New(config.Config{DefaultQuota: 250}))
}

func TestCreateAccountRejectsMalformedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader(`{"public_id":"acct_1","plan":`))
	rec := httptest.NewRecorder()
	newTestHandler().CreateAccount(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateAccountRejectsUnknownFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader(`{"public_id":"acct_1","plan":"pro","unexpected":true}`))
	rec := httptest.NewRecorder()
	newTestHandler().CreateAccount(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown fields, got %d", rec.Code)
	}
}

func TestCreateAccountHidesInternalIDAndUsesDefaultQuota(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader(`{"public_id":"acct_1","plan":"pro"}`))
	rec := httptest.NewRecorder()
	newTestHandler().CreateAccount(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if _, ok := payload["internal_id"]; ok {
		t.Fatalf("expected internal_id to stay private, got %v", payload)
	}
	if payload["quota"] != float64(250) {
		t.Fatalf("expected default quota 250, got %v", payload["quota"])
	}
}
