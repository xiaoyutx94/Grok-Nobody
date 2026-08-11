<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import * as chat from '@/api/chat'

// 消息模型
interface Msg {
  role: 'user' | 'assistant'
  content: string
  thinking: string
  tools: { name: string; status: string; detail?: string }[]
  toolResults: { name: string; output: string }[]
  images: string[]
  error?: string
  account?: string
}

// 动态模型目录（官方权威）
const models = ref<chat.GatewayModel[]>([])
const model = ref('grok-4.5')
const effort = ref('high')
const feat = ref(false)
const input = ref('')
const images = ref<string[]>([])
const msgs = ref<Msg[]>([])
const streaming = ref(false)
const thinkingOpen = ref<Record<number, boolean>>({})
const scrollBox = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLTextAreaElement | null>(null)
let aborter: AbortController | null = null

// 思考等级：同步官方（reasoning_efforts id 原样），无则回落官方 CLI 值域（id 原样）
const effortOptions = computed(() => {
  const m = models.value.find((x) => x.id === model.value)
  const official = m?.reasoning_efforts
  if (official?.length) return official.map((e) => ({ value: e.id, label: e.id }))
  return chat.EFFORT_OPTIONS
})

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
    if (!list.some((m) => m.id === model.value)) model.value = list[0].id
    effort.value = defaultEffortFor(model.value)
  } catch { /* 保持默认 */ }
}

function onModelChange() {
  effort.value = defaultEffortFor(model.value)
}

const EXAMPLES = [
  '帮我写一个 Python 快速排序，带注释',
  '解释一下 TCP 三次握手，用生活例子',
  '抓取 https://example.com 的标题（feat 模式）',
  '用中文写一首关于秋天的短诗'
]

onMounted(loadModels)

// ---------- markdown 渲染（上下文格式化；先转义防 XSS） ----------
function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

