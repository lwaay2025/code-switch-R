package services

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestApplyCodexPromptCacheInjectsStableKeyAndHeaders(t *testing.T) {
	provider := Provider{
		Name:                    "codex-test",
		APIURL:                  "https://api.example.com",
		APIKey:                  "test-api-key",
		CodexPromptCacheEnabled: true,
	}
	expectedKey := stableCodexPromptCacheKey(provider)
	body := []byte(`{"model":"gpt-5.1","input":"hello","stream":true}`)

	nextBody, nextHeaders, plan := applyCodexPromptCache(provider, "/v1/responses", map[string]string{}, body)
	if !plan.Enabled {
		t.Fatal("expected prompt cache plan to be enabled")
	}
	if !plan.Eligible {
		t.Fatal("expected prompt cache plan to be eligible")
	}
	if plan.Key != expectedKey {
		t.Fatalf("plan.Key = %q, want %q", plan.Key, expectedKey)
	}
	if got := gjson.GetBytes(nextBody, "prompt_cache_key").String(); got != plan.Key {
		t.Fatalf("prompt_cache_key = %q, want %q", got, plan.Key)
	}
	if gjson.GetBytes(nextBody, "prompt_cache_retention").Exists() {
		t.Fatal("expected /responses request to omit prompt_cache_retention in strict upstream mode")
	}
	if got := nextHeaders["Conversation_id"]; got != plan.Key {
		t.Fatalf("Conversation_id = %q, want %q", got, plan.Key)
	}
	if got := nextHeaders["Session_id"]; got != plan.Key {
		t.Fatalf("Session_id = %q, want %q", got, plan.Key)
	}

	nextBody2, nextHeaders2, plan2 := applyCodexPromptCache(provider, "/v1/responses", map[string]string{}, body)
	if plan2.Key != plan.Key {
		t.Fatalf("second prompt cache key = %q, want %q", plan2.Key, plan.Key)
	}
	if got := gjson.GetBytes(nextBody2, "prompt_cache_key").String(); got != plan.Key {
		t.Fatalf("second prompt_cache_key = %q, want %q", got, plan.Key)
	}
	if gjson.GetBytes(nextBody2, "prompt_cache_retention").Exists() {
		t.Fatal("expected second /responses request to omit prompt_cache_retention in strict upstream mode")
	}
	if nextHeaders2["Conversation_id"] != plan.Key || nextHeaders2["Session_id"] != plan.Key {
		t.Fatal("expected stable conversation/session headers on second request")
	}
}

func TestApplyCodexPromptCachePreservesExplicitKey(t *testing.T) {
	provider := Provider{
		APIURL:                  "https://api.example.com",
		APIKey:                  "test-api-key",
		CodexPromptCacheEnabled: true,
	}
	body := []byte(`{"model":"gpt-5.1","input":"hello","prompt_cache_key":"custom-key"}`)

	nextBody, nextHeaders, plan := applyCodexPromptCache(provider, "/v1/responses", map[string]string{}, body)
	if plan.Key != "custom-key" {
		t.Fatalf("plan key = %q, want %q", plan.Key, "custom-key")
	}
	if got := gjson.GetBytes(nextBody, "prompt_cache_key").String(); got != "custom-key" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "custom-key")
	}
	if gjson.GetBytes(nextBody, "prompt_cache_retention").Exists() {
		t.Fatal("expected strict upstream mode to omit prompt_cache_retention on /responses")
	}
	if nextHeaders["Conversation_id"] != "custom-key" || nextHeaders["Session_id"] != "custom-key" {
		t.Fatal("expected explicit key to be mirrored to headers")
	}
}

func TestApplyCodexPromptCacheDropsRetentionOnResponses(t *testing.T) {
	provider := Provider{
		APIURL:                  "https://api.example.com",
		APIKey:                  "test-api-key",
		CodexPromptCacheEnabled: true,
	}
	body := []byte(`{"model":"gpt-5.1-codex","input":"hello","prompt_cache_retention":"24h"}`)

	nextBody, _, plan := applyCodexPromptCache(provider, "/v1/responses", map[string]string{}, body)
	if !plan.Eligible {
		t.Fatal("expected prompt cache plan to be eligible")
	}
	if gjson.GetBytes(nextBody, "prompt_cache_retention").Exists() {
		t.Fatal("expected /responses request to drop prompt_cache_retention in strict upstream mode")
	}
}

func TestApplyCodexPromptCachePreservesRetentionOnCompact(t *testing.T) {
	provider := Provider{
		APIURL:                  "https://api.example.com",
		APIKey:                  "test-api-key",
		CodexPromptCacheEnabled: true,
	}
	body := []byte(`{"model":"gpt-5.1-codex","input":"hello","prompt_cache_retention":"24h"}`)

	nextBody, nextHeaders, plan := applyCodexPromptCache(provider, "/v1/responses/compact", map[string]string{}, body)
	if !plan.Eligible {
		t.Fatal("expected prompt cache plan to be eligible")
	}
	if got := gjson.GetBytes(nextBody, "prompt_cache_retention").String(); got != "24h" {
		t.Fatalf("prompt_cache_retention = %q, want %q", got, "24h")
	}
	if nextHeaders["Conversation_id"] == "" || nextHeaders["Session_id"] == "" {
		t.Fatal("expected compact request to mirror stable key into headers")
	}
}

