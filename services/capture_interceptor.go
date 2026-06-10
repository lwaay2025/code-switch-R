package services

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// MakeCaptureAwareHook wraps a SSE chunk callback (like ReqeustLogHook) to also
// intercept response chunks for the capture system.
func MakeCaptureAwareHook(c *gin.Context, next func(data []byte) (bool, []byte)) func(data []byte) (bool, []byte) {
	return func(data []byte) (bool, []byte) {
		CaptureInterceptResponseChunk(c, data)
		return next(data)
	}
}

// ============================================================
// 拦截器 - 在 proxyHandler 中的集成点
// ============================================================

// CaptureInterceptRequest 在 proxyHandler 转发请求前调用
// 应在 bodyBytes 被读取后、请求转发前调用
// 返回 turnID，用于后续关联响应
func CaptureInterceptRequest(c *gin.Context, kind, endpoint string, bodyBytes []byte, headers map[string]string) string {
	if GlobalCaptureManager == nil || !GlobalCaptureManager.IsEnabled() {
		return ""
	}
	if !strings.Contains(strings.ToLower(endpoint), "/responses") {
		return ""
	}

	session := GlobalCaptureManager.InterceptRequest(kind, endpoint, bodyBytes, headers)
	if session == nil {
		return ""
	}

	// 记录到 gin.Context 以便后续使用
	c.Set("capture_turn_id", session.TurnID)
	c.Set("capture_activity_id", session.ActivityID)
	fmt.Printf("[Capture] 📝 开始追踪 turn=%s activity=%s %s\\n",
		shortCaptureID(session.TurnID), shortCaptureID(session.ActivityID), endpoint)
	return session.TurnID
}

// CaptureInterceptResponseChunk 在 SSE 流式响应每个 chunk 到达时调用
// 应在 ReqeustLogHook 或 SSE 写入前调用
func CaptureInterceptResponseChunk(c *gin.Context, data []byte) {
	if GlobalCaptureManager == nil {
		return
	}
	if !GlobalCaptureManager.IsEnabled() {
		return
	}
	if c == nil {
		return
	}

	turnID, exists := c.Get("capture_turn_id")
	if !exists {
		writeDebugLog("H1", "INTERCEPT-CHUNK-NO-TURNID", map[string]interface{}{"dataLen": len(data)})
		return
	}

	tid, ok := turnID.(string)
	if !ok || tid == "" {
		writeDebugLog("H1", "INTERCEPT-CHUNK-BAD-TURNID", map[string]interface{}{"turnID": turnID})
		return
	}

	session := GlobalCaptureManager.GetSession(tid)
	if session == nil {
		writeDebugLog("H1", "INTERCEPT-CHUNK-NO-SESSION", map[string]interface{}{"turnID": tid, "dataLen": len(data)})
		return
	}
	GlobalCaptureManager.InterceptResponseChunk(tid, data)
}

// CaptureSetResponseHeaders stores upstream response headers on context for CaptureFinalizeTurn
// Should be called immediately after executeUpstreamRequest returns resp
func CaptureSetResponseHeaders(c *gin.Context, headers map[string][]string) {
	if c == nil || headers == nil {
		return
	}
	stored := make(map[string]string, len(headers))
	for k, v := range headers {
		if len(v) > 0 {
			stored[k] = v[0]
		}
	}
	c.Set("capture_resp_headers", stored)
}

