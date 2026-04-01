package config

import "os"

type Config struct {
	DefaultQuota int
}

func FromEnv() Config {
	if os.Getenv("ACCOUNT_DEFAULT_QUOTA") == "small" {
		return Config{DefaultQuota: 250}
	}
	return Config{DefaultQuota: 0}
}