func TestBuildCodexPromptCacheFingerprintIgnoresRetention(t *testing.T) {
	bodyA := []byte(`{"model":"gpt-5.1-codex","input":"hello","prompt_cache_retention":"24h"}`)
	bodyB := []byte(`{"model":"gpt-5.1-codex","input":"hello","prompt_cache_retention":"in_memory"}`)

	fingerprintA := buildCodexPromptCacheFingerprint(bodyA)
	fingerprintB := buildCodexPromptCacheFingerprint(bodyB)
	if fingerprintA == "" || fingerprintB == "" {
		t.Fatal("expected non-empty fingerprints")
	}
	if fingerprintA != fingerprintB {
		t.Fatalf("fingerprint mismatch: %q != %q", fingerprintA, fingerprintB)
	}
}

func TestResolveCodexPromptCacheKeyUsesStableAPIKey(t *testing.T) {
	provider := Provider{
		APIURL: "https://api.example.com",
		APIKey: "test-api-key",
	}

	keyA := resolveCodexPromptCacheKey(provider, nil, nil)
	if got, want := keyA, stableCodexPromptCacheKey(provider); got != want {
		t.Fatalf("keyA = %q, want %q", got, want)
	}
	keyB := resolveCodexPromptCacheKey(provider, nil, nil)
	if keyB != keyA {
		t.Fatalf("expected same deterministic cache key, got %q != %q", keyB, keyA)
	}
}

func TestResolveCodexPromptCacheKeySharesAcrossDifferentURLsWithSameAPIKey(t *testing.T) {
	providerA := Provider{
		Name:   "provider-a",
		APIURL: "https://api-a.example.com",
		APIKey: "same-key",
	}
	providerB := Provider{
		Name:   "provider-b",
		APIURL: "https://api-b.example.com",
		APIKey: "same-key",
	}

	keyA := resolveCodexPromptCacheKey(providerA, nil, nil)
	keyB := resolveCodexPromptCacheKey(providerB, nil, nil)
	if keyA != keyB {
		t.Fatalf("expected same API key to share deterministic cache key, got %q != %q", keyA, keyB)
	}
}

func TestResolveCodexPromptCacheKeySeparatesDifferentAPIKeys(t *testing.T) {
	providerA := Provider{
		APIURL: "https://api.example.com",
		APIKey: "key-a",
	}
	providerB := Provider{
		APIURL: "https://api.example.com",
		APIKey: "key-b",
	}

	keyA := resolveCodexPromptCacheKey(providerA, nil, nil)
	keyB := resolveCodexPromptCacheKey(providerB, nil, nil)
	if keyA == keyB {
		t.Fatalf("expected different upstreams to use different cache keys, got %q", keyA)
	}
}

func TestAnnotateCodexPromptCacheLogsCountsMatchableAndHits(t *testing.T) {
	logs := []ReqeustLog{
		{
			ID:                          1,
			HttpCode:                    200,
			OutputTokens:                10,
			CodexPromptCacheEnabled:     true,
			CodexPromptCacheEligible:    true,
			codexPromptCacheBucket:      "bucket-a",
			codexPromptCacheFingerprint: "fingerprint-a",
			codexPromptCacheInScope:     false,
		},
		{
			ID:                          2,
			HttpCode:                    200,
			OutputTokens:                12,
			CodexPromptCacheEnabled:     true,
			CodexPromptCacheEligible:    true,
			codexPromptCacheBucket:      "bucket-a",
			codexPromptCacheFingerprint: "fingerprint-a",
			codexPromptCacheInScope:     true,
		},
		{
			ID:                          3,
			HttpCode:                    200,
			OutputTokens:                15,
			CacheReadTokens:             128,
			CodexPromptCacheEnabled:     true,
			CodexPromptCacheEligible:    true,
			CodexPromptCacheHit:         true,
			codexPromptCacheBucket:      "bucket-a",
			codexPromptCacheFingerprint: "fingerprint-a",
			codexPromptCacheInScope:     true,
		},
		{
			ID:                          4,
			HttpCode:                    200,
			OutputTokens:                8,
			CodexPromptCacheEnabled:     true,
			CodexPromptCacheEligible:    true,
			codexPromptCacheBucket:      "bucket-a",
			codexPromptCacheFingerprint: "fingerprint-b",
			codexPromptCacheInScope:     true,
		},
	}

	stats := annotateCodexPromptCacheLogs(logs)
	if stats.EnabledRequests != 3 {
		t.Fatalf("EnabledRequests = %d, want 3", stats.EnabledRequests)
	}
	if stats.EligibleRequests != 3 {
		t.Fatalf("EligibleRequests = %d, want 3", stats.EligibleRequests)
	}
	if stats.MatchableRequests != 2 {
		t.Fatalf("MatchableRequests = %d, want 2", stats.MatchableRequests)
	}
	if stats.HitRequests != 1 {
		t.Fatalf("HitRequests = %d, want 1", stats.HitRequests)
	}
	if !logs[1].CodexPromptCacheMatchable {
		t.Fatal("expected second in-scope request to be marked matchable")
	}
	if !logs[2].CodexPromptCacheMatchable {
		t.Fatal("expected cache-hit request to be marked matchable")
	}
	if logs[3].CodexPromptCacheMatchable {
		t.Fatal("expected different fingerprint to remain non-matchable")
	}
}
