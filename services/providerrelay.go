package services

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daodao97/xgo/xdb"
	"github.com/daodao97/xgo/xrequest"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// LastUsedProvider 最后使用的供应商信息
// @author sm
type LastUsedProvider struct {
	Platform     string `json:"platform"`      // claude/codex/gemini
	ProviderName string `json:"provider_name"` // 供应商名称
	UpdatedAt    int64  `json:"updated_at"`    // 更新时间（毫秒）
}

type ProviderRelayService struct {
	providerService     *ProviderService
	geminiService       *GeminiService
	blacklistService    *BlacklistService
	notificationService *NotificationService
	concurrencyManager  *ProviderConcurrencyManager
	server              *http.Server
	addr                string
	lastUsed            map[string]*LastUsedProvider // 各平台最后使用的供应商
	lastUsedMu          sync.RWMutex                 // 保护 lastUsed 的锁
}

// errClientAbort 表示客户端中断连接，不应计入 provider 失败次数
var errClientAbort = errors.New("client aborted, skip failure count")

// errTokenZero 表示上游返回 2xx，但解析到 output_tokens=0（视为失败）
var errTokenZero = errors.New("output_tokens is 0")

func isResponsesCompactVariantEndpoint(endpoint string) bool {
	lowerEndpoint := strings.ToLower(strings.TrimSpace(endpoint))
	return strings.Contains(lowerEndpoint, "/responses/compact")
}

func normalizeOpenAIResponsesCompactRequestBody(bodyBytes []byte) ([]byte, bool, error) {
	if len(bytes.TrimSpace(bodyBytes)) == 0 {
		return bodyBytes, false, nil
	}

	withoutStore, err := sjson.DeleteBytes(bodyBytes, "store")
	if err != nil {
		return nil, false, err
	}
	withoutStream, err := sjson.DeleteBytes(withoutStore, "stream")
	if err != nil {
		return nil, false, err
	}
	return withoutStream, !bytes.Equal(withoutStream, bodyBytes), nil
}

func isLikelyClientAbortErr(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		return true
	}
	lowerMsg := strings.ToLower(err.Error())
	abortHints := []string{
		"broken pipe",
		"connection reset by peer",
		"connection aborted",
		"client disconnected",
		"context canceled",
		"operation was canceled",
		"use of closed network connection",
	}
	for _, hint := range abortHints {
		if strings.Contains(lowerMsg, hint) {
			return true
		}
	}
	return false
}

func NewProviderRelayService(providerService *ProviderService, geminiService *GeminiService, blacklistService *BlacklistService, notificationService *NotificationService, addr string) *ProviderRelayService {
	if addr == "" {
		addr = "127.0.0.1:18100" // 【安全修复】仅监听本地回环地址，防止 API Key 暴露到局域网
	}

	// 【修复】数据库初始化已移至 main.go 的 InitDatabase()
	// 此处不再调用 xdb.Inits()、ensureRequestLogTable()、ensureBlacklistTables()

	return &ProviderRelayService{
		providerService:     providerService,
		geminiService:       geminiService,
		blacklistService:    blacklistService,
		notificationService: notificationService,
		concurrencyManager:  NewProviderConcurrencyManager(),
		addr:                addr,
		lastUsed: map[string]*LastUsedProvider{
			"claude": nil,
			"codex":  nil,
			"gemini": nil,
		},
	}
}

// setLastUsedProvider 记录最后使用的供应商
// @author sm
func (prs *ProviderRelayService) setLastUsedProvider(platform, providerName string) {
	prs.lastUsedMu.Lock()
	defer prs.lastUsedMu.Unlock()
	prs.lastUsed[platform] = &LastUsedProvider{
		Platform:     platform,
		ProviderName: providerName,
		UpdatedAt:    time.Now().UnixMilli(),
	}
}

// GetLastUsedProvider 获取指定平台最后使用的供应商
// @author sm
func (prs *ProviderRelayService) GetLastUsedProvider(platform string) *LastUsedProvider {
	prs.lastUsedMu.RLock()
	defer prs.lastUsedMu.RUnlock()
	return prs.lastUsed[platform]
}

// GetAllLastUsedProviders 获取所有平台最后使用的供应商
// @author sm
func (prs *ProviderRelayService) GetAllLastUsedProviders() map[string]*LastUsedProvider {
	prs.lastUsedMu.RLock()
	defer prs.lastUsedMu.RUnlock()
	result := make(map[string]*LastUsedProvider)
	for k, v := range prs.lastUsed {
		result[k] = v
	}
	return result
}

func (prs *ProviderRelayService) Start() error {
	// 启动前验证配置
	if warnings := prs.validateConfig(); len(warnings) > 0 {
		fmt.Println("======== Provider 配置验证警告 ========")
		for _, warn := range warnings {
			fmt.Printf("⚠️  %s\n", warn)
		}
		fmt.Println("========================================")
	}

	router := gin.Default()
	prs.registerRoutes(router)

	prs.server = &http.Server{
		Addr:    prs.addr,
		Handler: router,
	}

	fmt.Printf("provider relay server listening on %s\n", prs.addr)

	go func() {
		if err := prs.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("provider relay server error: %v\n", err)
		}
	}()
	return nil
}

// validateConfig 验证所有 provider 的配置
// 返回警告列表（非阻塞性错误）
func (prs *ProviderRelayService) validateConfig() []string {
	warnings := make([]string, 0)

	for _, kind := range []string{"claude", "codex"} {
		providers, err := prs.providerService.LoadProviders(kind)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("[%s] 加载配置失败: %v", kind, err))
			continue
		}

		enabledCount := 0
		for _, p := range providers {
			if !p.Enabled {
				continue
			}
			enabledCount++

			// 验证每个启用的 provider
			if errs := p.ValidateConfiguration(); len(errs) > 0 {
				for _, errMsg := range errs {
					warnings = append(warnings, fmt.Sprintf("[%s/%s] %s", kind, p.Name, errMsg))
				}
			}

			// 检查是否配置了模型白名单或映射
			if (p.SupportedModels == nil || len(p.SupportedModels) == 0) &&
				(p.ModelMapping == nil || len(p.ModelMapping) == 0) {
				warnings = append(warnings, fmt.Sprintf(
					"[%s/%s] 未配置 supportedModels 或 modelMapping，将假设支持所有模型（可能导致降级失败）",
					kind, p.Name))
			}

			// 检查是否只配置了映射但没有白名单
			if len(p.ModelMapping) > 0 && len(p.SupportedModels) == 0 {
				warnings = append(warnings, fmt.Sprintf(
					"[%s/%s] 配置了 modelMapping 但未配置 supportedModels，映射目标将不做校验，请确认目标模型在供应商处可用",
					kind, p.Name))
			}
		}

		if enabledCount == 0 {
			warnings = append(warnings, fmt.Sprintf("[%s] 没有启用的 provider", kind))
		}
	}

	return warnings
}

func (prs *ProviderRelayService) Stop() error {
	if prs.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return prs.server.Shutdown(ctx)
}

func (prs *ProviderRelayService) Addr() string {
	return prs.addr
}

func (prs *ProviderRelayService) registerRoutes(router gin.IRouter) {
	router.POST("/v1/messages", prs.proxyHandler("claude", "/v1/messages"))
	router.POST("/:providerName/v1/messages", prs.proxyHandler("claude", "/v1/messages"))

	// OpenAI Responses API-compatible routes (Codex / OpenAI Gateway)
	// Keep both "/responses" and "/v1/responses" to match different client base_url behaviors.
	router.POST("/v1/responses", prs.proxyHandler("codex", "/v1/responses"))
	router.POST("/v1/responses/compact", prs.proxyHandler("codex", "/v1/responses/compact"))
	router.POST("/:providerName/v1/responses", prs.proxyHandler("codex", "/v1/responses"))
	router.POST("/:providerName/v1/responses/compact", prs.proxyHandler("codex", "/v1/responses/compact"))

	router.POST("/responses", prs.proxyHandler("codex", "/v1/responses"))
	router.POST("/responses/compact", prs.proxyHandler("codex", "/v1/responses/compact"))
	router.POST("/:providerName/responses", prs.proxyHandler("codex", "/v1/responses"))
	router.POST("/:providerName/responses/compact", prs.proxyHandler("codex", "/v1/responses/compact"))

	// /v1/models 端点（OpenAI-compatible API）
	// 默认走 Codex 平台（OpenAI/GPT 风格）
	router.GET("/v1/models", prs.modelsHandler("codex"))

	// Gemini API 端点（使用专门的路径前缀避免与 Claude 冲突）
	router.POST("/gemini/v1beta/*any", prs.geminiProxyHandler("/v1beta"))
	router.POST("/gemini/v1/*any", prs.geminiProxyHandler("/v1"))

	// 自定义 CLI 工具端点（路由格式: /custom/:toolId/v1/messages）
	// toolId 用于区分不同的 CLI 工具，对应 provider kind 为 "custom:{toolId}"
	router.POST("/custom/:toolId/v1/messages", prs.customCliProxyHandler())

	// 自定义 CLI 工具的 /v1/models 端点
	router.GET("/custom/:toolId/v1/models", prs.customModelsHandler())
}

