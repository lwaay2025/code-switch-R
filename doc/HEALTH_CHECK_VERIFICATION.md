# Health Check Fix - Verification Checklist

## Pre-Fix Behavior (Issues)

- [ ] Health check fails even though provider works for actual requests
- [ ] Error message: model not supported / invalid model name
- [ ] Providers with model mappings have incorrect health status
- [ ] Azure or strict OpenAI proxies reject health check requests

## Post-Fix Behavior (Expected)

- [ ] Health check uses mapped model names (same as relay)
- [ ] Health check includes `Accept: application/json` header
- [ ] Health check endpoint resolution matches relay behavior
- [ ] If provider works for real requests → health check passes

## Manual Verification Steps

### 1. Test Provider with Model Mapping

**Setup:**
1. Create a provider with model mapping, e.g.:
   ```json
   {
     "name": "TestProvider",
     "modelMapping": {
       "gpt-4o-mini": "openai/gpt-4o-mini"
     }
   }
   ```

**Test:**
1. Enable availability monitoring for the provider
2. Trigger a manual health check
3. Check logs for model mapping message:
   ```
   [HealthCheck] [codex/TestProvider] 模型映射: gpt-4o-mini -> openai/gpt-4o-mini
   ```
4. Verify health check status is "operational" (if provider is working)

**Expected:**
- Health check sends `openai/gpt-4o-mini` to upstream
- Provider responds successfully
- Health status shows as "operational"

### 2. Test Accept Header

**Setup:**
1. Use a provider that requires strict headers (e.g., Azure OpenAI)
2. Enable availability monitoring

**Test:**
1. Trigger health check
2. Monitor network traffic or provider logs
3. Verify `Accept: application/json` header is present

**Expected:**
- Request includes both `Content-Type` and `Accept` headers
- No 406 or 415 errors from provider

### 3. Test Endpoint Resolution

**Setup:**
1. Create three providers:
   - A: No custom endpoint (should use platform default)
   - B: Custom endpoint via `apiEndpoint`
   - C: Health check specific endpoint via `availabilityConfig.testEndpoint`

**Test:**
1. Trigger health checks for all three
2. Check which endpoints are used

**Expected:**
- Provider A: Uses `/responses` for Codex, `/v1/messages` for Claude
- Provider B: Uses custom endpoint from `apiEndpoint`
- Provider C: Uses `testEndpoint` from `availabilityConfig`

### 4. Test Without Model Mapping

**Setup:**
1. Create a provider without model mapping

**Test:**
1. Trigger health check
2. Verify original model name is used (e.g., `gpt-4o-mini`)

**Expected:**
- No model mapping log message
- Original test model name sent to upstream
- Health check works normally

## Automated Test Verification

Run the test suite:

```bash
cd /home/runner/work/code-switch-R/code-switch-R
go test -v ./services -run TestHealthCheck
```

**Expected output:**
```
=== RUN   TestHealthCheck_ModelMapping
--- PASS: TestHealthCheck_ModelMapping (0.XXs)
=== RUN   TestHealthCheck_AcceptHeader
--- PASS: TestHealthCheck_AcceptHeader (0.XXs)
=== RUN   TestHealthCheck_EndpointResolution
--- PASS: TestHealthCheck_EndpointResolution (0.XXs)
...
PASS
```

## Regression Testing

Verify existing functionality still works:

### Health Check Features
- [ ] Manual health check trigger works
- [ ] Auto-polling continues to work
- [ ] Health check history is saved correctly
- [ ] Availability monitoring toggle works
- [ ] Auto-blacklist integration works
- [ ] Health check UI displays correct status

### Provider Relay Features
- [ ] Actual request forwarding still works
- [ ] Model mapping works in relay (unchanged)
- [ ] Endpoint resolution works in relay (unchanged)
- [ ] Headers are correct in relay (unchanged)

## Integration Test

**End-to-End Scenario:**

1. Configure a provider with model mapping
2. Enable availability monitoring
3. Enable connectivity auto-blacklist
4. Wait for automated health check to run
5. Make an actual request through the relay
6. Verify both health check and actual request succeed with same model mapping

**Expected:**
- Health check: sends mapped model → passes
- Actual request: sends mapped model → works
- Both use identical model transformation logic

## Known Edge Cases

### Edge Case 1: Wildcard Model Mapping
```json
{
  "modelMapping": {
    "gpt-*": "openai/gpt-*"
  }
}
```
- Health check correctly expands wildcards
- Test model `gpt-4o-mini` → `openai/gpt-4o-mini`

### Edge Case 2: Multiple Mappings
```json
{
  "modelMapping": {
    "gpt-4": "openai/gpt-4-turbo",
    "gpt-4o-mini": "openai/gpt-4o-mini"
  }
}
```
- Correct mapping selected based on test model
- Exact matches preferred over wildcards

### Edge Case 3: No Supported Models
```json
{
  "supportedModels": {}
}
```
- Health check still runs with default test model
- No filtering based on supported models list

## Rollback Plan

If issues are found:

1. Revert to commit before fix:
   ```bash
   git revert HEAD~3..HEAD
   ```

2. Restore previous behavior:
   - Remove model mapping call
   - Remove Accept header
   - Restore old endpoint resolution logic

3. Re-test with reverted code

## Sign-off

- [ ] All manual verification steps completed
- [ ] All automated tests pass
- [ ] No regressions found
- [ ] Documentation reviewed
- [ ] Edge cases tested
- [ ] Integration test passed

**Verified by:** ________________
**Date:** ________________
**Notes:** ________________
