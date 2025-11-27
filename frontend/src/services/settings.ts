/**
 * 全局配置服务 API 封装
 */
import { Call } from '@wailsio/runtime'

const SETTINGS_SERVICE = 'codeswitch/services.SettingsService'

export interface BlacklistSettings {
  failureThreshold: number // 失败次数阈值（1-10）
  durationMinutes: number  // 拉黑时长（分钟：15/30/60）
}

/**
 * 获取拉黑配置
 */
export const getBlacklistSettings = async (): Promise<BlacklistSettings> => {
  const result = await Call.ByName(`${SETTINGS_SERVICE}.GetBlacklistSettingsStruct`)
  return result as BlacklistSettings
}

/**
 * 更新拉黑配置
 * @param threshold 失败阈值（1-10）
 * @param duration 拉黑时长（15/30/60 分钟）
 */
export const updateBlacklistSettings = async (
  threshold: number,
  duration: number
): Promise<void> => {
  await Call.ByName(`${SETTINGS_SERVICE}.UpdateBlacklistSettings`, threshold, duration)
}

/**
 * 获取等级拉黑开关状态
 * @returns 是否启用等级拉黑机制
 */
export const getLevelBlacklistEnabled = async (): Promise<boolean> => {
  const result = await Call.ByName(`${SETTINGS_SERVICE}.GetLevelBlacklistEnabled`)
  return result as boolean
}

/**
 * 设置等级拉黑开关状态
 * @param enabled 是否启用等级拉黑机制
 */
export const setLevelBlacklistEnabled = async (enabled: boolean): Promise<void> => {
  await Call.ByName(`${SETTINGS_SERVICE}.SetLevelBlacklistEnabled`, enabled)
}

/**
 * 获取拉黑功能总开关状态
 * @returns 是否启用拉黑功能
 */
export const getBlacklistEnabled = async (): Promise<boolean> => {
  const result = await Call.ByName(`${SETTINGS_SERVICE}.IsBlacklistEnabled`)
  return result as boolean
}

/**
 * 设置拉黑功能总开关状态
 * @param enabled 是否启用拉黑功能
 */
export const setBlacklistEnabled = async (enabled: boolean): Promise<void> => {
  await Call.ByName(`${SETTINGS_SERVICE}.UpdateBlacklistEnabled`, enabled)
}
