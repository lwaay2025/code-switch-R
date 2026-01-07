package services

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHealthCheck_ModelMapping 测试健康检查是否正确应用模型映射
// 确保健康检查使用映射后的模型名发送请求（与 ProviderRelayService 行为一致）
func TestHealthCheck_ModelMapping(t *testing.T) {
	// 创建测试服务器，记录接收到的请求
	var receivedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 读取请求体
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("Failed to read request body: %v", err)
		}
		defer r.Body.Close()

		// 解析模型名
		var reqData map[string]interface{}
		if err := json.Unmarshal(body, &reqData); err != nil {
			t.Fatalf("Failed to parse request JSON: %v", err)
		}

		if model, ok := reqData["model"].(string); ok {
			receivedModel = model
		}

		// 返回成功响应
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	// 创建带有模型映射的 Provider
	provider := Provider{
		ID:      1,
		Name:    "test-provider",
		APIURL:  server.URL,
		APIKey:  "test-key",
		Enabled: true,
		ModelMapping: map[string]string{
			"gpt-4o-mini": "openai/gpt-4o-mini", // 映射：测试模型 -> 上游模型
		},
	}

	// 创建健康检查服务
	hcs := &HealthCheckService{
		client: http.DefaultClient,
	}

	// 执行健康检查
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := hcs.checkProvider(ctx, provider, "codex")

	// 验证结果
	if result.Status != HealthStatusOperational {
		t.Errorf("Expected status %s, got %s (error: %s)", HealthStatusOperational, result.Status, result.ErrorMessage)
	}

	// 关键验证：确保服务器接收到的是映射后的模型名
	expectedModel := "openai/gpt-4o-mini"
	if receivedModel != expectedModel {
		t.Errorf("Model mapping not applied: expected %s, got %s", expectedModel, receivedModel)
	}
}

// TestHealthCheck_AcceptHeader 测试健康检查是否包含 Accept header
func TestHealthCheck_AcceptHeader(t *testing.T) {
	// 创建测试服务器，检查请求头
	var hasAcceptHeader bool
	var acceptValue string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptValue = r.Header.Get("Accept")
		hasAcceptHeader = acceptValue != ""

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	// 创建 Provider
	provider := Provider{
		ID:      1,
		Name:    "test-provider",
		APIURL:  server.URL,
		APIKey:  "test-key",
		Enabled: true,
	}

	// 创建健康检查服务
	hcs := &HealthCheckService{
		client: http.DefaultClient,
	}

	// 执行健康检查
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = hcs.checkProvider(ctx, provider, "codex")

	// 验证 Accept header 存在
	if !hasAcceptHeader {
		t.Error("Accept header is missing from health check request")
	}

	// 验证 Accept header 值
	expectedAccept := "application/json"
	if acceptValue != expectedAccept {
		t.Errorf("Accept header incorrect: expected %s, got %s", expectedAccept, acceptValue)
	}
}

// TestHealthCheck_EndpointResolution 测试端点解析是否与 ProviderRelayService 一致
func TestHealthCheck_EndpointResolution(t *testing.T) {
	tests := []struct {
		name             string
		provider         Provider
		platform         string
		expectedEndpoint string
	}{
		{
			name: "使用平台默认端点（Codex）",
			provider: Provider{
				APIEndpoint: "",
			},
			platform:         "codex",
			expectedEndpoint: "/responses",
		},
		{
			name: "使用平台默认端点（Claude）",
			provider: Provider{
				APIEndpoint: "",
			},
			platform:         "claude",
			expectedEndpoint: "/v1/messages",
		},
		{
			name: "使用用户配置的端点",
			provider: Provider{
				APIEndpoint: "/custom/endpoint",
			},
			platform:         "codex",
			expectedEndpoint: "/custom/endpoint",
		},
		{
			name: "使用健康检查专用端点",
			provider: Provider{
				APIEndpoint: "/production/endpoint",
				AvailabilityConfig: &AvailabilityConfig{
					TestEndpoint: "/health/check",
				},
			},
			platform:         "codex",
			expectedEndpoint: "/health/check",
		},
	}

	hcs := &HealthCheckService{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := hcs.getEffectiveEndpoint(&tt.provider, tt.platform)
			if endpoint != tt.expectedEndpoint {
				t.Errorf("Expected endpoint %s, got %s", tt.expectedEndpoint, endpoint)
			}
		})
	}
}

