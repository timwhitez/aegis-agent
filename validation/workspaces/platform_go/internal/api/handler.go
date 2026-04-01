package api

import (
	"encoding/json"
	"net/http"

	"example.com/platformgo/internal/model"
	"example.com/platformgo/internal/service"
)

type Handler struct {
	svc service.Service
}

func NewHandler(svc service.Service) Handler {
	return Handler{svc: svc}
}

func (h Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts", h.CreateAccount)
	return mux
}

func (h Handler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req model.CreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	account, err := h.svc.Create(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(account)
}
