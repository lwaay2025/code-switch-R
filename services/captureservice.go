package services

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/daodao97/xgo/xdb"
)

type CaptureService struct{}

func NewCaptureService() *CaptureService {
	return &CaptureService{}
}

func (s *CaptureService) GetEnabled() bool {
	if GlobalCaptureManager == nil {
		return false
	}
	return GlobalCaptureManager.IsEnabled()
}

func (s *CaptureService) SetEnabled(enabled bool) {
	if GlobalCaptureManager == nil {
		return
	}
	_ = GlobalCaptureManager.SetEnabled(enabled)
}

func (s *CaptureService) GetConfig() CaptureConfig {
	if GlobalCaptureManager == nil {
		return DefaultCaptureConfig()
	}
	return GlobalCaptureManager.GetConfig()
}

func (s *CaptureService) SetConfig(cfg CaptureConfig) {
	if GlobalCaptureManager == nil {
		return
	}
	_ = GlobalCaptureManager.SetConfig(cfg)
}

func (s *CaptureService) QueryActivities(limit, offset int) []map[string]string {
	results, err := QueryCaptureActivities(limit, offset)
	if err != nil {
		return []map[string]string{}
	}
	return results
}

func (s *CaptureService) QueryTurns(activityID string, limit, offset int) []map[string]string {
	results, err := QueryCaptureTurns(activityID, limit, offset)
	if err != nil {
		return []map[string]string{}
	}
	return results
}

func (s *CaptureService) QueryRequests(turnID string, limit, offset int) []map[string]string {
	results, err := QueryCaptureRequests(turnID, limit, offset)
	if err != nil {
		return []map[string]string{}
	}
	return results
}

func (s *CaptureService) QueryChunks(requestID string, limit, offset int) []map[string]string {
	results, err := QueryResponseChunks(requestID, limit, offset)
	if err != nil {
		return []map[string]string{}
	}
	return results
}

type CaptureStats struct {
	TotalActivities int   `json:"total_activities"`
	TotalTurns      int   `json:"total_turns"`
	TotalRequests   int   `json:"total_requests"`
	DbSizeBytes     int64 `json:"db_size_bytes"`
}

func (s *CaptureService) GetStats() CaptureStats {
	db, err := xdb.DB("default")
	if err != nil {
		return CaptureStats{}
	}
	var actCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM capture_activity").Scan(&actCount)
	var turnCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM capture_turn").Scan(&turnCount)
	var reqCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM capture_request").Scan(&reqCount)
	var dbSize int64
	_ = db.QueryRow("SELECT COALESCE(SUM(pgsize), 0) FROM dbstat WHERE name LIKE 'capture_%'").Scan(&dbSize)
	return CaptureStats{
		TotalActivities: actCount,
		TotalTurns:      turnCount,
		TotalRequests:   reqCount,
		DbSizeBytes:     dbSize,
	}
}

func (s *CaptureService) ExportJSON(turnID string) string {
	result := make(map[string]interface{})
	turns, err := QueryCaptureTurns(turnID, 1, 0)
	if err == nil && len(turns) > 0 {
		result["turn"] = turns[0]
	}
	requests, err := QueryCaptureRequests(turnID, 100, 0)
	if err == nil {
		var enriched []map[string]interface{}
		for _, req := range requests {
			item := make(map[string]interface{})
			for k, v := range req {
				item[k] = v
			}
			rid := req["request_id"]
			if chunks, cErr := QueryResponseChunks(rid, 10000, 0); cErr == nil {
				item["chunks"] = chunks
			}
			enriched = append(enriched, item)
		}
		result["requests"] = enriched
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return string(b)
}

func (s *CaptureService) RunCleanup() string {
	if GlobalCaptureManager == nil {
		return "capture manager not initialized"
	}
	GlobalCaptureManager.Cleanup()
	return "ok"
}

func (s *CaptureService) DeleteActivity(activityID string) error {
	return DeleteCaptureActivity(activityID)
}

func init() {
	go func() {
		time.Sleep(30 * time.Second)
		if GlobalCaptureManager != nil {
			GlobalCaptureManager.StartCleanupTicker(context.Background())
		}
	}()
}

func parseInt(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}