// TestBuildTestRequest 测试构建测试请求体
func TestBuildTestRequest(t *testing.T) {
	hcs := &HealthCheckService{}

	tests := []struct {
		name          string
		platform      string
		model         string
		expectNonNil  bool
		validateModel bool
	}{
		{
			name:          "Codex 平台请求",
			platform:      "codex",
			model:         "gpt-4o-mini",
			expectNonNil:  true,
			validateModel: true,
		},
		{
			name:          "Claude 平台请求",
			platform:      "claude",
			model:         "claude-3-5-haiku-20241022",
			expectNonNil:  true,
			validateModel: true,
		},
		{
			name:          "映射后的模型名",
			platform:      "codex",
			model:         "openai/gpt-4o-mini",
			expectNonNil:  true,
			validateModel: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := hcs.buildTestRequest(tt.platform, tt.model)

			if tt.expectNonNil && reqBody == nil {
				t.Error("Expected non-nil request body")
				return
			}

			if !tt.expectNonNil && reqBody != nil {
				t.Error("Expected nil request body")
				return
			}

			if tt.validateModel && reqBody != nil {
				// 解析请求体验证模型名
				var reqData map[string]interface{}
				if err := json.Unmarshal(reqBody, &reqData); err != nil {
					t.Fatalf("Failed to parse request body: %v", err)
				}

				model, ok := reqData["model"].(string)
				if !ok {
					t.Error("Model field missing or not a string")
					return
				}

				if model != tt.model {
					t.Errorf("Expected model %s in request body, got %s", tt.model, model)
				}
			}
		})
	}
}

// TestHealthCheck_NoModelMapping 测试没有模型映射时的行为
// 确保在没有映射的情况下，使用原始模型名
func TestHealthCheck_NoModelMapping(t *testing.T) {
	var receivedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqData map[string]interface{}
		json.Unmarshal(body, &reqData)
		if model, ok := reqData["model"].(string); ok {
			receivedModel = model
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	// Provider 没有配置模型映射
	provider := Provider{
		ID:           1,
		Name:         "test-provider",
		APIURL:       server.URL,
		APIKey:       "test-key",
		Enabled:      true,
		ModelMapping: nil, // 没有映射
	}

	hcs := &HealthCheckService{
		client: http.DefaultClient,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := hcs.checkProvider(ctx, provider, "codex")

	if result.Status != HealthStatusOperational {
		t.Errorf("Expected status %s, got %s", HealthStatusOperational, result.Status)
	}

	// 验证使用的是原始模型名（gpt-4o-mini 是 codex 平台的默认测试模型）
	expectedModel := "gpt-4o-mini"
	if receivedModel != expectedModel {
		t.Errorf("Expected original model %s, got %s", expectedModel, receivedModel)
	}
}

// TestHealthCheck_RequestBodyStructure 测试请求体结构
func TestHealthCheck_RequestBodyStructure(t *testing.T) {
	hcs := &HealthCheckService{}

	platforms := []string{"claude", "codex"}
	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			reqBody := hcs.buildTestRequest(platform, "test-model")
			if reqBody == nil {
				t.Fatal("Request body is nil")
			}

			var reqData map[string]interface{}
			if err := json.Unmarshal(reqBody, &reqData); err != nil {
				t.Fatalf("Failed to parse request body: %v", err)
			}

			// 验证必需字段
			requiredFields := []string{"model", "max_tokens", "messages"}
			for _, field := range requiredFields {
				if _, ok := reqData[field]; !ok {
					t.Errorf("Required field %s is missing", field)
				}
			}

			// 验证 messages 结构
			messages, ok := reqData["messages"].([]interface{})
			if !ok {
				t.Error("messages field is not an array")
				return
			}

			if len(messages) == 0 {
				t.Error("messages array is empty")
			}
		})
	}
}

// BenchmarkCheckProvider 基准测试：健康检查性能
func BenchmarkCheckProvider(b *testing.B) {
	// 创建模拟服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	provider := Provider{
		ID:      1,
		Name:    "bench-provider",
		APIURL:  server.URL,
		APIKey:  "test-key",
		Enabled: true,
		ModelMapping: map[string]string{
			"gpt-4o-mini": "openai/gpt-4o-mini",
		},
	}

	hcs := &HealthCheckService{
		client: http.DefaultClient,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = hcs.checkProvider(ctx, provider, "codex")
		cancel()
	}
}

// BenchmarkBuildTestRequest 基准测试：构建请求体性能
func BenchmarkBuildTestRequest(b *testing.B) {
	hcs := &HealthCheckService{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hcs.buildTestRequest("codex", "gpt-4o-mini")
	}
}
