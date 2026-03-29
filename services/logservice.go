package services

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	modelpricing "codeswitch/resources/model-pricing"

	"github.com/daodao97/xgo/xdb"
)

const timeLayout = "2006-01-02 15:04:05"
const dayLayout = "2006-01-02"

// request_log.created_at is populated by SQLite CURRENT_TIMESTAMP, which is UTC.
// Convert local query boundaries to the same storage timezone before coarse filtering.
func storageTimestamp(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

type LogService struct {
	pricing           *modelpricing.Service
	appSettings       *AppSettingsService
	retentionStopChan chan struct{}
	retentionWg       sync.WaitGroup
	retentionMu       sync.Mutex
}

func NewLogService(appSettings *AppSettingsService) *LogService {
	svc, err := modelpricing.DefaultService()
	if err != nil {
		log.Printf("pricing service init failed: %v", err)
	}
	return &LogService{
		pricing:     svc,
		appSettings: appSettings,
	}
}

func (ls *LogService) Start() error {
	if ls == nil {
		return nil
	}

	if _, err := ls.RunRetentionCleanup(); err != nil {
		log.Printf("[LogService] 首次日志保留清理失败: %v", err)
	}

	ls.retentionMu.Lock()
	if ls.retentionStopChan != nil {
		ls.retentionMu.Unlock()
		return nil
	}
	stopChan := make(chan struct{})
	ls.retentionStopChan = stopChan
	ls.retentionWg.Add(1)
	ls.retentionMu.Unlock()

	go func() {
		defer ls.retentionWg.Done()
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := ls.RunRetentionCleanup(); err != nil {
					log.Printf("[LogService] 定时日志保留清理失败: %v", err)
				}
			case <-stopChan:
				return
			}
		}
	}()

	return nil
}

func (ls *LogService) Stop() error {
	if ls == nil {
		return nil
	}
	ls.retentionMu.Lock()
	stopChan := ls.retentionStopChan
	ls.retentionStopChan = nil
	ls.retentionMu.Unlock()

	if stopChan != nil {
		close(stopChan)
		ls.retentionWg.Wait()
	}
	return nil
}

func (ls *LogService) RunRetentionCleanup() (int64, error) {
	if ls == nil || ls.appSettings == nil {
		return 0, nil
	}
	settings, err := ls.appSettings.GetAppSettings()
	if err != nil {
		return 0, err
	}
	if !settings.LogRetentionEnabled {
		return 0, nil
	}

	normalizeLogRetentionSettings(&settings)
	cutoffStart := startOfDay(time.Now().In(time.Local).AddDate(0, 0, -settings.LogRetentionDays))
	db, err := xdb.DB("default")
	if err != nil {
		return 0, err
	}

	result, err := db.Exec(`DELETE FROM request_log WHERE created_at < ?`, storageTimestamp(cutoffStart))
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	deleted, _ := result.RowsAffected()
	if deleted > 0 {
		log.Printf("[LogService] 已清理 %d 条过期日志（保留 %d 天）", deleted, settings.LogRetentionDays)
	}
	return deleted, nil
}

func (ls *LogService) ListRequestLogs(platform string, provider string, limit int) ([]ReqeustLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.OrderByDesc("id"),
		xdb.Limit(limit),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	if provider != "" {
		options = append(options, xdb.WhereEq("provider", provider))
	}
	records, err := model.Selects(options...)
	if err != nil {
		return nil, err
	}
	logs := make([]ReqeustLog, 0, len(records))
	for _, record := range records {
		logEntry := ReqeustLog{
			ID:                          record.GetInt64("id"),
			Platform:                    record.GetString("platform"),
			Model:                       record.GetString("model"),
			Provider:                    record.GetString("provider"),
			HttpCode:                    record.GetInt("http_code"),
			InputTokens:                 record.GetInt("input_tokens"),
			OutputTokens:                record.GetInt("output_tokens"),
			CacheCreateTokens:           record.GetInt("cache_create_tokens"),
			CacheReadTokens:             record.GetInt("cache_read_tokens"),
			ReasoningTokens:             record.GetInt("reasoning_tokens"),
			CreatedAt:                   record.GetString("created_at"),
			IsStream:                    record.GetBool("is_stream"),
			DurationSec:                 record.GetFloat64("duration_sec"),
			CodexPromptCacheEnabled:     record.GetBool("codex_prompt_cache_enabled"),
			CodexPromptCacheEligible:    record.GetBool("codex_prompt_cache_eligible"),
			CodexPromptCacheHit:         record.GetBool("codex_prompt_cache_hit"),
			codexPromptCacheBucket:      record.GetString("codex_prompt_cache_bucket"),
			codexPromptCacheFingerprint: record.GetString("codex_prompt_cache_fingerprint"),
			codexPromptCacheInScope:     true,
		}
		if !logEntry.CodexPromptCacheHit && logEntry.CacheReadTokens > 0 {
			logEntry.CodexPromptCacheHit = true
		}
		ls.decorateCost(&logEntry)
		logs = append(logs, logEntry)
	}
	annotateCodexPromptCacheLogs(logs)
	return logs, nil
}

