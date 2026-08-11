import { api, unwrap } from './client'

export const getConfig = async () => unwrap(await api.get('/api/v1/admin/grok-register/config'))
export const saveConfig = async (body: any) => unwrap(await api.put('/api/v1/admin/grok-register/config', body))
export const startRegister = async (body: any) => unwrap(await api.post('/api/v1/admin/grok-register/start', body))
export const stopRegister = async () => unwrap(await api.post('/api/v1/admin/grok-register/stop'))
export const getStatus = async () => unwrap(await api.get('/api/v1/admin/grok-register/status'))
export const getLogs = async () => unwrap<string[]>(await api.get('/api/v1/admin/grok-register/logs'))
export const clearLogs = async () => unwrap(await api.post('/api/v1/admin/grok-register/logs/clear'))

export interface ProxyEntry {
  url: string
  note?: string
  enabled: boolean
}
export interface ProxyTestResult {
  url: string
  ok: boolean
  ip?: string
  latency_ms: number
  error?: string
}

// 后端 engine.ProxyPool 同时返回 urls（兼容旧数据）与 entries（新数据以此为准）
export const getProxyPool = async () =>
  unwrap<{ urls: string[]; entries?: ProxyEntry[] }>(await api.get('/api/v1/admin/grok-register/proxy-pool'))
export const saveProxyPool = async (body: {urls?: string[]; text?: string; entries?: any[]}) =>
  unwrap(await api.put('/api/v1/admin/grok-register/proxy-pool', body))

export const testProxyPool = async (urls: string[]) =>
  unwrap<ProxyTestResult[]>(await api.post('/api/v1/admin/grok-register/proxy-pool/test', { urls }, { timeout: 120_000 }))

/** 同步打码并发到运行中的引擎（web 版「同步打码并发」的桌面实现）：
 *  直接调各引擎 /admin/workers 热调整，免重启 docker/重建容器。 */
export const syncWorkers = async (body: { concurrency: number; provider?: string }) =>
  unwrap<{
    results: { id: string; ok: boolean; workers: number; message?: string }[]
    concurrency: number
  }>(await api.post('/api/v1/admin/grok-register/sync-workers', body))

/* ── iCloud Privacy Mail 取码平台 ── */
export const icloudLogin = async (body: { base_url: string; username: string; password: string }) =>
  unwrap<{ logged_in: boolean }>(await api.post('/api/v1/admin/grok-register/icloud/login', body))
export const icloudHealth = async (body: { base_url: string; api_key: string }) =>
  unwrap<any>(await api.post('/api/v1/admin/grok-register/icloud/health', body))
export const icloudLogout = async (body: { base_url: string }) =>
  unwrap(await api.post('/api/v1/admin/grok-register/icloud/logout', body))
export const icloudAccounts = async (base_url: string) =>
  unwrap<{ accounts: any[] }>(await api.get('/api/v1/admin/grok-register/icloud/accounts', { params: { base_url } }))
export const icloudMailboxes = async (base_url: string) =>
  unwrap<{ mailboxes: any[] }>(await api.get('/api/v1/admin/grok-register/icloud/mailboxes', { params: { base_url } }))
export const icloudCreateMailboxes = async (body: { base_url: string; account_id?: string; count: number }) =>
  unwrap<{ created: number }>(await api.post('/api/v1/admin/grok-register/icloud/mailboxes/create', body))
export const icloudMailboxDelete = async (body: { base_url: string; id: string }) =>
  unwrap(await api.post('/api/v1/admin/grok-register/icloud/mailboxes/delete', body))
export const icloudSaveIMAPLogin = async (body: { base_url: string; account_id?: string; email: string; app_password: string }) =>
  unwrap(await api.post('/api/v1/admin/grok-register/icloud/imap-login/save', body))
export const icloudCheckIMAPLogin = async (body: { base_url: string; account_id?: string }) =>
  unwrap<any>(await api.post('/api/v1/admin/grok-register/icloud/imap-login/check', body))
export const icloudOpenApple = async () =>
  unwrap(await api.post('/api/v1/admin/grok-register/icloud/open-apple'))
export const icloudForwardTarget = async () =>
  unwrap<{ email: string }>(await api.get('/api/v1/admin/grok-register/icloud/forward-target'))
