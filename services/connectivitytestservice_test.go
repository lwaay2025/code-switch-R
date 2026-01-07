package services

import (
	"encoding/json"
	"testing"
)

func TestConnectivityBuildTestRequestCodexOmitsMaxTokens(t *testing.T) {
	cts := &ConnectivityTestService{}
	provider := Provider{}

	body, _ := cts.buildTestRequest("codex", &provider)

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	if _, ok := req["max_tokens"]; ok {
		t.Fatal("max_tokens should not be included for Codex connectivity tests")
	}
}
