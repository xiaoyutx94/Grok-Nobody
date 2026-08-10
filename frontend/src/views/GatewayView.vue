<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import * as gw from '@/api/gateway'
import { toast } from '@/utils/clipboard'

const status = ref<gw.GatewayStatus | null>(null)
const cfg = ref<gw.GatewayConfig>({ enabled: true, port: 18789 })
const addrs = ref<gw.GatewayAddresses | null>(null)
const saving = ref(false)
const busy = ref(true)
const copiedAddr = ref('')
let addrTimer: any = null

async function load() {
  busy.value = true
  try {
    status.value = await gw.getStatus()
    cfg.value = await gw.getConfig()
    addrs.value = await gw.getAddresses()
  } finally {
    busy.value = false
  }
}

async function save() {
  saving.value = true
  try {
    status.value = await gw.saveConfig(cfg.value)
    toast('网关配置已保存' + (cfg.value.enabled ? '，已应用' : '，网关已停止'))
  } catch (e: any) {
    toast('保存失败: ' + (e?.response?.data?.error || e?.message || e))
  } finally {
    saving.value = false
  }
}

onMounted(load)

const base = computed(() => `http://127.0.0.1:${cfg.value.port}`)
// 对外 base：优先局域网地址（内外网连接），回退本机
const publicBase = computed(() => {
  const lan = addrs.value?.lan?.[0]
  return lan ? `http://${lan}` : base.value
})
const curlOpenAI = computed(() => `curl ${publicBase.value}/v1/responses \\
  -H "Authorization: Bearer sk-xxx" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"grok-4.5","input":"ping"}'`)
const curlChat = computed(() => `curl ${publicBase.value}/v1/chat/completions \\
  -H "Authorization: Bearer sk-xxx" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"grok-4.5","messages":[{"role":"user","content":"ping"}]}'`)
const curlClaude = computed(() => `curl ${publicBase.value}/v1/messages \\
  -H "Authorization: Bearer sk-xxx" \\
  -H "Content-Type: application/json" \\
  -H "anthropic-version: 2023-06-01" \\
  -d '{"model":"grok-4.5","max_tokens":256,"messages":[{"role":"user","content":"ping"}]}'`)

function copy(text: string, kind = '') {
  // 不弹提示：视觉反馈靠按钮/图标变绿（✓ 2s）
  if (kind) {
    copiedAddr.value = kind
    if (addrTimer) clearTimeout(addrTimer)
    addrTimer = setTimeout(() => { copiedAddr.value = '' }, 2000)
  }
  navigator.clipboard?.writeText(text).catch(() => {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  })
}

function fmtUptime(sec?: number) {
  if (!sec) return '-'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  return h > 0 ? `${h}h${m}m` : m > 0 ? `${m}m${s}s` : `${s}s`
}
</script>