export const icloudSyncPool = async (base_url: string) =>
  unwrap<{ added: number; total: number }>(await api.post('/api/v1/admin/grok-register/icloud/sync-pool', { base_url }))
export const icloudPool = async () =>
  unwrap<{ entries: any[] }>(await api.get('/api/v1/admin/grok-register/icloud/pool'))
export const icloudPoolRelease = async (email: string) =>
  unwrap(await api.post('/api/v1/admin/grok-register/icloud/pool/release', { email }))
export const icloudPoolClear = async () =>
  unwrap(await api.post('/api/v1/admin/grok-register/icloud/pool/clear'))
export const icloudPanelStart = async (bin?: string) =>
  unwrap<{ url: string; status: any }>(await api.post('/api/v1/admin/grok-register/icloud/panel/start', { bin }))
export const icloudPanelStop = async () =>
  unwrap<{ status: any }>(await api.post('/api/v1/admin/grok-register/icloud/panel/stop'))
export const icloudPanelStatus = async () =>
  unwrap<any>(await api.get('/api/v1/admin/grok-register/icloud/panel/status'))
export const icloudAppleLoginStart = async (body: { base_url: string; apple_id: string; password: string; two_factor_method?: string }) =>
  unwrap<any>(await api.post('/api/v1/admin/grok-register/icloud/apple-login/start', body))
export const icloudAppleLogin2FA = async (body: { base_url: string; pending_id: string; code: string }) =>
  unwrap<any>(await api.post('/api/v1/admin/grok-register/icloud/apple-login/2fa', body))

/**
 * 代理出口设置：注册 / 打码 / 导入三条链路各自的出口。
 * 后端接口一直存在但前端从未调用过，导致 captcha_mode=global|selected
 * 依赖的打码出口池、以及入库出口池根本没法配。
 */
export interface ProxySettings {
  register_urls: string[]
  captcha_mode: 'direct' | 'registration' | 'global' | 'selected'
  captcha_urls: string[]
  import_mode: 'direct' | 'registration' | 'global' | 'selected'
  import_urls: string[]
}

export const getProxySettings = async () =>
  unwrap<ProxySettings>(await api.get('/api/v1/admin/grok-register/proxy-settings'))

export const saveProxySettings = async (body: Partial<ProxySettings>) =>
  unwrap<ProxySettings>(await api.put('/api/v1/admin/grok-register/proxy-settings', body))
export const fetchAccountUsage = async (id: string, useProxy = true) =>
  unwrap<any>(await api.get(`/api/v1/admin/grok-register/accounts/${id}/usage`, { params: { use_proxy: useProxy } }))
export const banAccount = async (id: string, reason = '手动封禁') =>
  unwrap(await api.post(`/api/v1/admin/grok-register/accounts/${id}/ban`, { reason }))
export const unbanAccount = async (id: string) =>
  unwrap(await api.post(`/api/v1/admin/grok-register/accounts/${id}/ban`, { unban: true }))

export const listAccounts = async () => unwrap<any[]>(await api.get('/api/v1/admin/grok-register/accounts'))
export const deleteAccounts = async (ids: string[]) => unwrap(await api.post('/api/v1/admin/grok-register/accounts/delete', { ids }))
export const clearAccounts = async () => unwrap(await api.post('/api/v1/admin/grok-register/accounts/clear'))
export const markImported = async (ids: string[], imported = true) =>
  unwrap(await api.post('/api/v1/admin/grok-register/accounts/mark-imported', { ids, imported }))
export const importOnce = async () => unwrap(await api.post('/api/v1/admin/grok-register/import'))

/** Open native Save-As dialog and write export file. User chooses path. */
export async function exportSaveAs(format: string, successOnly = true) {
  // 文件名由后端生成：带格式标识 + 账号数量（如 sub2api_10465账号_20260811.json）
  return unwrap<{ cancelled: boolean; path?: string; bytes?: number }>(
    await api.post('/api/v1/admin/grok-register/accounts/export-save', {
      format,
      success_only: successOnly
    })
  )
}

export async function copyExport(format: string) {
  const res = await api.get('/api/v1/admin/grok-register/accounts/export', {
    params: { format, success_only: true },
    responseType: 'text',
    transformResponse: [(d: any) => d]
  })
  const text = typeof res.data === 'string' ? res.data : String(res.data)
  await navigator.clipboard.writeText(text)
  return text
}

