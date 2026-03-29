package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexPromptCacheHashLen = 16
const codexPromptCacheStableKeyNamespace = "cli-proxy-api:codex:prompt-cache:"

type codexPromptCachePlan struct {
	Enabled     bool
	Eligible    bool
	Key         string
	BucketHash  string
	Fingerprint string
}

func applyCodexPromptCache(provider Provider, endpoint string, headers map[string]string, body []byte) ([]byte, map[string]string, codexPromptCachePlan) {
	plan := codexPromptCachePlan{
		Enabled: provider.CodexPromptCacheEnabled,
	}
	if !plan.Enabled || !isCodexPromptCacheEligibleEndpoint(endpoint) {
		return body, headers, plan
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || !gjson.Valid(trimmed) {
		return body, headers, plan
	}

	fingerprint := buildCodexPromptCacheFingerprint(body)
	if fingerprint == "" {
		return body, headers, plan
	}

	key := resolveCodexPromptCacheKey(provider, body, headers)
	if key == "" {
		return body, headers, plan
	}

	nextBody := body
	result := gjson.GetBytes(nextBody, "prompt_cache_key")
	if !result.Exists() || result.Type != gjson.String || result.String() != key {
		var err error
		nextBody, err = sjson.SetBytes(nextBody, "prompt_cache_key", key)
		if err != nil {
			return body, headers, plan
		}
	}
	if shouldDropCodexPromptCacheRetention(endpoint) {
		var err error
		nextBody, err = sjson.DeleteBytes(nextBody, "prompt_cache_retention")
		if err != nil {
			return body, headers, plan
		}
	}

	nextHeaders := cloneMap(headers)
	if nextHeaders == nil {
		nextHeaders = make(map[string]string, 2)
	}
	nextHeaders["Conversation_id"] = key
	nextHeaders["Session_id"] = key

	plan.Eligible = true
	plan.Key = key
	plan.BucketHash = shortStableHash(key)
	plan.Fingerprint = fingerprint
	return nextBody, nextHeaders, plan
}

func isCodexPromptCacheEligibleEndpoint(endpoint string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(endpoint))
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "/responses")
}

func shouldDropCodexPromptCacheRetention(endpoint string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(endpoint))
	if trimmed == "" {
		return false
	}
	return strings.Contains(trimmed, "/responses") && !strings.Contains(trimmed, "/responses/compact")
}

func resolveCodexPromptCacheKey(provider Provider, body []byte, headers map[string]string) string {
	if key := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); key != "" {
		return key
	}
	return stableCodexPromptCacheKey(provider)
}

func stableCodexPromptCacheKey(provider Provider) string {
	apiKey := strings.TrimSpace(provider.APIKey)
	if apiKey == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(codexPromptCacheStableKeyNamespace+apiKey)).String()
}

func buildCodexPromptCacheFingerprint(body []byte) string {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	root, ok := payload.(map[string]any)
	if !ok {
		return ""
	}

	delete(root, "prompt_cache_key")
	delete(root, "prompt_cache_retention")
	delete(root, "store")
	delete(root, "stream")

	normalized, err := json.Marshal(root)
	if err != nil {
		return ""
	}
	return shortStableHash(string(normalized))
}

func shortStableHash(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:codexPromptCacheHashLen]
}
