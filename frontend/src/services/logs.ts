import { Call } from '@wailsio/runtime'

export type LogPlatform = 'claude' | 'codex' | 'gemini'

export type RequestLog = {
  id: number
  platform: LogPlatform | ''
  model: string
  provider: string
  http_code: number
  input_tokens: number
  output_tokens: number
  cache_create_tokens: number
  cache_read_tokens: number
  reasoning_tokens: number
  codex_prompt_cache_enabled?: boolean
  codex_prompt_cache_eligible?: boolean
  codex_prompt_cache_hit?: boolean
  codex_prompt_cache_matchable?: boolean
  is_stream?: boolean | number
  duration_sec?: number
  created_at: string
  total_cost?: number
  input_cost?: number
  output_cost?: number
  cache_create_cost?: number
  cache_read_cost?: number
  ephemeral_5m_cost?: number
  ephemeral_1h_cost?: number
  has_pricing?: boolean
}

type RequestLogQuery = {
  platform?: LogPlatform | ''
  provider?: string
  limit?: number
  date?: string
}

export const fetchRequestLogs = async (query: RequestLogQuery = {}): Promise<RequestLog[]> => {
  const platform = query.platform ?? ''
  const provider = query.provider ?? ''
  const limit = query.limit ?? 100
  const date = (query.date ?? '').trim()
  if (date) {
    return Call.ByName('codeswitch/services.LogService.ListRequestLogsOnDate', platform, provider, date, limit)
  }
  return Call.ByName('codeswitch/services.LogService.ListRequestLogs', platform, provider, limit)
}

export const fetchLogProviders = async (
  platform: LogPlatform | '' = '',
  date: string = '',
): Promise<string[]> => {
  const normalizedDate = date.trim()
  if (normalizedDate) {
    return Call.ByName('codeswitch/services.LogService.ListProvidersOnDate', platform, normalizedDate)
  }
  return Call.ByName('codeswitch/services.LogService.ListProviders', platform)
}

export type LogStatsSeries = {
  day: string
  total_requests: number
  input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  cache_create_tokens: number
  cache_read_tokens: number
  total_cost: number
}

export type LogStats = {
  total_requests: number
  input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  cache_create_tokens: number
  cache_read_tokens: number
  duration_samples?: number
  duration_avg_sec?: number
  duration_p95_sec?: number
  duration_p99_sec?: number
  slow_requests?: number
  slow_rate?: number
  cost_total: number
  cost_input: number
  cost_output: number
  cost_cache_create: number
  cost_cache_read: number
  codex_prompt_cache_enabled_requests?: number
  codex_prompt_cache_eligible_requests?: number
  codex_prompt_cache_matchable_requests?: number
  codex_prompt_cache_hit_requests?: number
  codex_prompt_cache_hit_rate?: number
  series: LogStatsSeries[]
}

export const fetchLogStats = async (platform: LogPlatform | '' = '', date: string = ''): Promise<LogStats> => {
  const normalizedDate = date.trim()
  if (normalizedDate) {
    return Call.ByName('codeswitch/services.LogService.StatsOnDate', platform, normalizedDate)
  }
  return Call.ByName('codeswitch/services.LogService.StatsSince', platform)
}

export type ProviderDailyStat = {
  provider: string
  total_requests: number
  successful_requests: number
  failed_requests: number
  success_rate: number
  input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  cache_create_tokens: number
  cache_read_tokens: number
  duration_samples?: number
  duration_avg_sec?: number
  duration_p95_sec?: number
  duration_p99_sec?: number
  slow_requests?: number
  slow_rate?: number
  cost_total: number
  codex_prompt_cache_enabled_requests?: number
  codex_prompt_cache_eligible_requests?: number
  codex_prompt_cache_matchable_requests?: number
  codex_prompt_cache_hit_requests?: number
  codex_prompt_cache_hit_rate?: number
}

export const fetchProviderDailyStats = async (
  platform: LogPlatform | '' = '',
  date: string = '',
): Promise<ProviderDailyStat[]> => {
  const normalizedDate = date.trim()
  if (normalizedDate) {
    return Call.ByName('codeswitch/services.LogService.ProviderDailyStatsOnDate', platform, normalizedDate)
  }
  return Call.ByName('codeswitch/services.LogService.ProviderDailyStats', platform)
}

export type HeatmapStat = {
  day: string
  total_requests: number
  input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  total_cost: number
}

export const fetchHeatmapStats = async (days: number): Promise<HeatmapStat[]> => {
  const range = Number.isFinite(days) && days > 0 ? Math.floor(days) : 30
  return Call.ByName('codeswitch/services.LogService.HeatmapStats', range)
}
