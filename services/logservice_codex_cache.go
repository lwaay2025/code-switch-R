package services

import "sort"

type codexPromptCacheStats struct {
	EnabledRequests   int64
	EligibleRequests  int64
	MatchableRequests int64
	HitRequests       int64
}

func annotateCodexPromptCacheLogs(logs []ReqeustLog) codexPromptCacheStats {
	if len(logs) == 0 {
		return codexPromptCacheStats{}
	}

	order := make([]int, len(logs))
	for i := range logs {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		left := logs[order[i]]
		right := logs[order[j]]
		if left.ID == right.ID {
			return order[i] < order[j]
		}
		return left.ID < right.ID
	})

	stats := codexPromptCacheStats{}
	seenSuccessful := make(map[string]struct{})

	for _, idx := range order {
		logEntry := &logs[idx]
		if !logEntry.codexPromptCacheInScope {
			if logEntry.CodexPromptCacheEnabled && logEntry.CodexPromptCacheEligible {
				cacheKey := logEntry.codexPromptCacheBucket + "|" + logEntry.codexPromptCacheFingerprint
				if cacheKey != "|" && cacheKey != "" && isRequestLogSuccessful(*logEntry) {
					seenSuccessful[cacheKey] = struct{}{}
				}
			}
			continue
		}

		if logEntry.CodexPromptCacheEnabled {
			stats.EnabledRequests++
		}
		if !logEntry.CodexPromptCacheEnabled || !logEntry.CodexPromptCacheEligible {
			continue
		}

		stats.EligibleRequests++
		cacheKey := logEntry.codexPromptCacheBucket + "|" + logEntry.codexPromptCacheFingerprint
		if cacheKey != "|" && cacheKey != "" {
			if _, ok := seenSuccessful[cacheKey]; ok {
				logEntry.CodexPromptCacheMatchable = true
				stats.MatchableRequests++
				if logEntry.CodexPromptCacheHit {
					stats.HitRequests++
				}
			}
			if isRequestLogSuccessful(*logEntry) {
				seenSuccessful[cacheKey] = struct{}{}
			}
		}
	}

	return stats
}

func isRequestLogSuccessful(logEntry ReqeustLog) bool {
	return logEntry.HttpCode >= 200 && logEntry.HttpCode < 300 && logEntry.OutputTokens > 0
}
