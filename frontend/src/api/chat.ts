import { api } from './client'

// ---------- 对话功能（复刻官方 grok CLI：模型/思考等级/feat 工具模式/流式） ----------

export interface ChatMessage {
  role: 'user' | 'assistant'
  content: string
}

export interface ChatRequest {
  model: string
  effort: string // none|minimal|low|medium|high|xhigh|max
  feat: boolean
  message: string
  history: ChatMessage[]
}

// 官方 grok CLI 的 reasoning effort 值域（含中文说明）——模型目录无 reasoning_efforts 时的兜底
export const EFFORT_OPTIONS = [
  { value: 'none', label: '无推理（最快）' },
  { value: 'minimal', label: '极简推理' },
  { value: 'low', label: '轻量推理（更快）' },
  { value: 'medium', label: '平衡推理' },
  { value: 'high', label: '深度推理' },
  { value: 'xhigh', label: '扩展推理' },
  { value: 'max', label: '最大推理' }
]

// 官方模型目录条目（网关 /v1/models 动态转发 cli-chat-proxy 权威数据）
export interface GatewayModel {
  id: string
  name?: string
  description?: string
  owned_by?: string
  context_window?: number
  supports_reasoning_effort?: boolean
  reasoning_efforts?: { id: string; label?: string; default?: boolean }[]
}

// 从网关动态获取官方模型列表（内部 key 认证；网关端口从配置读取）
export async function getModels(): Promise<GatewayModel[]> {
  const key = await getInternalKey()
  let port = 18789
  try {
    const cfg = await (await fetch('/api/v1/admin/gateway/config')).json()
    if (cfg?.port) port = cfg.port
  } catch { /* 默认端口 */ }
  const resp = await fetch(`http://127.0.0.1:${port}/v1/models`, {
    headers: { Authorization: `Bearer ${key}` }
  })
  if (!resp.ok) return []
  const d = await resp.json()
  return Array.isArray(d?.data) ? d.data : []
}

// 模型下拉选项（动态）：id + 官方 name
export function modelLabel(m: GatewayModel): string {
  return m.name && m.name !== m.id ? `${m.id}（${m.name}）` : m.id
}

// 流式对话：返回 { events, abort } 事件迭代器。
// 事件类型：meta / thinking_delta / text_delta / tool_call / done / error
export async function* chatStream(req: ChatRequest, signal?: AbortSignal): AsyncGenerator<any> {
  const resp = await fetch('/api/v1/admin/gateway/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
    signal
  })
  if (!resp.ok || !resp.body) {
    throw new Error(`对话请求失败: ${resp.status}`)
  }
  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buf += decoder.decode(value, { stream: true })
      // 按事件边界切分（event: X\ndata: {...}\n\n）
      let idx: number
      while ((idx = buf.indexOf('\n\n')) >= 0) {
        const chunk = buf.slice(0, idx)
        buf = buf.slice(idx + 2)
        let event = 'message'
        let data = ''
        for (const line of chunk.split('\n')) {
          if (line.startsWith('event: ')) event = line.slice(7).trim()
          else if (line.startsWith('data: ')) data = line.slice(6).trim()
        }
        if (!data) continue
        try {
          yield { event, data: JSON.parse(data) }
        } catch { /* 跳过坏事件 */ }
      }
    }
  } finally {
    reader.releaseLock()
  }
}

export const getInternalKey = async () => (await api.get('/api/v1/admin/gateway/internal-key')).data?.key as string
