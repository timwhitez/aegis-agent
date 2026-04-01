package service

import (
	"example.com/platformgo/internal/config"
	"example.com/platformgo/internal/model"
	"example.com/platformgo/internal/quota"
)

type Service struct {
	cfg config.Config
}

func New(cfg config.Config) Service {
	return Service{cfg: cfg}
}

func (s Service) Create(req model.CreateAccountRequest) (model.Account, error) {
	limit, err := quota.Resolve(req.Quota, s.cfg.DefaultQuota)
	if err != nil {
		return model.Account{}, err
	}
	return model.Account{
		InternalID: "acct_internal_123",
		PublicID:   req.PublicID,
		Plan:       req.Plan,
		Quota:      limit,
	}, nil
}