// CaptureFinalizeTurn 在响应完成/出错时调用
func CaptureFinalizeTurn(c *gin.Context, status string, httpCode int) {
	if GlobalCaptureManager == nil {
		writeDebugLog("H1", "FINALIZE-NO-MGR", map[string]interface{}{"status": status, "code": httpCode})
		return
	}
	if !GlobalCaptureManager.IsEnabled() {
		return
	}

	turnID, exists := c.Get("capture_turn_id")
	if !exists {
		writeDebugLog("H1", "FINALIZE-NO-TURNID", map[string]interface{}{"status": status, "code": httpCode})
		return
	}

	tid, ok := turnID.(string)
	if !ok || tid == "" {
		writeDebugLog("H1", "FINALIZE-BAD-TURNID", map[string]interface{}{"turnID": turnID})
		return
	}

	session := GlobalCaptureManager.GetSession(tid)
	if session == nil {
		writeDebugLog("H1", "FINALIZE-NO-SESSION", map[string]interface{}{"turnID": tid, "status": status, "code": httpCode})
		return
	}

	// 收集 response headers
	respHeaders := make(map[string]string)
	// 优先从 context 获取上游响应头（CaptureSetResponseHeaders 存储的）
	if stored, exists := c.Get("capture_resp_headers"); exists {
		if storedMap, ok := stored.(map[string]string); ok {
			for k, v := range storedMap {
				respHeaders[k] = v
			}
		}
	}
	// 回退到 c.Writer.Header()（ToHttpResponseWriter 调用后已被填充）
	if c != nil && c.Writer != nil {
		for k, v := range c.Writer.Header() {
			if len(v) > 0 {
				respHeaders[k] = v[0]
			}
		}
	}

	GlobalCaptureManager.FinalizeTurn(tid, status, httpCode, respHeaders)
	fmt.Printf("[Capture] ✅ 完成 turn=%s status=%s code=%d\\n", shortCaptureID(tid), status, httpCode)
}

// ============================================================
// WebSocket 上游代理实现
// ============================================================

// WSCaptureDialer WebSocket 连接管理
// 注意：现有项目使用 gorilla/websocket 需要先添加依赖
// 以下为预留设计，实际需要 import "github.com/gorilla/websocket"

type WSCaptureConnection struct {
	ConnectionID string
	URL          string
	ActivityID   string
	MessageCount int
	CreatedAt    time.Time
	Closed       bool
}

// NewWSCaptureConnection 创建新的 WS 抓包连接
func NewWSCaptureConnection(url, activityID string) *WSCaptureConnection {
	return &WSCaptureConnection{
		ConnectionID: strings.ReplaceAll(uuid.NewString(), "-", ""),
		URL:          url,
		ActivityID:   activityID,
		CreatedAt:    time.Now(),
	}
}

// RecordSentMessage 记录发送的消息
func (wc *WSCaptureConnection) RecordSentMessage(content string, msgType string) {
	if GlobalCaptureManager == nil || !GlobalCaptureManager.IsEnabled() {
		return
	}
	wc.MessageCount++
	GlobalCaptureManager.InterceptWSMessage(wc.ConnectionID, wc.MessageCount, "send", content, msgType)
}

// RecordReceivedMessage 记录接收的消息
func (wc *WSCaptureConnection) RecordReceivedMessage(content string, msgType string) {
	if GlobalCaptureManager == nil || !GlobalCaptureManager.IsEnabled() {
		return
	}
	wc.MessageCount++
	GlobalCaptureManager.InterceptWSMessage(wc.ConnectionID, wc.MessageCount, "recv", content, msgType)
}

// Close 关闭连接并记录
func (wc *WSCaptureConnection) Close() {
	if wc.Closed {
		return
	}
	wc.Closed = true
	GlobalCaptureManager.CloseWSConnection(wc.ConnectionID)
}

// ============================================================
// WS 拦截中间件（Gin 路由）
// ============================================================

// WSCaptureMiddleware 是一个 Gin 中间件，用于拦截 WebSocket 升级请求
// 在请求进入 proxyHandler 前记录连接信息
func WSCaptureMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 检查是否为 WebSocket 升级请求
		upgrade := strings.ToLower(c.GetHeader("Upgrade"))
		if upgrade != "websocket" {
			c.Next()
			return
		}

		if GlobalCaptureManager == nil || !GlobalCaptureManager.IsEnabled() {
			c.Next()
			return
		}

		// 记录 WS 连接
		activityID := ""
		if body, err := c.GetRawData(); err == nil && len(body) > 0 {
			activityID = gjson.GetBytes(body, "session_id").String()
		}
		if activityID == "" {
			activityID = GlobalCaptureManager.resolveActivityID(
				map[string]string{"Conversation_id": c.GetHeader("Conversation_id")},
				nil,
			)
		}

		conn := NewWSCaptureConnection(c.Request.URL.String(), activityID)
		GlobalCaptureManager.InterceptWSConnection(conn.ConnectionID, activityID, conn.URL)

		c.Set("ws_capture_conn", conn)
		fmt.Printf("[Capture] 🔗 WS 连接追踪: conn=%s url=%s\\n", shortCaptureID(conn.ConnectionID), conn.URL)
		c.Next()
	}
}

func shortCaptureID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
