package api

import (
	"encoding/json"
	"net/http"
)

type Ticket struct {
	ExternalID string `json:"external_id"`
	Title      string `json:"title"`
}

func CreateTicket(w http.ResponseWriter, r *http.Request) {
	var input Ticket
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if input.Title == "" {
		http.Error(w, "missing title", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Ticket{
		ExternalID: input.ExternalID,
		Title:      input.Title,
	})
}
