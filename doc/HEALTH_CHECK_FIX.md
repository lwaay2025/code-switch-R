# Health Check Service Fix - Model Mapping and Headers Alignment

## Problem Statement

The health check logic in `services/healthcheckservice.go` was inconsistent with the actual request forwarding logic in `services/providerrelay.go`, leading to false positives or false negatives even when providers were functioning correctly.

### Key Issues Identified

1. **Missing Model Mapping**: The health check used a test model (defaulting to `gpt-4o-mini` for Codex) but failed to apply the provider's `ModelMapping`. The relay applies mapping via `GetEffectiveModel`, ensuring the upstream receives a supported model name. The health check was sending the unmapped name, which upstreams often reject.

2. **Missing Headers**: The health check was missing the `Accept: application/json` header, which some providers or proxies (like Azure or strict OpenAI mirrors) require.

3. **Endpoint Resolution**: The endpoint resolution logic in `getEffectiveEndpoint` was not fully aligned with how `ProviderRelayService` resolves endpoints.

## Solution Implemented

### 1. Model Mapping Application (Line 527)

**Before:**
```go
model := hcs.getEffectiveModel(&provider, platform)
reqBody := hcs.buildTestRequest(platform, model)
```

**After:**
```go
model := hcs.getEffectiveModel(&provider, platform)

// 应用模型映射（关键修复：与 ProviderRelayService 对齐）
// 使用映射后的模型名发送给上游，确保健康检查与实际请求行为一致
mappedModel := provider.GetEffectiveModel(model)
if mappedModel != model {
    log.Printf("[HealthCheck] [%s/%s] 模型映射: %s -> %s", platform, provider.Name, model, mappedModel)
}

reqBody := hcs.buildTestRequest(platform, mappedModel)
```

**Impact:** 
- Health checks now use the same model name transformation as actual requests
- Providers with model mappings will no longer fail health checks due to model name mismatches
- Logging added to track when mappings are applied for debugging

### 2. Accept Header Addition (Line 558)

**Before:**
```go
req.Header.Set("Content-Type", "application/json")
```

**After:**
```go
req.Header.Set("Content-Type", "application/json")
req.Header.Set("Accept", "application/json") // 修复：添加 Accept 头，某些提供商或代理需要此头
```

**Impact:**
- Ensures compatibility with strict providers and proxies (Azure, strict OpenAI mirrors)
- Matches the header configuration used in `ProviderRelayService.forwardRequest`

### 3. Endpoint Resolution Consistency (Lines 687-708)

**Before:**
```go
func (hcs *HealthCheckService) getEffectiveEndpoint(provider *Provider, platform string) string {
    if provider.AvailabilityConfig != nil && provider.AvailabilityConfig.TestEndpoint != "" {
        return provider.AvailabilityConfig.TestEndpoint
    }
    
    if provider.APIEndpoint != "" {
        return provider.GetEffectiveEndpoint("") // ❌ Empty default
    }
    
    switch strings.ToLower(platform) {
    case "claude":
        return "/v1/messages"
    case "codex":
        return "/responses"
    default:
        return "/v1/chat/completions"
    }
}
```

**After:**
```go
func (hcs *HealthCheckService) getEffectiveEndpoint(provider *Provider, platform string) string {
    if provider.AvailabilityConfig != nil && provider.AvailabilityConfig.TestEndpoint != "" {
        return provider.AvailabilityConfig.TestEndpoint
    }
    
    // 获取平台默认端点（用于 GetEffectiveEndpoint）
    var defaultEndpoint string
    switch strings.ToLower(platform) {
    case "claude":
        defaultEndpoint = "/v1/messages"
    case "codex":
        defaultEndpoint = "/responses"
    default:
        defaultEndpoint = "/v1/chat/completions"
    }
    
    // 使用 GetEffectiveEndpoint 确保与 ProviderRelayService 行为一致
    return provider.GetEffectiveEndpoint(defaultEndpoint) // ✅ Correct default
}
```

**Impact:**
- Endpoint resolution now matches exactly what `ProviderRelayService` does
- Custom endpoints are correctly handled via `Provider.GetEffectiveEndpoint()`
- Reduces discrepancies between health checks and actual requests

## Testing

Comprehensive tests were added in `services/healthcheckservice_test.go`:

### Test Coverage

1. **TestHealthCheck_ModelMapping**: Verifies model mapping is applied correctly
2. **TestHealthCheck_AcceptHeader**: Verifies Accept header is included
3. **TestHealthCheck_EndpointResolution**: Tests endpoint resolution for various scenarios
4. **TestHealthCheck_RequestBodyStructure**: Validates request body format
5. **TestHealthCheck_NoModelMapping**: Ensures original model name is used when no mapping exists
6. **BenchmarkCheckProvider**: Performance benchmarking
7. **BenchmarkBuildTestRequest**: Request building performance

### Example Test Case

```go
func TestHealthCheck_ModelMapping(t *testing.T) {
    // Provider with model mapping
    provider := Provider{
        ModelMapping: map[string]string{
            "gpt-4o-mini": "openai/gpt-4o-mini",
        },
    }
    
    // Verify mapped model is sent to upstream
    // Expected: "openai/gpt-4o-mini" (mapped)
    // Not: "gpt-4o-mini" (original)
}
```

## Verification

The fix aligns health check behavior with actual request forwarding:

### Before Fix
- ❌ Health check sends unmapped model name → upstream rejects → false negative
- ❌ Missing Accept header → some proxies reject → false negative  
- ❌ Endpoint resolution inconsistency → wrong endpoint used → false negative

### After Fix
- ✅ Health check sends mapped model name (same as relay)
- ✅ Accept header included (same as relay)
- ✅ Endpoint resolution matches relay exactly
- ✅ If a provider works for real requests, it passes health checks

## Compatibility

- **Backward Compatible**: Changes are backward compatible
- **No Configuration Required**: Existing configurations continue to work
- **Automatic Benefit**: All providers with model mappings automatically benefit

## Related Code

- `services/healthcheckservice.go` - Health check implementation
- `services/providerrelay.go` - Request forwarding logic (reference)
- `services/providerservice.go` - Provider model and methods
- `services/healthcheckservice_test.go` - Test coverage

## References

- Issue: Health check inconsistency causing false positives/negatives
- Key Methods:
  - `Provider.GetEffectiveModel(model string) string` - Model mapping logic
  - `Provider.GetEffectiveEndpoint(defaultEndpoint string) string` - Endpoint resolution
  - `HealthCheckService.checkProvider()` - Main health check logic
