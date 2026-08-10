import { api, unwrap } from './client'

// ---------- 网关管理 API（Grok 反代网关：分组 / Key / 服务 / 配置） ----------

export interface GatewayStatus {
  running: boolean
  addr?: string
  port: number
  enabled: boolean
  requests: number
  failures: number
  started_at?: string
  uptime_sec?: number
}

export interface GatewayConfig {
  enabled: boolean
  port: number
}

export interface GatewayGroup {
  id: string
  name: string
  desc?: string
  service_id?: string
  proxy_ids?: string[]
  builtin?: boolean
  created_at: string
}

export interface GroupStats {
  total: number
  available: number
  cooldown: number
  forbidden: number
}

export interface GatewayAPIKey {
  id: string
  key: string // 脱敏显示
  key_full?: string // 仅创建响应
  name: string
  group_id: string
  status: string
  created_at: string
}

export interface UpstreamService {
  id: string
  name: string
  type: string // grok | openai | anthropic
  base_url: string
  enabled: boolean
  builtin?: boolean
  created_at: string
}

export const getStatus = async () => unwrap<GatewayStatus>(await api.get('/api/v1/admin/gateway/status'))
export const getConfig = async () => unwrap<GatewayConfig>(await api.get('/api/v1/admin/gateway/config'))
export const saveConfig = async (body: GatewayConfig) =>
  unwrap<GatewayStatus>(await api.put('/api/v1/admin/gateway/config', body))

export const getGroups = async () =>
  unwrap<{ groups: GatewayGroup[]; stats: Record<string, GroupStats> }>(await api.get('/api/v1/admin/gateway/groups'))
export const createGroup = async (body: { name: string; desc?: string; service_id?: string; proxy_ids?: string[] }) =>
  unwrap<GatewayGroup>(await api.post('/api/v1/admin/gateway/groups', body))
export const updateGroup = async (id: string, patch: any) =>
  unwrap<GatewayGroup>(await api.put(`/api/v1/admin/gateway/groups/${id}`, patch))
export const deleteGroup = async (id: string) => unwrap(await api.delete(`/api/v1/admin/gateway/groups/${id}`))

export const getKeys = async () =>
  unwrap<{ keys: GatewayAPIKey[] }>(await api.get('/api/v1/admin/gateway/keys'))
export const createKey = async (body: { name: string; group_id: string }) =>
  unwrap<GatewayAPIKey>(await api.post('/api/v1/admin/gateway/keys', body))
export const setKeyStatus = async (id: string, status: string) =>
  unwrap<GatewayAPIKey>(await api.put(`/api/v1/admin/gateway/keys/${id}`, { status }))
export const deleteKey = async (id: string) => unwrap(await api.delete(`/api/v1/admin/gateway/keys/${id}`))

export const getServices = async () =>
  unwrap<{ services: UpstreamService[] }>(await api.get('/api/v1/admin/gateway/services'))
export const createService = async (body: { name: string; type: string; base_url: string; enabled: boolean }) =>
  unwrap<UpstreamService>(await api.post('/api/v1/admin/gateway/services', body))
export const updateService = async (id: string, patch: any) =>
  unwrap<UpstreamService>(await api.put(`/api/v1/admin/gateway/services/${id}`, patch))
export const deleteService = async (id: string) => unwrap(await api.delete(`/api/v1/admin/gateway/services/${id}`))

export interface GatewayAccountView {
  id: string
  email: string
  group_id: string
  group_name: string
  has_token: boolean
}
export const getGatewayAccounts = async () =>
  unwrap<{ accounts: GatewayAccountView[] }>(await api.get('/api/v1/admin/gateway/accounts'))
export const moveAccounts = async (body: { ids: string[]; group_id: string; group_name: string }) =>
  unwrap<{ moved: number }>(await api.post('/api/v1/admin/gateway/accounts/move', body))
