package model

type CreateAccountRequest struct {
	PublicID string `json:"public_id"`
	Plan     string `json:"plan"`
	Quota    int    `json:"quota,omitempty"`
}

type Account struct {
	InternalID string `json:"internal_id"`
	PublicID   string `json:"public_id"`
	Plan       string `json:"plan"`
	Quota      int    `json:"quota"`
}

type PublicAccount struct {
	PublicID string `json:"public_id"`
	Plan     string `json:"plan"`
	Quota    int    `json:"quota"`
}