func (prs *ProviderRelayService) proxyHandler(kind string, endpoint string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var bodyBytes []byte
		if c.Request.Body != nil {
			data, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
				return
			}
			bodyBytes = data
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		if isResponsesCompactVariantEndpoint(endpoint) {
			normalized, _, err := normalizeOpenAIResponsesCompactRequestBody(bodyBytes)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
				return
			}
			bodyBytes = normalized
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		isStream := gjson.GetBytes(bodyBytes, "stream").Bool()
		requestedModel := gjson.GetBytes(bodyBytes, "model").String()

		// 如果未指定模型，记录警告但不拦截
		if requestedModel == "" {
			fmt.Printf("[WARN] 请求未指定模型名，无法执行模型智能降级\n")
		}

		providers, err := prs.providerService.LoadProviders(kind)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load providers"})
			return
		}

		providerName := strings.TrimSpace(c.Param("providerName"))
		if providerName != "" {
			var selected *Provider
			for i := range providers {
				if strings.EqualFold(strings.TrimSpace(providers[i].Name), providerName) {
					selected = &providers[i]
					break
				}
			}

			if selected == nil {
				c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("provider '%s' not found", providerName)})
				return
			}

			provider := *selected
			if !provider.Enabled {
				c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("provider '%s' is disabled", provider.Name)})
				return
			}
			if strings.TrimSpace(provider.APIURL) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("provider '%s' missing api url", provider.Name)})
				return
			}
			if strings.TrimSpace(provider.APIKey) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("provider '%s' missing api key", provider.Name)})
				return
			}
			if errs := provider.ValidateConfiguration(); len(errs) > 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("provider '%s' invalid configuration", provider.Name), "details": errs})
				return
			}
			if requestedModel != "" && !provider.IsModelSupported(requestedModel) {
				c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("provider '%s' does not support model '%s'", provider.Name, requestedModel)})
				return
			}
			if isBlacklisted, until := prs.blacklistService.IsBlacklisted(kind, provider.Name); isBlacklisted {
				c.JSON(http.StatusForbidden, gin.H{
					"error":       fmt.Sprintf("provider '%s' is blacklisted", provider.Name),
					"blacklisted": true,
					"until":       until.Unix(),
				})
				return
			}

			effectiveModel := provider.GetEffectiveModel(requestedModel)
			currentBodyBytes := bodyBytes
			if effectiveModel != requestedModel && requestedModel != "" {
				modifiedBody, err := ReplaceModelInRequestBody(bodyBytes, effectiveModel)
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": "failed to map requested model"})
					return
				}
				currentBodyBytes = modifiedBody
			}

			query := flattenQuery(c.Request.URL.Query())
			clientHeaders := cloneHeaders(c.Request.Header)
			effectiveEndpoint := endpoint
			if !isResponsesCompactVariantEndpoint(endpoint) {
				effectiveEndpoint = provider.GetEffectiveEndpoint(endpoint)
			}
			release, acquired := prs.concurrencyManager.TryAcquire(
				providerConcurrencyKey(kind, provider.Name),
				provider.MaxConcurrentRequests,
			)
			if !acquired {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "provider is busy"})
				return
			}

			ok, forwardErr, responseWritten := prs.forwardRequest(c, kind, provider, effectiveEndpoint, query, clientHeaders, currentBodyBytes, isStream, effectiveModel)
			release()
			if ok {
				if err := prs.blacklistService.RecordSuccess(kind, provider.Name); err != nil {
					fmt.Printf("[WARN] 清零失败计数失败: %v\n", err)
				}
				prs.setLastUsedProvider(kind, provider.Name)
				return
			}

			if errors.Is(forwardErr, errClientAbort) {
				return
			}
			if err := prs.blacklistService.RecordFailure(kind, provider.Name); err != nil {
				fmt.Printf("[ERROR] 记录失败到黑名单失败: %v\n", err)
			}
			if responseWritten {
				return
			}

			errorMsg := "unknown error"
			if forwardErr != nil {
				errorMsg = forwardErr.Error()
			}
			c.JSON(http.StatusBadGateway, gin.H{
				"error":         fmt.Sprintf("provider '%s' request failed: %s", provider.Name, errorMsg),
				"provider_name": provider.Name,
			})
			return
		}

		active := make([]Provider, 0, len(providers))
		skippedCount := 0
		for _, provider := range providers {
			// 基础过滤：enabled、URL、APIKey
			if !provider.Enabled || provider.APIURL == "" || provider.APIKey == "" {
				continue
			}

			// 配置验证：失败则自动跳过
			if errs := provider.ValidateConfiguration(); len(errs) > 0 {
				fmt.Printf("[WARN] Provider %s 配置验证失败，已自动跳过: %v\n", provider.Name, errs)
				skippedCount++
				continue
			}

			// 核心过滤：只保留支持请求模型的 provider
			if requestedModel != "" && !provider.IsModelSupported(requestedModel) {
				fmt.Printf("[INFO] Provider %s 不支持模型 %s，已跳过\n", provider.Name, requestedModel)
				skippedCount++
				continue
			}

			// 黑名单检查：跳过已拉黑的 provider
			if isBlacklisted, until := prs.blacklistService.IsBlacklisted(kind, provider.Name); isBlacklisted {
				fmt.Printf("⛔ Provider %s 已拉黑，过期时间: %v\n", provider.Name, until.Format("15:04:05"))
				skippedCount++
				continue
			}

			active = append(active, provider)
		}

		if len(active) == 0 {
			if requestedModel != "" {
				c.JSON(http.StatusNotFound, gin.H{
					"error": fmt.Sprintf("没有可用的 provider 支持模型 '%s'（已跳过 %d 个不兼容的 provider）", requestedModel, skippedCount),
				})
			} else {
				c.JSON(http.StatusNotFound, gin.H{"error": "no providers available"})
			}
			return
		}

		fmt.Printf("[INFO] 找到 %d 个可用的 provider（已过滤 %d 个）：", len(active), skippedCount)
		for _, p := range active {
			fmt.Printf("%s ", p.Name)
		}
		fmt.Println()

		// 按 Level 分组
		levelGroups := make(map[int][]Provider)
		for _, provider := range active {
			level := provider.Level
			if level <= 0 {
				level = 1 // 未配置或零值时默认为 Level 1
			}
			levelGroups[level] = append(levelGroups[level], provider)
		}

		// 获取所有 level 并升序排序
		levels := make([]int, 0, len(levelGroups))
		for level := range levelGroups {
			levels = append(levels, level)
		}
		sort.Ints(levels)

		fmt.Printf("[INFO] 共 %d 个 Level 分组：%v\n", len(levels), levels)

		query := flattenQuery(c.Request.URL.Query())
		clientHeaders := cloneHeaders(c.Request.Header)

		// 获取拉黑功能开关状态
		blacklistEnabled := prs.blacklistService.ShouldUseFixedMode()

		// 【拉黑模式】：同 Provider 内重试（maxRetryPerProvider），失败按“整组重试”计数后切换到下一个 Provider
		// 设计目标：解耦“重试次数”与“失败阈值/拉黑阈值”，避免单次请求内重试导致失败计数过快累加
		if blacklistEnabled {
			fmt.Printf("[INFO] 🔒 拉黑模式已开启（同 Provider 内重试，失败按组计数后切换）\n")

			// 获取重试配置
			retryConfig := prs.blacklistService.GetRetryConfig()
			maxRetryPerProvider := retryConfig.MaxRetryPerProvider
			retryWaitSeconds := retryConfig.RetryWaitSeconds
			fmt.Printf("[INFO] 重试配置: 每 Provider 最多 %d 次重试，间隔 %d 秒\n",
				maxRetryPerProvider, retryWaitSeconds)

			var lastError error
			var lastProvider string
			totalAttempts := 0
			busySkipped := 0
			attemptedUpstream := false

			// 遍历所有 Level 和 Provider
			for _, level := range levels {
				providersInLevel := levelGroups[level]
				fmt.Printf("[INFO] === 尝试 Level %d（%d 个 provider）===\n", level, len(providersInLevel))

				for _, provider := range providersInLevel {
					// 检查是否已被拉黑（跳过已拉黑的 provider）
					if blacklisted, until := prs.blacklistService.IsBlacklisted(kind, provider.Name); blacklisted {
						fmt.Printf("[INFO] ⏭️ 跳过已拉黑的 Provider: %s (解禁时间: %v)\n", provider.Name, until)
						continue
					}

					// 获取实际模型名
					effectiveModel := provider.GetEffectiveModel(requestedModel)
					currentBodyBytes := bodyBytes
					if effectiveModel != requestedModel && requestedModel != "" {
						fmt.Printf("[INFO] Provider %s 映射模型: %s -> %s\n", provider.Name, requestedModel, effectiveModel)
						modifiedBody, err := ReplaceModelInRequestBody(bodyBytes, effectiveModel)
						if err != nil {
							fmt.Printf("[ERROR] 模型映射失败: %v，跳过此 Provider\n", err)
							continue
						}
						currentBodyBytes = modifiedBody
					}

					// 获取有效端点
					effectiveEndpoint := endpoint
					if !isResponsesCompactVariantEndpoint(endpoint) {
						effectiveEndpoint = provider.GetEffectiveEndpoint(endpoint)
					}

					// 同 Provider 内重试循环
					attemptedCount := 0
					stoppedEarlyDueToConcurrency := false
					var lastAttemptErr error
					for attempt := 0; attempt < maxRetryPerProvider; attempt++ {
						// 再次检查是否已被拉黑（重试过程中可能被拉黑）
						if blacklisted, _ := prs.blacklistService.IsBlacklisted(kind, provider.Name); blacklisted {
							fmt.Printf("[INFO] 🚫 Provider %s 已被拉黑，切换到下一个\n", provider.Name)
							break
						}

						fmt.Printf("[INFO] [拉黑模式] Provider: %s (Level %d) | 重试 %d/%d | Model: %s\n",
							provider.Name, level, attempt+1, maxRetryPerProvider, effectiveModel)

						release, acquired := prs.concurrencyManager.TryAcquire(
							providerConcurrencyKey(kind, provider.Name),
							provider.MaxConcurrentRequests,
						)
						if !acquired {
							busySkipped++
							fmt.Printf("[INFO] ⏭️ Provider %s 达到并发上限(%d)，跳过到下一个\n", provider.Name, provider.MaxConcurrentRequests)
							stoppedEarlyDueToConcurrency = true
							break
						}

						totalAttempts++
						attemptedCount++
						attemptedUpstream = true
						startTime := time.Now()
						ok, err, responseWritten := prs.forwardRequest(c, kind, provider, effectiveEndpoint, query, clientHeaders, currentBodyBytes, isStream, effectiveModel)
						duration := time.Since(startTime)
						release()

						if ok {
							fmt.Printf("[INFO] ✓ 成功: %s | 重试 %d 次 | 耗时: %.2fs\n",
								provider.Name, attempt+1, duration.Seconds())
							if err := prs.blacklistService.RecordSuccess(kind, provider.Name); err != nil {
								fmt.Printf("[WARN] 清零失败计数失败: %v\n", err)
							}
							prs.setLastUsedProvider(kind, provider.Name)
							return
						}

						lastAttemptErr = err

						errorMsg := "未知错误"
						if err != nil {
							errorMsg = err.Error()
						}
						fmt.Printf("[WARN] ✗ 失败: %s | 重试 %d/%d | 错误: %s | 耗时: %.2fs\n",
							provider.Name, attempt+1, maxRetryPerProvider, errorMsg, duration.Seconds())

						// 客户端中断不计入失败次数，直接返回
						if errors.Is(err, errClientAbort) {
							fmt.Printf("[INFO] 客户端中断，停止重试\n")
							return
						}

						if responseWritten {
							fmt.Printf("[WARN] 响应已写入客户端，停止重试与降级\n")
							if err := prs.blacklistService.RecordFailure(kind, provider.Name); err != nil {
								fmt.Printf("[ERROR] 记录失败到黑名单失败: %v\n", err)
							}
							return
						}

						// 等待后重试（除非是最后一次）
						if attempt < maxRetryPerProvider-1 {
							fmt.Printf("[INFO] ⏳ 等待 %d 秒后重试...\n", retryWaitSeconds)
							time.Sleep(time.Duration(retryWaitSeconds) * time.Second)
						}
					}

					if stoppedEarlyDueToConcurrency {
						continue
					}

					// 同 Provider 重试已耗尽：仅计为 1 次失败（用于累加 FailureThreshold）
					if attemptedCount > 0 {
						lastError = lastAttemptErr
						lastProvider = provider.Name
						if err := prs.blacklistService.RecordFailure(kind, provider.Name); err != nil {
							fmt.Printf("[ERROR] 记录失败到黑名单失败: %v\n", err)
						}
					}
				}
			}

			// 所有 Provider 都失败或被拉黑
			if !attemptedUpstream && busySkipped > 0 {
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":          "all providers are busy",
					"mode":           "concurrency_limit",
					"busy_providers": busySkipped,
				})
				return
			}

			fmt.Printf("[ERROR] 💥 拉黑模式：所有 Provider 都失败或被拉黑（共尝试 %d 次）\n", totalAttempts)

			errorMsg := "未知错误"
			if lastError != nil {
				errorMsg = lastError.Error()
			}
			c.JSON(http.StatusBadGateway, gin.H{
				"error":         fmt.Sprintf("所有 Provider 都失败或被拉黑，最后尝试: %s - %s", lastProvider, errorMsg),
				"lastProvider":  lastProvider,
				"totalAttempts": totalAttempts,
				"mode":          "blacklist_retry",
				"hint":          "拉黑模式已开启，同 Provider 内重试失败按组计数后切换。如需立即降级请关闭拉黑功能",
			})
			return
		}

		// 【降级模式】：拉黑功能关闭，失败自动尝试下一个 provider
		fmt.Printf("[INFO] 🔄 降级模式（拉黑功能已关闭）\n")

		var lastError error
		var lastProvider string
		var lastDuration time.Duration
		totalAttempts := 0
		busySkipped := 0
		attemptedUpstream := false

		for _, level := range levels {
			providersInLevel := levelGroups[level]
			fmt.Printf("[INFO] === 尝试 Level %d（%d 个 provider）===\n", level, len(providersInLevel))

			for i, provider := range providersInLevel {
				// 获取实际应该使用的模型名
				effectiveModel := provider.GetEffectiveModel(requestedModel)

				// 如果需要映射，修改请求体
				currentBodyBytes := bodyBytes
				if effectiveModel != requestedModel && requestedModel != "" {
					fmt.Printf("[INFO] Provider %s 映射模型: %s -> %s\n", provider.Name, requestedModel, effectiveModel)

					modifiedBody, err := ReplaceModelInRequestBody(bodyBytes, effectiveModel)
					if err != nil {
						fmt.Printf("[ERROR] 替换模型名失败: %v\n", err)
						// 映射失败不应阻止尝试其他 provider
						continue
					}
					currentBodyBytes = modifiedBody
				}

				fmt.Printf("[INFO]   [%d/%d] Provider: %s | Model: %s\n", i+1, len(providersInLevel), provider.Name, effectiveModel)

				// 尝试发送请求
				// 获取有效的端点（用户配置优先）
				effectiveEndpoint := endpoint
				if !isResponsesCompactVariantEndpoint(endpoint) {
					effectiveEndpoint = provider.GetEffectiveEndpoint(endpoint)
				}
				release, acquired := prs.concurrencyManager.TryAcquire(
					providerConcurrencyKey(kind, provider.Name),
					provider.MaxConcurrentRequests,
				)
				if !acquired {
					busySkipped++
					fmt.Printf("[INFO]   ⏭️ Provider %s 达到并发上限(%d)，跳过\n", provider.Name, provider.MaxConcurrentRequests)
					continue
				}

				totalAttempts++
				attemptedUpstream = true
				startTime := time.Now()
				ok, err, responseWritten := prs.forwardRequest(c, kind, provider, effectiveEndpoint, query, clientHeaders, currentBodyBytes, isStream, effectiveModel)
				duration := time.Since(startTime)
				release()

				if ok {
					fmt.Printf("[INFO]   ✓ Level %d 成功: %s | 耗时: %.2fs\n", level, provider.Name, duration.Seconds())

					// 成功：清零连续失败计数
					if err := prs.blacklistService.RecordSuccess(kind, provider.Name); err != nil {
						fmt.Printf("[WARN] 清零失败计数失败: %v\n", err)
					}

					// 记录最后使用的供应商
					prs.setLastUsedProvider(kind, provider.Name)

					return // 成功，立即返回
				}

				// 失败：记录错误并尝试下一个
				lastError = err
				lastProvider = provider.Name
				lastDuration = duration

				errorMsg := "未知错误"
				if err != nil {
					errorMsg = err.Error()
				}
				fmt.Printf("[WARN]   ✗ Level %d 失败: %s | 错误: %s | 耗时: %.2fs\n",
					level, provider.Name, errorMsg, duration.Seconds())

				// 客户端中断不计入失败次数
				if errors.Is(err, errClientAbort) {
					fmt.Printf("[INFO] 客户端中断，跳过失败计数: %s\n", provider.Name)
				} else if err := prs.blacklistService.RecordFailure(kind, provider.Name); err != nil {
					fmt.Printf("[ERROR] 记录失败到黑名单失败: %v\n", err)
				}

				if responseWritten {
					fmt.Printf("[WARN] 响应已写入客户端，停止降级\n")
					return
				}

				// 发送切换通知：检查是否有下一个可用的 provider
				if prs.notificationService != nil {
					nextProvider := ""
					// 先查找同级别的下一个
					if i+1 < len(providersInLevel) {
						nextProvider = providersInLevel[i+1].Name
					} else {
						// 查找下一个 level 的第一个 provider
						for _, nextLevel := range levels {
							if nextLevel > level && len(levelGroups[nextLevel]) > 0 {
								nextProvider = levelGroups[nextLevel][0].Name
								break
							}
						}
					}
					if nextProvider != "" {
						prs.notificationService.NotifyProviderSwitch(SwitchNotification{
							FromProvider: provider.Name,
							ToProvider:   nextProvider,
							Reason:       errorMsg,
							Platform:     kind,
						})
					}
				}
			}

			fmt.Printf("[WARN] Level %d 的所有 %d 个 provider 均失败，尝试下一 Level\n", level, len(providersInLevel))
		}

		// 所有 provider 都失败，返回 502
		if !attemptedUpstream && busySkipped > 0 {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":          "all providers are busy",
				"mode":           "concurrency_limit",
				"busy_providers": busySkipped,
			})
			return
		}

		errorMsg := "未知错误"
		if lastError != nil {
			errorMsg = lastError.Error()
		}
		fmt.Printf("[ERROR] 所有 %d 个 provider 均失败，最后尝试: %s | 错误: %s\n",
			totalAttempts, lastProvider, errorMsg)

		c.JSON(http.StatusBadGateway, gin.H{
			"error":          fmt.Sprintf("所有 %d 个 provider 均失败，最后错误: %s", totalAttempts, errorMsg),
			"last_provider":  lastProvider,
			"last_duration":  fmt.Sprintf("%.2fs", lastDuration.Seconds()),
			"total_attempts": totalAttempts,
		})
	}
}