func (ls *LogService) ListProviders(platform string) ([]string, error) {
	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.Field("DISTINCT provider as provider"),
		xdb.WhereNotEq("provider", ""),
		xdb.OrderByAsc("provider"),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		return nil, err
	}
	providers := make([]string, 0, len(records))
	for _, record := range records {
		name := strings.TrimSpace(record.GetString("provider"))
		if name != "" {
			providers = append(providers, name)
		}
	}
	return providers, nil
}

func (ls *LogService) ListRequestLogsOnDate(platform string, provider string, date string, limit int) ([]ReqeustLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	dayStart, dayEnd, err := parseDateRange(date)
	if err != nil {
		return nil, err
	}

	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.OrderByDesc("id"),
		xdb.WhereGte("created_at", storageTimestamp(dayStart.Add(-24*time.Hour))),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	if provider != "" {
		options = append(options, xdb.WhereEq("provider", provider))
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return []ReqeustLog{}, nil
		}
		return nil, err
	}

	logs := make([]ReqeustLog, 0, len(records))
	for _, record := range records {
		_, _, inRange := normalizeRecordTime(record, dayStart, dayEnd)
		logEntry := ReqeustLog{
			ID:                          record.GetInt64("id"),
			Platform:                    record.GetString("platform"),
			Model:                       record.GetString("model"),
			Provider:                    record.GetString("provider"),
			HttpCode:                    record.GetInt("http_code"),
			InputTokens:                 record.GetInt("input_tokens"),
			OutputTokens:                record.GetInt("output_tokens"),
			CacheCreateTokens:           record.GetInt("cache_create_tokens"),
			CacheReadTokens:             record.GetInt("cache_read_tokens"),
			ReasoningTokens:             record.GetInt("reasoning_tokens"),
			CreatedAt:                   record.GetString("created_at"),
			IsStream:                    record.GetBool("is_stream"),
			DurationSec:                 record.GetFloat64("duration_sec"),
			CodexPromptCacheEnabled:     record.GetBool("codex_prompt_cache_enabled"),
			CodexPromptCacheEligible:    record.GetBool("codex_prompt_cache_eligible"),
			CodexPromptCacheHit:         record.GetBool("codex_prompt_cache_hit"),
			codexPromptCacheBucket:      record.GetString("codex_prompt_cache_bucket"),
			codexPromptCacheFingerprint: record.GetString("codex_prompt_cache_fingerprint"),
			codexPromptCacheInScope:     inRange,
		}
		if !logEntry.CodexPromptCacheHit && logEntry.CacheReadTokens > 0 {
			logEntry.CodexPromptCacheHit = true
		}
		ls.decorateCost(&logEntry)
		logs = append(logs, logEntry)
	}
	annotateCodexPromptCacheLogs(logs)

	filtered := make([]ReqeustLog, 0, min(limit, len(logs)))
	for _, logEntry := range logs {
		if !logEntry.codexPromptCacheInScope {
			continue
		}
		filtered = append(filtered, logEntry)
		if len(filtered) >= limit {
			break
		}
	}

	return filtered, nil
}

func (ls *LogService) ListProvidersOnDate(platform string, date string) ([]string, error) {
	dayStart, dayEnd, err := parseDateRange(date)
	if err != nil {
		return nil, err
	}

	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.Field("provider", "created_at"),
		xdb.WhereNotEq("provider", ""),
		xdb.WhereGte("created_at", storageTimestamp(dayStart)),
		xdb.OrderByAsc("provider"),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return []string{}, nil
		}
		return nil, err
	}

	seen := make(map[string]struct{})
	providers := make([]string, 0, len(records))
	for _, record := range records {
		if _, _, inRange := normalizeRecordTime(record, dayStart, dayEnd); !inRange {
			continue
		}
		name := strings.TrimSpace(record.GetString("provider"))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers, nil
}

func (ls *LogService) HeatmapStats(days int) ([]HeatmapStat, error) {
	if days <= 0 {
		days = 30
	}
	totalHours := days * 24
	if totalHours <= 0 {
		totalHours = 24
	}
	rangeStart := startOfHour(time.Now())
	if totalHours > 1 {
		rangeStart = rangeStart.Add(-time.Duration(totalHours-1) * time.Hour)
	}
	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.WhereGe("created_at", storageTimestamp(rangeStart)),
		xdb.Field(
			"model",
			"input_tokens",
			"output_tokens",
			"reasoning_tokens",
			"cache_create_tokens",
			"cache_read_tokens",
			"created_at",
		),
		xdb.OrderByDesc("created_at"),
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return []HeatmapStat{}, nil
		}
		return nil, err
	}
	hourBuckets := map[int64]*HeatmapStat{}
	for _, record := range records {
		createdAt, _ := parseCreatedAt(record)
		if createdAt.IsZero() {
			continue
		}
		hourStart := startOfHour(createdAt)
		hourKey := hourStart.Unix()
		bucket := hourBuckets[hourKey]
		if bucket == nil {
			bucket = &HeatmapStat{Day: hourStart.Format("01-02 15")}
			hourBuckets[hourKey] = bucket
		}
		bucket.TotalRequests++
		input := record.GetInt("input_tokens")
		output := record.GetInt("output_tokens")
		reasoning := record.GetInt("reasoning_tokens")
		cacheCreate := record.GetInt("cache_create_tokens")
		cacheRead := record.GetInt("cache_read_tokens")
		bucket.InputTokens += int64(input)
		bucket.OutputTokens += int64(output)
		bucket.ReasoningTokens += int64(reasoning)
		usage := modelpricing.UsageSnapshot{
			InputTokens:       input,
			OutputTokens:      output,
			ReasoningTokens:   reasoning,
			CacheCreateTokens: cacheCreate,
			CacheReadTokens:   cacheRead,
		}
		cost := ls.calculateCost(record.GetString("model"), usage)
		bucket.TotalCost += cost.TotalCost
	}
	if len(hourBuckets) == 0 {
		return []HeatmapStat{}, nil
	}
	hourKeys := make([]int64, 0, len(hourBuckets))
	for key := range hourBuckets {
		hourKeys = append(hourKeys, key)
	}
	sort.Slice(hourKeys, func(i, j int) bool {
		return hourKeys[i] < hourKeys[j]
	})
	stats := make([]HeatmapStat, 0, min(len(hourKeys), totalHours))
	for i := len(hourKeys) - 1; i >= 0 && len(stats) < totalHours; i-- {
		stats = append(stats, *hourBuckets[hourKeys[i]])
	}
	return stats, nil
}

