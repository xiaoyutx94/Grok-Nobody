<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import * as home from '@/api/home'
import * as grok from '@/api/grok'
import * as edu from '@/api/edu'
import * as dk from '@/api/docker'
import * as pluginsApi from '@/api/plugins'

const mon = ref<any>(null)
const stats = ref<any>({})
const caps = ref<any>({})
const mailWorkers = ref<any[]>([])
const proxyPool = ref<any>({ urls: [], entries: [] })
const dockerRt = ref<any>({})
const pluginStatus = ref<any[]>([])
const msg = ref('')
let timer: any

const cpu = computed(() => mon.value?.cpu || {})
const mem = computed(() => mon.value?.memory || {})
const disk = computed(() => mon.value?.disk || {})
const disks = computed<any[]>(() => (Array.isArray(disk.value.disks) ? disk.value.disks : []))
const memSlots = computed<any[]>(() => (Array.isArray(mem.value.slots) ? mem.value.slots : []))
const memSlotGroups = computed(() => {
  const groups = new Map<string, { model: string; count: number; capacityGb: number }>()
  for (const s of memSlots.value) {
    const model = (s.part_number || '').trim() || `DDR${Number(s.capacity_gb || 0) >= 8 ? '5' : '4'} ${Number(s.capacity_gb || 0).toFixed(0)}GB`
    const g = groups.get(model) || { model, count: 0, capacityGb: Number(s.capacity_gb || 0) }
    g.count++
    groups.set(model, g)
  }
  return [...groups.values()]
})
const host = computed(() => mon.value?.host || {})
const cores = computed(() => (cpu.value.per_core || []) as number[])

// 状态卡：可用邮箱 / 代理 / Docker / 打码
const mailCount = computed(() => (mailWorkers.value || []).filter((w: any) => w.enabled !== false).length)
const proxyTotal = computed(() => (proxyPool.value?.urls || []).length)
const proxyEnabled = computed(() => {
  const es = proxyPool.value?.entries || []
  if (es.length) return es.filter((e: any) => e.enabled !== false).length
  return proxyTotal.value
})
const dockerOk = computed(() => !!dockerRt.value?.daemon_ok)
const slots = computed(() => dockerRt.value?.cur_slots ?? '—')
const healthyCount = computed(() => (pluginStatus.value || []).filter((p: any) => p.healthy).length)

// 迷你趋势图历史（轮询时累积，最多 60 点）
const hist = { cpu: [] as number[], mem: [] as number[], disk: [] as number[] }
const HIST_MAX = 60
function pushHist() {
  hist.cpu.push(pct(cpu.value.usage_percent)); if (hist.cpu.length > HIST_MAX) hist.cpu.shift()
  hist.mem.push(pct(mem.value.usage_percent)); if (hist.mem.length > HIST_MAX) hist.mem.shift()
  hist.disk.push(pct(disk.value.usage_percent)); if (hist.disk.length > HIST_MAX) hist.disk.shift()
}
// sparkline: SVG polyline（归一化 0-100 → 0-28 高）
function spark(kind: 'cpu' | 'mem' | 'disk'): string {
  const h = hist[kind]
  if (h.length < 2) return ''
  const w = 96, ht = 28
  const step = w / (HIST_MAX - 1)
  return h.map((v, i) => `${(i * step).toFixed(1)},${(ht - (pct(v) / 100) * ht).toFixed(1)}`).join(' ')
}
// 阈值语义色（现代范式：正常=青绿 70%+=琥珀 90%+=红）
function tone(v: any) {
  const n = pct(v)
  if (n >= 90) return '#f43f5e'
  if (n >= 70) return '#f59e0b'
  return '#10b981'
}

function fmtMB(v: any): string {
  const n = Number(v||0)
  if (n >= 1024) return (n/1024).toFixed(1) + ' GB'
  return n.toFixed(0) + ' MB'
}
function pct(v: any) {
  const n = Number(v || 0)
  return Math.max(0, Math.min(100, n))
}
function heat(v: any) {
  const n = pct(v)
  if (n >= 90) return 'hot'
  if (n >= 70) return 'warm'
  return 'ok'
}
function ring(v: any, kind: 'cpu' | 'mem' | 'disk') {
  const n = pct(v)
  const map: any = {
    cpu: ['#67e8f9', '#22d3ee', '#f59e0b'],
    mem: ['#93c5fd', '#60a5fa', '#22d3ee'],
    disk: ['#fde68a', '#fbbf24', '#fb7185'],
  }
  const c = map[kind]
  return {
    background: `conic-gradient(from -90deg, ${c[0]} 0%, ${c[1]} ${n * 0.55}%, ${c[2]} ${n}%, rgba(120,113,108,.16) ${n}% 100%)`,
  }
}

