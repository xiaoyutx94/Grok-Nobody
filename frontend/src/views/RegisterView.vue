<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import * as grok from '@/api/grok'
import * as edu from '@/api/edu'
import * as gw from '@/api/gateway'
import { confirmBox } from '@/utils/confirm'

const form = reactive({
  count: 1, concurrency: 1, delay: 0, step_delay: 0,
  email_mode: 'mailtm', captcha_provider: 'ezsolver',
  browser: 'chrome', os_type: 'macos', skip_verify: true,
  auto_import: false, import_convert_inflight: 4, import_proxy_mode: 'registration', proxy_pick_mode: 'random',
  gateway_group_id: '', gateway_group_name: '',
  auto_pause_on_failures: true, auto_pause_wait_minutes: 5, auto_pause_fail_threshold: 10,
  proxy: '', outlook_data: '', use_proxy_pool: true,
  icloud_account_ids: [] as string[],
  cf_worker_ids: [] as string[]
})

const cfg = ref<any>({})
const status = ref<any>({ running: false, results: [], accounts_count: 0 })

// 流水线模式开关(默认开启;存 batch_config.pipeline_mode,随 cfg 自动保存)
const pipelineMode = computed({
  get: () => cfg.value?.batch_config?.pipeline_mode !== false,
  set: (v: boolean) => {
    if (!cfg.value.batch_config) cfg.value.batch_config = {}
    cfg.value.batch_config.pipeline_mode = v
  }
})
const logs = ref<string[]>([])
const logFilter = ref('')
const logCopied = ref('')
const poolCount = ref(0)
const eduWorkers = ref<any[]>([])
const starting = ref(false)

// 网关分组（入库落组下拉）
const gatewayGroups = ref<gw.GatewayGroup[]>([])
async function loadGatewayGroups() {
  try {
    const g = await gw.getGroups()
    gatewayGroups.value = g.groups
  } catch { /* 网关不可用静默 */ }
}
function syncGroupName() {
  const g = gatewayGroups.value.find((x) => x.id === form.gateway_group_id)
  form.gateway_group_name = g?.name || ''
}
const stopping = ref(false)
const msg = ref('')
const msgKind = ref<'info' | 'error'>('info')
let timer: any

/* ---------- 可折叠分组：收起后表头仍显示摘要，不靠展开才能确认配置 ---------- */
type PanelKey = 'batch' | 'email' | 'captcha' | 'proxy' | 'fp' | 'behavior'
const PANEL_LS = 'umbraforge.register.panels'
const panel = reactive<Record<PanelKey, boolean>>({
  batch: true, email: true, captcha: true, proxy: false, fp: false, behavior: false
})
try {
  const saved = JSON.parse(localStorage.getItem(PANEL_LS) || 'null')
  if (saved && typeof saved === 'object') Object.assign(panel, saved)
} catch { /* 忽略损坏的本地状态 */ }
function togglePanel(k: PanelKey) {
  panel[k] = !panel[k]
  localStorage.setItem(PANEL_LS, JSON.stringify(panel))
}
/* ---------- 运行指标：全部由 /status 现有字段推导，不新增后端依赖 ---------- */
const total = computed(() => Number(status.value.total || 0) || form.count)
const completed = computed(() => Number(status.value.completed || 0))
const success = computed(() => Number(status.value.success || 0))
const fail = computed(() => Number(status.value.fail || 0))
const elapsed = computed(() => Number(status.value.elapsed_sec || 0))

const progressPct = computed(() => {
  const t = total.value
  if (t <= 0) return 0
  return Math.max(0, Math.min(100, (completed.value / t) * 100))
})
const successPct = computed(() => {
  const c = completed.value
  if (c <= 0) return 0
  return Math.max(0, Math.min(100, (success.value / c) * 100))
})
const ratePerMin = computed(() => {
  const s = elapsed.value
  if (s <= 0 || completed.value <= 0) return 0
  return (completed.value / s) * 60
})
const etaSec = computed(() => {
  const left = total.value - completed.value
  const r = ratePerMin.value
  if (!status.value.running || left <= 0 || r <= 0) return 0
  return Math.round((left / r) * 60)
})

/* ---------- 每分钟实测成功数（失败不算） ----------
   后端按分钟采样（status.per_min，最近 30 分钟）。估计速率 ratePerMin
   是把「已完成÷耗时」平均出来的，开头偏慢、尾段偏快；实测按分钟计数
   才看得出每一分钟到底成功几个。 */
const perMin = computed(() =>
  Array.isArray(status.value.per_min) ? status.value.per_min : []
)
const lastMinSucc = computed(() => {
  const p = perMin.value
  return p.length ? p[p.length - 1].success : 0
})
const perMinText = computed(() =>
  perMin.value
    .slice(-6)
    .map((m: any) => `${m.success}✓${m.fail}✗`)
    .join(' · ')
)
/* 打码闸/实际 workers（本批启动时采样）：闸=注册并发×2 余量（对齐 sub2api），
   瞬时活跃打码数远小于闸是正常的（worker 大部分时间在邮箱/提交）。 */
const captchaGate = computed(() => Number(status.value.captcha_gate || 0))
const captchaLive = computed(() => Number(status.value.captcha_live || 0))

/* ---------- 入库（SSO→OAuth）指标 ----------
   注册页此前完全看不到入库情况：自动入库在后端 onResult 里内联执行，
   既不进「账号管理页」那个 importProgressTracker，也不在 status 里留痕迹，
   用户开着自动入库跑一批只能从日志里一行行找 [自动入库]。 */
const importEnabled = computed(() => !!status.value.auto_import_enabled)
const importDone = computed(() => Number(status.value.import_done || 0))
const importOK = computed(() => Number(status.value.import_success || 0))
const importFail = computed(() => Number(status.value.import_failed || 0))
const importMS = computed(() => Number(status.value.import_elapsed_ms || 0))
const pendingImport = computed(() => Number(status.value.pending_import_count || 0))

// 入库分母用「注册成功数」而不是本批总数：只有注册成功的账号才会去转换，
// 用 total 当分母会让进度条永远走不满（失败的那些根本不进入库流程）。
const importPct = computed(() => {
  const base = success.value
  if (base <= 0) return 0
  return Math.max(0, Math.min(100, (importDone.value / base) * 100))
})
// 平均每个账号的转换耗时（累计耗时 / 已出结果数），比总耗时更能说明入库成本
const importAvgSec = computed(() => {
  const d = importDone.value
  if (d <= 0) return 0
  return importMS.value / d / 1000
})
// 面板只在「本批开了自动入库」或「有历史积压」时出现，空跑时不占地方
const showImport = computed(() => importEnabled.value || importDone.value > 0 || pendingImport.value > 0)

function fmtDur(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const r = s % 60
  if (m < 60) return `${m}m ${String(r).padStart(2, '0')}s`
  const h = Math.floor(m / 60)
  return `${h}h ${String(m % 60).padStart(2, '0')}m`
}
function autoPauseLabel(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  const m = Math.floor(s / 60)
  const r = s % 60
  return m > 0 ? `${m} 分 ${r} 秒` : `${r} 秒`
}

