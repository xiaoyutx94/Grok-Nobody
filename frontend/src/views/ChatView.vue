<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import * as chat from '@/api/chat'

// 消息模型
interface Msg {
  role: 'user' | 'assistant'
  content: string
  thinking: string
  tools: { name: string; status: string; detail?: string }[]
  error?: string
  account?: string
}

// 动态模型目录（官方权威）
const models = ref<chat.GatewayModel[]>([])
const model = ref('grok-4.5')
const effort = ref('high')
const feat = ref(false)
const fast = ref(false)
const input = ref('')
const msgs = ref<Msg[]>([])
const streaming = ref(false)
const thinkingOpen = ref<Record<number, boolean>>({})
const scrollBox = ref<HTMLElement | null>(null)
let aborter: AbortController | null = null

// 思考等级选项：优先当前模型的官方 reasoning_efforts，无则回落官方 CLI 值域
const effortOptions = computed(() => {
  const m = models.value.find((x) => x.id === model.value)
  const official = m?.reasoning_efforts
  if (official?.length) {
    return official.map((e) => ({
      value: e.id,
      label: e.label ? `${e.id} · ${e.label}` : e.id
    }))
  }
  return chat.EFFORT_OPTIONS
})

// 默认 effort：模型目录标记 default 的档位，无则 high
function defaultEffortFor(mid: string): string {
  const m = models.value.find((x) => x.id === mid)
  const def = m?.reasoning_efforts?.find((e) => e.default)
  return def?.id || effort.value
}

async function loadModels() {
  try {
    const list = await chat.getModels()
    if (!list.length) return
    models.value = list
    if (!list.some((m) => m.id === model.value)) {
      model.value = list[0].id
    }
    effort.value = defaultEffortFor(model.value)
  } catch { /* 网关不可用时保持默认 */ }
}

function onModelChange() {
  effort.value = defaultEffortFor(model.value)
}

const EXAMPLES = [
  '帮我写一个 Python 快速排序，带注释',
  '解释一下 TCP 三次握手，用生活例子',
  '搜一下今天东京天气（feat 模式）',
  '用中文写一首关于秋天的短诗'
]

onMounted(loadModels)

async function send() {
  const text = input.value.trim()
  if (!text || streaming.value) return
  msgs.value.push({ role: 'user', content: text, thinking: '', tools: [] })
  const cur: Msg = { role: 'assistant', content: '', thinking: '', tools: [] }
  msgs.value.push(cur)
  const idx = msgs.value.length - 1
  input.value = ''
  streaming.value = true
  thinkingOpen.value[idx] = true
  aborter = new AbortController()

  // fast 模式：强制极简推理（快速响应）
  const eff = fast.value ? 'minimal' : effort.value

  const history = msgs.value
    .slice(0, -1)
    .filter((m) => m.content)
    .map((m) => ({ role: m.role, content: m.content }))

  try {
    for await (const ev of chat.chatStream(
      { model: model.value, effort: eff, feat: feat.value, message: text, history },
      aborter.signal
    )) {
      const d = ev.data
      switch (ev.event) {
        case 'meta':
          cur.account = d.account
          break
        case 'thinking_delta':
          cur.thinking += d.text
          break
        case 'text_delta':
          cur.content += d.text
          await scroll()
          break
        case 'tool_call':
          if (d.status === 'start') cur.tools.push({ name: d.name, status: 'start' })
          else {
            const t = cur.tools.find((x) => x.name === d.name)
            if (t) { t.status = 'done'; t.detail = d.detail }
          }
          await scroll()
          break
        case 'error':
          cur.error = d.message
          break
        case 'done':
          if (!cur.content && !cur.thinking && !cur.tools.length && !cur.error) {
            cur.content = '（无输出）'
          }
          break
      }
      await scroll()
    }
  } catch (e: any) {
    if (e?.name !== 'AbortError') cur.error = String(e?.message || e)
  } finally {
    streaming.value = false
    aborter = null
    await scroll()
  }
}

function stop() {
  aborter?.abort()
}

async function scroll() {
  await nextTick()
  scrollBox.value?.scrollTo({ top: scrollBox.value.scrollHeight })
}