async function reloadBase() {
  mon.value = await home.getMonitor()
  pushHist()
  stats.value = await home.getRegisterStats()
  caps.value = await home.getCapabilities()
  // 状态卡数据：任一接口失败不影响其它卡
  const [ml, pp, dr, ps] = await Promise.allSettled([
    edu.listEdu(),
    grok.getProxyPool(),
    dk.getRuntime(),
    pluginsApi.listPlugins(),
  ])
  if (ml.status === 'fulfilled') mailWorkers.value = ml.value?.workers || []
  if (pp.status === 'fulfilled') proxyPool.value = pp.value || { urls: [], entries: [] }
  if (dr.status === 'fulfilled') dockerRt.value = dr.value || {}
  if (ps.status === 'fulfilled') pluginStatus.value = ps.value?.status || []
}

async function reload() {
  try {
    await reloadBase()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  }
}

onMounted(async () => {
  await reload()
  timer = setInterval(async () => {
    try { await reloadBase() } catch { /* ignore */ }
  }, 2500)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section class="card hero">
    <div class="page-head">
      <div>
        <div class="kicker">Dashboard</div>
        <h2 class="h1">系统监控</h2>
        <p class="sub">{{ host.hostname || 'localhost' }} · {{ host.platform || caps.os }} {{ host.platform_version || '' }} · {{ caps.arch || host.arch || '' }}</p>
      </div>
      <div class="toolbar">
        <span class="pill run"><i class="dot" />健康分 {{ mon?.health_score ?? '—' }}</span>
        <RouterLink class="btn btn-secondary" to="/register">Grok 注册</RouterLink>
        <RouterLink class="btn btn-ghost" to="/warp">WARP 代理</RouterLink>
        <RouterLink class="btn btn-ghost" to="/proxy-pool">代理池</RouterLink>
      </div>
    </div>
    <p v-if="msg" class="sub" style="color:var(--accent)">{{ msg }}</p>
  </section>

  <section class="g3 sys-grid">
    <!-- CPU -->
    <div class="card sys-card" :data-heat="heat(cpu.usage_percent)">
      <div class="sys-head">
        <span class="sys-name"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="7" y="7" width="10" height="10" rx="2"/><path d="M9 3v4M15 3v4M9 17v4M15 17v4M3 9h4M3 15h4M17 9h4M17 15h4"/></svg>CPU</span>
        <span class="sys-val" :style="{color: tone(cpu.usage_percent)}">{{ pct(cpu.usage_percent).toFixed(0) }}<em>%</em></span>
      </div>
      <div class="sys-bar"><i :style="{width: pct(cpu.usage_percent)+'%', background: tone(cpu.usage_percent)}"></i></div>
      <svg v-if="spark('cpu')" class="sys-spark" viewBox="0 0 96 28" preserveAspectRatio="none"><polyline :points="spark('cpu')" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/></svg>
      <div class="sys-meta">
        <span>{{ cpu.count_logical || cores.length || '—' }} 核心</span>
        <span class="mono" :title="cpu.model_name">{{ cpu.model_name || '' }}</span>
        <span class="mono">Load {{ Number(cpu.load1||0).toFixed(1) }}</span>
      </div>
      <div v-if="cores.length" class="sys-cores" :title="'每核使用率：' + cores.map(c=>pct(c).toFixed(0)+'%').join(' ')">
        <i v-for="(c,i) in cores" :key="i" :style="{height: pct(c)+'%', background: tone(c)}"></i>
      </div>
    </div>
    <!-- 内存 -->
    <div class="card sys-card" :data-heat="heat(mem.usage_percent)">
      <div class="sys-head">
        <span class="sys-name"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="6" width="18" height="12" rx="2"/><path d="M8 6v12M12 6v12M16 6v12"/></svg>内存</span>
        <span class="sys-val" :style="{color: tone(mem.usage_percent)}">{{ pct(mem.usage_percent).toFixed(0) }}<em>%</em></span>
      </div>
      <div class="sys-bar"><i :style="{width: pct(mem.usage_percent)+'%', background: tone(mem.usage_percent)}"></i></div>
      <svg v-if="spark('mem')" class="sys-spark" viewBox="0 0 96 28" preserveAspectRatio="none"><polyline :points="spark('mem')" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/></svg>
      <div class="sys-meta">
        <span>已用 {{ fmtMB(mem.used_mb) }} / {{ fmtMB(mem.total_mb) }}</span>
        <span>可用 {{ fmtMB(mem.available_mb) }}</span>
        <span>Swap {{ pct(mem.swap_percent).toFixed(0) }}%</span>
        <span v-if="memSlotGroups.length" class="slot-line">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:11px;height:11px;vertical-align:-1px"><rect x="3" y="8" width="18" height="10" rx="1.5"/><path d="M7 11v4M12 11v4M17 11v4"/></svg>
          <em v-for="(g, i) in memSlotGroups" :key="i" class="slot-chip" :title="g.model">{{ g.model }} ×{{ g.count }}</em>
        </span>
      </div>
    </div>
    <!-- 磁盘 -->
    <div class="card sys-card" :data-heat="heat(disk.usage_percent)">
      <div class="sys-head">
        <span class="sys-name"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 3"/><circle cx="12" cy="12" r="1" fill="currentColor"/></svg>磁盘 {{ disk.path || '/' }}</span>
        <span class="sys-val" :style="{color: tone(disk.usage_percent)}">{{ pct(disk.usage_percent).toFixed(0) }}<em>%</em></span>
      </div>
      <div class="sys-bar"><i :style="{width: pct(disk.usage_percent)+'%', background: tone(disk.usage_percent)}"></i></div>
      <svg v-if="spark('disk')" class="sys-spark" viewBox="0 0 96 28" preserveAspectRatio="none"><polyline :points="spark('disk')" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/></svg>
      <div class="sys-meta">
        <span>已用 {{ Number(disk.used_gb||0).toFixed(1) }} / {{ Number(disk.total_gb||0).toFixed(1) }} GB</span>
        <span>剩余 {{ Number(disk.free_gb||0).toFixed(1) }} GB</span>
        <span>{{ disk.fstype || '' }}</span>
      </div>
      <div v-if="disks.length > 1" class="disk-list">
        <div v-for="(d, i) in disks" :key="i" class="disk-row">
          <span class="disk-path">{{ d.path }}</span>
          <span class="disk-bar"><i :style="{width: pct(d.usage_percent)+'%', background: tone(d.usage_percent)}"></i></span>
          <span class="disk-num">{{ pct(d.usage_percent).toFixed(0) }}% · {{ Number(d.used_gb||0).toFixed(0) }}/{{ Number(d.total_gb||0).toFixed(0) }}G</span>
        </div>
      </div>
    </div>
  </section>

  <section class="g4">
    <div class="stat"><div class="k">账号总数</div><div class="v">{{ stats.accounts_total || 0 }}</div></div>
    <div class="stat"><div class="k">成功账号</div><div class="v ok">{{ stats.accounts_success || 0 }}</div></div>
    <div class="stat"><div class="k">平均/分钟</div><div class="v">{{ Number(stats.avg_per_min||0).toFixed(2) }}</div></div>
    <div class="stat"><div class="k">近1小时</div><div class="v">{{ stats.last_1h_count || 0 }}</div></div>
    <div class="stat"><div class="k">最快间隔</div><div class="v">{{ stats.fastest_sec ? Number(stats.fastest_sec).toFixed(1)+'s' : '—' }}</div></div>
    <div class="stat"><div class="k">最慢间隔</div><div class="v">{{ stats.slowest_sec ? Number(stats.slowest_sec).toFixed(1)+'s' : '—' }}</div></div>
    <div class="stat"><div class="k">中位间隔</div><div class="v">{{ stats.median_sec ? Number(stats.median_sec).toFixed(1)+'s' : '—' }}</div></div>
    <div class="stat"><div class="k">近24小时</div><div class="v">{{ stats.last_24h_count || 0 }}</div></div>
  </section>

  <section class="g4 status-grid">
    <div class="card equal-card">
      <div class="kicker">可用邮箱</div>
      <div class="st-v">{{ mailCount }}<em v-if="mailCount"> 个</em><em v-else class="dim">—</em></div>
      <div class="note">EDU 域名池域名数<template v-if="mailCount"> · Mail.tm 随时可用</template></div>
    </div>
    <div class="card equal-card">
      <div class="kicker">代理</div>
      <div class="st-v">{{ proxyEnabled }}<em>/{{ proxyTotal }}</em></div>
      <div class="note">可用 / 总数 · 失败自动冷却</div>
    </div>
    <div class="card equal-card">
      <div class="kicker">Docker</div>
      <div class="st-v" :class="dockerOk ? 'ok' : 'bad'">{{ dockerOk ? '就绪' : '未就绪' }}</div>
      <div class="note">打码槽位 {{ slots }} · 部署/启动到 Docker 管理页</div>
    </div>
    <div class="card equal-card">
      <div class="kicker">打码引擎</div>
      <div class="st-v" :class="healthyCount === 3 ? 'ok' : healthyCount > 0 ? 'warn' : 'bad'">{{ healthyCount }}<em>/3</em></div>
      <div class="note">healthy 引擎数 · 本地/容器模式</div>
    </div>
  </section>
</template>

<style scoped>
.status-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; }
@media (max-width: 1100px) { .status-grid { grid-template-columns: repeat(2, 1fr); } }
.status-grid .equal-card { min-height: 96px; display: flex; flex-direction: column; gap: 6px; }
.st-v {
  font-size: 26px; font-weight: 780; letter-spacing: -0.03em; line-height: 1.1;
  font-variant-numeric: tabular-nums;
}
.st-v em { font-style: normal; font-size: 13px; font-weight: 650; color: var(--ink-3); }
.st-v em.dim { opacity: 0.5; }
.st-v.ok { color: var(--ok); }
.st-v.warn { color: var(--warn); }
.st-v.bad { color: var(--bad); }
.st-v + .note { font-size: 11.5px; color: var(--ink-3); }
.hero {
  background:
    radial-gradient(700px 240px at 0% 0%, color-mix(in srgb, var(--accent) 16%, transparent), transparent 60%),
    var(--panel);
}
.gauge-card {
  display: grid;
  grid-template-columns: 92px 1fr;
  gap: 14px;
  align-items: center;
  min-height: 148px;
}
.gauge {
  width: 84px;
  height: 84px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  box-shadow: var(--shadow);
}
.gauge-core {
  width: 62px;
  height: 62px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  background: var(--panel-solid);
  font-weight: 800;
  font-size: 16px;
  letter-spacing: -0.03em;
}
/* 首页紧凑圆环 */
.gauge.mini {
  width: 52px;
  height: 52px;
  flex: none;
}
.gauge.mini .gauge-core {
  width: 38px;
  height: 38px;
  font-size: 12px;
  font-weight: 750;
}
/* 首页系统信息（现代数据密集卡片） */
.sys-grid { align-items: stretch; }
.sys-card {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 16px;
  border-radius: 16px;
  background: color-mix(in srgb, var(--panel-solid) 92%, transparent);
  border: 1px solid var(--line);
}
.sys-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}
.sys-name {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  font-weight: 680;
  letter-spacing: .02em;
  color: var(--ink-2);
  text-transform: uppercase;
}
.sys-name svg { width: 15px; height: 15px; opacity: .9; }
.sys-val {
  font-size: 26px;
  font-weight: 780;
  letter-spacing: -0.03em;
  font-variant-numeric: tabular-nums;
  line-height: 1;
}
.sys-val em { font-style: normal; font-size: 13px; font-weight: 650; margin-left: 2px; opacity: .75; }
.sys-bar {
  height: 6px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--ink) 8%, transparent);
  overflow: hidden;
}
.sys-bar i {
  display: block;
  height: 100%;
  border-radius: 999px;
  transition: width .45s ease;
}
.sys-spark {
  width: 100%;
  height: 28px;
  color: var(--ink-2);
  opacity: .85;
}
.sys-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  font-size: 11px;
  color: var(--text-tertiary);
  line-height: 1.5;
}
.sys-meta .mono { font-family: var(--mono); }
.sys-cores {
  display: flex;
  align-items: flex-end;
  gap: 3px;
  height: 34px;
  padding-top: 4px;
  border-top: 1px solid var(--line);
}
.sys-cores i {
  flex: 1;
  min-height: 2px;
  border-radius: 2px 2px 0 0;
  opacity: .85;
  transition: height .4s ease;
}
.gauge-title { margin-top: 4px; font-size: 16px; font-weight: 730; }
.spectrum {
  margin-top: 12px;
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(14px, 1fr));
  gap: 4px;
  height: 96px;
  align-items: end;
}
.spectrum-col {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  height: 100%;
  min-width: 0;
}
.spectrum-track {
  flex: 1;
  width: 100%;
  max-width: 12px;
  border-radius: 6px;
  background: color-mix(in srgb, var(--ink) 8%, transparent);
  display: flex;
  align-items: flex-end;
  overflow: hidden;
}
.spectrum-track > i {
  display: block;
  width: 100%;
  border-radius: inherit;
  background: linear-gradient(180deg, var(--accent-2), var(--accent));
  min-height: 4px;
}
.spectrum-col > span { font-size: 9px; color: var(--ink-3); }
.slot-line {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px;
  font-size: 11px;
}
.slot-chip {
  font-style: normal;
  padding: 1px 7px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  color: var(--ink);
  font-weight: 600;
  font-size: 10.5px;
}
.disk-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px dashed color-mix(in srgb, var(--ink) 12%, transparent);
}
.disk-row {
  display: grid;
  grid-template-columns: 52px 1fr 92px;
  align-items: center;
  gap: 8px;
}
.disk-path { font-size: 11px; font-weight: 600; color: var(--ink); }
.disk-bar {
  height: 6px;
  border-radius: 4px;
  background: color-mix(in srgb, var(--ink) 8%, transparent);
  overflow: hidden;
}
.disk-bar > i { display: block; height: 100%; border-radius: inherit; min-width: 2px; }
.disk-num { font-size: 10.5px; color: var(--ink-3); text-align: right; white-space: nowrap; }
</style>