<template>
  <div class="page-head">
    <div>
      <h2>API 网关</h2>
      <p class="dim">本地 Grok 反代：把账号池变成 OpenAI / Claude 双协议 API（监听 0.0.0.0，内外网可访问）</p>
    </div>
    <div class="badges">
      <span class="badge" :class="status?.running ? 'ok' : ''">
        <span class="dot" :class="status?.running ? 'on' : ''" />{{ status?.running ? '运行中' : '已停止' }}
      </span>
      <span class="badge mono">{{ status?.addr || '未监听' }}</span>
    </div>
  </div>

  <div class="card" style="padding: 14px">
    <div class="form-row">
      <label class="switch-line">
        <input type="checkbox" v-model="cfg.enabled" />
        <span>启用网关（随应用启动监听）</span>
      </label>
      <label class="port-line">
        端口
        <input class="port-input mono" type="number" min="1024" max="65535" v-model.number="cfg.port" :disabled="!cfg.enabled" />
      </label>
      <button class="btn" :disabled="saving" @click="save">{{ saving ? '保存中…' : '保存并应用' }}</button>
    </div>
    <div class="addr-block">
      <div class="addr-head">
        <span class="pill" :class="addrs?.listen_host === '0.0.0.0' ? 'ok' : ''">
          {{ addrs?.listen_host === '0.0.0.0' ? '内外网可访问' : '仅本机可访问' }}
        </span>
        <span class="dim small">监听 {{ addrs?.listen_host || '0.0.0.0' }}:{{ cfg.port }} · 点击地址复制</span>
      </div>
      <div class="addr-list">
        <button v-for="(a, i) in addrs?.lan || []" :key="a" class="addr-chip mono"
          :class="{ copied: copiedAddr === 'lan' + i }" :title="'复制 ' + a" @click="copy('http://' + a, 'lan' + i)">
          <span>http://{{ a }}</span><span class="chip-mark">{{ copiedAddr === 'lan' + i ? '✓' : '⧉' }}</span>
        </button>
        <button v-if="addrs?.local" class="addr-chip mono"
          :class="{ copied: copiedAddr === 'local' }" :title="'复制 ' + addrs.local" @click="copy('http://' + addrs.local, 'local')">
          <span>http://{{ addrs.local }}（本机）</span><span class="chip-mark">{{ copiedAddr === 'local' ? '✓' : '⧉' }}</span>
        </button>
      </div>
      <div v-if="addrs?.listen_host !== '127.0.0.1'" class="dim small note">
        局域网内设备可直接用上面地址连接；外网访问需在路由器对该端口做端口转发。API Key 是唯一门禁，请妥善保管。
      </div>
    </div>
  </div>

  <div class="gw-grid">
    <div class="card" style="padding: 14px">
      <h3>运行统计</h3>
      <div class="stat-row">
        <div class="stat-box">
          <div class="stat-num mono">{{ status?.requests ?? 0 }}</div>
          <div class="dim">累计请求</div>
        </div>
        <div class="stat-box">
          <div class="stat-num mono err">{{ status?.failures ?? 0 }}</div>
          <div class="dim">失败</div>
        </div>
        <div class="stat-box">
          <div class="stat-num mono">{{ fmtUptime(status?.uptime_sec) }}</div>
          <div class="dim">运行时长</div>
        </div>
      </div>
    </div>

    <div class="card" style="padding: 14px">
      <h3>端到端使用（三协议一键复制）</h3>
      <div class="curl-block">
        <div class="curl-title">
          <span class="pill">OpenAI · Responses</span>
          <button class="icon-btn" :class="{ copied: copiedAddr === 'curl-oa' }" :title="copiedAddr === 'curl-oa' ? '已复制' : '复制'" @click="copy(curlOpenAI, 'curl-oa')">{{ copiedAddr === 'curl-oa' ? '✓' : '⧉' }}</button>
        </div>
        <pre class="mono">{{ curlOpenAI }}</pre>
      </div>
      <div class="curl-block">
        <div class="curl-title">
          <span class="pill">OpenAI · Chat Completions</span>
          <button class="icon-btn" :class="{ copied: copiedAddr === 'curl-cc' }" :title="copiedAddr === 'curl-cc' ? '已复制' : '复制'" @click="copy(curlChat, 'curl-cc')">{{ copiedAddr === 'curl-cc' ? '✓' : '⧉' }}</button>
        </div>
        <pre class="mono">{{ curlChat }}</pre>
      </div>
      <div class="curl-block">
        <div class="curl-title">
          <span class="pill">Claude · Messages</span>
          <button class="icon-btn" :class="{ copied: copiedAddr === 'curl-ms' }" :title="copiedAddr === 'curl-ms' ? '已复制' : '复制'" @click="copy(curlClaude, 'curl-ms')">{{ copiedAddr === 'curl-ms' ? '✓' : '⧉' }}</button>
        </div>
        <pre class="mono">{{ curlClaude }}</pre>
      </div>
    </div>
  </div>

  <div class="card" style="padding: 14px">
    <h3>使用说明</h3>
    <ol class="note">
      <li>在「分组管理」把账号移入分组（需 OAuth 凭证），必要时给分组绑定代理池代理作为出口。</li>
      <li>在「API 密钥」为分组生成 <span class="mono">sk-xxx</span> 密钥。</li>
      <li>客户端 base_url 填 <span class="mono">{{ publicBase }}</span>（局域网用上面的地址），模型选 <span class="mono">grok-4.5</span>（默认），
        即可以 OpenAI / Claude 协议调用账号池。</li>
      <li>转发使用 grok CLI 官方指纹（x-xai-token-auth + 动态版本 + UA），单账号并发 ≤ 2，429 冷却 30 分钟，403 自动封禁该账号。</li>
    </ol>
  </div>
</template>

<style scoped>
.gw-grid { display: grid; grid-template-columns: 1fr 1.4fr; gap: 12px; margin-bottom: 12px; }
@media (max-width: 900px) { .gw-grid { grid-template-columns: 1fr; } }
.form-row { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; }
.switch-line { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.port-line { display: flex; align-items: center; gap: 6px; }
.port-input { width: 90px; padding: 6px 8px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg); color: var(--text); }
.addr-block { margin-top: 12px; border-top: 1px solid var(--border); padding-top: 10px; }
.addr-head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.addr-list { display: flex; flex-wrap: wrap; gap: 8px; }
.addr-chip { display: inline-flex; align-items: center; gap: 8px; padding: 6px 10px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-code, rgba(0,0,0,0.04)); color: var(--text); cursor: pointer; font-size: 12.5px; }
.addr-chip:hover { border-color: var(--accent, #4f7cff); }
.addr-chip.copied, .icon-btn.copied { color: var(--success, #1a7f37); border-color: var(--success, #1a7f37); }
.chip-mark { font-weight: 700; }
.pill.ok { color: var(--success, #1a7f37); }
.note { margin-top: 8px; }
.stat-row { display: flex; gap: 18px; margin-top: 10px; }
.stat-num { font-size: 22px; font-weight: 700; }
.stat-num.err { color: var(--danger, #d33); }
.curl-block { margin-top: 10px; }
.curl-title { display: flex; align-items: center; justify-content: space-between; margin-bottom: 4px; }
.curl-block pre { background: var(--bg-code, rgba(0,0,0,0.04)); padding: 10px; border-radius: 8px; font-size: 12px; overflow-x: auto; white-space: pre-wrap; word-break: break-all; }
.badge.ok { color: var(--success, #1a7f37); }
.note li { margin: 4px 0; }
</style>
