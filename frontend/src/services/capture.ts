import { Call } from '@wailsio/runtime'

export interface CaptureConfig {
  enabled: boolean
  retention_days: number
  max_records: number
}

export interface CaptureActivity {
  activity_id: string
  source: string
  first_seen_at: string
  last_seen_at: string
  turn_count: number
  request_count: number
  status: string
}

export interface CaptureTurn {
  turn_id: string
  activity_id: string
  previous_turn_id: string
  source: string
  model: string
  requested_at: string
  completed_at: string
  first_byte_at: string
  status: string
  http_code: number
  total_duration_ms: number
  chunk_count: number
  total_bytes: number
  aggregated_response_text: string
}

export interface CaptureRequest {
  request_id: string
  turn_id: string
  activity_id: string
  timestamp: string
  source: string
  protocol: string
  url: string
  method: string
  request_headers: string
  request_body: string
  response_headers: string
  response_status_code: number
  response_summary: string
}

export interface CaptureChunk {
  id: number
  chunk_index: number
  chunk_timestamp: string
  chunk_content: string
  chunk_type: string
}

export interface CaptureStats {
  total_activities: number
  total_turns: number
  total_requests: number
  db_size_bytes: number
}

const SERVICE = 'codeswitch/services.CaptureService'

// ============================================================
// 开关控制
// ============================================================

export const getCaptureEnabled = async (): Promise<boolean> => {
  return Call.ByName(`${SERVICE}.GetEnabled`)
}

export const setCaptureEnabled = async (enabled: boolean): Promise<void> => {
  return Call.ByName(`${SERVICE}.SetEnabled`, enabled)
}

// ============================================================
// 配置
// ============================================================

export const getCaptureConfig = async (): Promise<CaptureConfig> => {
  return Call.ByName(`${SERVICE}.GetConfig`)
}

export const setCaptureConfig = async (cfg: CaptureConfig): Promise<void> => {
  return Call.ByName(`${SERVICE}.SetConfig`, cfg)
}

// ============================================================
// 查询
// ============================================================

export const queryActivities = async (limit = 50, offset = 0): Promise<CaptureActivity[]> => {
  return Call.ByName(`${SERVICE}.QueryActivities`, limit, offset)
}

export const queryTurns = async (activityID: string, limit = 100, offset = 0): Promise<CaptureTurn[]> => {
  return Call.ByName(`${SERVICE}.QueryTurns`, activityID, limit, offset)
}

export const queryRequests = async (turnID: string, limit = 10, offset = 0): Promise<CaptureRequest[]> => {
  return Call.ByName(`${SERVICE}.QueryRequests`, turnID, limit, offset)
}

export const queryChunks = async (requestID: string, limit = 1000, offset = 0): Promise<CaptureChunk[]> => {
  return Call.ByName(`${SERVICE}.QueryChunks`, requestID, limit, offset)
}

// ============================================================
// 统计 & 导出
// ============================================================

export const getCaptureStats = async (): Promise<CaptureStats> => {
  return Call.ByName(`${SERVICE}.GetStats`)
}

export const deleteActivity = async (activityID: string): Promise<void> => {
  return Call.ByName(`${SERVICE}.DeleteActivity`, activityID)
}

export const exportCaptureJSON = async (turnID: string): Promise<string> => {
  return Call.ByName(`${SERVICE}.ExportJSON`, turnID)
}
