package multica

import "testing"

func TestParseUsageSummaryOmitsUnknownAndMapsCacheTokens(t *testing.T) {
	usage, ok := ParseUsageSummary("input_tokens=101 output_tokens=17 unknown=3", "cache_creation_input_tokens=83 cache_read_input_tokens=197")
	if !ok {
		t.Fatal("expected usage to be observed")
	}
	if usage.InputTokens != 101 || usage.OutputTokens != 17 || usage.CacheWriteTokens != 83 || usage.CacheReadTokens != 197 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if _, ok := ParseUsageSummary("unknown", ""); ok {
		t.Fatal("expected unknown usage to be omitted")
	}
}

func TestSplitModelRouteRoundTripsProviderAndModel(t *testing.T) {
	mode, model, err := SplitModelRoute("openai-response/gpt-5.5")
	if err != nil {
		t.Fatalf("split model route: %v", err)
	}
	if mode != "openai-response" || model != "gpt-5.5" {
		t.Fatalf("unexpected split: %s %s", mode, model)
	}
}
