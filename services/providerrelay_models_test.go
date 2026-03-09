package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func isolateHomeDir(t *testing.T) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Windows 下部分环境会通过 HOMEDRIVE + HOMEPATH 推导用户目录，补齐以确保隔离生效
	vol := filepath.VolumeName(home)
	homePath := strings.TrimPrefix(home, vol)
	if homePath == "" {
		homePath = `\`
	}
	t.Setenv("HOMEDRIVE", vol)
	t.Setenv("HOMEPATH", homePath)

	// 清理进程级代理环境变量，避免测试请求被外部代理污染
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")
	t.Setenv("all_proxy", "")
	t.Setenv("no_proxy", "")

	// 重置全局 HTTP 客户端到直连模式，避免其他测试修改的代理配置泄漏
	if err := InitHTTPClient(ProxyConfig{UseProxy: false}); err != nil {
		t.Fatalf("初始化测试 HTTP 客户端失败: %v", err)
	}
}

func TestResponsesCompactRoute(t *testing.T) {
	isolateHomeDir(t)
	gin.SetMode(gin.TestMode)

	upstreamHit := false
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true

		if r.Method != http.MethodPost {
			t.Errorf("期望 POST 请求，收到 %s", r.Method)
		}
		if r.URL.Path != "/v1/responses/compact" {
			t.Errorf("期望转发路径 /v1/responses/compact，收到 %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstreamServer.Close()

	providerService := NewProviderService()
	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)

	testProvider := Provider{
		ID:      1,
		Name:    "TestCodexProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "test-api-key",
		Enabled: true,
		Level:   1,
	}

	if err := providerService.SaveProviders("codex", []Provider{testProvider}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	relayService := NewProviderRelayService(providerService, nil, blacklistService, nil, "")
	router := gin.New()
	relayService.registerRoutes(router)

	body := strings.NewReader(`{"model":"gpt-4.1","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/responses/compact", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("/responses/compact 不应返回 404，响应体: %s", w.Body.String())
	}
	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 %d，收到 %d，响应体: %s", http.StatusOK, w.Code, w.Body.String())
	}
	if !upstreamHit {
		t.Fatal("期望命中 Codex 转发流程并请求上游，但实际上未命中")
	}
}

