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

// 官方 grok CLI 的 reasoning effort 值域（含中文说明）
export const EFFORT_OPTIONS = [
  { value: 'none', label: '无推理（最快）' },
  { value: 'minimal', label: '极简推理' },
  { value: 'low', label: '轻量推理（更快）' },
  { value: 'medium', label: '平衡推理' },
  { value: 'high', label: '深度推理' },
  { value: 'xhigh', label: '扩展推理' },
  { value: 'max', label: '最大推理' }
]

// 模型列表（与官方 grok CLI models 目录对齐）
export const MODEL_OPTIONS = [
  { value: 'grok-4.5', label: 'grok-4.5（旗舰）' },
  { value: 'grok-4.5-thinking', label: 'grok-4.5-thinking（深度思考）' },
  { value: 'grok-4', label: 'grok-4' },
  { value: 'grok-4-fast', label: 'grok-4-fast（最快）' },
  { value: 'grok-3.5', label: 'grok-3.5' },
  { value: 'grok-3', label: 'grok-3' }
]

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