/**
 * 入库转换。importProxyMode 留空 → 后端按「代理设置 → 导入出口」决定，
 * 与代理池页面配置一致（之前这里硬编码 'registration'，用户配直连也照样走代理）。
 */
/** 入库转换的实时进度（异步入库时轮询）。 */
export interface ImportProgress {
  running: boolean
  total: number
  done: number
  success: number
  failed: number
  skipped: number
  current?: string
  stage: 'idle' | 'preparing' | 'converting' | 'saving' | 'done' | 'failed'
  message?: string
  logs?: string[]
  elapsed_ms: number
}

/** 异步入库：立刻返回，用 getImportProgress 轮询进度。 */
export const convertAccountsAsync = async (ids: string[], importProxyMode = '') =>
  unwrap<ImportProgress>(await api.post('/api/v1/admin/grok-register/accounts/convert-async',
    { ids, import_proxy_mode: importProxyMode }))

export const getImportProgress = async () =>
  unwrap<ImportProgress>(await api.get('/api/v1/admin/grok-register/accounts-import-progress'))

export const convertAccounts = async (ids: string[], importProxyMode = '') =>
  unwrap(await api.post('/api/v1/admin/grok-register/accounts/convert', { ids, import_proxy_mode: importProxyMode }, { timeout: 600_000 }))

// ---- 单账号操作 ----
const accBase = '/api/v1/admin/grok-register/accounts'

export interface AccountPatch {
  email?: string
  password?: string
  sso?: string
  sso_rw?: string
  proxy?: string
  base_url?: string
  note?: string
  status?: string
  imported?: boolean
}

export interface TestChatResult {
  reply: string
  model: string
  latency_ms: number
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
  proxy_used?: string
}

export const getAccount = async (id: string) => unwrap<any>(await api.get(`${accBase}/${id}`))

export const updateAccount = async (id: string, patch: AccountPatch) =>
  unwrap<any>(await api.post(`${accBase}/${id}/update`, patch))

export const listAccountModels = async (id: string, useProxy = false) =>
  unwrap<{ models: { id: string; display_name?: string }[]; fallback: boolean; error?: string }>(
    await api.get(`${accBase}/${id}/models`, { params: { use_proxy: useProxy }, timeout: 60_000 })
  )

export const verifyAccount = async (id: string, useProxy = false) =>
  unwrap<{ models: number }>(await api.post(`${accBase}/${id}/verify`, { use_proxy: useProxy }, { timeout: 60_000 }))

export const refreshAccountOAuth = async (id: string, useProxy = false) =>
  unwrap<any>(await api.post(`${accBase}/${id}/refresh-oauth`, { use_proxy: useProxy }, { timeout: 180_000 }))

export const testAccountChat = async (id: string, body: { model?: string; prompt?: string; use_proxy?: boolean }) =>
  unwrap<TestChatResult>(await api.post(`${accBase}/${id}/test`, body, { timeout: 180_000 }))

export interface TestStreamEvent {
  type: 'test_start' | 'content' | 'test_complete'
  text?: string
  success?: boolean
  error?: string
  model?: string
  latency_ms?: number
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
  proxy_used?: string
}

/**
 * 流式测试对话。axios 不便读流，这里用 fetch + ReadableStream 逐帧解析 SSE。
 * signal 用于「关闭弹窗即中断」。
 */
export async function testAccountChatStream(
  id: string,
  body: { model?: string; prompt?: string; use_proxy?: boolean },
  onEvent: (e: TestStreamEvent) => void,
  signal?: AbortSignal
): Promise<void> {
  const base = (api.defaults.baseURL || '').replace(/\/$/, '')
  const resp = await fetch(`${base}${accBase}/${id}/test-stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal
  })
  if (!resp.ok || !resp.body) {
    throw new Error(`HTTP ${resp.status}`)
  }
  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    const lines = buf.split('\n')
    buf = lines.pop() || ''
    for (const line of lines) {
      const trimmed = line.trim()
      if (!trimmed.startsWith('data:')) continue
      const json = trimmed.slice(5).trim()
      if (!json) continue
      try {
        onEvent(JSON.parse(json) as TestStreamEvent)
      } catch {
        // 忽略坏帧
      }
    }
  }
}