func (ls *LogService) StatsSince(platform string) (LogStats, error) {
	const seriesHours = 24

	stats := LogStats{
		Series: make([]LogStatsSeries, 0, seriesHours),
	}
	now := time.Now()
	model := xdb.New("request_log")
	seriesStart := startOfDay(now)
	seriesEnd := seriesStart.Add(seriesHours * time.Hour)
	queryStart := seriesStart.Add(-24 * time.Hour)
	summaryStart := seriesStart
	options := []xdb.Option{
		xdb.WhereGte("created_at", storageTimestamp(queryStart)),
		xdb.Field(
			"id",
			"platform",
			"model",
			"http_code",
			"input_tokens",
			"output_tokens",
			"reasoning_tokens",
			"cache_create_tokens",
			"cache_read_tokens",
			"duration_sec",
			"codex_prompt_cache_enabled",
			"codex_prompt_cache_eligible",
			"codex_prompt_cache_hit",
			"codex_prompt_cache_bucket",
			"codex_prompt_cache_fingerprint",
			"created_at",
		),
		xdb.OrderByAsc("created_at"),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return stats, nil
		}
		return stats, err
	}

	seriesBuckets := make([]*LogStatsSeries, seriesHours)
	for i := 0; i < seriesHours; i++ {
		bucketTime := seriesStart.Add(time.Duration(i) * time.Hour)
		seriesBuckets[i] = &LogStatsSeries{
			Day: bucketTime.Format(timeLayout),
		}
	}

	analysisLogs := make([]ReqeustLog, 0, len(records))
	durationAcc := &durationAccumulator{}

	for _, record := range records {
		createdAt, hasTime := parseCreatedAt(record)
		dayKey := dayFromTimestamp(record.GetString("created_at"))
		isToday := dayKey == seriesStart.Format("2006-01-02")
		inSummary := false
		if hasTime {
			inSummary = !createdAt.Before(summaryStart) && createdAt.Before(seriesEnd)
		} else {
			inSummary = isToday
		}
		output := record.GetInt("output_tokens")
		cacheRead := record.GetInt("cache_read_tokens")
		analysisLogs = append(analysisLogs, ReqeustLog{
			ID:                          record.GetInt64("id"),
			Platform:                    record.GetString("platform"),
			HttpCode:                    record.GetInt("http_code"),
			OutputTokens:                output,
			CacheReadTokens:             cacheRead,
			CodexPromptCacheEnabled:     record.GetBool("codex_prompt_cache_enabled"),
			CodexPromptCacheEligible:    record.GetBool("codex_prompt_cache_eligible"),
			CodexPromptCacheHit:         record.GetBool("codex_prompt_cache_hit") || cacheRead > 0,
			codexPromptCacheBucket:      record.GetString("codex_prompt_cache_bucket"),
			codexPromptCacheFingerprint: record.GetString("codex_prompt_cache_fingerprint"),
			codexPromptCacheInScope:     inSummary,
		})

		if hasTime {
			if createdAt.Before(seriesStart) || !createdAt.Before(seriesEnd) {
				continue
			}
		} else {
			if !isToday {
				continue
			}
			createdAt = seriesStart
		}

		bucketIndex := 0
		if hasTime {
			bucketIndex = int(createdAt.Sub(seriesStart) / time.Hour)
			if bucketIndex < 0 {
				bucketIndex = 0
			}
			if bucketIndex >= seriesHours {
				bucketIndex = seriesHours - 1
			}
		}
		bucket := seriesBuckets[bucketIndex]
		input := record.GetInt("input_tokens")
		reasoning := record.GetInt("reasoning_tokens")
		cacheCreate := record.GetInt("cache_create_tokens")
		durationSec := record.GetFloat64("duration_sec")
		usage := modelpricing.UsageSnapshot{
			InputTokens:       input,
			OutputTokens:      output,
			ReasoningTokens:   reasoning,
			CacheCreateTokens: cacheCreate,
			CacheReadTokens:   cacheRead,
		}
		cost := ls.calculateCost(record.GetString("model"), usage)

		bucket.TotalRequests++
		bucket.InputTokens += int64(input)
		bucket.OutputTokens += int64(output)
		bucket.ReasoningTokens += int64(reasoning)
		bucket.CacheCreateTokens += int64(cacheCreate)
		bucket.CacheReadTokens += int64(cacheRead)
		bucket.TotalCost += cost.TotalCost

		if createdAt.IsZero() || createdAt.Before(summaryStart) {
			continue
		}
		stats.TotalRequests++
		stats.InputTokens += int64(input)
		stats.OutputTokens += int64(output)
		stats.ReasoningTokens += int64(reasoning)
		stats.CacheCreateTokens += int64(cacheCreate)
		stats.CacheReadTokens += int64(cacheRead)
		stats.CostInput += cost.InputCost
		stats.CostOutput += cost.OutputCost
		stats.CostCacheCreate += cost.CacheCreateCost
		stats.CostCacheRead += cost.CacheReadCost
		stats.CostTotal += cost.TotalCost
		durationAcc.Add(durationSec)
	}

	cacheStats := annotateCodexPromptCacheLogs(analysisLogs)
	stats.CodexPromptCacheEnabledRequests = cacheStats.EnabledRequests
	stats.CodexPromptCacheEligibleRequests = cacheStats.EligibleRequests
	stats.CodexPromptCacheMatchableRequests = cacheStats.MatchableRequests
	stats.CodexPromptCacheHitRequests = cacheStats.HitRequests
	if cacheStats.MatchableRequests > 0 {
		stats.CodexPromptCacheHitRate = float64(cacheStats.HitRequests) / float64(cacheStats.MatchableRequests)
	}
	applyDurationStatsToLogStats(&stats, durationAcc)

	for i := 0; i < seriesHours; i++ {
		if bucket := seriesBuckets[i]; bucket != nil {
			stats.Series = append(stats.Series, *bucket)
		} else {
			bucketTime := seriesStart.Add(time.Duration(i) * time.Hour)
			stats.Series = append(stats.Series, LogStatsSeries{
				Day: bucketTime.Format(timeLayout),
			})
		}
	}

	return stats, nil
}

