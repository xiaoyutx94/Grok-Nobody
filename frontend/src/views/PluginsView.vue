<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import * as plugins from '@/api/plugins'

const catalog = ref<any[]>([])
const status = ref<any[]>([])
const busy = ref('')
const note = ref('')
let timer: any

async function reload() {
  const data = await plugins.listPlugins()
  catalog.value = data.catalog || []
  status.value = data.status || []
}
function st(id: string) {
  return status.value.find((s) => s.id === id) || {}
}
const sysInfo = computed(() => {
  const s = status.value.find((x: any) => x.os) || {}
  return s.os || s.arch ? `${s.os || '?'}/${s.arch || '?'}` : ''
})

async function installLocal(id: string) {
  busy.value = id + 'local'
  note.value = ''
  try {
    const res = await plugins.installPlugin({ id, mode: 'local' })
    note.value = (res?.message || '已提交启动') + (res?.healthy ? ' · 健康 OK' : ' · 若未就绪请看下方消息')
    await reload()
  } catch (e: any) {
    note.value = e?.response?.data?.error || e.message
    await reload()
  } finally {
    busy.value = ''
  }
}

// 切到 Docker 模式：后端只判断与切换（不重新部署）。
// 已部署 → 秒级切换；未部署 → 后端返回明确提示，指路 Docker 管理一键部署。
async function installDocker(id: string) {
  busy.value = id + 'docker'
  note.value = ''
  try {
    const res = await plugins.installPlugin({ id, mode: 'docker' })
    note.value = (res?.message || '已切到 Docker 模式') + (res?.healthy ? ' · 健康 OK' : ' · 若未就绪请看下方消息')
    await reload()
  } catch (e: any) {
    note.value = e?.response?.data?.error || e.message
    await reload()
  } finally {
    busy.value = ''
  }
}

async function stop(id: string) {
  busy.value = 'stop' + id
  try { await plugins.stopPlugin(id); await reload() }
  finally { busy.value = '' }
}
onMounted(async () => {
  await reload()
  timer = setInterval(reload, 4000)
})
onUnmounted(() => {
  clearInterval(timer)
})
</script>