/** 运行态：驱动状态灯 / 按钮 / 顶部条语义色 */
const runState = computed<'idle' | 'running' | 'stopping' | 'paused'>(() => {
  if (status.value.stopping) return 'stopping'
  if (status.value.auto_paused) return 'paused'
  return status.value.running ? 'running' : 'idle'
})
const runLabel = computed(() => ({
  idle: '空闲', running: '运行中', stopping: '停止中', paused: '自动暂停'
}[runState.value]))
/* ---------- 折叠表头摘要 ---------- */
const EMAIL_LABEL: Record<string, string> = { mailtm: 'Mail.tm', edu: 'EDU 域名池', icloud_api: 'iCloud 取码平台', outlook: 'Outlook 导入' }
const sumBatch = computed(() => `${form.count} 个 · 并发 ${form.concurrency}`)
const sumEmail = computed(() => {
  const base = EMAIL_LABEL[form.email_mode] || form.email_mode
  if (form.email_mode === 'edu') return `${base} · ${form.cf_worker_ids.length}/${enabledEduWorkers.value.length} Worker`
  if (form.email_mode === 'outlook') return `${base} · ${outlookCount.value} 行`
  return base
})
const sumCaptcha = computed(() => form.captcha_provider)
const sumProxy = computed(() => {
  if (form.proxy.trim()) return '单代理覆盖'
  return form.use_proxy_pool ? `代理池 ${poolCount.value} 条 · ${form.proxy_pick_mode === 'random' ? '随机' : '轮转'}` : '直连'
})
const sumFp = computed(() => `${form.browser === 'random' ? '随机' : form.browser} / ${form.os_type === 'random' ? '随机' : form.os_type}`)
const sumBehavior = computed(() => {
  const on: string[] = []
  if (form.skip_verify) on.push('跳过验证')
  if (form.auto_import) on.push('自动入库')
  if (form.auto_pause_on_failures) on.push('失败暂停')
  return on.length ? on.join(' · ') : '全部关闭'
})
const outlookCount = computed(() => form.outlook_data.split('\n').map((l) => l.trim()).filter(Boolean).length)

/* iCloud 取码平台：账号列表（来自平台同步）+ 选择辅助 */
const icloudAccounts = ref<any[]>([])
const icloudSelectedCount = computed(() => form.icloud_account_ids.length)
function toggleIcloudAcc(id: string) {
  const i = form.icloud_account_ids.indexOf(id)
  if (i >= 0) form.icloud_account_ids.splice(i, 1)
  else form.icloud_account_ids.push(id)
}
function selectAllIcloud() {
  form.icloud_account_ids = icloudAccounts.value.map(a => a.id)
}
function clearIcloud() { form.icloud_account_ids = [] }
async function loadIcloudAccounts() {
  try {
    const cfg = await grok.getConfig()
    let base = cfg.icloud_api_base_url?.trim?.() || ''
    // 未配置地址时自动启动内置平台（与注册发起时的行为一致，
    // Apple 登录态/邮箱池数据存在即可拉起），账号列表即时可见
    if (!base) {
      try {
        await grok.icloudPanelStart()
        const st = await grok.icloudPanelStatus()
        base = st?.url || ''
      } catch { base = '' }
    }
    if (!base) { icloudAccounts.value = []; return }
    const r = await grok.icloudAccounts(base)
    icloudAccounts.value = r.accounts || []
  } catch { icloudAccounts.value = [] }
}

/* ---------- 打码 Key 就绪状态：启动前把阻塞条件前置显示 ---------- */
const CAPTCHA_KEYED: Record<string, string> = { yescaptcha: 'yes_captcha_key', nextcaptcha: 'next_captcha_key' }
function captchaReady(p: string): boolean {
  const field = CAPTCHA_KEYED[p]
  if (!field) return true
  return !!String(cfg.value?.[field] || '').trim()
}
const captchaBlocked = computed(() => !captchaReady(form.captcha_provider))

/* ---------- 日志：单次遍历同时算 tone，避免模板里重复正则 ---------- */
const LOG_TAIL = 800
function toneOf(line: string): '' | 'ok' | 'warn' | 'bad' {
  const s = line.toLowerCase()
  if (s.includes('success') || s.includes('成功')) return 'ok'
  if (s.includes('error') || s.includes('fail') || s.includes('失败')) return 'bad'
  if (s.includes('warn') || s.includes('retry') || s.includes('重试')) return 'warn'
  return ''
}
const filteredLogs = computed(() => {
  const q = logFilter.value.trim().toLowerCase()
  const src = q ? logs.value.filter((l) => String(l).toLowerCase().includes(q)) : logs.value
  return src
})
const logRows = computed(() => {
  const src = filteredLogs.value
  const tail = src.length > LOG_TAIL ? src.slice(-LOG_TAIL) : src
  return tail.map((text, i) => ({ id: i, text, tone: toneOf(String(text)) }))
})
const logHidden = computed(() => Math.max(0, filteredLogs.value.length - logRows.value.length))

/* ---------- 右列吸顶偏移：滚动容器是 .content（不是视口），
     指挥条高度又随提示条变化，只能实测后写进 CSS 变量 ---------- */
const cmdEl = ref<HTMLElement | null>(null)
const cmdH = ref(120)
let cmdRO: ResizeObserver | null = null
onMounted(() => {
  if (!cmdEl.value || typeof ResizeObserver === 'undefined') return
  cmdRO = new ResizeObserver(() => {
    // offsetHeight 含 padding + border，contentRect 不含，这里要的是外框高
    const h = cmdEl.value?.offsetHeight
    if (h) cmdH.value = Math.round(h)
  })
  cmdRO.observe(cmdEl.value)
})
onUnmounted(() => { cmdRO?.disconnect(); cmdRO = null })

const logBox = ref<HTMLElement | null>(null)
const autoFollow = ref(true)
function onLogScroll() {
  const el = logBox.value
  if (!el) return
  autoFollow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 48
}
async function scrollLogsToEnd() {
  await nextTick()
  const el = logBox.value
  if (el) el.scrollTop = el.scrollHeight
}
// 跟随信号不能只看渲染行数：日志到 LOG_TAIL 上限后行数恒定，
// 新行会不再触发滚动。用「总行数 + 末行内容」当变更信号。
const logSignal = computed(() => {
  const src = filteredLogs.value
  return src.length + '|' + (src.length ? String(src[src.length - 1]) : '')
})
watch(logSignal, () => { if (autoFollow.value) void scrollLogsToEnd() })

async function copyLogs(all = false) {
  const lines = all ? logs.value : filteredLogs.value
  const text = lines.join('\n')
  if (!text) { logCopied.value = '无日志可复制'; setTimeout(() => { logCopied.value = '' }, 2000); return }
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }
  logCopied.value = `已复制 ${lines.length} 行`
  setTimeout(() => { logCopied.value = '' }, 2000)
}
async function clearLogs() {
  if (!(await confirmBox('清空当前运行日志？该操作不可恢复。'))) return
  await grok.clearLogs()
  await reload()
}
/* ---------- EDU Worker 多选 ---------- */
const enabledEduWorkers = computed(() => (eduWorkers.value || []).filter((w: any) => w.enabled !== false && w.domain && w.worker_url))
const eduAllSelected = computed(() =>
  enabledEduWorkers.value.length > 0 &&
  enabledEduWorkers.value.every((w: any) => form.cf_worker_ids.includes(w.id))
)
function toggleEduWorker(id: string) {
  const s = new Set(form.cf_worker_ids)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  form.cf_worker_ids = [...s]
}
function selectAllEdu() { form.cf_worker_ids = enabledEduWorkers.value.map((w: any) => w.id) }
function clearEdu() { form.cf_worker_ids = [] }

watch(() => form.email_mode, (m) => {
  if (m === 'edu' && form.cf_worker_ids.length === 0 && enabledEduWorkers.value.length) {
    form.cf_worker_ids = enabledEduWorkers.value.map((w: any) => w.id)
  }
})

async function reload() {
  cfg.value = await grok.getConfig()
  form.count = cfg.value.default_count || form.count
  form.concurrency = cfg.value.default_concurrency || form.concurrency
  form.email_mode = cfg.value.default_email_mode || form.email_mode
  form.captcha_provider = cfg.value.captcha_provider || form.captcha_provider
  form.browser = cfg.value.default_browser || form.browser
  form.os_type = cfg.value.default_os_type || form.os_type
  form.skip_verify = cfg.value?.skip_verify !== false
  form.auto_import = !!cfg.value?.auto_import
  form.gateway_group_id = cfg.value?.gateway_group_id || ''
  form.gateway_group_name = cfg.value?.gateway_group_name || ''
  form.import_convert_inflight = cfg.value?.import_convert_inflight || 4
  form.import_proxy_mode = cfg.value?.import_proxy_mode || 'registration'
  form.proxy_pick_mode = cfg.value?.proxy_pick_mode || 'random'
  form.auto_pause_on_failures = cfg.value?.auto_pause_on_failures !== false
  form.auto_pause_wait_minutes = cfg.value?.auto_pause_wait_minutes || 5
  form.auto_pause_fail_threshold = cfg.value?.auto_pause_fail_threshold || 10
  void loadIcloudAccounts()
  const pool = await grok.getProxyPool()
  poolCount.value = (pool.urls || []).length
  status.value = await grok.getStatus()
  logs.value = await grok.getLogs()
  try {
    const st = await edu.listEdu()
    eduWorkers.value = st?.workers || []
    if (form.email_mode === 'edu' && form.cf_worker_ids.length === 0 && enabledEduWorkers.value.length) {
      form.cf_worker_ids = enabledEduWorkers.value.map((w: any) => w.id)
    }
  } catch {
    eduWorkers.value = []
  }
  workerSyncReady = true // load 完成后才允许「改并发→热同步」触发
}