func (ls *LogService) ProviderDailyStats(platform string) ([]ProviderDailyStat, error) {
	start := startOfDay(time.Now())
	end := start.Add(24 * time.Hour)
	queryStart := start.Add(-24 * time.Hour)
	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.WhereGte("created_at", storageTimestamp(queryStart)),
		xdb.Field(
			"id",
			"provider",
			"platform",
			"model",
			"http_code",
			"input_tokens",
			"output_tokens",
			"reasoning_tokens",
			"cache_create_tokens",
			"cache_read_tokens",
			"duration_sec",
			"codex_prompt_cache_enabled",
			"codex_prompt_cache_eligible",
			"codex_prompt_cache_hit",
			"codex_prompt_cache_bucket",
			"codex_prompt_cache_fingerprint",
			"created_at",
		),
		xdb.OrderByAsc("created_at"),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return []ProviderDailyStat{}, nil
		}
		return nil, err
	}
	statMap := map[string]*ProviderDailyStat{}
	analysisLogs := make([]ReqeustLog, 0, len(records))
	durationMap := map[string]*durationAccumulator{}
	for _, record := range records {
		provider := providerNameForStats(record.GetString("provider"))
		_, _, inRange := normalizeRecordTime(record, start, end)
		output := record.GetInt("output_tokens")
		cacheRead := record.GetInt("cache_read_tokens")
		analysisLogs = append(analysisLogs, ReqeustLog{
			ID:                          record.GetInt64("id"),
			Platform:                    record.GetString("platform"),
			Provider:                    provider,
			HttpCode:                    record.GetInt("http_code"),
			OutputTokens:                output,
			CacheReadTokens:             cacheRead,
			CodexPromptCacheEnabled:     record.GetBool("codex_prompt_cache_enabled"),
			CodexPromptCacheEligible:    record.GetBool("codex_prompt_cache_eligible"),
			CodexPromptCacheHit:         record.GetBool("codex_prompt_cache_hit") || cacheRead > 0,
			codexPromptCacheBucket:      record.GetString("codex_prompt_cache_bucket"),
			codexPromptCacheFingerprint: record.GetString("codex_prompt_cache_fingerprint"),
			codexPromptCacheInScope:     inRange,
		})
		if !inRange {
			continue
		}

		stat := statMap[provider]
		if stat == nil {
			stat = &ProviderDailyStat{Provider: provider}
			statMap[provider] = stat
		}
		httpCode := record.GetInt("http_code")
		input := record.GetInt("input_tokens")
		reasoning := record.GetInt("reasoning_tokens")
		cacheCreate := record.GetInt("cache_create_tokens")
		durationSec := record.GetFloat64("duration_sec")
		usage := modelpricing.UsageSnapshot{
			InputTokens:       input,
			OutputTokens:      output,
			ReasoningTokens:   reasoning,
			CacheCreateTokens: cacheCreate,
			CacheReadTokens:   cacheRead,
		}
		cost := ls.calculateCost(record.GetString("model"), usage)
		stat.TotalRequests++
		// 只有 HTTP 200-299 且 output_tokens > 0 才算成功
		if httpCode >= 200 && httpCode < 300 && output > 0 {
			stat.SuccessfulRequests++
		} else {
			stat.FailedRequests++
		}
		stat.InputTokens += int64(input)
		stat.OutputTokens += int64(output)
		stat.ReasoningTokens += int64(reasoning)
		stat.CacheCreateTokens += int64(cacheCreate)
		stat.CacheReadTokens += int64(cacheRead)
		stat.CostTotal += cost.TotalCost
		durationAcc := durationMap[provider]
		if durationAcc == nil {
			durationAcc = &durationAccumulator{}
			durationMap[provider] = durationAcc
		}
		durationAcc.Add(durationSec)
	}
	applyProviderCodexPromptCacheStats(analysisLogs, statMap)
	stats := make([]ProviderDailyStat, 0, len(statMap))
	for _, stat := range statMap {
		if stat.TotalRequests > 0 {
			stat.SuccessRate = float64(stat.SuccessfulRequests) / float64(stat.TotalRequests)
		}
		if stat.CodexPromptCacheMatchableRequests > 0 {
			stat.CodexPromptCacheHitRate = float64(stat.CodexPromptCacheHitRequests) / float64(stat.CodexPromptCacheMatchableRequests)
		}
		applyDurationStatsToProviderStat(stat, durationMap[providerNameForStats(stat.Provider)])
		stats = append(stats, *stat)
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].TotalRequests == stats[j].TotalRequests {
			return stats[i].Provider < stats[j].Provider
		}
		return stats[i].TotalRequests > stats[j].TotalRequests
	})
	return stats, nil
}

