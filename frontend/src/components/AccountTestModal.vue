<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import * as grok from '@/api/grok'
import { copyWithToast, toast } from '@/utils/clipboard'

const props = defineProps<{ account: any | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'tested'): void }>()

type Status = 'idle' | 'running' | 'success' | 'error'

const status = ref<Status>('idle')
const models = ref<{ id: string; display_name?: string }[]>([])
const modelsFallback = ref(false)
const loadingModels = ref(false)
const model = ref('grok-4.5')
const prompt = ref('')
const useProxy = ref(false)
const streamMode = ref(true)
const reply = ref('')
const errorMsg = ref('')
const lines = ref<{ text: string; tone: string }[]>([])
const meta = ref<{ latency?: number; total?: number; prompt?: number; completion?: number; proxy?: string }>({})
const termRef = ref<HTMLElement | null>(null)
let abort: AbortController | null = null

const hasOAuth = computed(() => Boolean(props.account?.access_token))
const hasProxy = computed(() => Boolean(props.account?.proxy))
const canRun = computed(() => hasOAuth.value && status.value !== 'running')

function addLine(text: string, tone = 'dim') {
  lines.value.push({ text, tone })
  scrollTerm()
}

async function scrollTerm() {
  await nextTick()
  if (termRef.value) termRef.value.scrollTop = termRef.value.scrollHeight
}

function reset() {
  status.value = 'idle'
  reply.value = ''
  errorMsg.value = ''
  lines.value = []
  meta.value = {}
}

watch(
  () => props.account?.id,
  async (id) => {
    stop()
    reset()
    prompt.value = ''
    if (!id) return
    useProxy.value = false
    await loadModels()
  },
  { immediate: true }
)

async function loadModels() {
  if (!props.account?.id || !hasOAuth.value) {
    // 无凭证时也给内置列表，UI 不至于空白
    models.value = [{ id: 'grok-4.5', display_name: 'Grok 4.5' }]
    model.value = 'grok-4.5'
    return
  }
  loadingModels.value = true
  try {
    const res = await grok.listAccountModels(props.account.id, useProxy.value)
    models.value = res?.models || []
    modelsFallback.value = Boolean(res?.fallback)
    if (models.value.length) {
      const preferred = models.value.find((m) => m.id === 'grok-4.5')
      model.value = preferred?.id || models.value[0].id
    }
  } catch (e: any) {
    modelsFallback.value = true
    models.value = [{ id: 'grok-4.5', display_name: 'Grok 4.5' }]
    model.value = 'grok-4.5'
  } finally {
    loadingModels.value = false
  }
}

function stop() {
  if (abort) {
    abort.abort()
    abort = null
  }
}

async function run() {
  if (!props.account?.id || !canRun.value) return
  reset()
  status.value = 'running'
  addLine(`▸ 账号 ${props.account.email || props.account.id}`, 'info')
  addLine(`▸ 模型 ${model.value}`, 'info')
  addLine(`▸ 出口 ${useProxy.value && hasProxy.value ? '账号注册代理' : '本机直连'}`, 'info')
  addLine('')

  const body = { model: model.value, prompt: prompt.value.trim(), use_proxy: useProxy.value }
  try {
    if (streamMode.value) {
      abort = new AbortController()
      await grok.testAccountChatStream(
        props.account.id,
        body,
        (ev) => {
          if (ev.type === 'content' && ev.text) {
            reply.value += ev.text
            scrollTerm()
          } else if (ev.type === 'test_complete') {
            meta.value = {
              latency: ev.latency_ms,
              total: ev.total_tokens,
              prompt: ev.prompt_tokens,
              completion: ev.completion_tokens,
              proxy: ev.proxy_used
            }
            if (ev.success) {
              status.value = 'success'
            } else {
              status.value = 'error'
              errorMsg.value = ev.error || '测试失败'
            }
          }
        },
        abort.signal
      )
      // 流结束但没收到 test_complete（连接被掐断）
      if (status.value === 'running') {
        status.value = reply.value ? 'success' : 'error'
        if (!reply.value) errorMsg.value = '连接中断，未收到回复'
      }
    } else {
      const res = await grok.testAccountChat(props.account.id, body)
      reply.value = res.reply
      meta.value = {
        latency: res.latency_ms,
        total: res.total_tokens,
        prompt: res.prompt_tokens,
        completion: res.completion_tokens,
        proxy: res.proxy_used
      }
      status.value = 'success'
    }
  } catch (e: any) {
    if (e?.name === 'AbortError') {
      status.value = 'idle'
      return
    }
    status.value = 'error'
    errorMsg.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    abort = null
    emit('tested')
  }
}