async function saveCfg() {
  cfg.value = await grok.saveConfig({
    ...cfg.value,
    captcha_provider: form.captcha_provider,
    default_count: form.count,
    default_concurrency: form.concurrency,
    default_email_mode: form.email_mode,
    default_delay: form.delay,
    default_step_delay: form.step_delay,
    default_browser: form.browser,
    default_os_type: form.os_type,
    skip_verify: form.skip_verify,
    auto_import: form.auto_import,
    import_convert_inflight: form.import_convert_inflight,
    import_proxy_mode: form.import_proxy_mode,
    gateway_group_id: form.gateway_group_id,
    gateway_group_name: form.gateway_group_name,
    proxy_pick_mode: form.proxy_pick_mode,
    auto_pause_on_failures: form.auto_pause_on_failures,
    auto_pause_wait_minutes: form.auto_pause_wait_minutes,
    auto_pause_fail_threshold: form.auto_pause_fail_threshold
  })
}

// 表单修改自动保存（防抖 600ms）：不再依赖手动「保存配置」，重启不丢
let cfgSaveTimer: any = null
watch(
  () => ({
    count: form.count, concurrency: form.concurrency, delay: form.delay, step_delay: form.step_delay,
    email_mode: form.email_mode, captcha_provider: form.captcha_provider,
    browser: form.browser, os_type: form.os_type,
    skip_verify: form.skip_verify,
    auto_import: form.auto_import, import_convert_inflight: form.import_convert_inflight, import_proxy_mode: form.import_proxy_mode,
    gateway_group_id: form.gateway_group_id, gateway_group_name: form.gateway_group_name,
    proxy_pick_mode: form.proxy_pick_mode,
    auto_pause_on_failures: form.auto_pause_on_failures,
    auto_pause_wait_minutes: form.auto_pause_wait_minutes,
    auto_pause_fail_threshold: form.auto_pause_fail_threshold,
    pipeline_mode: cfg.value?.batch_config?.pipeline_mode
  }),
  () => {
    if (cfgSaveTimer) clearTimeout(cfgSaveTimer)
    cfgSaveTimer = setTimeout(() => { void saveCfg().catch(() => {}) }, 600)
  },
  { deep: true }
)
onUnmounted(() => { if (cfgSaveTimer) clearTimeout(cfgSaveTimer) })

// 改注册并发 → 自动热同步打码 workers（对齐 web 版「同步打码并发」）：
// 直接调引擎 /admin/workers，免重启 docker/重建容器；运行中不推
// （下次启动生效，与 web 版一致）。打码器未运行/不可达时静默（配置照存）。
let workerSyncTimer: any = null
let workerSyncReady = false
const lastSyncedWorkers = { n: -1 }
watch(
  () => form.concurrency,
  (n, prev) => {
    if (!workerSyncReady || Number(n) === Number(prev)) return
    if (status.value?.running) return
    const v = Math.max(1, Number(n) || 1)
    if (workerSyncTimer) clearTimeout(workerSyncTimer)
    workerSyncTimer = setTimeout(async () => {
      workerSyncTimer = null
      if (lastSyncedWorkers.n === v) return
      lastSyncedWorkers.n = v
      try {
        const rs = await grok.syncWorkers({ concurrency: v, provider: form.captcha_provider })
        const results = rs?.results || []
        const ok = results.filter((r: any) => r.ok)
        if (ok.length) {
          msg.value = `打码并发已同步为 ${rs?.concurrency ?? v}（${ok.map((r: any) => r.id).join('/')} · 免重启，含排队余量）`
        }
      } catch { /* 打码器未运行：静默，配置照常保存 */ }
    }, 800)
  }
)
onUnmounted(() => { if (workerSyncTimer) clearTimeout(workerSyncTimer) })

function fail_(m: string) { msgKind.value = 'error'; msg.value = m }

async function start() {
  starting.value = true; msg.value = ''
  try {
    if (form.captcha_provider === 'yescaptcha' && !cfg.value?.yes_captcha_key) {
      return fail_('YesCaptcha 未配置 API Key，请到「设置 / 打码」填写并保存')
    }
    if (form.captcha_provider === 'nextcaptcha' && !cfg.value?.next_captcha_key) {
      return fail_('NextCaptcha 未配置 API Key，请到「设置 / 打码」填写并保存')
    }
    if (form.email_mode === 'edu') {
      if (!enabledEduWorkers.value.length) {
        return fail_('EDU 池为空：请到「EDU 邮箱」加载域名并开通/入池')
      }
      if (!form.cf_worker_ids.length) {
        return fail_('请至少勾选一个 EDU Worker')
      }
    }
    if (form.email_mode === 'outlook' && !form.outlook_data.trim()) {
      return fail_('Outlook 模式需要粘贴 email----password----client_id----refresh_token')
    }
    await saveCfg()
    const body: any = { ...form }
    if (form.email_mode !== 'icloud_api') delete body.icloud_account_ids
    if (!form.use_proxy_pool) body.proxy_urls = []
    if (form.email_mode !== 'edu') delete body.cf_worker_ids
    // 提交前明示实际邮箱模式（防止「选了 A 跑了 B」类问题被静默吞掉）
    msg.value = `开始注册：${EMAIL_LABEL[form.email_mode] || form.email_mode} × ${form.count}（并发 ${form.concurrency}）`
    await grok.startRegister(body)
    await reload()
  } catch (e: any) {
    fail_(e?.response?.data?.error || e.message || String(e))
  } finally { starting.value = false }
}

async function stop() {
  stopping.value = true
  try { await grok.stopRegister(); await reload() } finally { stopping.value = false }
}