function renderMd(text: string): string {
  const esc = escapeHtml(text)
  const blocks: string[] = []
  const withCode = esc.replace(/```(\w*)\n([\s\S]*?)```/g, (_m, _lang, code) => {
    blocks.push(code)
    return `\u0000CODE${blocks.length - 1}\u0000`
  })
  let html = ''
  let inList = false
  const closeList = () => { if (inList) { html += '</ul>'; inList = false } }
  for (const line of withCode.split('\n')) {
    const h = line.match(/^(#{1,3})\s+(.*)$/)
    if (h) { closeList(); html += `<h${h[1].length}>${h[2]}</h${h[1].length}>`; continue }
    const li = line.match(/^\s*[-*]\s+(.*)$/)
    if (li) {
      if (!inList) { html += '<ul>'; inList = true }
      html += `<li>${li[1]}</li>`
      continue
    }
    closeList()
    if (!line.trim()) { html += '<br/>'; continue }
    let l = line
    l = l.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    l = l.replace(/`([^`]+)`/g, '<code>$1</code>')
    html += `<p>${l}</p>`
  }
  closeList()
  return html.replace(/\u0000CODE(\d+)\u0000/g, (_m, i) => `<pre><code>${blocks[+i]}</code></pre>`)
}

// ---------- 粘贴图片 ----------
function onPaste(e: ClipboardEvent) {
  const items = e.clipboardData?.items || []
  for (const it of items) {
    if (it.type.startsWith('image/')) {
      const f = it.getAsFile()
      if (!f) continue
      if (f.size > 8 * 1024 * 1024) continue
      const reader = new FileReader()
      reader.onload = () => {
        images.value.push(String(reader.result))
        input.value = input.value.replace(/[\r\n]+$/, '')
      }
      reader.readAsDataURL(f)
      e.preventDefault()
      break
    }
  }
}

// ---------- 发送 ----------
async function send() {
  const text = input.value.trim()
  if ((!text && !images.value.length) || streaming.value) return
  msgs.value.push({ role: 'user', content: text, thinking: '', tools: [], toolResults: [], images: [...images.value] })
  const cur: Msg = { role: 'assistant', content: '', thinking: '', tools: [], toolResults: [], images: [] }
  msgs.value.push(cur)
  const idx = msgs.value.length - 1
  input.value = ''
  images.value = []
  streaming.value = true
  thinkingOpen.value[idx] = true
  aborter = new AbortController()

  const history = msgs.value
    .slice(0, -1)
    .filter((m) => m.content)
    .map((m) => ({ role: m.role, content: m.content }))

  try {
    for await (const ev of chat.chatStream(
      {
        model: model.value,
        effort: effort.value,
        feat: feat.value,
        message: text,
        history,
        images: cur.images.length ? cur.images : undefined
      } as any,
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
          break
        case 'tool_call':
          if (d.status === 'start') cur.tools.push({ name: d.name, status: 'start' })
          else {
            const t = cur.tools.find((x) => x.name === d.name)
            if (t) { t.status = 'done'; t.detail = d.detail }
          }
          break
        case 'tool_result':
          cur.toolResults.push({ name: d.name, output: d.output })
          break
        case 'error':
          cur.error = d.message
          break
        case 'done':
          if (!cur.content && !cur.thinking && !cur.tools.length && !cur.error) cur.content = '（无输出）'
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

// ---------- 消息操作：复制 / 编辑重发 ----------
const copyDone = ref<Record<number, boolean>>({})
async function copyMsg(i: number) {
  const m = msgs.value[i]
  if (!m) return
  try {
    await navigator.clipboard.writeText(m.content || m.thinking || '')
    copyDone.value[i] = true
    setTimeout(() => (copyDone.value[i] = false), 2000)
  } catch {
    /* 剪贴板不可用时静默 */
  }
}

// 编辑重发：回填输入框 + 截断该条及之后（重新生成）
const editIdx = ref(-1)
function editMsg(i: number) {
  const m = msgs.value[i]
  if (!m || streaming.value) return
  editIdx.value = i
  input.value = m.content
  images.value = [...m.images]
  msgs.value = msgs.value.slice(0, i)
  thinkingOpen.value = {}
  scrollBox.value?.scrollTo({ top: scrollBox.value.scrollHeight })
  nextTick(() => {
    inputEl.value?.focus()
    autoGrow({ target: inputEl.value } as any)
  })
}
function cancelEdit() {
  editIdx.value = -1
  input.value = ''
  images.value = []
}

async function scroll() {
  await nextTick()
  scrollBox.value?.scrollTo({ top: scrollBox.value.scrollHeight })
}

function useExample(ex: string) {
  input.value = ex
}

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
        <div class="note">多轮上下文 · 流式输出 · 思考折叠 · feat 工具调用 · 粘贴图片 · 全账号池免分组</div>
        <div class="examples">
          <button v-for="ex in EXAMPLES" :key="ex" class="chip" @click="useExample(ex)">{{ ex }}</button>
        </div>
      </div>

      <div v-for="(m, i) in msgs" :key="i" class="msg" :class="'is-' + m.role">
        <!-- 操作条：复制 / 编辑重发（hover 显示） -->
        <div class="msg-ops">
          <button class="op-btn" :class="{ 'is-done': copyDone[i] }" title="复制" @click="copyMsg(i)">
            <span v-if="copyDone[i]" class="op-ok">✓</span>
            <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>
          </button>
          <button v-if="m.role === 'user'" class="op-btn" title="编辑后重发" @click="editMsg(i)">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
          </button>
        </div>
        <!-- 用户附件图片 -->
        <div v-if="m.images.length" class="attach">
          <img v-for="(img, ii) in m.images" :key="ii" :src="img" class="attach-img" alt="attachment" />
        </div>

        <!-- 工具调用 -->
        <div v-if="m.tools.length" class="tools">
          <div v-for="(t, ti) in m.tools" :key="ti" class="tool" :class="'is-' + t.status">
            <span class="tool-ico">{{ t.name.includes('x_search') ? '✕' : t.name === 'web_fetch' ? '⛏' : '🔍' }}</span>
            <span class="tool-name">{{ t.name }}</span>
            <span class="tool-status">{{ t.status === 'start' ? '执行中…' : '完成' }}</span>
            <span v-if="t.detail && t.detail !== 'completed'" class="tool-detail">{{ t.detail }}</span>
          </div>
          <div v-for="(tr, tri) in m.toolResults" :key="'r' + tri" class="tool-result">
            <div class="tr-head">↳ {{ tr.name }} 结果</div>
            <pre class="tr-body">{{ tr.output }}</pre>
          </div>
        </div>

        <!-- 思考（折叠；streaming 且开启思考时始终显示占位，避免等待期无反馈） -->
        <div v-if="m.thinking || (i === msgs.length - 1 && streaming && effort !== 'none')" class="thinking" :class="{ 'is-live': !m.thinking }">
          <button class="think-head" @click="thinkingOpen[i] = !thinkingOpen[i]">
            <span>{{ thinkingOpen[i] ? '▾' : '▸' }}</span>
            <span>{{ m.thinking ? `思考过程 · ${m.thinking.length} 字` : '思考中…' }}</span>
            <span v-if="!m.thinking" class="think-dots"><i></i><i></i><i></i></span>
          </button>
          <div v-if="thinkingOpen[i] && m.thinking" class="think-body">{{ m.thinking }}</div>
        </div>

        <!-- 正文：assistant 用 markdown 格式化；用户消息原样显示（不段落化，单行就是单行） -->
        <div v-if="m.role === 'assistant' && m.content" class="content" v-html="renderMd(m.content)"></div>
        <div v-else-if="m.role === 'user' && m.content" class="content user-content">{{ m.content }}</div>
        <div v-if="m.error" class="error">{{ m.error }}</div>
        <div v-if="i === msgs.length - 1 && streaming" class="cursor">▋</div>
      </div>
    </div>

    <!-- 输入区（IDE 风格：配置全部内嵌） -->
    <div class="composer" :class="{ 'is-focus': input || images.length }">
      <div class="toolbar">
        <span class="mode-label">模型</span>
        <select class="tb-select" v-model="model" title="模型" @change="onModelChange">
          <option v-for="m in models" :key="m.id" :value="m.id">{{ chat.modelLabel(m) }}</option>
          <option v-if="!models.length" value="grok-4.5">grok-4.5</option>
        </select>

        <span class="tb-sep" />

        <span class="mode-label">思考</span>
        <select class="tb-select" v-model="effort" title="思考等级">
          <option v-for="e in effortOptions" :key="e.value" :value="e.value">{{ e.label }}</option>
        </select>

        <span class="tb-sep" />

        <button class="tb-chip" :class="{ on: feat }" @click="feat = !feat" title="任务模式：web 搜索 / 网页抓取工具">feat</button>
      </div>

      <!-- 图片附件预览 -->
      <div v-if="images.length" class="attach-preview">
        <div v-for="(img, i) in images" :key="i" class="attach-item">
          <img :src="img" alt="paste" />
          <button class="attach-x" @click="images.splice(i, 1)">✕</button>
        </div>
      </div>

      <div class="input-row">
        <textarea ref="inputEl" v-model="input" rows="1" class="input" :placeholder="editIdx >= 0 ? '修改后 Enter 重发…（Esc 取消）' : '输入消息…（Enter 发送 / Shift+Enter 换行 / 可直接粘贴图片）'"
          :disabled="streaming" @keydown.enter.exact.prevent="send" @keydown.esc="cancelEdit" @paste="onPaste" @input="autoGrow" />
        <button v-if="streaming" class="send-btn stop" title="停止" @click="stop">■</button>
        <button v-else class="send-btn" :disabled="!input.trim() && !images.length" title="发送" @click="send">↑</button>
      </div>
      <div v-if="editIdx >= 0" class="edit-tag">
        正在编辑上一条消息
        <button class="edit-x" @click="cancelEdit">✕ 取消</button>
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

.msg { display: flex; flex-direction: column; gap: 6px; max-width: 92%; position: relative; }
.msg.is-user { align-self: flex-end; }
.msg.is-assistant { align-self: flex-start; width: 100%; }
/* 消息操作条（hover 显示） */
.msg-ops { position: absolute; top: -6px; right: 2px; display: flex; gap: 2px; opacity: 0; transition: opacity 0.15s; z-index: 3; }
.msg:hover .msg-ops { opacity: 1; }
.op-btn { width: 24px; height: 24px; border-radius: 7px; border: 1px solid var(--line); background: var(--panel); color: var(--muted); display: inline-flex; align-items: center; justify-content: center; cursor: pointer; }
.op-btn:hover { color: var(--ink); border-color: var(--ink-3); }
.op-btn.is-done { color: var(--ok, #16a34a); border-color: var(--ok, #16a34a); }
.op-btn svg { width: 13px; height: 13px; }
.op-ok { font-size: 13px; font-weight: 800; }
/* 用户消息：原样显示（单行就是单行，不 markdown 段落化） */
.user-content { background: color-mix(in srgb, var(--accent, #c2410c) 8%, transparent); border: 1px solid color-mix(in srgb, var(--accent, #c2410c) 22%, transparent); border-radius: 12px; padding: 8px 12px; }
/* 思考占位动画 */
.thinking.is-live .think-dots { display: inline-flex; gap: 3px; margin-left: 6px; }
.think-dots i { width: 4px; height: 4px; border-radius: 50%; background: currentColor; animation: td 1.2s infinite; }
.think-dots i:nth-child(2) { animation-delay: 0.2s; }
.think-dots i:nth-child(3) { animation-delay: 0.4s; }
@keyframes td { 0%, 60%, 100% { opacity: 0.25; } 30% { opacity: 1; } }
/* 编辑态标签 */
.edit-tag { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; color: var(--accent, #c2410c); margin-top: 6px; }
.edit-x { border: none; background: transparent; color: inherit; cursor: pointer; font-size: 11px; text-decoration: underline; }
.content { white-space: pre-wrap; word-break: break-word; line-height: 1.7; font-size: 13.5px; }
.content :deep(h1), .content :deep(h2), .content :deep(h3) { margin: 10px 0 4px; font-size: 15px; line-height: 1.4; }
.content :deep(p) { margin: 4px 0; }
.content :deep(ul) { margin: 4px 0; padding-left: 20px; }
.content :deep(code) { background: rgba(0,0,0,0.08); padding: 1px 5px; border-radius: 4px; font-size: 12px; font-family: ui-monospace, monospace; }
.content :deep(pre) { background: rgba(0,0,0,0.08); padding: 10px 12px; border-radius: 8px; overflow-x: auto; margin: 6px 0; }
.content :deep(pre code) { background: none; padding: 0; }
.is-user .content {
  background: var(--accent, #4f7cff); color: #fff; padding: 9px 14px;
  border-radius: 14px 14px 3px 14px; max-width: 78%;
}
.is-user .content :deep(code), .is-user .content :deep(pre) { background: rgba(255,255,255,0.18); }
.error { color: var(--danger, #d33); font-size: 12.5px; }
.cursor { animation: blink 1s step-end infinite; color: var(--accent, #4f7cff); font-size: 15px; }
@keyframes blink { 50% { opacity: 0; } }

.attach { display: flex; gap: 8px; flex-wrap: wrap; }
.attach-img { max-width: 220px; max-height: 160px; border-radius: 10px; border: 1px solid var(--border); }

.thinking { border-left: 2px solid color-mix(in srgb, var(--warn, #b8860b) 55%, transparent); padding-left: 10px; margin: 2px 0; }
.think-head { display: flex; align-items: center; gap: 6px; background: none; border: none; cursor: pointer; color: var(--warn, #b8860b); font-size: 12px; font-weight: 600; padding: 2px 0; }
.think-body { white-space: pre-wrap; word-break: break-word; color: var(--muted); font-size: 12px; line-height: 1.65; margin-top: 4px; max-height: 240px; overflow-y: auto; }

.tools { display: flex; flex-direction: column; gap: 5px; }
.tool { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; padding: 4px 10px; border-radius: 8px; background: var(--bg-code, rgba(0,0,0,0.05)); align-self: flex-start; }
.tool-name { font-weight: 600; font-family: ui-monospace, monospace; }
.tool-status { color: var(--muted); font-size: 11px; }
.tool-detail { color: var(--muted); font-size: 11px; max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tool-result { align-self: flex-start; max-width: 90%; background: var(--bg-code, rgba(0,0,0,0.05)); border-radius: 8px; padding: 6px 10px; }
.tr-head { font-size: 11px; font-weight: 600; opacity: 0.7; }
.tr-body { font-size: 11px; white-space: pre-wrap; word-break: break-word; max-height: 140px; overflow-y: auto; margin-top: 4px; font-family: ui-monospace, monospace; }

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

.attach-preview { display: flex; gap: 8px; padding: 6px 12px 0; flex-wrap: wrap; }
.attach-item { position: relative; }
.attach-item img { max-width: 96px; max-height: 72px; border-radius: 8px; border: 1px solid var(--border); }
.attach-x { position: absolute; top: -6px; right: -6px; width: 18px; height: 18px; border-radius: 50%; border: none; background: var(--danger, #d33); color: #fff; font-size: 10px; cursor: pointer; }

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
