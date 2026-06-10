package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daodao97/xgo/xdb"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// ============================================================
// 全局开关与配置
// ============================================================

var (
	globalCaptureEnabled atomic.Bool
)

type CaptureConfig struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
	MaxRecords    int  `json:"max_records"`
}

func DefaultCaptureConfig() CaptureConfig {
	return CaptureConfig{
		Enabled:       false,
		RetentionDays: 3,
		MaxRecords:    1000,
	}
}

// ============================================================
// CaptureSession - 一次 request-response 的完整追踪
// ============================================================

type CaptureChunk struct {
	Index     int       `json:"index"`
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
	Type      string    `json:"type"`
}

type CaptureSession struct {
	mu sync.Mutex

	RequestID    string `json:"request_id"`
	TurnID       string `json:"turn_id"`
	ActivityID   string `json:"activity_id"`
	PrevTurnID   string `json:"prev_turn_id"`
	Timestamp    time.Time `json:"timestamp"`
	Source       string    `json:"source"`
	Protocol     string    `json:"protocol"`
	URL          string    `json:"url"`
	Method       string    `json:"method"`
	ReqHeaders   string    `json:"req_headers"`
	ReqBody      string    `json:"req_body"`
	Model        string    `json:"model"`

	FirstByteAt    time.Time       `json:"first_byte_at"`
	LastChunkAt    time.Time       `json:"last_chunk_at"`
	StatusCode     int             `json:"status_code"`
	RespHeaders    string          `json:"resp_headers"`
	Status         string          `json:"status"`

	ChunkCount     int             `json:"chunk_count"`
	TotalBytes     int64           `json:"total_bytes"`
	Chunks         []CaptureChunk  `json:"-"`
	AggregatedText string          `json:"aggregated_text"`
	Flushed        bool            `json:"-"`
}

func NewCaptureSession(requestID, turnID, activityID, prevTurnID string) *CaptureSession {
	return &CaptureSession{
		RequestID:  requestID,
		TurnID:     turnID,
		ActivityID: activityID,
		PrevTurnID: prevTurnID,
		Timestamp:  time.Now(),
		Status:     "pending",
		Protocol:   "SSE",
		Chunks:     make([]CaptureChunk, 0, 64),
	}
}

func (s *CaptureSession) RecordChunk(content string, chunkType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.FirstByteAt.IsZero() {
		s.FirstByteAt = now
		s.Status = "streaming"
	}
	s.Chunks = append(s.Chunks, CaptureChunk{
		Index:     s.ChunkCount,
		Timestamp: now,
		Content:   content,
		Type:      chunkType,
	})
	s.ChunkCount++
	s.TotalBytes += int64(len(content))
	s.AggregatedText += content
	s.LastChunkAt = now
}

func (s *CaptureSession) Finalize(status string, httpCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
	if httpCode > 0 {
		s.StatusCode = httpCode
	}
	if s.LastChunkAt.IsZero() {
		s.LastChunkAt = time.Now()
	}
}

// ============================================================
// CaptureManager - 全局抓包管理器
// ============================================================

type CaptureManager struct {
	mu       sync.RWMutex
	sessions map[string]*CaptureSession
	config   CaptureConfig
}

var GlobalCaptureManager *CaptureManager

func InitCaptureManager() {
	GlobalCaptureManager = &CaptureManager{
		sessions: make(map[string]*CaptureSession),
		config:   DefaultCaptureConfig(),
	}
	GlobalCaptureManager.loadConfig()
}

// ============================================================
// 开关控制
// ============================================================

func (cm *CaptureManager) IsEnabled() bool {
	return globalCaptureEnabled.Load()
}

func (cm *CaptureManager) SetEnabled(enabled bool) error {
	globalCaptureEnabled.Store(enabled)
	val := "false"
	if enabled {
		val = "true"
	}
	err := GlobalDBQueue.Exec("INSERT OR REPLACE INTO app_settings (key, value) VALUES ('capture_enabled', ?)", val)
	if !enabled {
		cm.mu.Lock()
		cm.sessions = make(map[string]*CaptureSession)
		cm.mu.Unlock()
	}
	return err
}

