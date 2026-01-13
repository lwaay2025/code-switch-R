import { Call } from '@wailsio/runtime'

export type AppSettings = {
  show_heatmap: boolean
  show_home_title: boolean
  auto_start: boolean
  auto_update: boolean
  auto_connectivity_test: boolean
  enable_switch_notify: boolean // 供应商切换通知开关
  use_proxy: boolean // 是否启用代理服务器
  proxy_address: string // 代理地址
  proxy_type: string // 代理类型：http/https/socks5
  user_agent: string // 全局 User-Agent
}

const DEFAULT_SETTINGS: AppSettings = {
  show_heatmap: true,
  show_home_title: true,
  auto_start: false,
  auto_update: true,
  auto_connectivity_test: false,
  enable_switch_notify: true, // 默认开启
  use_proxy: false, // 默认不使用代理
  proxy_address: '', // 默认代理地址为空
  proxy_type: 'http', // 默认代理类型为 HTTP
  user_agent: 'code-switch-r/healthcheck',
}

export const fetchAppSettings = async (): Promise<AppSettings> => {
  const data = await Call.ByName('codeswitch/services.AppSettingsService.GetAppSettings')
  return data ?? DEFAULT_SETTINGS
}

export const saveAppSettings = async (settings: AppSettings): Promise<AppSettings> => {
  return Call.ByName('codeswitch/services.AppSettingsService.SaveAppSettings', settings)
}