onMounted(async () => {
  await reload()
  await scrollLogsToEnd()
  loadGatewayGroups()
  timer = setInterval(async () => {
    try {
      status.value = await grok.getStatus()
      logs.value = await grok.getLogs()
      // 代理池数量实时同步（代理池页删除/添加后这里自动更新）
      const pool = await grok.getProxyPool()
      poolCount.value = (pool.urls || []).length
    } catch {}
  }, 1200)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <!-- ============ 指挥条：状态 / 进度 / 主操作 一行内闭环 ============ -->
  <section ref="cmdEl" class="cmd" :data-state="runState">
    <div class="cmd-top">
      <div class="cmd-id">
        <span class="cmd-led"><i /></span>
        <div class="cmd-name">
          <h2>Grok 注册</h2>
          <p>{{ runLabel }}<template v-if="status.running"> · 已运行 {{ fmtDur(elapsed) }}</template></p>
        </div>
      </div>

      <div class="cmd-kpi">
        <div class="kpi"><b>{{ completed }}<em>/{{ total }}</em></b><span>完成</span></div>
        <div class="kpi"><b class="ok">{{ success }}</b><span>成功</span></div>
        <div class="kpi"><b class="bad">{{ fail }}</b><span>失败</span></div>
        <div class="kpi"><b>{{ ratePerMin > 0 ? ratePerMin.toFixed(1) : '—' }}</b><span>个/分钟(估)</span></div>
        <div class="kpi" :title="'最近 6 分钟实测（成功✓/失败✗）：' + perMinText">
          <b :class="lastMinSucc > 0 ? 'ok' : ''">{{ perMin.length ? lastMinSucc : '—' }}</b><span>实测成功/分</span>
        </div>
        <div class="kpi" title="打码闸=注册并发×2 余量；瞬时活跃打码少是正常的（worker 大部分时间在邮箱/提交）">
          <b>{{ captchaLive || '—' }}<em v-if="captchaGate">/{{ captchaGate }}</em></b><span>打码闸</span>
        </div>
        <div class="kpi"><b>{{ etaSec > 0 ? fmtDur(etaSec) : '—' }}</b><span>预计剩余</span></div>
        <div class="kpi"><b>{{ status.accounts_count || 0 }}</b><span>账号库</span></div>
        <div v-if="showImport" class="kpi">
          <b :class="importFail ? 'warn' : 'ok'">{{ importOK }}<em v-if="success">/{{ success }}</em></b>
          <span>已入库</span>
        </div>
        <div v-if="pendingImport" class="kpi"><b class="warn">{{ pendingImport }}</b><span>待入库</span></div>
      </div>

      <div class="cmd-act">
        <button class="btn btn-primary" :disabled="status.running || starting" @click="start">
          {{ starting ? '启动中…' : '开始注册' }}
        </button>
        <button class="btn btn-danger" :disabled="!status.running || stopping" @click="stop">
          {{ stopping ? '停止中…' : '停止' }}
        </button>
        <button class="btn btn-icon btn-secondary" data-tip="刷新状态" @click="reload" aria-label="刷新状态">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/></svg>
        </button>
      </div>
    </div>

    <div class="rail" :class="{ idle: !status.running && !completed }">
      <div class="rail-bar">
        <i class="rail-fill" :style="{ width: progressPct + '%' }" />
        <i class="rail-fail" :style="{ width: (total > 0 ? (fail / total) * 100 : 0) + '%' }" />
      </div>
      <span class="rail-pct">{{ progressPct.toFixed(0) }}%</span>
    </div>

    <!-- 入库进度条：与注册进度分开一条，因为分母不同（入库只针对注册成功的账号） -->
    <div v-if="showImport" class="imprail">
      <span class="imprail-k">入库</span>
      <div class="imprail-bar">
        <i class="imprail-fill" :style="{ width: importPct + '%' }" />
        <i class="imprail-fail"
           :style="{ width: (success > 0 ? (importFail / success) * 100 : 0) + '%' }" />
      </div>
      <span class="imprail-n">{{ importDone }}<em>/{{ success }}</em></span>
      <span v-if="importOK" class="imprail-t ok">成功 {{ importOK }}</span>
      <span v-if="importFail" class="imprail-t bad">失败 {{ importFail }}</span>
      <span v-if="importAvgSec > 0" class="imprail-t">均 {{ importAvgSec.toFixed(1) }}s/个</span>
      <span v-if="importMS > 0" class="imprail-t">累计 {{ fmtDur(Math.round(importMS / 1000)) }}</span>
      <span v-if="pendingImport" class="imprail-t warn">积压 {{ pendingImport }}</span>
      <span v-if="!importEnabled && !importDone" class="imprail-t dim">本批未开自动入库</span>
      <span v-if="status.import_last_email" class="imprail-cur mono" :title="status.import_last_email">
        {{ status.import_last_email }}
      </span>
    </div>
    <div v-if="status.import_last_error" class="banner bad">
      <b>入库失败</b> {{ status.import_last_error }}（账号保留 SSO，可到「账号管理」重试）
    </div>

    <div v-if="status.auto_paused" class="banner warn">
      <b>风控避让</b> 连续失败触发自动暂停 · 剩余 {{ autoPauseLabel(status.auto_pause_remaining_sec || 0) }}
    </div>
    <div v-if="msg" class="banner" :class="msgKind === 'error' ? 'bad' : 'info'">{{ msg }}</div>
  </section>

  <!-- ============ 主体：左配置（可折叠） / 右遥测（满高日志） ============ -->
  <section class="work" :style="{ '--cmd-h': cmdH + 'px' }">
    <div class="cfg-col">
      <!-- ---- 批次 ---- -->
      <div class="pane" :class="{ open: panel.batch }">
        <button type="button" class="pane-head" @click="togglePanel('batch')">
          <span class="pane-ico">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 6h16M4 12h16M4 18h10"/></svg>
          </span>
          <span class="pane-t">批次与节奏</span>
          <span class="pane-s">{{ sumBatch }}</span>
          <svg class="pane-cv" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
        </button>
        <div v-show="panel.batch" class="pane-body">
          <div class="quad">
            <label class="fld">数量<input class="input" v-model.number="form.count" type="number" min="1" /></label>
            <label class="fld">并发<input class="input" v-model.number="form.concurrency" type="number" min="1" max="50" /></label>
            <label class="fld">延迟 (s)<input class="input" v-model.number="form.delay" type="number" min="0" /></label>
            <label class="fld">步进延迟 (s)<input class="input" v-model.number="form.step_delay" type="number" min="0" /></label>
          </div>
          <label class="fld check"><input type="checkbox" v-model="pipelineMode" /> 流水线模式(预取打码)<span class="hint-inline">打码与等码/提交重叠,吞吐 +50%</span></label>
          <p class="hint">并发越高越快，但触发风控概率上升；步进延迟作用于单账号内的每一步。</p>
        </div>
      </div>

      <!-- ---- 邮箱来源 ---- -->
      <div class="pane" :class="{ open: panel.email }">
        <button type="button" class="pane-head" @click="togglePanel('email')">
          <span class="pane-ico">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/></svg>
          </span>
          <span class="pane-t">邮箱来源</span>
          <span class="pane-s">{{ sumEmail }}</span>
          <svg class="pane-cv" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
        </button>
        <div v-show="panel.email" class="pane-body">
          <div class="opts">
            <button
              v-for="m in (['mailtm','edu','icloud_api','outlook'] as const)"
              :key="m" type="button" class="opt" :class="{ on: form.email_mode === m }"
              @click="form.email_mode = m"
            >
              <b>{{ EMAIL_LABEL[m] }}</b>
              <span>{{ m === 'mailtm' ? '临时邮箱 · 免配置' : m === 'edu' ? 'CF 自有域名 · 可多选' : m === 'icloud_api' ? 'iCloud 取码平台 · 自动取号' : '自备凭证 · 批量粘贴' }}</span>
            </button>
          </div>

          <!-- EDU 子面板 -->
          <div v-if="form.email_mode === 'edu'" class="sub">
            <div class="sub-head">
              <span class="sub-t">Worker 选择 · {{ form.cf_worker_ids.length }}/{{ enabledEduWorkers.length }}</span>
              <div class="row">
                <button class="btn btn-ghost btn-xs" type="button" @click="selectAllEdu" :disabled="!enabledEduWorkers.length">全选</button>
                <button class="btn btn-ghost btn-xs" type="button" @click="clearEdu" :disabled="!form.cf_worker_ids.length">清空</button>
                <RouterLink class="btn btn-ghost btn-xs" to="/edu">管理</RouterLink>
              </div>
            </div>
            <div v-if="!enabledEduWorkers.length" class="empty">
              池为空或无启用 Worker。到
              <RouterLink class="link" to="/edu">EDU 邮箱</RouterLink>
              加载域名，把 CF 已开通的域名勾选「开通/入池」。
            </div>
            <div v-else class="wk-list">
              <label v-for="w in enabledEduWorkers" :key="w.id" class="wk" :class="{ on: form.cf_worker_ids.includes(w.id) }">
                <input type="checkbox" :checked="form.cf_worker_ids.includes(w.id)" @change="toggleEduWorker(w.id)" />
                <span class="wk-n">{{ w.name || w.domain }}</span>
                <span class="wk-d mono">{{ w.domain }}</span>
                <span v-if="w.use_edu_subdomain" class="tag">@edu</span>
              </label>
            </div>
            <p v-if="enabledEduWorkers.length" class="hint">{{ eduAllSelected ? '已全选启用中的 Worker，注册时轮询分配。' : '多选后按轮询/随机分配域名。' }}</p>
          </div>

          <!-- Outlook 子面板 -->
          <div v-if="form.email_mode === 'outlook'" class="sub">
            <div class="sub-head">
              <span class="sub-t">Outlook 凭证 · 已识别 {{ outlookCount }} 行</span>
            </div>
            <textarea class="textarea" v-model="form.outlook_data" placeholder="email----password----client_id----refresh_token" />
            <p class="hint">每行一个账号，四段以 <code>----</code> 分隔。</p>
          </div>

          <!-- iCloud 取码平台子面板：按账号选择 -->
          <div v-if="form.email_mode === 'icloud_api'" class="sub">
            <div class="sub-head">
              <span class="sub-t">按账号选择 · 已选 {{ icloudSelectedCount }}/{{ icloudAccounts.length }}</span>
              <div class="row">
                <button class="btn btn-ghost btn-xs" type="button" @click="selectAllIcloud" :disabled="!icloudAccounts.length">全选</button>
                <button class="btn btn-ghost btn-xs" type="button" @click="clearIcloud" :disabled="!form.icloud_account_ids.length">清空</button>
                <RouterLink class="btn btn-ghost btn-xs" to="/icloud">管理</RouterLink>
              </div>
            </div>
            <div v-if="!icloudAccounts.length" class="empty">
              未同步平台账号。到
              <RouterLink class="link" to="/icloud">iCloud 邮箱</RouterLink>
              登录取码平台并同步账号/邮箱池。
            </div>
            <div v-else class="wk-list">
              <label v-for="a in icloudAccounts" :key="a.id" class="wk" :class="{ on: form.icloud_account_ids.includes(a.id) }">
                <input type="checkbox" :checked="form.icloud_account_ids.includes(a.id)" @change="toggleIcloudAcc(a.id)" />
                <span class="wk-n">{{ a.apple_id || a.label || a.id }}</span>
                <span class="wk-d mono">{{ a.label || a.id }}</span>
              </label>
            </div>
            <p class="hint">勾选后注册只从这些账号的邮箱池取号；一个都不选 = 全部账号。池空时自动回退平台 claim 取号。</p>
          </div>
        </div>
      </div>

      <!-- ---- 打码通道 ---- -->
      <div class="pane" :class="{ open: panel.captcha }">
        <button type="button" class="pane-head" @click="togglePanel('captcha')">
          <span class="pane-ico">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l7 3v6c0 4-3 7-7 9-4-2-7-5-7-9V6z"/><path d="m9 12 2 2 4-4"/></svg>
          </span>
          <span class="pane-t">打码通道</span>
          <span class="pane-s" :class="{ warn: captchaBlocked }">{{ sumCaptcha }}{{ captchaBlocked ? ' · 缺 Key' : '' }}</span>
          <svg class="pane-cv" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
        </button>
        <div v-show="panel.captcha" class="pane-body">
          <div class="chips">
            <button
              v-for="p in ['ezsolver','veloraturn','auralith','yescaptcha','nextcaptcha']"
              :key="p" type="button" class="chip" :class="{ 'is-on': form.captcha_provider === p }"
              @click="form.captcha_provider = p"
            >
              {{ p }}
              <i v-if="CAPTCHA_KEYED[p]" class="key-dot" :class="captchaReady(p) ? 'ok' : 'bad'" />
            </button>
          </div>
          <p v-if="captchaBlocked" class="hint warn">
            {{ form.captcha_provider }} 需要 API Key，当前未配置 ·
            <RouterLink class="link" to="/settings">去设置</RouterLink>
          </p>
          <p v-else class="hint">
            ezsolver / veloraturn / auralith 为内置插件，无需 Key ·
            <RouterLink class="link" to="/settings">打码设置</RouterLink>
          </p>
        </div>
      </div>

      <!-- ---- 网络与代理 ---- -->
      <div class="pane" :class="{ open: panel.proxy }">
        <button type="button" class="pane-head" @click="togglePanel('proxy')">
          <span class="pane-ico">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M11 5 7 9l4 4"/><path d="M7 9h10a4 4 0 0 1 0 8"/><path d="M13 19l4-4-4-4"/></svg>
          </span>
          <span class="pane-t">网络与代理</span>
          <span class="pane-s">{{ sumProxy }}</span>
          <svg class="pane-cv" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
        </button>
        <div v-show="panel.proxy" class="pane-body">
          <label class="sw">
            <input v-model="form.use_proxy_pool" type="checkbox" />
            <span class="sw-t">使用代理池</span>
            <span class="sw-n">{{ poolCount }} 条可用 · <RouterLink class="link" to="/proxy-pool">管理</RouterLink></span>
          </label>
          <div v-if="form.use_proxy_pool" class="inset">
            <span class="inset-t">分配方式</span>
            <div class="chips">
              <button type="button" class="chip" :class="{ 'is-on': form.proxy_pick_mode === 'random' }" @click="form.proxy_pick_mode = 'random'">随机</button>
              <button type="button" class="chip" :class="{ 'is-on': form.proxy_pick_mode === 'round_robin' }" @click="form.proxy_pick_mode = 'round_robin'">顺序轮转</button>
            </div>
          </div>
          <label class="fld">单代理覆盖<span class="fld-n">优先于代理池</span>
            <input class="input mono" v-model="form.proxy" placeholder="socks5://127.0.0.1:1080" />
          </label>
        </div>
      </div>

      <!-- ---- 指纹 ---- -->
      <div class="pane" :class="{ open: panel.fp }">
        <button type="button" class="pane-head" @click="togglePanel('fp')">
          <span class="pane-ico">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 4c-3 0-5 2-5 5v6"/><path d="M12 8c-1.5 0-2 1-2 2v6"/><path d="M12 4c3 0 5 2 5 5v6"/><path d="M12 8c1.5 0 2 1 2 2v6"/></svg>
          </span>
          <span class="pane-t">浏览器指纹</span>
          <span class="pane-s">{{ sumFp }}</span>
          <svg class="pane-cv" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
        </button>
        <div v-show="panel.fp" class="pane-body">
          <div class="fp-row">
            <span class="fp-k">浏览器</span>
            <div class="chips">
              <button v-for="b in ['random','chrome','edge','firefox','safari']" :key="b" type="button" class="chip" :class="{ 'is-on': form.browser === b }" @click="form.browser = b">{{ b === 'random' ? '随机' : b }}</button>
            </div>
          </div>
          <div class="fp-row">
            <span class="fp-k">系统</span>
            <div class="chips">
              <button v-for="o in ['random','macos','windows','linux','android','ios']" :key="o" type="button" class="chip" :class="{ 'is-on': form.os_type === o }" @click="form.os_type = o">{{ o === 'random' ? '随机' : o }}</button>
            </div>
          </div>
          <p class="hint">随机 = 每个账号使用不同指纹组合，降低批量特征。</p>
        </div>
      </div>

      <!-- ---- 执行策略 ---- -->
      <div class="pane" :class="{ open: panel.behavior }">
        <button type="button" class="pane-head" @click="togglePanel('behavior')">
          <span class="pane-ico">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5 5l2 2M17 17l2 2M19 5l-2 2M7 17l-2 2"/></svg>
          </span>
          <span class="pane-t">执行策略</span>
          <span class="pane-s">{{ sumBehavior }}</span>
          <svg class="pane-cv" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
        </button>
        <div v-show="panel.behavior" class="pane-body">
          <label class="sw">
            <input v-model="form.skip_verify" type="checkbox" />
            <span class="sw-t">跳过验证</span>
            <span class="sw-n">不验活，注册即入列</span>
          </label>

          <label class="sw">
            <input v-model="form.auto_import" type="checkbox" />
            <span class="sw-t">自动入库</span>
            <span class="sw-n">成功后转 OAuth 完整凭证</span>
          </label>
          <div v-if="form.auto_import" class="inset grid2">
            <label class="fld">入库线程
              <input class="input" type="number" v-model.number="form.import_convert_inflight" min="1" max="32" />
            </label>
            <label class="fld">入库代理
              <select class="input" v-model="form.import_proxy_mode">
                <option value="registration">跟随注册</option>
                <option value="direct">直连</option>
              </select>
            </label>
          </div>

          <label class="fld gw-group-fld">注册入库到网关分组
            <select class="input" v-model="form.gateway_group_id" @change="syncGroupName">
              <option value="">（不入组）</option>
              <option v-for="g in gatewayGroups" :key="g.id" :value="g.id">{{ g.name }}</option>
            </select>
            <span class="sw-n">注册成功自动入库时落入该分组（独立于「自动入库」开关，选组即生效）</span>
          </label>

          <label class="sw">
            <input v-model="form.auto_pause_on_failures" type="checkbox" />
            <span class="sw-t">连续失败自动暂停</span>
            <span class="sw-n">风控避让</span>
          </label>
          <div v-if="form.auto_pause_on_failures" class="inset grid2">
            <label class="fld">触发次数
              <input class="input" type="number" v-model.number="form.auto_pause_fail_threshold" min="1" max="100" />
            </label>
            <label class="fld">等待分钟
              <input class="input" type="number" v-model.number="form.auto_pause_wait_minutes" min="1" max="120" />
            </label>
          </div>
        </div>
      </div>

      <div class="cfg-foot">
        <span class="hint">改动自动保存</span>
        <div class="row">
          <RouterLink class="btn btn-ghost btn-xs" to="/accounts">账号管理</RouterLink>
        </div>
      </div>
    </div>

    <!-- ============ 右列：遥测 + 日志 ============ -->
    <div class="mon-col">
      <div class="mon">
        <div class="mon-head">
          <span class="kicker">当前状态</span>
          <span class="pill" :class="{ run: status.running }"><i class="dot" />{{ runLabel }}</span>
        </div>
        <div class="gauge">
          <div class="gauge-t"><span>成功率</span><b>{{ completed ? successPct.toFixed(1) + '%' : '—' }}</b></div>
          <div class="gauge-bar"><i :style="{ width: successPct + '%' }" /></div>
        </div>
        <!-- 入库成功率与注册成功率分开看：注册成功但入库失败的账号只有 SSO
             没有 OAuth 凭证，实际还不能用，混在一个数字里会高估产出。 -->
        <div v-if="showImport" class="gauge">
          <div class="gauge-t">
            <span>入库率</span>
            <b>{{ importDone ? ((importOK / importDone) * 100).toFixed(1) + '%' : '—' }}</b>
          </div>
          <div class="gauge-bar">
            <i class="is-imp" :style="{ width: (importDone ? (importOK / importDone) * 100 : 0) + '%' }" />
          </div>
        </div>
        <div class="mon-msg">{{ status.message || '等待任务' }}</div>
        <div v-if="status.last_email" class="mon-kv"><span>最近邮箱</span><b class="mono">{{ status.last_email }}</b></div>
        <div v-if="status.last_error" class="mon-kv err"><span>最近错误</span><b>{{ status.last_error }}</b></div>
        <div v-if="showImport" class="mon-kv">
          <span>入库</span>
          <b>{{ importOK }} 成功 · {{ importFail }} 失败<template v-if="importAvgSec > 0"> · 均 {{ importAvgSec.toFixed(1) }}s</template></b>
        </div>
        <div v-if="pendingImport" class="mon-kv">
          <span>待入库</span>
          <b class="warn">{{ pendingImport }} 个（有 SSO 无凭证）</b>
        </div>
      </div>

      <div class="logs">
        <div class="logs-head">
          <span class="kicker">运行日志 · {{ filteredLogs.length }}<template v-if="logFilter"> / {{ logs.length }}</template></span>
          <div class="logs-act">
            <input class="input logs-q" v-model="logFilter" placeholder="筛选关键字" />
            <button class="btn btn-icon btn-secondary" :data-tip="logFilter ? '复制筛选结果' : '复制可见日志'" @click="copyLogs(false)" aria-label="复制日志">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
            </button>
            <button class="btn btn-icon btn-secondary" data-tip="复制全部" @click="copyLogs(true)" aria-label="复制全部日志">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M8 4h9a2 2 0 0 1 2 2v9"/><rect x="4" y="8" width="11" height="12" rx="2"/></svg>
            </button>
            <button class="btn btn-icon btn-secondary" data-tip="清空日志" @click="clearLogs" aria-label="清空日志">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M9 7V5h6v2M6 7l1 13h10l1-13"/></svg>
            </button>
          </div>
        </div>
        <p v-if="logCopied" class="hint accent">{{ logCopied }}</p>
        <div ref="logBox" class="log-box" @scroll.passive="onLogScroll">
          <div v-if="!logRows.length" class="log-empty">{{ logs.length ? '无匹配日志' : '等待日志…' }}</div>
          <template v-else>
            <div v-if="logHidden" class="log-more">… 已省略较早的 {{ logHidden }} 行</div>
            <div v-for="r in logRows" :key="r.id" class="log-line" :data-tone="r.tone">{{ r.text }}</div>
          </template>
        </div>
        <div class="logs-foot">
          <label class="follow"><input type="checkbox" v-model="autoFollow" @change="autoFollow && scrollLogsToEnd()" /> 自动滚动</label>
          <span class="hint">1.2s 轮询 · 保留最近 2000 行</span>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
/* ================= 指挥条 ================= */
.cmd {
  position: sticky;
  top: -22px;               /* 抵消 .content 的 22px padding，吸顶时贴边 */
  z-index: 20;
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px 18px;
  border: 1px solid var(--line);
  border-radius: var(--r);
  background:
    radial-gradient(620px 200px at 0% 0%, color-mix(in srgb, var(--accent) 12%, transparent), transparent 62%),
    var(--panel);
  backdrop-filter: blur(18px) saturate(150%);
  -webkit-backdrop-filter: blur(18px) saturate(150%);
  box-shadow: var(--shadow);
}
.cmd-top {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 16px;
}
.cmd-id { display: flex; align-items: center; gap: 12px; min-width: 0; flex: none; }
.cmd-led {
  width: 26px; height: 26px; flex: none;
  border-radius: 50%;
  display: grid; place-items: center;
  color: var(--ink-3);
  background: color-mix(in srgb, currentColor 14%, transparent);
}
.cmd-led i { width: 9px; height: 9px; border-radius: 50%; background: currentColor; }
.cmd[data-state="running"] .cmd-led { color: var(--ok); animation: led 1.6s ease-out infinite; }
.cmd[data-state="paused"] .cmd-led { color: var(--accent); }
.cmd[data-state="stopping"] .cmd-led { color: var(--bad); }
@keyframes led {
  0% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--ok) 45%, transparent); }
  100% { box-shadow: 0 0 0 11px transparent; }
}
.cmd-name h2 {
  margin: 0;
  font-size: 20px; font-weight: 750; letter-spacing: -0.03em; line-height: 1.15;
}
.cmd-name p {
  margin: 2px 0 0;
  font-size: 11px; font-weight: 700; letter-spacing: 0.1em; text-transform: uppercase;
  color: var(--ink-3);
}
/* KPI 组：吃掉全部弹性空间，数字变长时在组内换行，绝不把按钮挤出本行 */
.cmd-kpi {
  display: flex;
  flex-wrap: wrap;
  align-items: stretch;
  gap: 0;
  flex: 1 1 0;
  min-width: 0;
  margin-left: auto;
}
.kpi {
  display: flex; flex-direction: column; gap: 1px;
  padding: 0 14px;
  border-left: 1px solid var(--line-2);
  white-space: nowrap;
}
.kpi:first-child { border-left: 0; padding-left: 0; }
.kpi b {
  font-size: clamp(13px, 1.5vw, 19px);
  font-weight: 760; letter-spacing: -0.02em;
  font-variant-numeric: tabular-nums; line-height: 1.1;
  overflow: hidden; text-overflow: ellipsis; max-width: 140px;
}
.kpi b em { font-style: normal; font-size: 12px; font-weight: 650; color: var(--ink-3); }
/* .ok / .bad / .warn 由 main.css 全局提供（--ok/--bad/--warn 两套主题都有定义），
   这里不再重复声明 —— 入库 KPI 的 .warn 走的就是那条全局规则 */