func (cm *CaptureManager) GetConfig() CaptureConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.config
}

func (cm *CaptureManager) SetConfig(cfg CaptureConfig) error {
	cm.mu.Lock()
	cm.config = cfg
	cm.mu.Unlock()
	if err := cm.saveConfig(); err != nil {
		return err
	}
	if cfg.Enabled {
		go cm.Cleanup()
	}
	return nil
}

func (cm *CaptureManager) loadConfig() {
	cfg := DefaultCaptureConfig()
	db, err := xdb.DB("default")
	if err != nil {
		return
	}
	var enabled string
	if err := db.QueryRow("SELECT value FROM app_settings WHERE key = 'capture_enabled'").Scan(&enabled); err == nil {
		cfg.Enabled = enabled == "true"
	}
	var days string
	if err := db.QueryRow("SELECT value FROM app_settings WHERE key = 'capture_retention_days'").Scan(&days); err == nil {
		fmt.Sscanf(days, "%d", &cfg.RetentionDays)
	}
	var maxRec string
	if err := db.QueryRow("SELECT value FROM app_settings WHERE key = 'capture_max_records'").Scan(&maxRec); err == nil {
		fmt.Sscanf(maxRec, "%d", &cfg.MaxRecords)
	}
	cm.mu.Lock()
	cm.config = cfg
	cm.mu.Unlock()
	globalCaptureEnabled.Store(cfg.Enabled)
}

func (cm *CaptureManager) saveConfig() error {
	cm.mu.RLock()
	cfg := cm.config
	cm.mu.RUnlock()
	var err error
	err = GlobalDBQueue.Exec("INSERT OR REPLACE INTO app_settings (key, value) VALUES ('capture_enabled', ?)", fmt.Sprintf("%v", cfg.Enabled))
	if err != nil { return err }
	err = GlobalDBQueue.Exec("INSERT OR REPLACE INTO app_settings (key, value) VALUES ('capture_retention_days', ?)", fmt.Sprintf("%d", cfg.RetentionDays))
	if err != nil { return err }
	err = GlobalDBQueue.Exec("INSERT OR REPLACE INTO app_settings (key, value) VALUES ('capture_max_records', ?)", fmt.Sprintf("%d", cfg.MaxRecords))
	return err
}

// ============================================================
// Session 管理
// ============================================================

func (cm *CaptureManager) GetSession(turnID string) *CaptureSession {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.sessions[turnID]
}

func (cm *CaptureManager) RemoveSession(turnID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.sessions, turnID)
}

// ============================================================
// 核心拦截入口
// ============================================================

func (cm *CaptureManager) InterceptRequest(source, endpoint string, body []byte, headers map[string]string) *CaptureSession {
	if !cm.IsEnabled() {
		return nil
	}
	if !isCaptureEligibleEndpoint(endpoint) {
		return nil
	}

	activityID := cm.resolveActivityID(headers, body)
	turnID, prevTurnID := cm.resolveTurnID(body)
	requestID := uuid.NewString()

	// 异步数据库写入
	go func() {
		_ = InsertCaptureActivity(activityID, source)
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		model := gjson.GetBytes(body, "model").String()
		_ = InsertCaptureTurn(turnID, activityID, prevTurnID, source, model, now)
		reqHeadersJSON := mapToJSON(headers)
		reqBodyStr := string(body)
		_ = InsertCaptureRequest(requestID, turnID, activityID, source, "SSE", endpoint, "POST", reqHeadersJSON, reqBodyStr, now)
		_ = IncrementCaptureActivityCounters(activityID, 1, 1)
	}()

	session := NewCaptureSession(requestID, turnID, activityID, prevTurnID)
	session.Source = source
	session.URL = endpoint
	session.Method = "POST"
	session.ReqHeaders = mapToJSON(headers)
	session.ReqBody = string(body)
	session.Model = gjson.GetBytes(body, "model").String()

	cm.mu.Lock()
	cm.sessions[turnID] = session
	cm.mu.Unlock()

	return session
}