func (prs *ProviderRelayService) forwardRequest(
	c *gin.Context,
	kind string,
	provider Provider,
	endpoint string,
	query map[string]string,
	clientHeaders map[string]string,
	bodyBytes []byte,
	isStream bool,
	model string,
) (bool, error, bool) {
	targetURL := joinURL(provider.APIURL, endpoint)
	headers := cloneMap(clientHeaders)

	// 根据认证方式设置请求头（默认 Bearer，与 v2.2.x 保持一致）
	authType := strings.ToLower(strings.TrimSpace(provider.ConnectivityAuthType))
	switch authType {
	case "x-api-key":
		// 仅当用户显式选择 x-api-key 时使用（Anthropic 官方 API）
		headers["x-api-key"] = provider.APIKey
		headers["anthropic-version"] = "2023-06-01"
	case "", "bearer":
		// 默认使用 Bearer token（兼容所有第三方中转）
		headers["Authorization"] = fmt.Sprintf("Bearer %s", provider.APIKey)
	default:
		// 自定义 Header 名
		headerName := strings.TrimSpace(provider.ConnectivityAuthType)
		if headerName == "" || strings.EqualFold(headerName, "custom") {
			headerName = "Authorization"
		}
		headers[headerName] = provider.APIKey
	}

	// responses/compact 是一个专用子端点：强制使用 JSON 语义，避免客户端携带 SSE Accept 导致上游返回非预期格式
	if isResponsesCompactVariantEndpoint(endpoint) {
		headers["Accept"] = "application/json"
	} else if _, ok := headers["Accept"]; !ok {
		headers["Accept"] = "application/json"
	}

	responseChainPlan := codexResponseChainPlan{}
	if kind == "codex" {
		var rewriteErr error
		bodyBytes, responseChainPlan, rewriteErr = prepareCodexResponseChain(provider, endpoint, headers, bodyBytes)
		if rewriteErr != nil {
			return false, fmt.Errorf("rewrite codex response chain request: %w", rewriteErr), false
		}
		logCodexResponseChainRewrite(endpoint, responseChainPlan, bodyBytes)
		delete(headers, codexResponseChainSessionHeader)
	}

	promptCachePlan := codexPromptCachePlan{}
	if kind == "codex" {
		bodyBytes, headers, promptCachePlan = applyCodexPromptCache(provider, endpoint, headers, bodyBytes)
	}

	requestLog := &ReqeustLog{
		Platform:                    kind,
		Provider:                    provider.Name,
		Model:                       model,
		IsStream:                    isStream,
		CodexPromptCacheEnabled:     promptCachePlan.Enabled,
		CodexPromptCacheEligible:    promptCachePlan.Eligible,
		codexPromptCacheBucket:      promptCachePlan.BucketHash,
		codexPromptCacheFingerprint: promptCachePlan.Fingerprint,
		codexPromptCacheInScope:     true,
	}
	skipPersist := false
	start := time.Now()
	defer func() {
		requestLog.DurationSec = time.Since(start).Seconds()
		requestLog.CodexPromptCacheHit = requestLog.CodexPromptCacheEligible && requestLog.CacheReadTokens > 0
		if skipPersist {
			return
		}

		// 【修复】判空保护：避免队列未初始化时 panic
		if GlobalDBQueueLogs == nil {
			fmt.Printf("⚠️  写入 request_log 失败: 队列未初始化\n")
			return
		}

		// 使用批量队列写入 request_log（高频同构操作，批量提交）
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := GlobalDBQueueLogs.ExecBatchCtx(ctx, `
			INSERT INTO request_log (
				platform, model, provider, http_code,
				input_tokens, output_tokens, cache_create_tokens, cache_read_tokens,
				reasoning_tokens, is_stream, duration_sec,
				codex_prompt_cache_enabled, codex_prompt_cache_eligible, codex_prompt_cache_hit,
				codex_prompt_cache_bucket, codex_prompt_cache_fingerprint
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			requestLog.Platform,
			requestLog.Model,
			requestLog.Provider,
			requestLog.HttpCode,
			requestLog.InputTokens,
			requestLog.OutputTokens,
			requestLog.CacheCreateTokens,
			requestLog.CacheReadTokens,
			requestLog.ReasoningTokens,
			boolToInt(requestLog.IsStream),
			requestLog.DurationSec,
			boolToInt(requestLog.CodexPromptCacheEnabled),
			boolToInt(requestLog.CodexPromptCacheEligible),
			boolToInt(requestLog.CodexPromptCacheHit),
			requestLog.codexPromptCacheBucket,
			requestLog.codexPromptCacheFingerprint,
		)

		if err != nil {
			fmt.Printf("写入 request_log 失败: %v\n", err)
		}
	}()

	req := xrequest.New().
		SetClient(GetHTTPClient()).
		SetHeaders(headers).
		SetQueryParams(query).
		SetRetry(1, 500*time.Millisecond).
		SetTimeout(32 * time.Hour) // 32小时超时，适配超大型项目分析

	reqBody := bytes.NewReader(bodyBytes)
	req = req.SetBody(reqBody)

	resp, err := req.Post(targetURL)

	// 无论成功失败，先尝试记录 HttpCode
	if resp != nil {
		requestLog.HttpCode = resp.StatusCode()
	}

	if err != nil {
		// resp 存在但 err != nil：可能是客户端中断，不计入失败
		if resp != nil && requestLog.HttpCode == 0 {
			if isLikelyClientAbortErr(c, err) {
				skipPersist = true
				fmt.Printf("[INFO] Provider %s 响应存在且状态码为0，判定为客户端中断\n", provider.Name)
				return false, fmt.Errorf("%w: %v", errClientAbort, err), false
			}
			requestLog.HttpCode = http.StatusBadGateway
			return false, fmt.Errorf("upstream transport error (status=0): %w", err), false
		}
		return false, err, false
	}

	if resp == nil {
		return false, fmt.Errorf("empty response"), false
	}

	status := requestLog.HttpCode

	if resp.Error() != nil {
		// resp 存在、有错误、但状态码为 0：客户端中断，不计入失败
		if status == 0 {
			if isLikelyClientAbortErr(c, resp.Error()) {
				skipPersist = true
				fmt.Printf("[INFO] Provider %s 响应错误且状态码为0，判定为客户端中断\n", provider.Name)
				return false, fmt.Errorf("%w: %v", errClientAbort, resp.Error()), false
			}
			requestLog.HttpCode = http.StatusBadGateway
			return false, fmt.Errorf("upstream response error (status=0): %w", resp.Error()), false
		}
		return false, resp.Error(), false
	}

	// 状态码为 0 且无错误：不再当作成功，优先识别客户端中断，否则按上游异常处理
	if status == 0 {
		if c.Request != nil && c.Request.Context().Err() != nil {
			skipPersist = true
			fmt.Printf("[INFO] Provider %s 返回状态码0且请求上下文已取消，判定为客户端中断\n", provider.Name)
			return false, fmt.Errorf("%w: %v", errClientAbort, c.Request.Context().Err()), false
		}
		requestLog.HttpCode = http.StatusBadGateway
		return false, fmt.Errorf("upstream status 0"), false
	}

	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		// 非流式：先读完 body 解析 token，再决定是否写回客户端（允许 token=0 时降级到下一个 provider）
		if !isStream {
			body := resp.Bytes()
			if responseChainPlan.Active && responseChainPlan.SessionKey != "" {
				c.Writer.Header().Set(codexResponseChainSessionHeader, responseChainPlan.SessionKey)
			}
			parseNonStreamTokenUsage(kind, body, requestLog)
			if requestLog.OutputTokens == 0 && !isResponsesCompactVariantEndpoint(endpoint) {
				if kind == "codex" && isUsableCodexResponseBody(body) {
					persistCodexResponseChain(responseChainPlan, extractCodexResponseID(body))
					_, copyErr := resp.ToHttpResponseWriter(c.Writer)
					if copyErr != nil {
						fmt.Printf("[WARN] 复制响应到客户端失败（不影响provider成功判定）: %v\n", copyErr)
					}
					return true, nil, true
				}
				return false, errTokenZero, false
			}
			if kind == "codex" {
				persistCodexResponseChain(responseChainPlan, extractCodexResponseID(body))
			}
			_, copyErr := resp.ToHttpResponseWriter(c.Writer)
			if copyErr != nil {
				fmt.Printf("[WARN] 复制响应到客户端失败（不影响provider成功判定）: %v\n", copyErr)
			}
			return true, nil, true
		}

		// 流式：先转发，再根据解析出的 token 判断是否失败（但响应已写入，不能降级）
		var chainCapture *codexResponseChainCapture
		if kind == "codex" && responseChainPlan.Active {
			chainCapture = &codexResponseChainCapture{}
			if responseChainPlan.SessionKey != "" {
				c.Writer.Header().Set(codexResponseChainSessionHeader, responseChainPlan.SessionKey)
			}
		}
		_, copyErr := resp.ToHttpResponseWriter(c.Writer, ReqeustLogHook(c, kind, requestLog, chainCapture))
		if copyErr != nil {
			fmt.Printf("[WARN] 复制响应到客户端失败（不影响provider成功判定）: %v\n", copyErr)
			return true, nil, true
		}
		if requestLog.OutputTokens == 0 && !isResponsesCompactVariantEndpoint(endpoint) {
			if kind == "codex" && chainCapture != nil && chainCapture.IsUsableSuccess() {
				persistCodexResponseChain(responseChainPlan, chainCapture.GetResponseID())
				return true, nil, true
			}
			return false, errTokenZero, true
		}
		if kind == "codex" {
			persistCodexResponseChain(responseChainPlan, chainCapture.GetResponseID())
		}
		return true, nil, true
	}

	return false, fmt.Errorf("upstream status %d", status), false
}

func cloneHeaders(header http.Header) map[string]string {
	cloned := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) > 0 {
			cloned[key] = values[len(values)-1]
		}
	}
	return cloned
}

func cloneMap(m map[string]string) map[string]string {
	cloned := make(map[string]string, len(m))
	for k, v := range m {
		cloned[k] = v
	}
	return cloned
}

func flattenQuery(values map[string][]string) map[string]string {
	query := make(map[string]string, len(values))
	for key, items := range values {
		if len(items) > 0 {
			query[key] = items[len(items)-1]
		}
	}
	return query
}

func joinURL(base string, endpoint string) string {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return base
	}
	endpoint = "/" + strings.TrimPrefix(endpoint, "/")

	// Avoid duplicating "/v1" when users configure APIURL with or without it.
	// Example:
	//   base=https://api.openai.com/v1 + endpoint=/v1/responses => https://api.openai.com/v1/responses
	//   base=https://api.openai.com    + endpoint=/v1/responses => https://api.openai.com/v1/responses
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		lowerEndpoint := strings.ToLower(endpoint)
		if lowerEndpoint == "/v1" {
			endpoint = ""
		} else if strings.HasPrefix(lowerEndpoint, "/v1/") {
			endpoint = endpoint[len("/v1"):]
		}
	}

	return base + endpoint
}

func providerConcurrencyKey(kind, providerName string) string {
	kind = strings.TrimSpace(kind)
	providerName = strings.TrimSpace(providerName)
	if kind == "" {
		kind = "unknown"
	}
	return kind + "::" + providerName
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func ensureRequestLogColumn(db *sql.DB, column string, definition string) error {
	query := fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('request_log') WHERE name = '%s'", column)
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		alter := fmt.Sprintf("ALTER TABLE request_log ADD COLUMN %s %s", column, definition)
		if _, err := db.Exec(alter); err != nil {
			return err
		}
	}
	return nil
}

func ensureRequestLogTable() error {
	db, err := xdb.DB("default")
	if err != nil {
		return err
	}
	return ensureRequestLogTableWithDB(db)
}

func ensureRequestLogTableWithDB(db *sql.DB) error {
	const createTableSQL = `CREATE TABLE IF NOT EXISTS request_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform TEXT,
		model TEXT,
		provider TEXT,
		http_code INTEGER,
		input_tokens INTEGER,
		output_tokens INTEGER,
		cache_create_tokens INTEGER,
		cache_read_tokens INTEGER,
		reasoning_tokens INTEGER,
		is_stream INTEGER DEFAULT 0,
		duration_sec REAL DEFAULT 0,
		codex_prompt_cache_enabled INTEGER DEFAULT 0,
		codex_prompt_cache_eligible INTEGER DEFAULT 0,
		codex_prompt_cache_hit INTEGER DEFAULT 0,
		codex_prompt_cache_bucket TEXT DEFAULT '',
		codex_prompt_cache_fingerprint TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`

	if _, err := db.Exec(createTableSQL); err != nil {
		return err
	}

	if err := ensureRequestLogColumn(db, "created_at", "DATETIME DEFAULT CURRENT_TIMESTAMP"); err != nil {
		return err
	}
	if err := ensureRequestLogColumn(db, "is_stream", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureRequestLogColumn(db, "duration_sec", "REAL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureRequestLogColumn(db, "codex_prompt_cache_enabled", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureRequestLogColumn(db, "codex_prompt_cache_eligible", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureRequestLogColumn(db, "codex_prompt_cache_hit", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureRequestLogColumn(db, "codex_prompt_cache_bucket", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureRequestLogColumn(db, "codex_prompt_cache_fingerprint", "TEXT DEFAULT ''"); err != nil {
		return err
	}

	return nil
}

func ReqeustLogHook(c *gin.Context, kind string, usage *ReqeustLog, chainCapture *codexResponseChainCapture) func(data []byte) (bool, []byte) { // SSE 钩子：累计字节和解析 token 用量
	return func(data []byte) (bool, []byte) {
		payload := strings.TrimSpace(string(data))

		parserFn := ClaudeCodeParseTokenUsageFromResponse
		switch kind {
		case "codex":
			parserFn = CodexParseTokenUsageFromResponse
		case "gemini":
			parserFn = GeminiParseTokenUsageFromResponse
		}
		parseEventPayload(payload, parserFn, usage)
		if kind == "codex" && chainCapture != nil {
			chainCapture.ObservePayload(payload)
		}

		return true, data
	}
}

func parseEventPayload(payload string, parser func(string, *ReqeustLog), usage *ReqeustLog) {
	lines := strings.Split(payload, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			parser(strings.TrimPrefix(line, "data: "), usage)
		}
	}
}

// parseNonStreamTokenUsage parses token usage from a non-SSE JSON response body.
// It is intentionally strict: missing/zero output_tokens will be treated as failure by the caller.
func parseNonStreamTokenUsage(kind string, body []byte, usage *ReqeustLog) {
	if usage == nil || len(body) == 0 {
		return
	}

	switch kind {
	case "codex":
		parseCodexNonStreamUsage(body, usage)
	default:
		parseClaudeNonStreamUsage(body, usage)
	}
}

func parseClaudeNonStreamUsage(body []byte, usage *ReqeustLog) {
	if usage == nil || len(body) == 0 {
		return
	}

	if gjson.GetBytes(body, "usage").Exists() {
		usage.InputTokens += int(gjson.GetBytes(body, "usage.input_tokens").Int())
		usage.OutputTokens += int(gjson.GetBytes(body, "usage.output_tokens").Int())
		usage.CacheCreateTokens += int(gjson.GetBytes(body, "usage.cache_creation_input_tokens").Int())
		usage.CacheReadTokens += int(gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
		return
	}

	if gjson.GetBytes(body, "message.usage").Exists() {
		usage.InputTokens += int(gjson.GetBytes(body, "message.usage.input_tokens").Int())
		usage.OutputTokens += int(gjson.GetBytes(body, "message.usage.output_tokens").Int())
		usage.CacheCreateTokens += int(gjson.GetBytes(body, "message.usage.cache_creation_input_tokens").Int())
		usage.CacheReadTokens += int(gjson.GetBytes(body, "message.usage.cache_read_input_tokens").Int())
	}
}

func parseCodexNonStreamUsage(body []byte, usage *ReqeustLog) {
	if usage == nil || len(body) == 0 {
		return
	}

	if gjson.GetBytes(body, "usage").Exists() {
		usage.InputTokens += int(gjson.GetBytes(body, "usage.input_tokens").Int())
		usage.OutputTokens += int(gjson.GetBytes(body, "usage.output_tokens").Int())
		usage.CacheReadTokens += int(gjson.GetBytes(body, "usage.input_tokens_details.cached_tokens").Int())
		usage.ReasoningTokens += int(gjson.GetBytes(body, "usage.output_tokens_details.reasoning_tokens").Int())
		return
	}

	if gjson.GetBytes(body, "response.usage").Exists() {
		usage.InputTokens += int(gjson.GetBytes(body, "response.usage.input_tokens").Int())
		usage.OutputTokens += int(gjson.GetBytes(body, "response.usage.output_tokens").Int())
		usage.CacheReadTokens += int(gjson.GetBytes(body, "response.usage.input_tokens_details.cached_tokens").Int())
		usage.ReasoningTokens += int(gjson.GetBytes(body, "response.usage.output_tokens_details.reasoning_tokens").Int())
	}
}

type ReqeustLog struct {
	ID                        int64   `json:"id"`
	Platform                  string  `json:"platform"` // claude、codex 或 gemini
	Model                     string  `json:"model"`
	Provider                  string  `json:"provider"` // provider name
	HttpCode                  int     `json:"http_code"`
	InputTokens               int     `json:"input_tokens"`
	OutputTokens              int     `json:"output_tokens"`
	CacheCreateTokens         int     `json:"cache_create_tokens"`
	CacheReadTokens           int     `json:"cache_read_tokens"`
	ReasoningTokens           int     `json:"reasoning_tokens"`
	IsStream                  bool    `json:"is_stream"`
	DurationSec               float64 `json:"duration_sec"`
	CreatedAt                 string  `json:"created_at"`
	InputCost                 float64 `json:"input_cost"`
	OutputCost                float64 `json:"output_cost"`
	ReasoningCost             float64 `json:"reasoning_cost"`
	CacheCreateCost           float64 `json:"cache_create_cost"`
	CacheReadCost             float64 `json:"cache_read_cost"`
	Ephemeral5mCost           float64 `json:"ephemeral_5m_cost"`
	Ephemeral1hCost           float64 `json:"ephemeral_1h_cost"`
	TotalCost                 float64 `json:"total_cost"`
	HasPricing                bool    `json:"has_pricing"`
	CodexPromptCacheEnabled   bool    `json:"codex_prompt_cache_enabled,omitempty"`
	CodexPromptCacheEligible  bool    `json:"codex_prompt_cache_eligible,omitempty"`
	CodexPromptCacheHit       bool    `json:"codex_prompt_cache_hit,omitempty"`
	CodexPromptCacheMatchable bool    `json:"codex_prompt_cache_matchable,omitempty"`

	codexPromptCacheBucket      string `json:"-"`
	codexPromptCacheFingerprint string `json:"-"`
	codexPromptCacheInScope     bool   `json:"-"`
}

// claude code usage parser
func ClaudeCodeParseTokenUsageFromResponse(data string, usage *ReqeustLog) {
	usage.InputTokens += int(gjson.Get(data, "message.usage.input_tokens").Int())
	usage.OutputTokens += int(gjson.Get(data, "message.usage.output_tokens").Int())
	usage.CacheCreateTokens += int(gjson.Get(data, "message.usage.cache_creation_input_tokens").Int())
	usage.CacheReadTokens += int(gjson.Get(data, "message.usage.cache_read_input_tokens").Int())

	usage.InputTokens += int(gjson.Get(data, "usage.input_tokens").Int())
	usage.OutputTokens += int(gjson.Get(data, "usage.output_tokens").Int())
}

// codex usage parser
func CodexParseTokenUsageFromResponse(data string, usage *ReqeustLog) {
	usage.InputTokens += int(gjson.Get(data, "response.usage.input_tokens").Int())
	usage.OutputTokens += int(gjson.Get(data, "response.usage.output_tokens").Int())
	usage.CacheReadTokens += int(gjson.Get(data, "response.usage.input_tokens_details.cached_tokens").Int())
	usage.ReasoningTokens += int(gjson.Get(data, "response.usage.output_tokens_details.reasoning_tokens").Int())
}

// gemini usage parser (流式响应专用)
// Gemini SSE 流中每个 chunk 都会携带完整的 usageMetadata，需取最大值而非累加
func GeminiParseTokenUsageFromResponse(data string, usage *ReqeustLog) {
	usageResult := gjson.Get(data, "usageMetadata")
	if !usageResult.Exists() {
		return
	}
	mergeGeminiUsageMetadata(usageResult, usage)
}

// mergeGeminiUsageMetadata 合并 Gemini usageMetadata 到 ReqeustLog（取最大值去重）
// Gemini 流式响应特点：每个 chunk 包含截止当前的累计用量，因此取最大值即可
func mergeGeminiUsageMetadata(usage gjson.Result, reqLog *ReqeustLog) {
	if !usage.Exists() || reqLog == nil {
		return
	}

	// 取最大值（流式响应中后续 chunk 包含前面的累计值）
	if v := int(usage.Get("promptTokenCount").Int()); v > reqLog.InputTokens {
		reqLog.InputTokens = v
	}
	if v := int(usage.Get("candidatesTokenCount").Int()); v > reqLog.OutputTokens {
		reqLog.OutputTokens = v
	}
	if v := int(usage.Get("cachedContentTokenCount").Int()); v > reqLog.CacheReadTokens {
		reqLog.CacheReadTokens = v
	}
	// Gemini thinking/reasoning tokens (thoughtsTokenCount)
	// 参考: https://ai.google.dev/gemini-api/docs/thinking
	if v := int(usage.Get("thoughtsTokenCount").Int()); v > reqLog.ReasoningTokens {
		reqLog.ReasoningTokens = v
	}

	// 若仅提供 totalTokenCount，按 total - input 估算输出 token
	total := usage.Get("totalTokenCount").Int()
	if total > 0 && reqLog.OutputTokens == 0 && reqLog.InputTokens > 0 && reqLog.InputTokens < int(total) {
		reqLog.OutputTokens = int(total) - reqLog.InputTokens
	}
}

// streamGeminiResponseWithHook 流式传输 Gemini 响应并通过 Hook 提取 token 用量
// 【修复】维护跨 chunk 缓冲，确保完整 SSE 事件解析
// Gemini SSE 格式: "data: {json}\n\n" 或 "data: [DONE]\n\n"
func streamGeminiResponseWithHook(body io.Reader, writer io.Writer, requestLog *ReqeustLog) error {
	buf := make([]byte, 8192)   // 增大缓冲区减少系统调用
	var lineBuf strings.Builder // 跨 chunk 行缓冲

	for {
		n, err := body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			// 写入客户端（优先保证数据传输）
			if _, writeErr := writer.Write(chunk); writeErr != nil {
				return writeErr
			}
			// 如果是 http.Flusher，立即刷新
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			// 解析 SSE 数据提取 token 用量（使用缓冲处理跨 chunk 情况）
			parseGeminiSSEWithBuffer(string(chunk), &lineBuf, requestLog)
		}
		if err != nil {
			// 处理缓冲区残留数据
			if lineBuf.Len() > 0 {
				parseGeminiSSELine(lineBuf.String(), requestLog)
				lineBuf.Reset()
			}
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// parseGeminiSSEWithBuffer 使用缓冲处理跨 chunk 的 SSE 事件
// 【修复】解决 JSON 被 TCP 分割到多个 chunk 导致解析失败的问题
func parseGeminiSSEWithBuffer(chunk string, lineBuf *strings.Builder, requestLog *ReqeustLog) {
	// 将当前 chunk 追加到缓冲
	lineBuf.WriteString(chunk)
	content := lineBuf.String()

	// 按双换行符分割完整的 SSE 事件
	// SSE 格式: "data: {...}\n\n" 或 "data: {...}\r\n\r\n"
	for {
		// 查找事件分隔符（双换行）
		idx := strings.Index(content, "\n\n")
		if idx == -1 {
			// 尝试 \r\n\r\n 分隔符
			idx = strings.Index(content, "\r\n\r\n")
			if idx == -1 {
				break // 没有完整事件，等待更多数据
			}
			idx += 4 // \r\n\r\n 长度
		} else {
			idx += 2 // \n\n 长度
		}

		// 提取完整事件
		event := content[:idx]
		content = content[idx:]

		// 解析事件中的 data 行
		parseGeminiSSELine(event, requestLog)
	}

	// 更新缓冲区为未处理的残留数据
	lineBuf.Reset()
	lineBuf.WriteString(content)
}

// parseGeminiSSELine 解析单个 SSE 事件提取 usageMetadata
// 【优化】只在包含 usageMetadata 时才调用 gjson 解析
func parseGeminiSSELine(event string, requestLog *ReqeustLog) {
	lines := strings.Split(event, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" || data == "" {
			continue
		}
		// 【优化】快速检查是否包含 usageMetadata，避免无效解析
		if !strings.Contains(data, "usageMetadata") {
			continue
		}
		GeminiParseTokenUsageFromResponse(data, requestLog)
	}
}

// ReplaceModelInRequestBody 替换请求体中的模型名
// 使用 gjson + sjson 实现高性能 JSON 操作，避免完整反序列化
func ReplaceModelInRequestBody(bodyBytes []byte, newModel string) ([]byte, error) {
	// 检查请求体中是否存在 model 字段
	result := gjson.GetBytes(bodyBytes, "model")
	if !result.Exists() {
		return bodyBytes, fmt.Errorf("请求体中未找到 model 字段")
	}

	// 使用 sjson.SetBytes 替换模型名（高性能操作）
	modified, err := sjson.SetBytes(bodyBytes, "model", newModel)
	if err != nil {
		return bodyBytes, fmt.Errorf("替换模型名失败: %w", err)
	}

	return modified, nil
}

// geminiProxyHandler 处理 Gemini API 请求（支持 Level 分组降级和黑名单）
func (prs *ProviderRelayService) geminiProxyHandler(apiVersion string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 获取完整路径（例如 /v1beta/models/gemini-2.5-pro:generateContent）
		fullPath := c.Param("any")
		endpoint := apiVersion + fullPath

		// 保留查询参数（如 ?alt=sse, ?key= 等）
		query := c.Request.URL.RawQuery
		if query != "" {
			endpoint = endpoint + "?" + query
		}

		fmt.Printf("[Gemini] 收到请求: %s\n", endpoint)

		// 读取请求体
		var bodyBytes []byte
		if c.Request.Body != nil {
			data, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
				return
			}
			bodyBytes = data
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		// 判断是否为流式请求
		isStream := strings.Contains(endpoint, ":streamGenerateContent") || strings.Contains(query, "alt=sse")

		// 加载 Gemini providers
		providers := prs.geminiService.GetProviders()
		if len(providers) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no gemini providers configured"})
			return
		}

		// 1. 过滤可用的 providers（启用 + BaseURL 配置 + 未被拉黑）
		var activeProviders []GeminiProvider
		for _, p := range providers {
			if !p.Enabled || p.BaseURL == "" {
				continue
			}
			// 检查黑名单
			if isBlacklisted, until := prs.blacklistService.IsBlacklisted("gemini", p.Name); isBlacklisted {
				fmt.Printf("[Gemini] ⛔ Provider %s 已拉黑，过期时间: %v\n", p.Name, until.Format("15:04:05"))
				continue
			}
			// Level 默认值处理
			if p.Level <= 0 {
				p.Level = 1
			}
			activeProviders = append(activeProviders, p)
		}

		if len(activeProviders) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "no active gemini provider (all disabled or blacklisted)"})
			return
		}

		// 2. 按 Level 分组
		levelGroups := make(map[int][]GeminiProvider)
		for _, p := range activeProviders {
			levelGroups[p.Level] = append(levelGroups[p.Level], p)
		}

		// 获取排序后的 Level 列表
		var sortedLevels []int
		for level := range levelGroups {
			sortedLevels = append(sortedLevels, level)
		}
		sort.Ints(sortedLevels)

		fmt.Printf("[Gemini] 共 %d 个 Level 分组: %v\n", len(sortedLevels), sortedLevels)

		// 请求日志
		requestLog := &ReqeustLog{
			Platform:     "gemini",
			IsStream:     isStream,
			InputTokens:  0,
			OutputTokens: 0,
		}
		start := time.Now()

		// 保存日志的 defer
		defer func() {
			requestLog.DurationSec = time.Since(start).Seconds()
			if GlobalDBQueueLogs == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = GlobalDBQueueLogs.ExecBatchCtx(ctx, `
				INSERT INTO request_log (
					platform, model, provider, http_code,
					input_tokens, output_tokens, cache_create_tokens, cache_read_tokens,
					reasoning_tokens, is_stream, duration_sec
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`,
				requestLog.Platform, requestLog.Model, requestLog.Provider, requestLog.HttpCode,
				requestLog.InputTokens, requestLog.OutputTokens, requestLog.CacheCreateTokens,
				requestLog.CacheReadTokens, requestLog.ReasoningTokens,
				boolToInt(requestLog.IsStream), requestLog.DurationSec,
			)
		}()

		// 获取拉黑功能开关状态
		blacklistEnabled := prs.blacklistService.ShouldUseFixedMode()

		// 【拉黑模式】：同 Provider 内重试（maxRetryPerProvider），失败按“整组重试”计数后切换到下一个 Provider
		if blacklistEnabled {
			fmt.Printf("[Gemini] 🔒 拉黑模式已开启（同 Provider 内重试，失败按组计数后切换）\n")

			// 获取重试配置
			retryConfig := prs.blacklistService.GetRetryConfig()
			maxRetryPerProvider := retryConfig.MaxRetryPerProvider
			retryWaitSeconds := retryConfig.RetryWaitSeconds
			fmt.Printf("[Gemini] 重试配置: 每 Provider 最多 %d 次重试，间隔 %d 秒\n",
				maxRetryPerProvider, retryWaitSeconds)

			var lastError string
			var lastProvider string
			totalAttempts := 0
			busySkipped := 0
			attemptedUpstream := false

			// 遍历所有 Level 和 Provider
			for _, level := range sortedLevels {
				providersInLevel := levelGroups[level]
				fmt.Printf("[Gemini] === 尝试 Level %d（%d 个 provider）===\n", level, len(providersInLevel))

				for _, provider := range providersInLevel {
					// 检查是否已被拉黑（跳过已拉黑的 provider）
					if blacklisted, until := prs.blacklistService.IsBlacklisted("gemini", provider.Name); blacklisted {
						fmt.Printf("[Gemini] ⏭️ 跳过已拉黑的 Provider: %s (解禁时间: %v)\n", provider.Name, until)
						continue
					}

					// 预填日志
					requestLog.Provider = provider.Name
					requestLog.Model = provider.Model

					// 同 Provider 内重试循环
					attemptedCount := 0
					stoppedEarlyDueToConcurrency := false
					var lastAttemptErrMsg string
					for attempt := 0; attempt < maxRetryPerProvider; attempt++ {
						// 再次检查是否已被拉黑（重试过程中可能被拉黑）
						if blacklisted, _ := prs.blacklistService.IsBlacklisted("gemini", provider.Name); blacklisted {
							fmt.Printf("[Gemini] 🚫 Provider %s 已被拉黑，切换到下一个\n", provider.Name)
							break
						}

						fmt.Printf("[Gemini] [拉黑模式] Provider: %s (Level %d) | 重试 %d/%d\n",
							provider.Name, level, attempt+1, maxRetryPerProvider)

						release, acquired := prs.concurrencyManager.TryAcquire(
							providerConcurrencyKey("gemini", provider.Name),
							provider.MaxConcurrentRequests,
						)
						if !acquired {
							busySkipped++
							fmt.Printf("[Gemini] ⏭️ Provider %s 达到并发上限(%d)，跳过到下一个\n", provider.Name, provider.MaxConcurrentRequests)
							stoppedEarlyDueToConcurrency = true
							break
						}

						totalAttempts++
						attemptedCount++
						attemptedUpstream = true
						ok, errMsg, responseWritten := prs.forwardGeminiRequest(c, &provider, endpoint, bodyBytes, isStream, requestLog)
						release()
						if ok {
							fmt.Printf("[Gemini] ✓ 成功: %s | 重试 %d 次\n", provider.Name, attempt+1)
							_ = prs.blacklistService.RecordSuccess("gemini", provider.Name)
							prs.setLastUsedProvider("gemini", provider.Name)
							return
						}

						// 【关键修复】如果响应已写入客户端，不能重试或降级，直接返回
						if responseWritten {
							fmt.Printf("[Gemini] ⚠️ 响应已部分写入，无法重试: %s | 错误: %s\n", provider.Name, errMsg)
							_ = prs.blacklistService.RecordFailure("gemini", provider.Name)
							return
						}

						// 失败处理
						lastAttemptErrMsg = errMsg

						fmt.Printf("[Gemini] ✗ 失败: %s | 重试 %d/%d | 错误: %s\n",
							provider.Name, attempt+1, maxRetryPerProvider, errMsg)

						// 等待后重试（除非是最后一次）
						if attempt < maxRetryPerProvider-1 {
							fmt.Printf("[Gemini] ⏳ 等待 %d 秒后重试...\n", retryWaitSeconds)
							time.Sleep(time.Duration(retryWaitSeconds) * time.Second)
						}
					}

					if stoppedEarlyDueToConcurrency {
						continue
					}

					// 同 Provider 重试已耗尽：仅计为 1 次失败（用于累加 FailureThreshold）
					if attemptedCount > 0 {
						lastError = lastAttemptErrMsg
						lastProvider = provider.Name
						_ = prs.blacklistService.RecordFailure("gemini", provider.Name)
					}
				}
			}

			// 所有 Provider 都失败或被拉黑
			fmt.Printf("[Gemini] 💥 拉黑模式：所有 Provider 都失败或被拉黑（共尝试 %d 次）\n", totalAttempts)

			if !attemptedUpstream && busySkipped > 0 {
				requestLog.HttpCode = http.StatusTooManyRequests
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":          "all gemini providers are busy",
					"mode":           "concurrency_limit",
					"busy_providers": busySkipped,
				})
				return
			}

			if requestLog.HttpCode == 0 {
				requestLog.HttpCode = http.StatusBadGateway
			}
			c.JSON(http.StatusBadGateway, gin.H{
				"error":         fmt.Sprintf("所有 Provider 都失败或被拉黑，最后尝试: %s - %s", lastProvider, lastError),
				"lastProvider":  lastProvider,
				"totalAttempts": totalAttempts,
				"mode":          "blacklist_retry",
				"hint":          "拉黑模式已开启，同 Provider 内重试失败按组计数后切换。如需立即降级请关闭拉黑功能",
			})
			return
		}

		// 【降级模式】：按 Level 顺序尝试所有 provider
		var lastError string
		busySkipped := 0
		attemptedUpstream := false
		for _, level := range sortedLevels {
			providersInLevel := levelGroups[level]
			fmt.Printf("[Gemini] === 尝试 Level %d（%d 个 provider）===\n", level, len(providersInLevel))

			for idx, provider := range providersInLevel {
				fmt.Printf("[Gemini]   [%d/%d] Provider: %s\n", idx+1, len(providersInLevel), provider.Name)

				// 预填日志，失败也能落库
				requestLog.Provider = provider.Name
				requestLog.Model = provider.Model

				release, acquired := prs.concurrencyManager.TryAcquire(
					providerConcurrencyKey("gemini", provider.Name),
					provider.MaxConcurrentRequests,
				)
				if !acquired {
					busySkipped++
					fmt.Printf("[Gemini]   ⏭️ Provider %s 达到并发上限(%d)，跳过\n", provider.Name, provider.MaxConcurrentRequests)
					continue
				}

				attemptedUpstream = true
				ok, errMsg, responseWritten := prs.forwardGeminiRequest(c, &provider, endpoint, bodyBytes, isStream, requestLog)
				release()
				if ok {
					_ = prs.blacklistService.RecordSuccess("gemini", provider.Name)
					// 记录最后使用的供应商
					prs.setLastUsedProvider("gemini", provider.Name)
					fmt.Printf("[Gemini] ✓ 请求完成 | Provider: %s | 总耗时: %.2fs\n", provider.Name, time.Since(start).Seconds())
					return // 成功，退出
				}

				// 【关键修复】如果响应已写入客户端，不能降级到其他 provider，直接返回
				if responseWritten {
					fmt.Printf("[Gemini] ⚠️ 响应已部分写入，无法降级: %s | 错误: %s\n", provider.Name, errMsg)
					_ = prs.blacklistService.RecordFailure("gemini", provider.Name)
					return
				}

				// 失败，记录并继续
				lastError = errMsg
				_ = prs.blacklistService.RecordFailure("gemini", provider.Name)
			}

			fmt.Printf("[Gemini] Level %d 的所有 %d 个 provider 均失败，尝试下一 Level\n", level, len(providersInLevel))
		}

		// 所有 Level 都失败
		if !attemptedUpstream && busySkipped > 0 {
			requestLog.HttpCode = http.StatusTooManyRequests
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":          "all gemini providers are busy",
				"mode":           "concurrency_limit",
				"busy_providers": busySkipped,
			})
			return
		}

		if requestLog.HttpCode == 0 {
			requestLog.HttpCode = http.StatusBadGateway
		}
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "all gemini providers failed",
			"details": lastError,
		})
		fmt.Printf("[Gemini] ✗ 所有 provider 均失败 | 最后错误: %s\n", lastError)
	}
}

// extractGeminiModelFromEndpoint 从 Gemini API endpoint 中提取模型名
// 例如 "/v1beta/models/gemini-2.5-pro:generateContent?alt=sse" -> "gemini-2.5-pro"
func extractGeminiModelFromEndpoint(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	// 移除查询参数
	if qIdx := strings.Index(endpoint, "?"); qIdx >= 0 {
		endpoint = endpoint[:qIdx]
	}
	// 查找 models/ 后面的部分
	idx := strings.Index(endpoint, "models/")
	if idx == -1 {
		return ""
	}
	rest := endpoint[idx+len("models/"):]
	if rest == "" {
		return ""
	}
	// 移除动作部分（如 :generateContent, :streamGenerateContent）
	if colonIdx := strings.Index(rest, ":"); colonIdx >= 0 {
		rest = rest[:colonIdx]
	}
	return strings.TrimSpace(rest)
}

// forwardGeminiRequest 转发 Gemini 请求到指定 provider
// 返回 (成功, 错误信息, 是否已写入响应)
// 【重要】当 responseWritten=true 时，调用方不得重试或降级，因为响应头/数据已发送给客户端
func (prs *ProviderRelayService) forwardGeminiRequest(
	c *gin.Context,
	provider *GeminiProvider,
	endpoint string,
	bodyBytes []byte,
	isStream bool,
	requestLog *ReqeustLog,
) (success bool, errMsg string, responseWritten bool) {
	providerStart := time.Now()

	// 构建目标 URL
	targetURL := strings.TrimSuffix(provider.BaseURL, "/") + endpoint

	// 预先填充日志，保证失败也能记录 provider 和模型
	requestLog.Provider = provider.Name
	// 【修复】每次尝试开始前重置 HttpCode，避免重试时沿用上一次的状态码
	requestLog.HttpCode = 0
	requestLog.InputTokens = 0
	requestLog.OutputTokens = 0
	requestLog.CacheCreateTokens = 0
	requestLog.CacheReadTokens = 0
	requestLog.ReasoningTokens = 0
	// 优先从 endpoint 提取模型名（如 gemini-2.5-pro），否则回退到 provider.Model
	if extractedModel := extractGeminiModelFromEndpoint(endpoint); extractedModel != "" {
		requestLog.Model = extractedModel
	} else {
		requestLog.Model = provider.Model
	}

	// 创建 HTTP 请求
	req, err := http.NewRequest("POST", targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return false, fmt.Sprintf("创建请求失败: %v", err), false
	}

	// 复制请求头
	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// 设置 API Key
	if provider.APIKey != "" {
		req.Header.Set("x-goog-api-key", provider.APIKey)
	}

	// 发送请求
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(req)
	providerDuration := time.Since(providerStart).Seconds()

	if err != nil {
		fmt.Printf("[Gemini]   ✗ 失败: %s | 错误: %v | 耗时: %.2fs\n", provider.Name, err, providerDuration)
		return false, fmt.Sprintf("请求失败: %v", err), false
	}
	defer resp.Body.Close()

	// 先记录上游状态码，失败场景也能落库
	requestLog.HttpCode = resp.StatusCode

	// 检查响应状态
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorBody, _ := io.ReadAll(resp.Body)
		fmt.Printf("[Gemini]   ✗ 失败: %s | HTTP %d | 耗时: %.2fs\n", provider.Name, resp.StatusCode, providerDuration)
		return false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(errorBody)), false
	}

	fmt.Printf("[Gemini]   ✓ 连接成功: %s | HTTP %d | 耗时: %.2fs\n", provider.Name, resp.StatusCode, providerDuration)

	// 处理响应
	if isStream {
		// 流式模式：先写 header 再流式传输
		for key, values := range resp.Header {
			for _, value := range values {
				c.Header(key, value)
			}
		}
		c.Status(resp.StatusCode)
		c.Writer.Flush()
		// 【重要】从 Flush() 开始，响应头已写入客户端，任何失败都不能重试
		copyErr := streamGeminiResponseWithHook(resp.Body, c.Writer, requestLog)
		if copyErr != nil {
			fmt.Printf("[Gemini]   ⚠️ 流式传输中断: %s | 错误: %v\n", provider.Name, copyErr)
			// 流式传输中断：已写入部分响应，客户端会收到不完整数据
			return false, fmt.Sprintf("流式传输中断: %v", copyErr), true
		}
		if requestLog.OutputTokens == 0 {
			return false, errTokenZero.Error(), true
		}
	} else {
		// 非流式模式：先读完 body 再写 header（允许读取失败时重试）
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			fmt.Printf("[Gemini]   ⚠️ 读取响应失败: %s | 错误: %v\n", provider.Name, readErr)
			// 【修复】此时 header 尚未写入客户端，可以重试/降级
			return false, fmt.Sprintf("读取响应失败: %v", readErr), false
		}
		// 解析 Gemini 用量数据
		parseGeminiUsageMetadata(body, requestLog)
		if requestLog.OutputTokens == 0 {
			return false, errTokenZero.Error(), false
		}
		// 读取成功后再写 header 和 body
		for key, values := range resp.Header {
			for _, value := range values {
				c.Header(key, value)
			}
		}
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}

	return true, "", true
}

// parseGeminiUsageMetadata 从 Gemini 非流式响应中提取用量，填充 request_log
// 复用 mergeGeminiUsageMetadata 统一解析逻辑
func parseGeminiUsageMetadata(body []byte, reqLog *ReqeustLog) {
	if len(body) == 0 || reqLog == nil {
		return
	}
	usage := gjson.GetBytes(body, "usageMetadata")
	if !usage.Exists() {
		return
	}
	mergeGeminiUsageMetadata(usage, reqLog)
}

// customCliProxyHandler 处理自定义 CLI 工具的 API 请求
// 路由格式: /custom/:toolId/v1/messages
// toolId 用于区分不同的 CLI 工具，对应 provider kind 为 "custom:{toolId}"
func (prs *ProviderRelayService) customCliProxyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从 URL 参数提取 toolId
		toolId := c.Param("toolId")
		if toolId == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "toolId is required"})
			return
		}

		// 构建 provider kind（格式: "custom:{toolId}"）
		kind := "custom:" + toolId
		endpoint := "/v1/messages"

		fmt.Printf("[CustomCLI] 收到请求: toolId=%s, kind=%s\n", toolId, kind)

		// 读取请求体
		var bodyBytes []byte
		if c.Request.Body != nil {
			data, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
				return
			}
			bodyBytes = data
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		isStream := gjson.GetBytes(bodyBytes, "stream").Bool()
		requestedModel := gjson.GetBytes(bodyBytes, "model").String()

		if requestedModel == "" {
			fmt.Printf("[CustomCLI][WARN] 请求未指定模型名，无法执行模型智能降级\n")
		}

		// 加载该 CLI 工具的 providers
		providers, err := prs.providerService.LoadProviders(kind)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to load providers for %s: %v", kind, err)})
			return
		}

		// 过滤可用的 providers
		active := make([]Provider, 0, len(providers))
		skippedCount := 0
		for _, provider := range providers {
			if !provider.Enabled || provider.APIURL == "" || provider.APIKey == "" {
				continue
			}

			if errs := provider.ValidateConfiguration(); len(errs) > 0 {
				fmt.Printf("[CustomCLI][WARN] Provider %s 配置验证失败，已自动跳过: %v\n", provider.Name, errs)
				skippedCount++
				continue
			}

			if requestedModel != "" && !provider.IsModelSupported(requestedModel) {
				fmt.Printf("[CustomCLI][INFO] Provider %s 不支持模型 %s，已跳过\n", provider.Name, requestedModel)
				skippedCount++
				continue
			}

			// 黑名单检查
			if isBlacklisted, until := prs.blacklistService.IsBlacklisted(kind, provider.Name); isBlacklisted {
				fmt.Printf("[CustomCLI] ⛔ Provider %s 已拉黑，过期时间: %v\n", provider.Name, until.Format("15:04:05"))
				skippedCount++
				continue
			}

			active = append(active, provider)
		}

		if len(active) == 0 {
			if requestedModel != "" {
				c.JSON(http.StatusNotFound, gin.H{
					"error": fmt.Sprintf("没有可用的 provider 支持模型 '%s'（已跳过 %d 个不兼容的 provider）", requestedModel, skippedCount),
				})
			} else {
				c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("no providers available for %s", kind)})
			}
			return
		}

		fmt.Printf("[CustomCLI][INFO] 找到 %d 个可用的 provider（已过滤 %d 个）：", len(active), skippedCount)
		for _, p := range active {
			fmt.Printf("%s ", p.Name)
		}
		fmt.Println()

		// 按 Level 分组
		levelGroups := make(map[int][]Provider)
		for _, provider := range active {
			level := provider.Level
			if level <= 0 {
				level = 1
			}
			levelGroups[level] = append(levelGroups[level], provider)
		}

		levels := make([]int, 0, len(levelGroups))
		for level := range levelGroups {
			levels = append(levels, level)
		}
		sort.Ints(levels)

		fmt.Printf("[CustomCLI][INFO] 共 %d 个 Level 分组：%v\n", len(levels), levels)

		query := flattenQuery(c.Request.URL.Query())
		clientHeaders := cloneHeaders(c.Request.Header)

		// 获取拉黑功能开关状态
		blacklistEnabled := prs.blacklistService.ShouldUseFixedMode()

		// 【拉黑模式】：同 Provider 内重试（maxRetryPerProvider），失败按“整组重试”计数后切换到下一个 Provider
		if blacklistEnabled {
			fmt.Printf("[CustomCLI][INFO] 🔒 拉黑模式已开启（同 Provider 内重试，失败按组计数后切换）\n")

			// 获取重试配置
			retryConfig := prs.blacklistService.GetRetryConfig()
			maxRetryPerProvider := retryConfig.MaxRetryPerProvider
			retryWaitSeconds := retryConfig.RetryWaitSeconds
			fmt.Printf("[CustomCLI][INFO] 重试配置: 每 Provider 最多 %d 次重试，间隔 %d 秒\n",
				maxRetryPerProvider, retryWaitSeconds)

			var lastError error
			var lastProvider string
			totalAttempts := 0
			busySkipped := 0
			attemptedUpstream := false

			// 遍历所有 Level 和 Provider
			for _, level := range levels {
				providersInLevel := levelGroups[level]
				fmt.Printf("[CustomCLI][INFO] === 尝试 Level %d（%d 个 provider）===\n", level, len(providersInLevel))

				for _, provider := range providersInLevel {
					// 检查是否已被拉黑（跳过已拉黑的 provider）
					if blacklisted, until := prs.blacklistService.IsBlacklisted(kind, provider.Name); blacklisted {
						fmt.Printf("[CustomCLI][INFO] ⏭️ 跳过已拉黑的 Provider: %s (解禁时间: %v)\n", provider.Name, until)
						continue
					}

					// 获取实际模型名
					effectiveModel := provider.GetEffectiveModel(requestedModel)
					currentBodyBytes := bodyBytes
					if effectiveModel != requestedModel && requestedModel != "" {
						fmt.Printf("[CustomCLI][INFO] Provider %s 映射模型: %s -> %s\n", provider.Name, requestedModel, effectiveModel)
						modifiedBody, err := ReplaceModelInRequestBody(bodyBytes, effectiveModel)
						if err != nil {
							fmt.Printf("[CustomCLI][ERROR] 模型映射失败: %v，跳过此 Provider\n", err)
							continue
						}
						currentBodyBytes = modifiedBody
					}

					// 获取有效端点
					effectiveEndpoint := endpoint
					if !isResponsesCompactVariantEndpoint(endpoint) {
						effectiveEndpoint = provider.GetEffectiveEndpoint(endpoint)
					}

					// 同 Provider 内重试循环
					attemptedCount := 0
					stoppedEarlyDueToConcurrency := false
					var lastAttemptErr error
					for attempt := 0; attempt < maxRetryPerProvider; attempt++ {
						// 再次检查是否已被拉黑（重试过程中可能被拉黑）
						if blacklisted, _ := prs.blacklistService.IsBlacklisted(kind, provider.Name); blacklisted {
							fmt.Printf("[CustomCLI][INFO] 🚫 Provider %s 已被拉黑，切换到下一个\n", provider.Name)
							break
						}

						fmt.Printf("[CustomCLI][INFO] [拉黑模式] Provider: %s (Level %d) | 重试 %d/%d | Model: %s\n",
							provider.Name, level, attempt+1, maxRetryPerProvider, effectiveModel)

						release, acquired := prs.concurrencyManager.TryAcquire(
							providerConcurrencyKey(kind, provider.Name),
							provider.MaxConcurrentRequests,
						)
						if !acquired {
							busySkipped++
							fmt.Printf("[CustomCLI][INFO] ⏭️ Provider %s 达到并发上限(%d)，跳过到下一个\n", provider.Name, provider.MaxConcurrentRequests)
							stoppedEarlyDueToConcurrency = true
							break
						}

						totalAttempts++
						attemptedCount++
						attemptedUpstream = true
						startTime := time.Now()
						ok, err, responseWritten := prs.forwardRequest(c, kind, provider, effectiveEndpoint, query, clientHeaders, currentBodyBytes, isStream, effectiveModel)
						duration := time.Since(startTime)
						release()

						if ok {
							fmt.Printf("[CustomCLI][INFO] ✓ 成功: %s | 重试 %d 次 | 耗时: %.2fs\n",
								provider.Name, attempt+1, duration.Seconds())
							if err := prs.blacklistService.RecordSuccess(kind, provider.Name); err != nil {
								fmt.Printf("[CustomCLI][WARN] 清零失败计数失败: %v\n", err)
							}
							prs.setLastUsedProvider(kind, provider.Name)
							return
						}

						lastAttemptErr = err

						errorMsg := "未知错误"
						if err != nil {
							errorMsg = err.Error()
						}
						fmt.Printf("[CustomCLI][WARN] ✗ 失败: %s | 重试 %d/%d | 错误: %s | 耗时: %.2fs\n",
							provider.Name, attempt+1, maxRetryPerProvider, errorMsg, duration.Seconds())

						// 客户端中断不计入失败次数，直接返回
						if errors.Is(err, errClientAbort) {
							fmt.Printf("[CustomCLI][INFO] 客户端中断，停止重试\n")
							return
						}

						if responseWritten {
							fmt.Printf("[CustomCLI][WARN] 响应已写入客户端，停止重试与降级\n")
							if err := prs.blacklistService.RecordFailure(kind, provider.Name); err != nil {
								fmt.Printf("[CustomCLI][ERROR] 记录失败到黑名单失败: %v\n", err)
							}
							return
						}

						// 等待后重试（除非是最后一次）
						if attempt < maxRetryPerProvider-1 {
							fmt.Printf("[CustomCLI][INFO] ⏳ 等待 %d 秒后重试...\n", retryWaitSeconds)
							time.Sleep(time.Duration(retryWaitSeconds) * time.Second)
						}
					}

					if stoppedEarlyDueToConcurrency {
						continue
					}

					// 同 Provider 重试已耗尽：仅计为 1 次失败（用于累加 FailureThreshold）
					if attemptedCount > 0 {
						lastError = lastAttemptErr
						lastProvider = provider.Name
						if err := prs.blacklistService.RecordFailure(kind, provider.Name); err != nil {
							fmt.Printf("[CustomCLI][ERROR] 记录失败到黑名单失败: %v\n", err)
						}
					}
				}
			}

			// 所有 Provider 都失败或被拉黑
			if !attemptedUpstream && busySkipped > 0 {
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":          "all providers are busy",
					"mode":           "concurrency_limit",
					"busy_providers": busySkipped,
				})
				return
			}

			fmt.Printf("[CustomCLI][ERROR] 💥 拉黑模式：所有 Provider 都失败或被拉黑（共尝试 %d 次）\n", totalAttempts)

			errorMsg := "未知错误"
			if lastError != nil {
				errorMsg = lastError.Error()
			}
			c.JSON(http.StatusBadGateway, gin.H{
				"error":         fmt.Sprintf("所有 Provider 都失败或被拉黑，最后尝试: %s - %s", lastProvider, errorMsg),
				"lastProvider":  lastProvider,
				"totalAttempts": totalAttempts,
				"mode":          "blacklist_retry",
				"hint":          "拉黑模式已开启，同 Provider 内重试失败按组计数后切换。如需立即降级请关闭拉黑功能",
			})
			return
		}

		// 【降级模式】：失败自动尝试下一个 provider
		fmt.Printf("[CustomCLI][INFO] 🔄 降级模式（拉黑功能已关闭）\n")

		var lastError error
		var lastProvider string
		var lastDuration time.Duration
		totalAttempts := 0
		busySkipped := 0
		attemptedUpstream := false

		for _, level := range levels {
			providersInLevel := levelGroups[level]
			fmt.Printf("[CustomCLI][INFO] === 尝试 Level %d（%d 个 provider）===\n", level, len(providersInLevel))

			for i, provider := range providersInLevel {
				effectiveModel := provider.GetEffectiveModel(requestedModel)
				currentBodyBytes := bodyBytes
				if effectiveModel != requestedModel && requestedModel != "" {
					fmt.Printf("[CustomCLI][INFO] Provider %s 映射模型: %s -> %s\n", provider.Name, requestedModel, effectiveModel)
					modifiedBody, err := ReplaceModelInRequestBody(bodyBytes, effectiveModel)
					if err != nil {
						fmt.Printf("[CustomCLI][ERROR] 替换模型名失败: %v\n", err)
						continue
					}
					currentBodyBytes = modifiedBody
				}

				fmt.Printf("[CustomCLI][INFO]   [%d/%d] Provider: %s | Model: %s\n", i+1, len(providersInLevel), provider.Name, effectiveModel)
				// 获取有效的端点（用户配置优先）
				effectiveEndpoint := endpoint
				if !isResponsesCompactVariantEndpoint(endpoint) {
					effectiveEndpoint = provider.GetEffectiveEndpoint(endpoint)
				}

				release, acquired := prs.concurrencyManager.TryAcquire(
					providerConcurrencyKey(kind, provider.Name),
					provider.MaxConcurrentRequests,
				)
				if !acquired {
					busySkipped++
					fmt.Printf("[CustomCLI][INFO]   ⏭️ Provider %s 达到并发上限(%d)，跳过\n", provider.Name, provider.MaxConcurrentRequests)
					continue
				}

				totalAttempts++
				attemptedUpstream = true
				startTime := time.Now()
				ok, err, responseWritten := prs.forwardRequest(c, kind, provider, effectiveEndpoint, query, clientHeaders, currentBodyBytes, isStream, effectiveModel)
				duration := time.Since(startTime)
				release()

				if ok {
					fmt.Printf("[CustomCLI][INFO]   ✓ Level %d 成功: %s | 耗时: %.2fs\n", level, provider.Name, duration.Seconds())
					if err := prs.blacklistService.RecordSuccess(kind, provider.Name); err != nil {
						fmt.Printf("[CustomCLI][WARN] 清零失败计数失败: %v\n", err)
					}
					prs.setLastUsedProvider(kind, provider.Name)
					return
				}

				lastError = err
				lastProvider = provider.Name
				lastDuration = duration

				errorMsg := "未知错误"
				if err != nil {
					errorMsg = err.Error()
				}
				fmt.Printf("[CustomCLI][WARN]   ✗ Level %d 失败: %s | 错误: %s | 耗时: %.2fs\n",
					level, provider.Name, errorMsg, duration.Seconds())

				if errors.Is(err, errClientAbort) {
					fmt.Printf("[CustomCLI][INFO] 客户端中断，跳过失败计数: %s\n", provider.Name)
				} else if err := prs.blacklistService.RecordFailure(kind, provider.Name); err != nil {
					fmt.Printf("[CustomCLI][ERROR] 记录失败到黑名单失败: %v\n", err)
				}

				if responseWritten {
					fmt.Printf("[CustomCLI][WARN] 响应已写入客户端，停止降级\n")
					return
				}

				// 发送切换通知
				if prs.notificationService != nil {
					nextProvider := ""
					if i+1 < len(providersInLevel) {
						nextProvider = providersInLevel[i+1].Name
					} else {
						for _, nextLevel := range levels {
							if nextLevel > level && len(levelGroups[nextLevel]) > 0 {
								nextProvider = levelGroups[nextLevel][0].Name
								break
							}
						}
					}
					if nextProvider != "" {
						prs.notificationService.NotifyProviderSwitch(SwitchNotification{
							FromProvider: provider.Name,
							ToProvider:   nextProvider,
							Reason:       errorMsg,
							Platform:     kind,
						})
					}
				}
			}

			fmt.Printf("[CustomCLI][WARN] Level %d 的所有 %d 个 provider 均失败，尝试下一 Level\n", level, len(providersInLevel))
		}

		// 所有 provider 都失败
		if !attemptedUpstream && busySkipped > 0 {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":          "all providers are busy",
				"mode":           "concurrency_limit",
				"busy_providers": busySkipped,
			})
			return
		}

		errorMsg := "未知错误"
		if lastError != nil {
			errorMsg = lastError.Error()
		}
		fmt.Printf("[CustomCLI][ERROR] 所有 %d 个 provider 均失败，最后尝试: %s | 错误: %s\n",
			totalAttempts, lastProvider, errorMsg)

		c.JSON(http.StatusBadGateway, gin.H{
			"error":          fmt.Sprintf("所有 %d 个 provider 均失败，最后错误: %s", totalAttempts, errorMsg),
			"last_provider":  lastProvider,
			"last_duration":  fmt.Sprintf("%.2fs", lastDuration.Seconds()),
			"total_attempts": totalAttempts,
		})
	}
}

// forwardModelsRequest 共享的 /v1/models 请求转发逻辑
// 返回 (selectedProvider, error)
func (prs *ProviderRelayService) forwardModelsRequest(
	c *gin.Context,
	kind string,
	logPrefix string,
) error {
	fmt.Printf("[%s] 收到 /v1/models 请求, kind=%s\n", logPrefix, kind)

	// 加载 providers
	providers, err := prs.providerService.LoadProviders(kind)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load providers"})
		return fmt.Errorf("failed to load providers: %w", err)
	}

	// 过滤可用的 providers（启用 + URL + APIKey）
	var activeProviders []Provider
	for _, provider := range providers {
		if !provider.Enabled || provider.APIURL == "" || provider.APIKey == "" {
			continue
		}

		// 黑名单检查：跳过已拉黑的 provider
		if isBlacklisted, until := prs.blacklistService.IsBlacklisted(kind, provider.Name); isBlacklisted {
			fmt.Printf("[%s] ⛔ Provider %s 已拉黑，过期时间: %v\n", logPrefix, provider.Name, until.Format("15:04:05"))
			continue
		}

		activeProviders = append(activeProviders, provider)
	}

	if len(activeProviders) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no providers available"})
		return fmt.Errorf("no providers available")
	}

	// 按 Level 分组并排序
	levelGroups := make(map[int][]Provider)
	for _, provider := range activeProviders {
		level := provider.Level
		if level <= 0 {
			level = 1
		}
		levelGroups[level] = append(levelGroups[level], provider)
	}

	levels := make([]int, 0, len(levelGroups))
	for level := range levelGroups {
		levels = append(levels, level)
	}
	sort.Ints(levels)

	// 尝试第一个可用的 provider（按 Level 升序）
	var selectedProvider *Provider
	for _, level := range levels {
		if len(levelGroups[level]) > 0 {
			p := levelGroups[level][0]
			selectedProvider = &p
			break
		}
	}

	if selectedProvider == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no providers available"})
		return fmt.Errorf("no providers available after filtering")
	}

	fmt.Printf("[%s] 使用 Provider: %s | URL: %s\n", logPrefix, selectedProvider.Name, selectedProvider.APIURL)

	// 构建目标 URL（拼接 provider 的 APIURL 和 /v1/models）
	targetURL := joinURL(selectedProvider.APIURL, "/v1/models")

	// 创建 HTTP 请求
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("创建请求失败: %v", err)})
		return fmt.Errorf("failed to create request: %w", err)
	}

	// 复制客户端请求头
	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// 根据认证方式设置请求头
	authType := strings.ToLower(strings.TrimSpace(selectedProvider.ConnectivityAuthType))
	if authType == "" {
		if strings.EqualFold(kind, "claude") {
			authType = "x-api-key"
		} else {
			authType = "bearer"
		}
	}
	switch authType {
	case "x-api-key":
		req.Header.Set("x-api-key", selectedProvider.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "bearer":
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", selectedProvider.APIKey))
	default:
		headerName := strings.TrimSpace(selectedProvider.ConnectivityAuthType)
		if headerName == "" || strings.EqualFold(headerName, "custom") {
			headerName = "Authorization"
		}
		req.Header.Set(headerName, selectedProvider.APIKey)
	}

	// 设置默认 Accept 头
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	// 发送请求
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[%s] ✗ 请求失败: %s | 错误: %v\n", logPrefix, selectedProvider.Name, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("请求失败: %v", err)})
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("[%s] ✗ 读取响应失败: %s | 错误: %v\n", logPrefix, selectedProvider.Name, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("读取响应失败: %v", err)})
		return fmt.Errorf("failed to read response: %w", err)
	}

	// 复制响应头
	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}

	fmt.Printf("[%s] ✓ 成功: %s | HTTP %d\n", logPrefix, selectedProvider.Name, resp.StatusCode)

	// 返回响应
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
	return nil
}

// modelsHandler 处理 /v1/models 请求（OpenAI-compatible API）
// 将请求转发到第一个可用的 provider 并注入 API Key
func (prs *ProviderRelayService) modelsHandler(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		_ = prs.forwardModelsRequest(c, kind, "Models")
	}
}

// customModelsHandler 处理自定义 CLI 工具的 /v1/models 请求
// 路由格式: /custom/:toolId/v1/models
func (prs *ProviderRelayService) customModelsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从 URL 参数提取 toolId
		toolId := c.Param("toolId")
		if toolId == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "toolId is required"})
			return
		}

		// 构建 provider kind（格式: "custom:{toolId}"）
		kind := "custom:" + toolId

		_ = prs.forwardModelsRequest(c, kind, "CustomModels")
	}
}
