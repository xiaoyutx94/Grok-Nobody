<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import * as gw from '@/api/gateway'
import { toast } from '@/utils/clipboard'

const status = ref<gw.GatewayStatus | null>(null)
const cfg = ref<gw.GatewayConfig>({ enabled: true, port: 18789 })
const saving = ref(false)
const busy = ref(true)

async function load() {
  busy.value = true
  try {
    status.value = await gw.getStatus()
    cfg.value = await gw.getConfig()
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
const curlOpenAI = computed(() => `curl ${base.value}/v1/responses \\
  -H "Authorization: Bearer sk-xxx" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"grok-4.5","input":"ping"}'`)
const curlChat = computed(() => `curl ${base.value}/v1/chat/completions \\
  -H "Authorization: Bearer sk-xxx" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"grok-4.5","messages":[{"role":"user","content":"ping"}]}'`)
const curlClaude = computed(() => `curl ${base.value}/v1/messages \\
  -H "Authorization: Bearer sk-xxx" \\
  -H "Content-Type: application/json" \\
  -H "anthropic-version: 2023-06-01" \\
  -d '{"model":"grok-4.5","max_tokens":256,"messages":[{"role":"user","content":"ping"}]}'`)

function copy(text: string) {
  toast('已复制到剪贴板')
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
      <p class="dim">本地 Grok 反代：把账号池变成 OpenAI / Claude 双协议 API（仅本机 127.0.0.1 可访问）</p>
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
          <button class="icon-btn" title="复制" @click="copy(curlOpenAI)">⧉</button>
        </div>
        <pre class="mono">{{ curlOpenAI }}</pre>
      </div>
      <div class="curl-block">
        <div class="curl-title">
          <span class="pill">OpenAI · Chat Completions</span>
          <button class="icon-btn" title="复制" @click="copy(curlChat)">⧉</button>
        </div>
        <pre class="mono">{{ curlChat }}</pre>
      </div>
      <div class="curl-block">
        <div class="curl-title">
          <span class="pill">Claude · Messages</span>
          <button class="icon-btn" title="复制" @click="copy(curlClaude)">⧉</button>
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
      <li>客户端 base_url 填 <span class="mono">{{ base }}</span>，模型选 <span class="mono">grok-4.5</span>（默认），
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
.stat-row { display: flex; gap: 18px; margin-top: 10px; }
.stat-num { font-size: 22px; font-weight: 700; }
.stat-num.err { color: var(--danger, #d33); }
.curl-block { margin-top: 10px; }
.curl-title { display: flex; align-items: center; justify-content: space-between; margin-bottom: 4px; }
.curl-block pre { background: var(--bg-code, rgba(0,0,0,0.04)); padding: 10px; border-radius: 8px; font-size: 12px; overflow-x: auto; white-space: pre-wrap; word-break: break-all; }
.badge.ok { color: var(--success, #1a7f37); }
.note li { margin: 4px 0; }
</style>
