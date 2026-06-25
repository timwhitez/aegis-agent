package config

import "testing"

func TestKnownModelContextWindow(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  int
		ok    bool
	}{
		{name: "exact", model: "gpt-5.5", want: 300000, ok: true},
		{name: "provider route", model: "openai/gpt-5.5", want: 300000, ok: true},
		{name: "max variant", model: "gpt-5.5__max", want: 300000, ok: true},
		{name: "case insensitive", model: "GPT-5.5", want: 300000, ok: true},
		{name: "unknown", model: "unknown-model", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := KnownModelContextWindow(tt.model)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("KnownModelContextWindow(%q)=(%d,%v), want (%d,%v)", tt.model, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestResolveContextWindowTokens(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		configured int
		want       int
	}{
		{name: "configured wins", model: "gpt-5.5", configured: 272000, want: 272000},
		{name: "known model", model: "gpt-5.5", want: 300000},
		{name: "default unknown", model: "unknown", want: DefaultContextWindowTokens},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveContextWindowTokens(tt.model, tt.configured); got != tt.want {
				t.Fatalf("ResolveContextWindowTokens(%q,%d)=%d, want %d", tt.model, tt.configured, got, tt.want)
			}
		})
	}
}

func TestDeriveInputCharThreshold(t *testing.T) {
	tests := []struct {
		name   string
		tokens int
		factor float64
		want   int
	}{
		{name: "default window", tokens: 200000, factor: 0.85, want: 680000},
		{name: "gpt55 window", tokens: 300000, factor: 0.85, want: 1020000},
		{name: "invalid factor default", tokens: 200000, factor: 2, want: 680000},
		{name: "invalid tokens default", tokens: 0, factor: 0.85, want: 680000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveInputCharThreshold(tt.tokens, tt.factor); got != tt.want {
				t.Fatalf("DeriveInputCharThreshold(%d,%f)=%d, want %d", tt.tokens, tt.factor, got, tt.want)
			}
		})
	}
}