func (ls *LogService) StatsOnDate(platform string, date string) (LogStats, error) {
	const seriesHours = 24

	stats := LogStats{
		Series: make([]LogStatsSeries, 0, seriesHours),
	}

	dayStart, dayEnd, err := parseDateRange(date)
	if err != nil {
		return stats, err
	}

	model := xdb.New("request_log")
	queryStart := dayStart.Add(-24 * time.Hour)
	options := []xdb.Option{
		xdb.WhereGte("created_at", storageTimestamp(queryStart)),
		xdb.Field(
			"id",
			"platform",
			"model",
			"http_code",
			"input_tokens",
			"output_tokens",
			"reasoning_tokens",
			"cache_create_tokens",
			"cache_read_tokens",
			"duration_sec",
			"codex_prompt_cache_enabled",
			"codex_prompt_cache_eligible",
			"codex_prompt_cache_hit",
			"codex_prompt_cache_bucket",
			"codex_prompt_cache_fingerprint",
			"created_at",
		),
		xdb.OrderByAsc("created_at"),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return stats, nil
		}
		return stats, err
	}

	seriesBuckets := make([]*LogStatsSeries, seriesHours)
	for i := 0; i < seriesHours; i++ {
		bucketTime := dayStart.Add(time.Duration(i) * time.Hour)
		seriesBuckets[i] = &LogStatsSeries{
			Day: bucketTime.Format(timeLayout),
		}
	}

	analysisLogs := make([]ReqeustLog, 0, len(records))
	durationAcc := &durationAccumulator{}

	for _, record := range records {
		createdAt, hasTime, inRange := normalizeRecordTime(record, dayStart, dayEnd)
		output := record.GetInt("output_tokens")
		cacheRead := record.GetInt("cache_read_tokens")
		analysisLogs = append(analysisLogs, ReqeustLog{
			ID:                          record.GetInt64("id"),
			Platform:                    record.GetString("platform"),
			HttpCode:                    record.GetInt("http_code"),
			OutputTokens:                output,
			CacheReadTokens:             cacheRead,
			CodexPromptCacheEnabled:     record.GetBool("codex_prompt_cache_enabled"),
			CodexPromptCacheEligible:    record.GetBool("codex_prompt_cache_eligible"),
			CodexPromptCacheHit:         record.GetBool("codex_prompt_cache_hit") || cacheRead > 0,
			codexPromptCacheBucket:      record.GetString("codex_prompt_cache_bucket"),
			codexPromptCacheFingerprint: record.GetString("codex_prompt_cache_fingerprint"),
			codexPromptCacheInScope:     inRange,
		})
		if !inRange {
			continue
		}

		bucketIndex := 0
		if hasTime {
			bucketIndex = int(createdAt.Sub(dayStart) / time.Hour)
			if bucketIndex < 0 {
				bucketIndex = 0
			}
			if bucketIndex >= seriesHours {
				bucketIndex = seriesHours - 1
			}
		}

		bucket := seriesBuckets[bucketIndex]
		input := record.GetInt("input_tokens")
		reasoning := record.GetInt("reasoning_tokens")
		cacheCreate := record.GetInt("cache_create_tokens")
		durationSec := record.GetFloat64("duration_sec")
		usage := modelpricing.UsageSnapshot{
			InputTokens:       input,
			OutputTokens:      output,
			ReasoningTokens:   reasoning,
			CacheCreateTokens: cacheCreate,
			CacheReadTokens:   cacheRead,
		}
		cost := ls.calculateCost(record.GetString("model"), usage)

		bucket.TotalRequests++
		bucket.InputTokens += int64(input)
		bucket.OutputTokens += int64(output)
		bucket.ReasoningTokens += int64(reasoning)
		bucket.CacheCreateTokens += int64(cacheCreate)
		bucket.CacheReadTokens += int64(cacheRead)
		bucket.TotalCost += cost.TotalCost

		stats.TotalRequests++
		stats.InputTokens += int64(input)
		stats.OutputTokens += int64(output)
		stats.ReasoningTokens += int64(reasoning)
		stats.CacheCreateTokens += int64(cacheCreate)
		stats.CacheReadTokens += int64(cacheRead)
		stats.CostInput += cost.InputCost
		stats.CostOutput += cost.OutputCost
		stats.CostCacheCreate += cost.CacheCreateCost
		stats.CostCacheRead += cost.CacheReadCost
		stats.CostTotal += cost.TotalCost
		durationAcc.Add(durationSec)
	}

	cacheStats := annotateCodexPromptCacheLogs(analysisLogs)
	stats.CodexPromptCacheEnabledRequests = cacheStats.EnabledRequests
	stats.CodexPromptCacheEligibleRequests = cacheStats.EligibleRequests
	stats.CodexPromptCacheMatchableRequests = cacheStats.MatchableRequests
	stats.CodexPromptCacheHitRequests = cacheStats.HitRequests
	if cacheStats.MatchableRequests > 0 {
		stats.CodexPromptCacheHitRate = float64(cacheStats.HitRequests) / float64(cacheStats.MatchableRequests)
	}
	applyDurationStatsToLogStats(&stats, durationAcc)

	for i := 0; i < seriesHours; i++ {
		if bucket := seriesBuckets[i]; bucket != nil {
			stats.Series = append(stats.Series, *bucket)
		} else {
			bucketTime := dayStart.Add(time.Duration(i) * time.Hour)
			stats.Series = append(stats.Series, LogStatsSeries{
				Day: bucketTime.Format(timeLayout),
			})
		}
	}

	return stats, nil
}

