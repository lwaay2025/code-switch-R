package services

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
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
	hcs := &HealthCheckService{}

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
	var userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptValue = r.Header.Get("Accept")
		hasAcceptHeader = acceptValue != ""
		userAgent = r.Header.Get("User-Agent")

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
	hcs := &HealthCheckService{}

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

	// 验证 User-Agent header
	if userAgent == "" {
		t.Errorf("User-Agent header missing")
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
		tokensField   string
		expectInput   bool
		expectMessage bool
	}{
		{
			name:          "Codex 平台请求",
			platform:      "codex",
			model:         "gpt-4o-mini",
			expectNonNil:  true,
			validateModel: true,
			tokensField:   "max_output_tokens",
			expectInput:   true,
			expectMessage: false,
		},
		{
			name:          "Claude 平台请求",
			platform:      "claude",
			model:         "claude-3-5-haiku-20241022",
			expectNonNil:  true,
			validateModel: true,
			tokensField:   "max_tokens",
			expectInput:   false,
			expectMessage: true,
		},
		{
			name:          "映射后的模型名",
			platform:      "codex",
			model:         "openai/gpt-4o-mini",
			expectNonNil:  true,
			validateModel: true,
			tokensField:   "max_output_tokens",
			expectInput:   true,
			expectMessage: false,
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

				if tt.tokensField != "" {
					if _, ok := reqData[tt.tokensField]; !ok {
						t.Errorf("Expected %s in request body but not found", tt.tokensField)
					}
				}

				_, hasMessages := reqData["messages"]
				if tt.expectMessage && !hasMessages {
					t.Error("Expected messages in request body but not found")
				}
				if !tt.expectMessage && hasMessages {
					t.Error("messages should not be included for this platform")
				}

				_, hasInput := reqData["input"]
				if tt.expectInput && !hasInput {
					t.Error("Expected input in request body but not found")
				}
				if !tt.expectInput && hasInput {
					t.Error("input should not be included for this platform")
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

	hcs := &HealthCheckService{}

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

			if platform == "claude" {
				requiredFields := []string{"model", "max_tokens", "messages"}
				for _, field := range requiredFields {
					if _, ok := reqData[field]; !ok {
						t.Errorf("Required field %s is missing", field)
					}
				}

				messages, ok := reqData["messages"].([]interface{})
				if !ok {
					t.Error("messages field is not an array")
					return
				}
				if len(messages) == 0 {
					t.Error("messages array is empty")
				}
			}

			if platform == "codex" {
				requiredFields := []string{"model", "input", "max_output_tokens"}
				for _, field := range requiredFields {
					if _, ok := reqData[field]; !ok {
						t.Errorf("Required field %s is missing", field)
					}
				}

				v, ok := reqData["input"].([]interface{})
				if !ok {
					t.Errorf("input should be array, got %T", reqData["input"])
					return
				}
				if len(v) == 0 {
					t.Error("input array is empty")
					return
				}
				first, ok := v[0].(map[string]interface{})
				if !ok {
					t.Errorf("input array first element should be object, got %T", v[0])
					return
				}
				role, _ := first["role"].(string)
				content, _ := first["content"].(string)
				if strings.TrimSpace(role) == "" || strings.TrimSpace(content) == "" {
					t.Errorf("input first item missing role/content: %v", first)
				}
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

	hcs := &HealthCheckService{}

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

// TestHealthCheck_ProxyLogging 测试健康检查是否正确记录代理信息
func TestHealthCheck_ProxyLogging(t *testing.T) {
	// 捕获日志输出
	var logBuffer bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logBuffer)
	defer log.SetOutput(oldOutput) // 恢复原始日志输出

	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	// 测试用例：直连模式
	t.Run("DirectConnection", func(t *testing.T) {
		logBuffer.Reset()

		// 设置直连模式
		InitHTTPClient(ProxyConfig{UseProxy: false})

		provider := Provider{
			ID:      1,
			Name:    "test-provider-direct",
			APIURL:  server.URL,
			APIKey:  "test-key",
			Enabled: true,
		}

		hcs := &HealthCheckService{}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = hcs.checkProvider(ctx, provider, "codex")

		// 验证日志包含"直连"
		logOutput := logBuffer.String()
		if !strings.Contains(logOutput, "直连") {
			t.Errorf("日志应该包含'直连'，实际日志: %s", logOutput)
		}
		if !strings.Contains(logOutput, "发起可用性检测") {
			t.Error("日志应该包含'发起可用性检测'")
		}
		if !strings.Contains(logOutput, "检测结果") {
			t.Error("日志应该包含'检测结果'")
		}
	})

	// 测试用例：代理模式
	t.Run("ProxyConnection", func(t *testing.T) {
		logBuffer.Reset()

		// 设置代理模式
		InitHTTPClient(ProxyConfig{
			UseProxy:     true,
			ProxyAddress: "http://127.0.0.1:7890",
			ProxyType:    "http",
		})

		provider := Provider{
			ID:      2,
			Name:    "test-provider-proxy",
			APIURL:  server.URL,
			APIKey:  "test-key",
			Enabled: true,
		}

		hcs := &HealthCheckService{}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = hcs.checkProvider(ctx, provider, "codex")

		// 验证日志包含代理地址
		logOutput := logBuffer.String()
		if !strings.Contains(logOutput, "代理 http://127.0.0.1:7890") {
			t.Errorf("日志应该包含'代理 http://127.0.0.1:7890'，实际日志: %s", logOutput)
		}
		if !strings.Contains(logOutput, "发起可用性检测") {
			t.Error("日志应该包含'发起可用性检测'")
		}
		if !strings.Contains(logOutput, "检测结果") {
			t.Error("日志应该包含'检测结果'")
		}
	})
}

// TestHealthCheck_DynamicHTTPClient 测试健康检查是否动态获取 HTTP 客户端
func TestHealthCheck_DynamicHTTPClient(t *testing.T) {
	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	provider := Provider{
		ID:      1,
		Name:    "test-provider",
		APIURL:  server.URL,
		APIKey:  "test-key",
		Enabled: true,
	}

	hcs := &HealthCheckService{}

	// 第一次检测：使用直连
	InitHTTPClient(ProxyConfig{UseProxy: false})
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()
	result1 := hcs.checkProvider(ctx1, provider, "codex")
	if result1.Status != HealthStatusOperational {
		t.Errorf("第一次检测应该成功，实际状态: %s", result1.Status)
	}

	// 更新代理配置
	UpdateHTTPClient(ProxyConfig{
		UseProxy:     true,
		ProxyAddress: "http://127.0.0.1:7890",
		ProxyType:    "http",
	})

	// 第二次检测：应该使用新的代理配置（尽管代理不可达，但我们测试的是配置是否更新）
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	result2 := hcs.checkProvider(ctx2, provider, "codex")

	// 注意：由于测试服务器在本地，即使设置了不可达的代理，请求也可能成功或失败
	// 这里我们主要验证代码能够正常执行，不会 panic
	if result2 == nil {
		t.Error("第二次检测不应该返回 nil")
	}
}

// TestGetProxyConfig 测试 GetProxyConfig 函数
func TestGetProxyConfig(t *testing.T) {
	// 测试初始配置
	InitHTTPClient(ProxyConfig{
		UseProxy:     false,
		ProxyAddress: "",
		ProxyType:    "",
	})

	config := GetProxyConfig()
	if config.UseProxy {
		t.Error("初始配置应该是直连模式")
	}

	// 更新为代理模式
	UpdateHTTPClient(ProxyConfig{
		UseProxy:     true,
		ProxyAddress: "http://proxy.example.com:8080",
		ProxyType:    "http",
	})

	config = GetProxyConfig()
	if !config.UseProxy {
		t.Error("配置应该是代理模式")
	}
	if config.ProxyAddress != "http://proxy.example.com:8080" {
		t.Errorf("代理地址错误，期望: %s，实际: %s", "http://proxy.example.com:8080", config.ProxyAddress)
	}
	if config.ProxyType != "http" {
		t.Errorf("代理类型错误，期望: %s，实际: %s", "http", config.ProxyType)
	}
}