function useExample(ex: string) {
  input.value = ex
}

// 输入框自动增高（最多 200px）
function autoGrow(e: Event) {
  const el = e.target as HTMLTextAreaElement
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 200) + 'px'
}
</script>

<template>
  <div class="chat-layout">
    <!-- 消息区 -->
    <div class="chat-scroll" ref="scrollBox">
      <div v-if="!msgs.length" class="empty">
        <div class="empty-title">对话</div>
        <div class="note">多轮上下文 · 流式输出 · 思考折叠 · feat 工具调用 · 全账号池免分组</div>
        <div class="examples">
          <button v-for="ex in EXAMPLES" :key="ex" class="chip" @click="useExample(ex)">{{ ex }}</button>
        </div>
      </div>

      <div v-for="(m, i) in msgs" :key="i" class="msg" :class="'is-' + m.role">
        <!-- 工具调用 -->
        <div v-if="m.tools.length" class="tools">
          <div v-for="(t, ti) in m.tools" :key="ti" class="tool" :class="'is-' + t.status">
            <span class="tool-ico">{{ t.name.includes('x_search') ? '✕' : '🔍' }}</span>
            <span class="tool-name">{{ t.name }}</span>
            <span class="tool-status">{{ t.status === 'start' ? '搜索中…' : '完成' }}</span>
            <span v-if="t.detail && t.detail !== 'completed'" class="tool-detail">{{ t.detail }}</span>
          </div>
        </div>

        <!-- 思考（折叠） -->
        <div v-if="m.thinking" class="thinking">
          <button class="think-head" @click="thinkingOpen[i] = !thinkingOpen[i]">
            <span>{{ thinkingOpen[i] ? '▾' : '▸' }}</span>
            <span>思考过程 · {{ m.thinking.length }} 字</span>
          </button>
          <div v-if="thinkingOpen[i]" class="think-body">{{ m.thinking }}</div>
        </div>

        <!-- 正文 -->
        <div v-if="m.content" class="content">{{ m.content }}</div>
        <div v-if="m.error" class="error">{{ m.error }}</div>
        <div v-if="i === msgs.length - 1 && streaming" class="cursor">▋</div>
      </div>
    </div>

    <!-- 输入区（IDE 风格：配置全部内嵌） -->
    <div class="composer" :class="{ 'is-focus': input }">
      <div class="toolbar">
        <span class="mode-label">模型</span>
        <select class="tb-select" v-model="model" title="模型" @change="onModelChange">
          <option v-for="m in models" :key="m.id" :value="m.id">{{ chat.modelLabel(m) }}</option>
          <option v-if="!models.length" value="grok-4.5">grok-4.5（加载中…）</option>
        </select>

        <span class="tb-sep" />

        <span class="mode-label">思考</span>
        <select class="tb-select" v-model="effort" title="思考等级" :disabled="fast">
          <option v-for="e in effortOptions" :key="e.value" :value="e.value">{{ e.label }}</option>
        </select>

        <span class="tb-sep" />

        <button class="tb-chip" :class="{ on: feat }" @click="feat = !feat" title="任务模式：启用 web 搜索等工具调用">feat</button>
        <button class="tb-chip" :class="{ on: fast }" @click="fast = !fast" title="快速模式：极简推理，更快响应">fast</button>
      </div>

      <div class="input-row">
        <textarea v-model="input" rows="1" class="input" placeholder="输入消息…（Enter 发送 / Shift+Enter 换行）"
          :disabled="streaming" @keydown.enter.exact.prevent="send"
          @input="autoGrow" />
        <button v-if="streaming" class="send-btn stop" title="停止" @click="stop">■</button>
        <button v-else class="send-btn" :disabled="!input.trim()" title="发送" @click="send">↑</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.chat-layout { display: flex; flex-direction: column; height: calc(100vh - 120px); gap: 10px; }

/* 消息区 */
.chat-scroll { flex: 1; overflow-y: auto; padding: 4px 2px; display: flex; flex-direction: column; gap: 16px; }
.empty { margin: auto; text-align: center; color: var(--muted); max-width: 560px; }
.examples { display: flex; flex-wrap: wrap; gap: 8px; justify-content: center; margin-top: 14px; }