func (ls *LogService) ProviderDailyStatsOnDate(platform string, date string) ([]ProviderDailyStat, error) {
	dayStart, dayEnd, err := parseDateRange(date)
	if err != nil {
		return nil, err
	}

	model := xdb.New("request_log")
	queryStart := dayStart.Add(-24 * time.Hour)
	options := []xdb.Option{
		xdb.WhereGte("created_at", storageTimestamp(queryStart)),
		xdb.Field(
			"id",
			"provider",
			"platform",
			"model",
			"http_code",
			"input_tokens",
			"output_tokens",
			"reasoning_tokens",
			"cache_create_tokens",
			"cache_read_tokens",
			"duration_sec",
			"codex_prompt_cache_enabled",
			"codex_prompt_cache_eligible",
			"codex_prompt_cache_hit",
			"codex_prompt_cache_bucket",
			"codex_prompt_cache_fingerprint",
			"created_at",
		),
		xdb.OrderByAsc("created_at"),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return []ProviderDailyStat{}, nil
		}
		return nil, err
	}

	statMap := map[string]*ProviderDailyStat{}
	analysisLogs := make([]ReqeustLog, 0, len(records))
	durationMap := map[string]*durationAccumulator{}
	for _, record := range records {
		_, _, inRange := normalizeRecordTime(record, dayStart, dayEnd)
		provider := providerNameForStats(record.GetString("provider"))
		output := record.GetInt("output_tokens")
		cacheRead := record.GetInt("cache_read_tokens")
		analysisLogs = append(analysisLogs, ReqeustLog{
			ID:                          record.GetInt64("id"),
			Platform:                    record.GetString("platform"),
			Provider:                    provider,
			HttpCode:                    record.GetInt("http_code"),
			OutputTokens:                output,
			CacheReadTokens:             cacheRead,
			CodexPromptCacheEnabled:     record.GetBool("codex_prompt_cache_enabled"),
			CodexPromptCacheEligible:    record.GetBool("codex_prompt_cache_eligible"),
			CodexPromptCacheHit:         record.GetBool("codex_prompt_cache_hit") || cacheRead > 0,
			codexPromptCacheBucket:      record.GetString("codex_prompt_cache_bucket"),
			codexPromptCacheFingerprint: record.GetString("codex_prompt_cache_fingerprint"),
			codexPromptCacheInScope:     inRange,
		})
		if !inRange {
			continue
		}

		stat := statMap[provider]
		if stat == nil {
			stat = &ProviderDailyStat{Provider: provider}
			statMap[provider] = stat
		}

		httpCode := record.GetInt("http_code")
		input := record.GetInt("input_tokens")
		reasoning := record.GetInt("reasoning_tokens")
		cacheCreate := record.GetInt("cache_create_tokens")
		durationSec := record.GetFloat64("duration_sec")
		usage := modelpricing.UsageSnapshot{
			InputTokens:       input,
			OutputTokens:      output,
			ReasoningTokens:   reasoning,
			CacheCreateTokens: cacheCreate,
			CacheReadTokens:   cacheRead,
		}
		cost := ls.calculateCost(record.GetString("model"), usage)

		stat.TotalRequests++
		if httpCode >= 200 && httpCode < 300 && output > 0 {
			stat.SuccessfulRequests++
		} else {
			stat.FailedRequests++
		}
		stat.InputTokens += int64(input)
		stat.OutputTokens += int64(output)
		stat.ReasoningTokens += int64(reasoning)
		stat.CacheCreateTokens += int64(cacheCreate)
		stat.CacheReadTokens += int64(cacheRead)
		stat.CostTotal += cost.TotalCost
		durationAcc := durationMap[provider]
		if durationAcc == nil {
			durationAcc = &durationAccumulator{}
			durationMap[provider] = durationAcc
		}
		durationAcc.Add(durationSec)
	}
	applyProviderCodexPromptCacheStats(analysisLogs, statMap)

	stats := make([]ProviderDailyStat, 0, len(statMap))
	for _, stat := range statMap {
		if stat.TotalRequests > 0 {
			stat.SuccessRate = float64(stat.SuccessfulRequests) / float64(stat.TotalRequests)
		}
		if stat.CodexPromptCacheMatchableRequests > 0 {
			stat.CodexPromptCacheHitRate = float64(stat.CodexPromptCacheHitRequests) / float64(stat.CodexPromptCacheMatchableRequests)
		}
		applyDurationStatsToProviderStat(stat, durationMap[providerNameForStats(stat.Provider)])
		stats = append(stats, *stat)
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].TotalRequests == stats[j].TotalRequests {
			return stats[i].Provider < stats[j].Provider
		}
		return stats[i].TotalRequests > stats[j].TotalRequests
	})

	return stats, nil
}