function close() {
  stop()
  emit('close')
}

async function copyReply() {
  await copyWithToast(reply.value, '回复')
}

async function verify() {
  if (!props.account?.id) return
  reset()
  status.value = 'running'
  addLine('▸ 凭证校验：拉取上游 /models', 'info')
  try {
    const res = await grok.verifyAccount(props.account.id, useProxy.value)
    status.value = 'success'
    addLine(`✓ 凭证可用，上游返回 ${res?.models ?? 0} 个模型`, 'ok')
    toast('凭证可用')
  } catch (e: any) {
    status.value = 'error'
    errorMsg.value = e?.response?.data?.error || e?.message || String(e)
  } finally {
    emit('tested')
  }
}
</script>

<template>
  <div v-if="account" class="overlay" @click.self="close">
    <div class="sheet sheet-lg">
      <div class="sheet-head">
        <div>
          <div class="kicker">测试对话</div>
          <h3 class="sheet-title mono">{{ account.email || account.id }}</h3>
        </div>
        <button class="btn btn-icon btn-ghost" data-tip="关闭" @click="close">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>
        </button>
      </div>

      <div v-if="!hasOAuth" class="banner is-warn">
        该账号还没有 OAuth 凭证（未入库转换），无法发起对话。先在列表里对它执行「入库转换」或「重取凭证」。
      </div>

      <div class="test-grid">
        <label class="field">
          模型
          <select class="select ctl" v-model="model" :disabled="loadingModels || status==='running'">
            <option v-for="m in models" :key="m.id" :value="m.id">{{ m.display_name || m.id }}</option>
          </select>
        </label>
        <div class="field">
          出口
          <div class="ctl row" style="gap:14px;min-height:38px">
            <label class="check" :class="{ 'is-off': !hasProxy }">
              <input type="checkbox" v-model="useProxy" :disabled="!hasProxy || status==='running'" @change="loadModels" />
              <span>走账号代理</span>
            </label>
            <label class="check">
              <input type="checkbox" v-model="streamMode" :disabled="status==='running'" />
              <span>流式</span>
            </label>
          </div>
        </div>
      </div>
      <p v-if="!hasProxy" class="note">该账号未记录注册代理，只能直连。</p>
      <p v-else-if="account.proxy" class="note mono" style="word-break:break-all">代理：{{ account.proxy }}</p>
      <p v-if="modelsFallback" class="note">模型列表取自内置清单（上游 /models 未取到）。</p>

      <label class="field" style="margin-top:12px">
        提示词（留空用默认）
        <textarea class="textarea ctl" style="min-height:72px" v-model="prompt"
          placeholder="用一句话自我介绍，并回复当前模型名称。" :disabled="status==='running'" />
      </label>

      <div class="term-wrap">
        <div ref="termRef" class="log term">
          <div v-if="status==='idle' && !lines.length" class="t-dim">▸ 就绪，点「发送测试」开始</div>
          <div v-for="(l, i) in lines" :key="i" :class="'t-' + l.tone">{{ l.text }}</div>
          <div v-if="reply" class="t-reply">{{ reply }}<span v-if="status==='running'" class="caret">▋</span></div>
          <div v-else-if="status==='running'" class="t-warn">▸ 连接上游中…</div>
          <div v-if="status==='success'" class="t-ok term-foot">✓ 测试完成</div>
          <div v-else-if="status==='error'" class="t-bad term-foot">✗ {{ errorMsg }}</div>
        </div>
        <button v-if="reply" class="btn btn-icon btn-ghost term-copy" data-tip="复制回复" @click="copyReply">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
        </button>
      </div>

      <div v-if="meta.latency !== undefined" class="metrics">
        <span class="pill">耗时 {{ meta.latency }} ms</span>
        <span v-if="meta.total" class="pill">tokens {{ meta.total }}</span>
        <span v-if="meta.prompt" class="pill">in {{ meta.prompt }}</span>
        <span v-if="meta.completion" class="pill">out {{ meta.completion }}</span>
        <span v-if="meta.proxy" class="pill mono">出口 {{ meta.proxy }}</span>
      </div>

      <div class="sheet-foot">
        <button class="btn btn-secondary" :disabled="!hasOAuth || status==='running'" @click="verify">仅校验凭证</button>
        <div class="spacer" />
        <button class="btn btn-ghost" @click="close">关闭</button>
        <button class="btn btn-primary" :disabled="!canRun" @click="run">
          {{ status==='running' ? '测试中…' : status==='idle' ? '发送测试' : '重新测试' }}
        </button>
      </div>
    </div>
  </div>
</template>