.msg { display: flex; flex-direction: column; gap: 6px; max-width: 92%; }
.msg.is-user { align-self: flex-end; }
.msg.is-assistant { align-self: flex-start; width: 100%; }
.content { white-space: pre-wrap; word-break: break-word; line-height: 1.7; font-size: 13.5px; }
.is-user .content {
  background: var(--accent, #4f7cff); color: #fff; padding: 9px 14px;
  border-radius: 14px 14px 3px 14px; max-width: 78%;
}
.is-assistant .content { padding: 2px 4px; }
.error { color: var(--danger, #d33); font-size: 12.5px; }
.cursor { animation: blink 1s step-end infinite; color: var(--accent, #4f7cff); font-size: 15px; }
@keyframes blink { 50% { opacity: 0; } }

.thinking { border-left: 2px solid color-mix(in srgb, var(--warn, #b8860b) 55%, transparent); padding-left: 10px; margin: 2px 0; }
.think-head { display: flex; align-items: center; gap: 6px; background: none; border: none; cursor: pointer; color: var(--warn, #b8860b); font-size: 12px; font-weight: 600; padding: 2px 0; }
.think-body { white-space: pre-wrap; word-break: break-word; color: var(--muted); font-size: 12px; line-height: 1.65; margin-top: 4px; max-height: 240px; overflow-y: auto; }

.tools { display: flex; flex-direction: column; gap: 5px; }
.tool { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; padding: 4px 10px; border-radius: 8px; background: var(--bg-code, rgba(0,0,0,0.05)); align-self: flex-start; }
.tool-name { font-weight: 600; font-family: ui-monospace, monospace; }
.tool-status { color: var(--muted); font-size: 11px; }
.tool-detail { color: var(--muted); font-size: 11px; max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 输入区（IDE 风格） */
.composer {
  border: 1px solid var(--border); border-radius: 12px; background: var(--bg-card, #fff);
  box-shadow: 0 2px 12px rgba(0,0,0,0.06); overflow: hidden;
}
.composer.is-focus { border-color: var(--accent, #4f7cff); }
.toolbar {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  padding: 7px 12px; border-bottom: 1px solid var(--border);
  background: color-mix(in srgb, var(--bg-code, #f5f5f5) 40%, transparent);
}
.mode-label { font-size: 11px; opacity: 0.6; }
.tb-select {
  border: 1px solid var(--border); border-radius: 7px; background: var(--bg-card, #fff);
  color: var(--text); font-size: 12px; padding: 3px 6px; max-width: 190px; cursor: pointer;
}
.tb-sep { width: 1px; height: 16px; background: var(--border); }
.tb-chip {
  border: 1px solid var(--border); border-radius: 7px; background: var(--bg-card, #fff);
  color: var(--text); font-size: 11.5px; font-weight: 600; padding: 3px 10px; cursor: pointer;
  font-family: ui-monospace, monospace;
}
.tb-chip:hover { border-color: var(--accent, #4f7cff); }
.tb-chip.on { border-color: var(--accent, #4f7cff); color: var(--accent, #4f7cff); background: color-mix(in srgb, var(--accent, #4f7cff) 10%, transparent); }
.tb-chip:disabled { opacity: 0.4; cursor: not-allowed; }

.input-row { display: flex; align-items: flex-end; gap: 8px; padding: 8px 10px; }
.input-row textarea {
  flex: 1; border: none; outline: none; resize: none; background: transparent;
  font-size: 13.5px; line-height: 1.6; max-height: 200px; padding: 4px 2px;
}
.send-btn {
  width: 30px; height: 30px; border-radius: 50%; border: none; flex: 0 0 auto;
  background: var(--accent, #4f7cff); color: #fff; font-size: 15px; font-weight: 700;
  cursor: pointer; display: flex; align-items: center; justify-content: center;
}
.send-btn:disabled { opacity: 0.4; cursor: not-allowed; }
.send-btn.stop { background: var(--danger, #d33); font-size: 11px; }
</style>