func (ls *LogService) decorateCost(logEntry *ReqeustLog) {
	if ls == nil || ls.pricing == nil || logEntry == nil {
		return
	}
	usage := modelpricing.UsageSnapshot{
		InputTokens:       logEntry.InputTokens,
		OutputTokens:      logEntry.OutputTokens,
		ReasoningTokens:   logEntry.ReasoningTokens,
		CacheCreateTokens: logEntry.CacheCreateTokens,
		CacheReadTokens:   logEntry.CacheReadTokens,
	}
	cost := ls.pricing.CalculateCost(logEntry.Model, usage)
	logEntry.HasPricing = cost.HasPricing
	logEntry.InputCost = cost.InputCost
	logEntry.OutputCost = cost.OutputCost
	logEntry.ReasoningCost = cost.ReasoningCost
	logEntry.CacheCreateCost = cost.CacheCreateCost
	logEntry.CacheReadCost = cost.CacheReadCost
	logEntry.Ephemeral5mCost = cost.Ephemeral5mCost
	logEntry.Ephemeral1hCost = cost.Ephemeral1hCost
	logEntry.TotalCost = cost.TotalCost
}

func (ls *LogService) calculateCost(model string, usage modelpricing.UsageSnapshot) modelpricing.CostBreakdown {
	if ls == nil || ls.pricing == nil {
		return modelpricing.CostBreakdown{}
	}
	return ls.pricing.CalculateCost(model, usage)
}

func parseDateRange(date string) (time.Time, time.Time, error) {
	trimmed := strings.TrimSpace(date)
	if trimmed == "" {
		start := startOfDay(time.Now().In(time.Local))
		return start, start.Add(24 * time.Hour), nil
	}

	if parsed, err := time.ParseInLocation(dayLayout, trimmed, time.Local); err == nil {
		start := startOfDay(parsed)
		return start, start.Add(24 * time.Hour), nil
	}

	parts := strings.Split(trimmed, "-")
	if len(parts) == 3 {
		year, yErr := strconv.Atoi(strings.TrimSpace(parts[0]))
		month, mErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		day, dErr := strconv.Atoi(strings.TrimSpace(parts[2]))
		if yErr == nil && mErr == nil && dErr == nil &&
			month >= 1 && month <= 12 && day >= 1 && day <= 31 {
			candidate := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local)
			if candidate.Year() == year &&
				int(candidate.Month()) == month &&
				candidate.Day() == day {
				start := startOfDay(candidate)
				return start, start.Add(24 * time.Hour), nil
			}
		}
	}

	return time.Time{}, time.Time{}, fmt.Errorf("invalid date format %q, expected YYYY-MM-DD", trimmed)
}

func normalizeRecordTime(record xdb.Record, dayStart, dayEnd time.Time) (time.Time, bool, bool) {
	createdAt, hasTime := parseCreatedAt(record)
	if hasTime {
		if createdAt.Before(dayStart) || !createdAt.Before(dayEnd) {
			return time.Time{}, true, false
		}
		return createdAt, true, true
	}

	dayKey := dayFromTimestamp(record.GetString("created_at"))
	if dayKey != dayStart.Format(dayLayout) {
		return time.Time{}, false, false
	}

	return dayStart, false, true
}

func parseCreatedAt(record xdb.Record) (time.Time, bool) {
	if t := record.GetTime("created_at"); t != nil {
		return t.In(time.Local), true
	}
	raw := strings.TrimSpace(record.GetString("created_at"))
	if raw == "" {
		return time.Time{}, false
	}

	layouts := []string{
		timeLayout,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 MST",
		"2006-01-02T15:04:05-0700",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.In(time.Local), true
		}
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return parsed.In(time.Local), true
		}
	}

	if normalized := strings.Replace(raw, " ", "T", 1); normalized != raw {
		if parsed, err := time.Parse(time.RFC3339, normalized); err == nil {
			return parsed.In(time.Local), true
		}
	}

	if len(raw) >= len("2006-01-02") {
		if parsed, err := time.ParseInLocation("2006-01-02", raw[:10], time.Local); err == nil {
			return parsed, false
		}
	}

	return time.Time{}, false
}

func dayFromTimestamp(value string) string {
	if len(value) >= len("2006-01-02") {
		if t, err := time.ParseInLocation(timeLayout, value, time.Local); err == nil {
			return t.Format("2006-01-02")
		}
		return value[:10]
	}
	return value
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func startOfHour(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, t.Hour(), 0, 0, 0, t.Location())
}