func (cm *CaptureManager) InterceptResponseChunk(turnID string, data []byte) {
	if !cm.IsEnabled() {
		return
	}
	session := cm.GetSession(turnID)
	if session == nil {
		return
	}

	line := strings.TrimSpace(string(data))
	if line == "" {
		return
	}

	// SSE event: 行不包含有效负载，跳过它等待后续 data: 行一起处理
	if strings.HasPrefix(line, "event: ") {
		return
	}

	// 去掉 data: 前缀，提取 JSON 负载
	var jsonPayload string
	if strings.HasPrefix(line, "data: ") {
		jsonPayload = strings.TrimSpace(line[6:])
	} else {
		jsonPayload = line
	}

	chunkType := "event"
	if jsonPayload == "[DONE]" || jsonPayload == "[done]" {
		chunkType = "done"
	} else if gjson.Valid(jsonPayload) {
		if t := strings.TrimSpace(gjson.Get(jsonPayload, "type").String()); t != "" {
			chunkType = t
		}
	}
	session.RecordChunk(jsonPayload, chunkType)
}

func (cm *CaptureManager) FinalizeTurn(turnID string, status string, httpCode int, respHeaders map[string]string) {
	session := cm.GetSession(turnID)
	if session == nil {
		return
	}

	// Prevent duplicate FinalizeTurn calls
	session.mu.Lock()
	if session.Flushed {
		writeDebugLog("H1", "FINALIZE-ALREADY-FLUSHED", map[string]interface{}{"turnID": turnID})
		session.mu.Unlock()
		return
	}
	session.Flushed = true
	session.mu.Unlock()

	session.Finalize(status, httpCode)

	writeDebugLog("H1", "FINALIZE-PRE-GOROUTINE", map[string]interface{}{"turnID": turnID, "flushed": true, "chunkCount": session.ChunkCount})
	// Deep-copy respHeaders to avoid concurrent map issue
	headersCopy := make(map[string]string, len(respHeaders))
	for k, v := range respHeaders {
		headersCopy[k] = v
	}

	go func(s *CaptureSession, headersCopy map[string]string) {
		writeDebugLog("H14", "FINALIZE-START", map[string]interface{}{"turnID": turnID, "chunkCount": s.ChunkCount})

		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		durationMs := int(time.Since(s.Timestamp).Milliseconds())

		_ = UpdateCaptureTurnSummary(s.TurnID, s.Status, s.StatusCode, now, durationMs, s.ChunkCount, int(s.TotalBytes))
		writeDebugLog("H14", "FINALIZE-TURN-SUMMARY", map[string]interface{}{"turnID": turnID})

		if !s.FirstByteAt.IsZero() {
			_ = UpdateCaptureTurnFirstByte(s.TurnID, s.FirstByteAt.UTC().Format("2006-01-02 15:04:05"))
		}
		writeDebugLog("H14", "FINALIZE-FIRST-BYTE", map[string]interface{}{"turnID": turnID})

		if len(s.AggregatedText) > 0 {
			_ = UpdateCaptureTurnAggregatedText(s.TurnID, s.AggregatedText)
		}
		writeDebugLog("H14", "FINALIZE-AGG-TEXT", map[string]interface{}{"turnID": turnID})

		respHeadersJSON := mapToJSON(headersCopy)
		summary := buildResponseSummary(s)
		_ = UpdateCaptureRequestResponse(s.RequestID, respHeadersJSON, s.StatusCode, summary)
		writeDebugLog("H14", "FINALIZE-REQ-RESPONSE", map[string]interface{}{"turnID": turnID})

		if len(s.Chunks) > 0 {
			chunkRecords := make([]ChunkRecord, len(s.Chunks))
			for i, c := range s.Chunks {
				chunkRecords[i] = ChunkRecord{
					RequestID: s.RequestID,
					TurnID:    s.TurnID,
					Index:     c.Index,
					Content:   c.Content,
					ChunkType: c.Type,
				}
			}
			_ = BulkInsertResponseChunks(chunkRecords)
			writeDebugLog("H14", "FINALIZE-BULK-CHUNKS", map[string]interface{}{"turnID": turnID, "count": len(chunkRecords)})
		}

		_ = UpdateCaptureActivityLastSeen(s.ActivityID)
		writeDebugLog("H14", "FINALIZE-ACTIVITY", map[string]interface{}{"turnID": turnID})

		// 所有 DB 写入完成后，再清理内存中的 session
		cm.RemoveSession(s.TurnID)
		writeDebugLog("H1", "FINALIZE-REMOVED-SESSION", map[string]interface{}{"turnID": turnID})

		writeDebugLog("H14", "FINALIZE-DONE", map[string]interface{}{"turnID": turnID})
	}(session, headersCopy)
}