<template>
  <div class="vhead">
    <div class="vhead-title">
      <div class="kicker">Plugins</div>
      <h2>插件中心</h2>
    </div>
    <div class="stat-chips">
      <span class="schip is-ok">
        <span class="k">健康</span><b>{{ catalog.filter(p => st(p.id).healthy).length }}/{{ catalog.length }}</b>
      </span>
      <span class="schip" :class="status.some(s => s.docker_ok) ? 'is-ok' : ''">
        <span class="k">Docker</span><b>{{ status.some(s => s.docker_ok) ? '可用' : '未就绪' }}</b>
      </span>
      <span class="schip">
        <span class="k">本机</span><b>{{ sysInfo || '—' }}</b>
      </span>
    </div>
    <div class="spacer" />
    <div class="vhead-actions">
      <button class="btn btn-secondary btn-sm" :disabled="!!busy" @click="reload">刷新状态</button>
    </div>
  </div>

  <p v-if="note" class="vbanner info">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v.01M12 11v5"/></svg>
    <span>{{ note }}</span>
  </p>

  <div class="vgrid">
    <div v-for="p in catalog" :key="p.id" class="vcard pcard">
      <div class="pcard-top">
        <div style="min-width:0">
          <div class="pcard-name">{{ p.name }}</div>
          <div class="note vtrunc" :title="p.description">{{ p.description }}</div>
        </div>
        <span class="vtag" :class="st(p.id).healthy ? 'ok' : 'bad'">
          <span class="vdot" :class="st(p.id).healthy ? 'ok' : 'bad'" />
          {{ st(p.id).healthy ? '运行中' : '未就绪' }}
        </span>
      </div>

      <div class="vmetrics" style="grid-template-columns:1fr 1fr">
        <div class="vmetric">
          <div class="k">端口</div>
          <div class="v mono">{{ p.default_port }}</div>
        </div>
        <div class="vmetric">
          <div class="k">模式</div>
          <div class="v">{{ st(p.id).mode === 'docker' ? 'Docker' : (st(p.id).mode || '—') }}</div>
        </div>
      </div>

      <div class="pcard-msg note" :title="st(p.id).message">{{ st(p.id).message || '—' }}</div>

      <div class="pcard-acts">
        <button class="btn btn-primary btn-sm" :disabled="!!busy" @click="installLocal(p.id)"
                :class="{ 'mode-on': st(p.id).mode !== 'docker' }"
                title="切到本地模式（若 Docker 一体容器在运行会先停止它以释放端口）">
          {{ busy === p.id + 'local' ? '启动中…' : '本地启动' }}
        </button>
        <button class="btn btn-secondary btn-sm" :disabled="!!busy" @click="installDocker(p.id)"
                :class="{ 'mode-on': st(p.id).mode === 'docker' }" title="切到 Docker 模式（已部署则秒级切换）">
          {{ busy === p.id + 'docker' ? '切换中…' : 'Docker' }}
        </button>
        <button class="icon-btn stop-btn" title="停止该插件" :disabled="!!busy" @click="stop(p.id)">
          <svg viewBox="0 0 24 24" fill="currentColor"><rect x="7" y="7" width="10" height="10" rx="2"/></svg>
        </button>
      </div>
    </div>
    <div v-if="!catalog.length" class="vcard vempty">
      <div class="t">插件清单加载中…</div>
      <div class="d">若长时间为空，检查后端是否正常</div>
    </div>
  </div>

  <details class="vhelp">
    <summary>打码引擎怎么选</summary>
    <div class="vhelp-body">
      三个都是<b>免费自建</b>打码引擎，注册页（设置 → 打码通道）随时切换，每次只用一个：
      <ul>
        <li><b>Auralith</b> — 浏览器池，预编译二进制，开箱即用，默认推荐</li>
        <li><b>VeloraTurn</b> — 独立 Go 引擎，与 EzSolver 同一套 HTTP 协议</li>
        <li><b>EzSolver</b> — Python nodriver，首次启动会自动建 venv 装依赖，较慢</li>
      </ul>
      <br />
      三者都必须用<b>有头浏览器</b>（headless 会被 Turnstile 判定自动化）。
      macOS 上打码窗口会被自动缩小并推到屏幕角落，同时把焦点还给原应用，
      所以不会打断你手上的事；要临时看窗口调试，设 <code>UMBRAFORGE_CAPTCHA_OFFSCREEN=0</code>。<br /><br />
      <b>Docker 模式</b>：三个引擎会装进同一个容器（<code>umbraforge-captcha</code>），
      浏览器跑在 Xvfb 虚拟屏里，完全不出现在桌面上。部署入口在
      <b>「Docker 管理 → 打码引擎」</b>：一键部署全部，切换通道只切端口、零重部署。
    </div>
  </details>
</template>

<style scoped>
.pcard {
  display: flex;
  flex-direction: column;
  gap: 11px;
}
.pcard-top {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}
.pcard-name {
  font-size: 15px;
  font-weight: 740;
  letter-spacing: -0.01em;
}
.pcard-msg {
  flex: 1 1 auto;
  min-height: 2.6em;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-all;
  font-size: 11px;
  line-height: 1.45;
}
.pcard-acts {
  display: flex;
  gap: 6px;
  margin-top: auto;
}
.pcard-acts .btn { flex: 1; justify-content: center; }
/* 停止是破坏性操作，悬停给红色反馈，避免误按 */
.stop-btn { flex: none; }
.stop-btn:hover:not(:disabled) {
  color: var(--bad);
  background: color-mix(in srgb, var(--bad) 12%, transparent);
}
/* 当前生效的模式按钮高亮 */
.pcard-acts .btn.mode-on {
  border-color: var(--accent);
  color: var(--accent);
  box-shadow: 0 0 0 1px var(--accent) inset;
}
</style>
