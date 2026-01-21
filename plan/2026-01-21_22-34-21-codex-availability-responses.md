---
mode: plan
cwd: D:\\项目\\code-switch-R
task: Codex 可用性检测与 /responses 协议对齐（修复 {detail: Input must be a list}）
complexity: medium
planning_method: builtin
created_at: 2026-01-21T22:34:21.0445428+08:00
---

# Plan: Codex 可用性检测与 /responses 协议对齐

🎯 任务概述

当前 Codex 平台的可用性/连通性检测在对接 /responses（Responses API）时出现协议不一致，导致上游返回 {detail: Input must be a list} 这类校验错误（常见于 FastAPI/Pydantic 严格校验的兼容服务）。目标是：定位是哪个检测链路在发起请求、确认上游期望的请求体/响应体结构，并将 Codex 检测请求与 Responses 协议对齐，同时保持对历史/非标准实现的兼容。

📋 执行计划

1. 收集与复现
   - 收集你“怎么请求的”完整样例：URL、Header、Body、是否 stream、以及返回的原始响应（含状态码与 body）。
   - 在本地/开发环境用同样参数复现错误，确保能稳定触发。

2. 确认触发路径（可用性 vs 连通性）
   - 从前端触发点与后端暴露服务入手，确认错误来自 HealthCheckService 还是 ConnectivityTestService（两者对 Codex 的 /responses 处理目前存在差异）。
   - 记录“触发动作 → 调用服务 → 选择 endpoint → 组装 body → 发往上游”的完整链路。

3. 明确上游的 Responses 兼容级别
   - 判定上游是否是 OpenAI 官方 Responses API、OpenAI-Compatible 代理、还是自建 FastAPI 服务（决定 input 支持 string 还是必须 list，以及 list 的元素形态）。
   - 输出一份“上游期望 schema”小结：必填字段、input 类型、token 字段、错误返回格式。

4. 统一 Codex 的请求构建策略
   - 抽象/统一“当 endpoint 为 /responses 时”的请求体生成：以 Responses 语义为准，优先使用 `input` 的「消息数组」形态（`[{role, content}]`），并兼容把 `input` 为 string 的旧调用自动包一层转为 list。
   - 清理/避免在 /responses 场景仍发送 Chat Completions 风格字段（例如顶层 `messages`），并明确 `max_output_tokens` / `max_tokens` 的平台差异。

5. 对齐响应解析与成功判定
   - 为 /responses 单独实现最小可行的成功判定与提取逻辑（例如从 output/content 提取文本，或只以 2xx 为成功并记录响应片段）。
   - 统一错误归因：把 {detail: ...}、OpenAI `error.message`、以及非 JSON body 映射到可读的 `error_message`，便于 UI 与日志定位。

6. 增强可观测性（不泄露密钥）
   - 在检测请求处增加“结构化日志”：endpoint、模型（含映射前后）、input 的类型信息（string vs array）、响应状态码与关键错误字段；注意脱敏 Authorization/apiKey。

7. 补齐与加固测试
   - 为 Codex /responses 建立 httptest/mock server：断言请求体中 input 的 JSON 类型必须为 array，并覆盖 {detail: Input must be a list} 这类返回。
   - 回归 Claude /v1/messages 与默认 /v1/chat/completions 的既有测试，避免改动引入跨平台回归。

8. 手动回归 + 文档落地
   - 在 UI 的“可用性页面/检测按钮”与“连通性测试（如存在）”各跑一遍：确认不再出现 {detail: Input must be a list}，并且状态判定合理。
   - 更新文档：明确 Codex /responses 的请求示例、已知兼容差异、以及当上游不兼容时的降级/绕行配置建议（例如改用 /v1/chat/completions 或配置 testEndpoint）。

⚠️ 风险与注意事项

- 兼容性风险：部分非标准上游可能“容忍 messages 到 /responses”，改为严格 Responses body 后反而失败；需要通过“endpoint 配置/降级策略”保底。
- 误判风险：仅以 HTTP 2xx 判成功可能掩盖语义错误；但过度解析也会被不同上游 schema 拖累，建议先最小可行判定再逐步增强。
- 可观测性风险：日志若不脱敏会泄露密钥；所有输出必须只记录结构与截断内容。

📎 参考

- services/healthcheckservice.go:738
- services/connectivitytestservice.go:224
- services/providerrelay.go:192
- frontend/src/services/healthcheck.ts:50

## 附：你当前上游期望的 /responses 请求形态（收敛目标）

上游返回 `{detail: Input must be a list}` 说明它至少要求 `input` 必须是数组（且更可能要求数组元素是消息对象）。

推荐的最小请求（与 Responses 风格一致）：

```json
{
  "model": "gpt-5.2",
  "input": [{ "role": "user", "content": "hi" }],
  "max_output_tokens": 1
}
```