// ============================================================
// WebSocket 支持
// ============================================================

func (cm *CaptureManager) InterceptWSConnection(connectionID, activityID, url string) {
	if !cm.IsEnabled() { return }
	_ = InsertWSConnection(connectionID, activityID, url)
}

func (cm *CaptureManager) InterceptWSMessage(connectionID string, msgIndex int, direction, content, msgType string) {
	if !cm.IsEnabled() { return }
	_ = InsertWSMessage(connectionID, msgIndex, direction, content, msgType)
}

func (cm *CaptureManager) CloseWSConnection(connectionID string) {
	if !cm.IsEnabled() { return }
	_ = UpdateWSConnectionClosed(connectionID)
}

// ============================================================
// 辅助函数
// ============================================================

func (cm *CaptureManager) resolveActivityID(headers map[string]string, body []byte) string {
	for _, h := range []string{"Conversation_id", "Session_id", "X-CodeSwitch-Session-Key"} {
		if v := getHeaderValueFold(headers, h); v != "" {
			return v
		}
	}
	if body != nil {
		if v := gjson.GetBytes(body, "prompt_cache_key").String(); v != "" {
			return v
		}
	}
	return uuid.NewString()
}

func (cm *CaptureManager) resolveTurnID(body []byte) (turnID string, prevTurnID string) {
	if body != nil {
		if v := gjson.GetBytes(body, "previous_response_id").String(); v != "" {
			prevTurnID = v
			turnID = v
		}
	}
	if turnID == "" {
		turnID = uuid.NewString()
	}
	return
}

func isCaptureEligibleEndpoint(endpoint string) bool {
	lower := strings.ToLower(strings.TrimSpace(endpoint))
	return strings.Contains(lower, "/responses")
}

func mapToJSON(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func buildResponseSummary(s *CaptureSession) string {
	if s == nil {
		return "{}"
	}
	summary := map[string]interface{}{
		"first_byte_at":   s.FirstByteAt.Format("2006-01-02 15:04:05.000"),
		"last_chunk_at":   s.LastChunkAt.Format("2006-01-02 15:04:05.000"),
		"duration_ms":     time.Since(s.Timestamp).Milliseconds(),
		"status":          s.Status,
		"status_code":     s.StatusCode,
		"chunk_count":     s.ChunkCount,
		"total_bytes":     s.TotalBytes,
		"aggregated_text": s.AggregatedText,
	}
	b, _ := json.Marshal(summary)
	return string(b)
}

func (cm *CaptureManager) Cleanup() {
	if cm == nil {
		return
	}
	cm.mu.RLock()
	days := cm.config.RetentionDays
	maxRec := cm.config.MaxRecords
	cm.mu.RUnlock()

	if days > 0 {
		if deleted, err := DeleteCaptureOlderThan(days); err != nil {
			fmt.Printf("⚠️  抓包数据清理失败: %v\n", err)
		} else if deleted > 0 {
			fmt.Printf("🧹 抓包数据清理: 删除了 %d 条过期记录\n", deleted)
		}
	}
	if maxRec > 0 {
		if deleted, err := DeleteExcessCaptureRecords(maxRec); err != nil {
			fmt.Printf("⚠️  抓包数据超量清理失败: %v\n", err)
		} else if deleted > 0 {
			fmt.Printf("🧹 抓包数据超量清理: 删除了 %d 条超额记录\n", deleted)
		}
	}
}

func (cm *CaptureManager) StartCleanupTicker(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		for {
			select {
			case <-ticker.C:
				cm.Cleanup()
			case <-ctx.Done():
				ticker.Stop()
				return
			}
		}
	}()
}