func TestV1ResponsesCompactRoute(t *testing.T) {
	isolateHomeDir(t)
	gin.SetMode(gin.TestMode)

	upstreamHit := false
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true

		if r.Method != http.MethodPost {
			t.Errorf("期望 POST 请求，收到 %s", r.Method)
		}
		if r.URL.Path != "/v1/responses/compact" {
			t.Errorf("期望转发路径 /v1/responses/compact，收到 %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_v1_compact","object":"response","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstreamServer.Close()

	providerService := NewProviderService()
	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)

	testProvider := Provider{
		ID:      1,
		Name:    "TestCodexProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "test-api-key",
		Enabled: true,
		Level:   1,
	}

	if err := providerService.SaveProviders("codex", []Provider{testProvider}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	relayService := NewProviderRelayService(providerService, nil, blacklistService, nil, "")
	router := gin.New()
	relayService.registerRoutes(router)

	body := strings.NewReader(`{"model":"gpt-4.1","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("/v1/responses/compact 不应返回 404，响应体: %s", w.Body.String())
	}
	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 %d，收到 %d，响应体: %s", http.StatusOK, w.Code, w.Body.String())
	}
	if !upstreamHit {
		t.Fatal("期望命中 Codex 转发流程并请求上游，但实际上未命中")
	}
}

func TestResponsesCompactPassthroughWhenUsageMissing(t *testing.T) {
	isolateHomeDir(t)
	gin.SetMode(gin.TestMode)

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/compact" {
			t.Errorf("期望转发路径 /v1/responses/compact，收到 %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// 模拟 compact 响应不返回 usage 的情况：不应因为 output_tokens==0 被代理吞掉
		_, _ = w.Write([]byte(`{"id":"resp_compact_no_usage","object":"response"}`))
	}))
	defer upstreamServer.Close()

	providerService := NewProviderService()
	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)

	testProvider := Provider{
		ID:      1,
		Name:    "TestCodexProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "test-api-key",
		Enabled: true,
		Level:   1,
	}

	if err := providerService.SaveProviders("codex", []Provider{testProvider}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	relayService := NewProviderRelayService(providerService, nil, blacklistService, nil, "")
	router := gin.New()
	relayService.registerRoutes(router)

	body := strings.NewReader(`{"model":"gpt-4.1","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 %d，收到 %d，响应体: %s", http.StatusOK, w.Code, w.Body.String())
	}
	if w.Body.String() != `{"id":"resp_compact_no_usage","object":"response"}` {
		t.Fatalf("期望响应体原样透传，收到: %s", w.Body.String())
	}
}

func TestResponsesCompactStripsStoreAndStream(t *testing.T) {
	isolateHomeDir(t)
	gin.SetMode(gin.TestMode)

	upstreamHit := false
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		if r.URL.Path != "/v1/responses/compact" {
			t.Errorf("期望转发路径 /v1/responses/compact，收到 %s", r.URL.Path)
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("读取上游请求体失败: %v", err)
		}

		var req map[string]any
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			t.Fatalf("上游收到的请求体不是有效 JSON: %v, body=%s", err, string(bodyBytes))
		}

		if _, ok := req["store"]; ok {
			t.Fatalf("compact 请求体不应包含 store 字段，body=%s", string(bodyBytes))
		}
		if _, ok := req["stream"]; ok {
			t.Fatalf("compact 请求体不应包含 stream 字段，body=%s", string(bodyBytes))
		}

		if req["model"] != "gpt-5.3-codex" {
			t.Fatalf("期望 model=gpt-5.3-codex，收到 %v", req["model"])
		}
		if _, ok := req["input"]; !ok {
			t.Fatalf("期望请求体包含 input 字段，body=%s", string(bodyBytes))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_compact_strip","object":"response"}`))
	}))
	defer upstreamServer.Close()

	providerService := NewProviderService()
	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)

	testProvider := Provider{
		ID:      1,
		Name:    "TestCodexProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "test-api-key",
		Enabled: true,
		Level:   1,
	}

	if err := providerService.SaveProviders("codex", []Provider{testProvider}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	relayService := NewProviderRelayService(providerService, nil, blacklistService, nil, "")
	router := gin.New()
	relayService.registerRoutes(router)

	body := strings.NewReader(`{"model":"gpt-5.3-codex","input":[{"role":"user","content":"hello"}],"store":true,"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 %d，收到 %d，响应体: %s", http.StatusOK, w.Code, w.Body.String())
	}
	if !upstreamHit {
		t.Fatal("期望命中 Codex 转发流程并请求上游，但实际上未命中")
	}
}

func TestPrefixedCodexRouteTargetsProviderByName(t *testing.T) {
	isolateHomeDir(t)
	gin.SetMode(gin.TestMode)

	hitProviderA := false
	hitProviderB := false

	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitProviderA = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_a","object":"response","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstreamA.Close()

	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitProviderB = true
		if r.URL.Path != "/v1/responses" {
			t.Errorf("期望转发路径 /v1/responses，收到 %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_b","object":"response","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstreamB.Close()

	providerService := NewProviderService()
	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)

	providers := []Provider{
		{ID: 1, Name: "alpha", APIURL: upstreamA.URL, APIKey: "k1", Enabled: true, Level: 1},
		{ID: 2, Name: "beta", APIURL: upstreamB.URL, APIKey: "k2", Enabled: true, Level: 1},
	}
	if err := providerService.SaveProviders("codex", providers); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	relayService := NewProviderRelayService(providerService, nil, blacklistService, nil, "")
	router := gin.New()
	relayService.registerRoutes(router)

	body := strings.NewReader(`{"model":"gpt-4.1","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/beta/responses", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 %d，收到 %d，响应体: %s", http.StatusOK, w.Code, w.Body.String())
	}
	if hitProviderA {
		t.Fatal("命中错误 provider：请求不应转发到 alpha")
	}
	if !hitProviderB {
		t.Fatal("请求应转发到指定 provider beta")
	}
}

func TestPrefixedProviderNotFoundReturns404(t *testing.T) {
	isolateHomeDir(t)
	gin.SetMode(gin.TestMode)

	providerService := NewProviderService()
	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)

	providers := []Provider{
		{ID: 1, Name: "alpha", APIURL: "https://example.invalid", APIKey: "k1", Enabled: true, Level: 1},
	}
	if err := providerService.SaveProviders("claude", providers); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	relayService := NewProviderRelayService(providerService, nil, blacklistService, nil, "")
	router := gin.New()
	relayService.registerRoutes(router)

	body := strings.NewReader(`{"model":"claude-sonnet-4","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/missing/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("期望状态码 %d，收到 %d，响应体: %s", http.StatusNotFound, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not found") {
		t.Fatalf("响应体应包含 not found，实际: %s", w.Body.String())
	}
}

// TestModelsHandler 测试 /v1/models 端点处理器
func TestModelsHandler(t *testing.T) {
	isolateHomeDir(t)

	// 设置测试环境
	gin.SetMode(gin.TestMode)

	// 创建模拟的上游服务器
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求方法
		if r.Method != "GET" {
			t.Errorf("期望 GET 请求，收到 %s", r.Method)
		}

		// 验证路径
		if r.URL.Path != "/v1/models" {
			t.Errorf("期望路径 /v1/models，收到 %s", r.URL.Path)
		}

		// 验证认证头（默认应使用 bearer，针对 Codex/OpenAI 平台的 /v1/models）
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			t.Error("缺少 Authorization 头")
		}
		if authHeader != "Bearer test-api-key" {
			t.Errorf("Authorization 头不正确，期望 'Bearer test-api-key'，收到 '%s'", authHeader)
		}

		// 返回模拟的模型列表
		response := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"id":       "claude-sonnet-4",
					"object":   "model",
					"created":  1234567890,
					"owned_by": "anthropic",
				},
				{
					"id":       "claude-opus-4",
					"object":   "model",
					"created":  1234567890,
					"owned_by": "anthropic",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer upstreamServer.Close()

	// 创建测试用的 ProviderService
	providerService := NewProviderService()
	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)

	// 创建测试用的 provider（使用模拟服务器的 URL）
	testProvider := Provider{
		ID:      1,
		Name:    "TestProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "test-api-key",
		Enabled: true,
		Level:   1,
	}

	// 保存 provider 配置（默认 codex 平台）
	err := providerService.SaveProviders("codex", []Provider{testProvider})
	if err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	// 创建 ProviderRelayService
	relayService := NewProviderRelayService(providerService, nil, blacklistService, nil, "")

	// 创建测试路由
	router := gin.New()
	relayService.registerRoutes(router)

	// 创建测试请求
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	// 执行请求
	router.ServeHTTP(w, req)

	// 验证响应状态码
	if w.Code != http.StatusOK {
		t.Errorf("期望状态码 %d，收到 %d", http.StatusOK, w.Code)
		t.Logf("响应体: %s", w.Body.String())
	}

	// 验证响应内容类型
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("期望 Content-Type 为 'application/json'，收到 '%s'", contentType)
	}

	// 验证响应体可以解析为 JSON
	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("响应体不是有效的 JSON: %v", err)
		t.Logf("响应体: %s", w.Body.String())
	}

	// 验证响应包含 data 字段
	if _, ok := response["data"]; !ok {
		t.Error("响应缺少 'data' 字段")
	}
}

// TestCustomModelsHandler 测试自定义 CLI 工具的 /v1/models 端点
func TestCustomModelsHandler(t *testing.T) {
	isolateHomeDir(t)

	// 设置测试环境
	gin.SetMode(gin.TestMode)

	// 创建模拟的上游服务器
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求方法
		if r.Method != "GET" {
			t.Errorf("期望 GET 请求，收到 %s", r.Method)
		}

		// 验证路径
		if r.URL.Path != "/v1/models" {
			t.Errorf("期望路径 /v1/models，收到 %s", r.URL.Path)
		}

		// 验证 Authorization 头
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer custom-api-key" {
			t.Errorf("Authorization 头不正确，期望 'Bearer custom-api-key'，收到 '%s'", authHeader)
		}

		// 返回模拟的模型列表
		response := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"id":      "custom-model-1",
					"object":  "model",
					"created": 1234567890,
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer upstreamServer.Close()

	// 创建测试用的 ProviderService
	providerService := NewProviderService()
	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)

	// 创建测试用的 provider（使用模拟服务器的 URL）
	testProvider := Provider{
		ID:      1,
		Name:    "CustomTestProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "custom-api-key",
		Enabled: true,
		Level:   1,
	}

	// 保存 provider 配置（使用自定义 CLI 工具的 kind）
	toolId := "mytool"
	kind := "custom:" + toolId
	err := providerService.SaveProviders(kind, []Provider{testProvider})
	if err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	// 创建 ProviderRelayService
	relayService := NewProviderRelayService(providerService, nil, blacklistService, nil, "")

	// 创建测试路由
	router := gin.New()
	relayService.registerRoutes(router)

	// 创建测试请求
	req := httptest.NewRequest("GET", "/custom/mytool/v1/models", nil)
	w := httptest.NewRecorder()

	// 执行请求
	router.ServeHTTP(w, req)

	// 验证响应状态码
	if w.Code != http.StatusOK {
		t.Errorf("期望状态码 %d，收到 %d", http.StatusOK, w.Code)
		t.Logf("响应体: %s", w.Body.String())
	}

	// 验证响应内容类型
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("期望 Content-Type 为 'application/json'，收到 '%s'", contentType)
	}

	// 验证响应体可以解析为 JSON
	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("响应体不是有效的 JSON: %v", err)
		t.Logf("响应体: %s", w.Body.String())
	}

	// 验证响应包含 data 字段
	if _, ok := response["data"]; !ok {
		t.Error("响应缺少 'data' 字段")
	}
}

// TestModelsHandler_NoProviders 测试没有可用 provider 的情况
func TestModelsHandler_NoProviders(t *testing.T) {
	isolateHomeDir(t)

	gin.SetMode(gin.TestMode)

	// 创建空的 ProviderService
	providerService := NewProviderService()
	settingsService := &SettingsService{}
	blacklistService := NewBlacklistService(settingsService, nil)

	// 创建 ProviderRelayService（没有配置任何 provider）
	relayService := NewProviderRelayService(providerService, nil, blacklistService, nil, "")

	// 创建测试路由
	router := gin.New()
	relayService.registerRoutes(router)

	// 创建测试请求
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	// 执行请求
	router.ServeHTTP(w, req)

	// 验证响应状态码应该是 404（没有可用的 provider）
	if w.Code != http.StatusNotFound {
		t.Errorf("期望状态码 %d，收到 %d", http.StatusNotFound, w.Code)
	}

	// 验证响应包含错误信息
	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("响应体不是有效的 JSON: %v", err)
	}

	if _, ok := response["error"]; !ok {
		t.Error("响应缺少 'error' 字段")
	}
}