.kpi span {
  font-size: 9.5px; font-weight: 800; letter-spacing: 0.12em; text-transform: uppercase;
  color: var(--ink-3);
}
/* 操作按钮组：固定不压缩、不换行，永远与 KPI 同行右端 */
.cmd-act { display: flex; align-items: center; gap: 8px; flex: none; }

/* 进度轨：成功段 + 失败段叠加 */
.rail { display: flex; align-items: center; gap: 10px; }
.rail.idle { opacity: 0.5; }
.rail-bar {
  position: relative;
  flex: 1;
  height: 7px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--ink) 9%, transparent);
  overflow: hidden;
}
.rail-fill, .rail-fail {
  position: absolute; top: 0; bottom: 0;
  border-radius: 999px;
  transition: width 0.5s ease;
}
.rail-fill { left: 0; background: linear-gradient(90deg, var(--accent-2), var(--accent)); }
.rail-fail { right: 0; background: color-mix(in srgb, var(--bad) 80%, transparent); }
.rail-pct {
  min-width: 3.1rem; text-align: right;
  font-size: 12px; font-weight: 750; font-variant-numeric: tabular-nums;
  color: var(--ink-2);
}

/* 入库轨：与注册进度轨分开一条，因为分母不同 ——
   入库只针对「注册成功」的账号，用批次总数当分母永远走不满 */