func providerNameForStats(value string) string {
	provider := strings.TrimSpace(value)
	if provider == "" {
		return "(unknown)"
	}
	return provider
}

func applyProviderCodexPromptCacheStats(logs []ReqeustLog, statMap map[string]*ProviderDailyStat) {
	if len(logs) == 0 || len(statMap) == 0 {
		return
	}

	annotateCodexPromptCacheLogs(logs)
	for _, logEntry := range logs {
		if !logEntry.codexPromptCacheInScope {
			continue
		}

		stat := statMap[providerNameForStats(logEntry.Provider)]
		if stat == nil {
			continue
		}

		if logEntry.CodexPromptCacheEnabled {
			stat.CodexPromptCacheEnabledRequests++
		}
		if !logEntry.CodexPromptCacheEnabled || !logEntry.CodexPromptCacheEligible {
			continue
		}

		stat.CodexPromptCacheEligibleRequests++
		if !logEntry.CodexPromptCacheMatchable {
			continue
		}

		stat.CodexPromptCacheMatchableRequests++
		if logEntry.CodexPromptCacheHit {
			stat.CodexPromptCacheHitRequests++
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isNoSuchTableErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no such table")
}

type HeatmapStat struct {
	Day             string  `json:"day"`
	TotalRequests   int64   `json:"total_requests"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	TotalCost       float64 `json:"total_cost"`
}

type LogStats struct {
	TotalRequests                     int64            `json:"total_requests"`
	InputTokens                       int64            `json:"input_tokens"`
	OutputTokens                      int64            `json:"output_tokens"`
	ReasoningTokens                   int64            `json:"reasoning_tokens"`
	CacheCreateTokens                 int64            `json:"cache_create_tokens"`
	CacheReadTokens                   int64            `json:"cache_read_tokens"`
	DurationSamples                   int64            `json:"duration_samples"`
	DurationAvgSec                    float64          `json:"duration_avg_sec"`
	DurationP95Sec                    float64          `json:"duration_p95_sec"`
	DurationP99Sec                    float64          `json:"duration_p99_sec"`
	SlowRequests                      int64            `json:"slow_requests"`
	SlowRate                          float64          `json:"slow_rate"`
	CostTotal                         float64          `json:"cost_total"`
	CostInput                         float64          `json:"cost_input"`
	CostOutput                        float64          `json:"cost_output"`
	CostCacheCreate                   float64          `json:"cost_cache_create"`
	CostCacheRead                     float64          `json:"cost_cache_read"`
	CodexPromptCacheEnabledRequests   int64            `json:"codex_prompt_cache_enabled_requests"`
	CodexPromptCacheEligibleRequests  int64            `json:"codex_prompt_cache_eligible_requests"`
	CodexPromptCacheMatchableRequests int64            `json:"codex_prompt_cache_matchable_requests"`
	CodexPromptCacheHitRequests       int64            `json:"codex_prompt_cache_hit_requests"`
	CodexPromptCacheHitRate           float64          `json:"codex_prompt_cache_hit_rate"`
	Series                            []LogStatsSeries `json:"series"`
}

type ProviderDailyStat struct {
	Provider                          string  `json:"provider"`
	TotalRequests                     int64   `json:"total_requests"`
	SuccessfulRequests                int64   `json:"successful_requests"`
	FailedRequests                    int64   `json:"failed_requests"`
	SuccessRate                       float64 `json:"success_rate"`
	InputTokens                       int64   `json:"input_tokens"`
	OutputTokens                      int64   `json:"output_tokens"`
	ReasoningTokens                   int64   `json:"reasoning_tokens"`
	CacheCreateTokens                 int64   `json:"cache_create_tokens"`
	CacheReadTokens                   int64   `json:"cache_read_tokens"`
	DurationSamples                   int64   `json:"duration_samples"`
	DurationAvgSec                    float64 `json:"duration_avg_sec"`
	DurationP95Sec                    float64 `json:"duration_p95_sec"`
	DurationP99Sec                    float64 `json:"duration_p99_sec"`
	SlowRequests                      int64   `json:"slow_requests"`
	SlowRate                          float64 `json:"slow_rate"`
	CostTotal                         float64 `json:"cost_total"`
	CodexPromptCacheEnabledRequests   int64   `json:"codex_prompt_cache_enabled_requests"`
	CodexPromptCacheEligibleRequests  int64   `json:"codex_prompt_cache_eligible_requests"`
	CodexPromptCacheMatchableRequests int64   `json:"codex_prompt_cache_matchable_requests"`
	CodexPromptCacheHitRequests       int64   `json:"codex_prompt_cache_hit_requests"`
	CodexPromptCacheHitRate           float64 `json:"codex_prompt_cache_hit_rate"`
}

type LogStatsSeries struct {
	Day               string  `json:"day"`
	TotalRequests     int64   `json:"total_requests"`
	InputTokens       int64   `json:"input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	ReasoningTokens   int64   `json:"reasoning_tokens"`
	CacheCreateTokens int64   `json:"cache_create_tokens"`
	CacheReadTokens   int64   `json:"cache_read_tokens"`
	TotalCost         float64 `json:"total_cost"`
}