.imprail {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  font-size: 11px;
}
.imprail-k {
  font-size: 9.5px; font-weight: 800; letter-spacing: 0.12em; text-transform: uppercase;
  color: var(--ink-3); flex: 0 0 auto;
}
.imprail-bar {
  position: relative; flex: 1 1 120px; min-width: 80px; max-width: 260px; height: 5px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--ink) 9%, transparent);
  overflow: hidden;
}
.imprail-fill, .imprail-fail {
  position: absolute; top: 0; bottom: 0; border-radius: 999px;
  transition: width 0.5s ease;
}
.imprail-fill { left: 0; background: linear-gradient(90deg, color-mix(in srgb, var(--ok) 70%, #34d399), var(--ok)); }
.imprail-fail { right: 0; background: color-mix(in srgb, var(--bad) 80%, transparent); }
.imprail-n {
  font-weight: 750; font-variant-numeric: tabular-nums; color: var(--ink-2);
  flex: 0 0 auto;
}
.imprail-n em { font-style: normal; font-weight: 650; color: var(--ink-3); }
.imprail-t { color: var(--ink-3); flex: 0 0 auto; }
.imprail-t.ok { color: var(--ok); font-weight: 700; }
.imprail-t.bad { color: var(--bad); font-weight: 700; }
.imprail-t.warn { color: var(--warn); font-weight: 700; }
.imprail-t.dim { opacity: 0.7; }
/* 最近入库的邮箱：占满剩余宽度并单行截断，不能把整条轨挤换行 */
.imprail-cur {
  flex: 1 1 90px; min-width: 0;
  color: var(--ink-3);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
/* 提示条 */
.banner {
  padding: 9px 12px;
  border-radius: var(--r-sm);
  font-size: 12.5px;
  line-height: 1.5;
  border: 1px solid transparent;
}
.banner b { font-weight: 750; margin-right: 4px; }
.banner.warn {
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border-color: color-mix(in srgb, var(--accent) 30%, transparent);
}
.banner.bad {
  color: var(--bad);
  background: color-mix(in srgb, var(--bad) 10%, transparent);
  border-color: color-mix(in srgb, var(--bad) 28%, transparent);
}
.banner.info {
  color: var(--ok);
  background: color-mix(in srgb, var(--ok) 10%, transparent);
  border-color: color-mix(in srgb, var(--ok) 26%, transparent);
}

/* ================= 主体两列 ================= */
.work {
  display: grid;
  grid-template-columns: minmax(0, 1.05fr) minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}
.cfg-col { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
.mon-col {
  /* 吸在指挥条正下方：偏移 = 实测指挥条高 - .content 的 22px padding + 6px 呼吸 */
  position: sticky;
  top: calc(var(--cmd-h, 120px) - 16px);
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
  /* .content 可视高 = 100vh - top-h - 44px(上下 padding)，再减掉吸顶偏移 */
  max-height: calc(100vh - var(--top-h) - 28px - var(--cmd-h, 120px));
  /* 没有这两条，子级 flex 项（.logs → .log-box 的 min-height）会顶破
     max-height，日志直接溢出到卡片外面。min-height:0 解除 flex 子项的
     自动最小尺寸，overflow:hidden 兜住任何越界内容。 */
  min-height: 0;
  overflow: hidden;
}
@media (max-width: 1080px) {
  .work { grid-template-columns: 1fr; }
  .mon-col { position: static; max-height: none; }
  .cmd { position: static; }
}
/* ================= 折叠分组 ================= */
.pane {
  border: 1px solid var(--line);
  border-radius: var(--r-sm);
  background: var(--panel-solid);
  box-shadow: var(--shadow);
  overflow: hidden;
}
.pane-head {
  width: 100%;
  display: grid;
  grid-template-columns: 26px auto 1fr 18px;
  align-items: center;
  gap: 10px;
  padding: 11px 13px;
  border: 0;
  background: transparent;
  cursor: pointer;
  text-align: left;
  transition: background 0.15s ease;
}
.pane-head:hover { background: color-mix(in srgb, var(--ink) 3.5%, transparent); }
.pane.open .pane-head { border-bottom: 1px solid var(--line); }
.pane-ico {
  width: 26px; height: 26px;
  display: grid; place-items: center;
  border-radius: 8px;
  color: var(--ink-3);
  background: color-mix(in srgb, var(--ink) 5%, transparent);
  transition: 0.15s ease;
}
.pane-ico svg { width: 15px; height: 15px; }
.pane.open .pane-ico { color: var(--accent); background: var(--accent-soft); }
.pane-t { font-size: 13px; font-weight: 700; letter-spacing: -0.01em; }
.pane-s {
  text-align: right;
  font-size: 11.5px;
  color: var(--ink-3);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.pane-s.warn { color: var(--accent); font-weight: 700; }
.pane-cv { width: 16px; height: 16px; color: var(--ink-3); transition: transform 0.2s ease; }
.pane.open .pane-cv { transform: rotate(180deg); }
.pane-body {
  display: flex; flex-direction: column; gap: 12px;
  padding: 13px;
}
/* ---- 表单原子 ---- */
.quad {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(96px, 1fr));
  gap: 10px;
}
.fld {
  display: block;
  font-size: 11.5px; font-weight: 680; color: var(--ink-2);
}
.fld > .input, .fld > .textarea, .fld > select { margin-top: 5px; }
.fld .fld-n { margin-left: 6px; font-weight: 500; color: var(--ink-3); }
.fld.check { display: flex; align-items: center; gap: 7px; margin-top: 9px; font-weight: 550; }
.fld.check input { accent-color: var(--accent); width: 15px; height: 15px; }
.hint-inline { font-weight: 450; color: var(--ink-3); }
.hint { margin: 0; font-size: 11px; line-height: 1.5; color: var(--ink-3); }
.hint.warn { color: var(--accent); font-weight: 650; }
.hint.accent { color: var(--accent); }
.hint code {
  font-family: var(--mono); font-size: 10.5px;
  padding: 1px 4px; border-radius: 4px;
  background: color-mix(in srgb, var(--ink) 7%, transparent);
}

/* ---- 大选项卡（邮箱来源） ---- */
.opts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(146px, 1fr));
  gap: 8px;
}
.opt {
  display: flex; flex-direction: column; gap: 2px;
  padding: 10px 12px;
  border: 1px solid var(--line-2);
  border-radius: 11px;
  background: var(--panel-soft);
  text-align: left;
  cursor: pointer;
  transition: 0.15s ease;
}
.opt:hover { border-color: color-mix(in srgb, var(--accent) 45%, var(--line-2)); transform: translateY(-1px); }
.opt.on {
  border-color: var(--accent);
  background: var(--accent-soft);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 14%, transparent);
}
.opt b { font-size: 12.5px; font-weight: 720; }
.opt.on b { color: var(--accent); }
.opt span { font-size: 10.5px; line-height: 1.45; color: var(--ink-3); }
/* ---- 子面板（EDU / Outlook） ---- */
.sub {
  display: flex; flex-direction: column; gap: 8px;
  padding: 11px;
  border-radius: 11px;
  border: 1px solid var(--line);
  background: var(--panel-soft);
}
.sub-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.sub-t { font-size: 11px; font-weight: 800; letter-spacing: 0.1em; text-transform: uppercase; color: var(--ink-3); }
.wk-list {
  display: flex; flex-direction: column; gap: 4px;
  max-height: 11rem;
  overflow: auto;
  padding-right: 2px;
}
.wk {
  display: grid;
  grid-template-columns: auto minmax(0, auto) minmax(0, 1fr) auto;
  align-items: center;
  gap: 8px;
  padding: 6px 9px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: var(--panel-solid);
  cursor: pointer;
  transition: 0.13s ease;
}
.wk:hover { border-color: color-mix(in srgb, var(--accent) 35%, transparent); }
.wk.on { background: var(--accent-soft); border-color: color-mix(in srgb, var(--accent) 30%, transparent); }
.wk-n { font-size: 12px; font-weight: 680; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.wk-d {
  font-size: 10.5px; color: var(--ink-3);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.tag {
  flex: none;
  padding: 1px 6px;
  border-radius: 999px;
  font-size: 9.5px; font-weight: 750;
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 16%, transparent);
}
.empty {
  padding: 12px;
  border-radius: 9px;
  border: 1px dashed var(--line-2);
  font-size: 11.5px; line-height: 1.55; color: var(--ink-3);
}
/* ---- 开关行 + 依赖缩进 ---- */
.sw {
  display: grid;
  grid-template-columns: auto auto 1fr;
  align-items: center;
  gap: 9px;
  padding: 7px 9px;
  margin: 0 -9px;
  border-radius: 9px;
  cursor: pointer;
  transition: background 0.13s ease;
}
.sw:hover { background: color-mix(in srgb, var(--ink) 3.5%, transparent); }
.sw-t { font-size: 12.5px; font-weight: 680; }
.sw-n { font-size: 10.5px; color: var(--ink-3); }
.gw-group-fld { display: flex; flex-direction: column; gap: 3px; margin: 8px 0 4px 3px; padding: 8px 10px; background: color-mix(in srgb, var(--accent-soft, #4f7cff) 6%, transparent); border-radius: 8px; }
.gw-group-fld .input { margin-top: 4px; }
.inset {
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
  margin-left: 3px;
  padding: 9px 0 9px 12px;
  border-left: 2px solid var(--accent-soft);
}
.inset.grid2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.inset-t { font-size: 11px; font-weight: 700; color: var(--ink-3); }

/* ---- 指纹行 ---- */
.fp-row {
  display: grid;
  grid-template-columns: 3.6rem 1fr;
  align-items: start;
  gap: 10px;
}
.fp-k { font-size: 11.5px; font-weight: 700; color: var(--ink-3); padding-top: 6px; }

/* 打码 Key 就绪点 */
.key-dot {
  width: 5px; height: 5px;
  margin-left: 5px;
  border-radius: 50%;
  display: inline-block;
  vertical-align: middle;
}
.key-dot.ok { background: var(--ok); }
.key-dot.bad { background: var(--bad); }
.chip.is-on .key-dot.bad { background: #fff; }

.btn-xs { min-height: 28px; padding: 0 10px; font-size: 11.5px; border-radius: 9px; }
.cfg-foot {
  display: flex; align-items: center; justify-content: space-between;
  gap: 10px; flex-wrap: wrap;
  padding: 4px 2px 0;
}

/* ================= 右列：遥测 ================= */
.mon {
  display: flex; flex-direction: column; gap: 10px;
  padding: 14px;
  border: 1px solid var(--line);
  border-radius: var(--r-sm);
  background: var(--panel-solid);
  box-shadow: var(--shadow);
  flex: none;
}
.mon-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.gauge { display: flex; flex-direction: column; gap: 5px; }
.gauge-t { display: flex; align-items: baseline; justify-content: space-between; }
.gauge-t span { font-size: 11px; font-weight: 700; color: var(--ink-3); }
.gauge-t b { font-size: 16px; font-weight: 760; font-variant-numeric: tabular-nums; letter-spacing: -0.02em; }
.gauge-bar {
  height: 6px; border-radius: 999px;
  background: color-mix(in srgb, var(--ink) 9%, transparent);
  overflow: hidden;
}
.gauge-bar i {
  display: block; height: 100%; border-radius: 999px;
  background: linear-gradient(90deg, color-mix(in srgb, var(--ok) 70%, #34d399), var(--ok));
  transition: width 0.5s ease;
}
/* 入库率用强调色与「成功率」的绿色区分开：两条同色会让人以为是同一个指标，
   而注册成功但入库失败的账号只有 SSO、还不能用，混看会高估产出 */
.gauge-bar i.is-imp {
  background: linear-gradient(90deg, var(--accent-2), var(--accent));
}
.mon-msg {
  font-size: 12px; line-height: 1.5; color: var(--ink-2);
  word-break: break-all;
}
.mon-kv {
  display: flex; align-items: baseline; justify-content: space-between; gap: 10px;
  padding-top: 8px;
  border-top: 1px solid var(--line);
}
.mon-kv span {
  flex: none;
  font-size: 9.5px; font-weight: 800; letter-spacing: 0.1em; text-transform: uppercase;
  color: var(--ink-3);
}
.mon-kv b {
  min-width: 0; text-align: right;
  font-size: 11.5px; font-weight: 650;
  word-break: break-all;
}
.mon-kv.err b { color: var(--bad); }
/* ================= 右列：日志（吃满剩余高度） ================= */
.logs {
  display: flex;
  flex-direction: column;
  gap: 8px;
  flex: 1;
  min-height: 0;
  /* 卡片自身兜底：内部任何子项越界都裁在圆角边框内，不再糊到卡片外 */
  overflow: hidden;
  padding: 14px;
  border: 1px solid var(--line);
  border-radius: var(--r-sm);
  background: var(--panel-solid);
  box-shadow: var(--shadow);
}
.logs-head {
  display: flex; align-items: center; justify-content: space-between;
  gap: 8px; flex-wrap: wrap;
}
.logs-act { display: flex; align-items: center; gap: 6px; }
.logs-q { width: 8.4rem; min-height: 30px; padding: 4px 9px; font-size: 11.5px; border-radius: 9px; }
.logs-act .btn-icon {
  width: 30px; min-width: 30px; height: 30px; min-height: 30px;
  border-radius: 9px;
}
.logs-act .btn-icon svg { width: 15px; height: 15px; }
.log-box {
  flex: 1;
  /* 不能用 min-height 硬底（会顶破 .mon-col 的 max-height 导致日志跑出卡片）。
     min-height:0 让它老实收缩，靠 flex-basis 争取高度，容器矮时内部滚动。 */
  min-height: 0;
  flex-basis: 260px;
  overflow: auto;
  overscroll-behavior: contain;
  border-radius: 11px;
  border: 1px solid var(--line);
  background: #14110f;
  color: #e7e5e4;
  padding: 10px 12px;
  font-family: var(--mono);
  font-size: 11.5px;
  line-height: 1.55;
  user-select: text;
}
.log-line { white-space: pre-wrap; word-break: break-all; }
.log-line[data-tone="ok"] { color: #6ee7b7; }
.log-line[data-tone="warn"] { color: #fcd34d; }
.log-line[data-tone="bad"] { color: #fda4af; }
.log-empty { opacity: 0.5; }
.log-more { opacity: 0.45; padding-bottom: 4px; }
.logs-foot {
  display: flex; align-items: center; justify-content: space-between;
  gap: 10px; flex-wrap: wrap;
}
.follow { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; color: var(--ink-3); cursor: pointer; }
</style>
