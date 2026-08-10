<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, onUnmounted, reactive, ref } from 'vue'
import * as grok from '@/api/grok'
import * as pluginsApi from '@/api/plugins'
import { confirmBox } from '@/utils/confirm'
import { toast } from '@/utils/clipboard'
import { useAutoSave } from '@/utils/autosave'

const msg = ref('')
const saving = ref(false)
const showYes = ref(false)
const showNext = ref(false)

// 与后端 resolveCaptchaWorkers 一致：设置值当上限，注册并发当实际驱动量。
// 前端算一份只为把「到底会开几个浏览器」显示出来，避免又变成看不见的配置。
const CAPTCHA_WORKER_CEILING = 16
function effectiveWorkers(explicit: number, concurrency: number): number {
  const c = concurrency > 0 ? concurrency : 1
  const follow = Math.min(c + Math.max(1, Math.floor(c / 2)), CAPTCHA_WORKER_CEILING)
  return explicit > 0 && explicit < follow ? explicit : follow
}
const form = reactive({
  captcha_provider: 'ezsolver',
  ezsolver_url: 'http://127.0.0.1:8192',
  ezsolver_timeout_sec: 120,
  ezsolver_max_concurrency: 0,
  ezsolver_api_key: '',
  ezsolver_captcha_retry: 5,
  auralith_url: 'http://127.0.0.1:8194',
  auralith_api_key: '',
  auralith_timeout_sec: 120,
  auralith_max_concurrency: 0,
  auralith_retry: 2,
  yes_captcha_key: '',
  next_captcha_key: '',
  captcha_proxy_mode: 'registration',
  default_browser: 'chrome',
  default_os_type: 'macos',
  skip_verify: false,
  // 注册默认值（此前只有部分暴露）
  default_count: 1,
  default_concurrency: 1,
  default_email_mode: 'mailtm',
  default_delay: 0,
  default_step_delay: 0,
  auto_export: false,
  // 入库
  auto_import: false,
  import_convert_inflight: 4,
  import_proxy_mode: 'registration',
  // 代理选择 + 风控避让
  proxy_pick_mode: 'random',
  auto_pause_on_failures: true,
  auto_pause_wait_minutes: 5,
  auto_pause_fail_threshold: 10
})

// 批次调优：此前这 13 项只有 DefaultBatchConfig() 的硬编码值，
// 既不落盘也无 UI —— 注册日志里的「解除粘性以便重连换线」就是这组参数控制的。
const batch = reactive({
  max_proxy_fails: 3,
  proxy_cooldown_minutes: 10,
  min_proxy_interval: 0,
  max_per_ip: 100,
  proxy_working_headroom: 0,
  proxy_precheck_all: false,
  rate_capacity: 50,
  rate_base: 50,
  rate_min: 10,
  stagger_sec: 0,
  warp_rotate_mode: 'none',
  warp_rotate_every: 0,
  otp_poll_ms: 500,
  human_delay: false
})

const yesReady = computed(() => !!form.yes_captcha_key && form.yes_captcha_key !== '***')
const nextReady = computed(() => !!form.next_captcha_key && form.next_captcha_key !== '***')

async function reload() {
  const cfg = await grok.getConfig()
  Object.assign(form, {
    captcha_provider: cfg.captcha_provider || 'ezsolver',
    ezsolver_url: cfg.ezsolver_url || 'http://127.0.0.1:8192',
    ezsolver_timeout_sec: cfg.ezsolver_timeout_sec || 120,
    // 0 = 跟随注册并发（合法值），不能用 || 兜回 8，否则用户存的 0 会被改写
    ezsolver_max_concurrency: cfg.ezsolver_max_concurrency ?? 0,
    ezsolver_api_key: cfg.ezsolver_api_key || '',
    ezsolver_captcha_retry: cfg.ezsolver_captcha_retry || 5,
    auralith_url: cfg.auralith_url || 'http://127.0.0.1:8194',
    auralith_api_key: cfg.auralith_api_key || '',
    auralith_timeout_sec: cfg.auralith_timeout_sec || 120,
    auralith_max_concurrency: cfg.auralith_max_concurrency ?? 0,
    auralith_retry: cfg.auralith_retry || 2,
    yes_captcha_key: cfg.yes_captcha_key || '',
    next_captcha_key: cfg.next_captcha_key || '',
    captcha_proxy_mode: cfg.captcha_proxy_mode || 'registration',
    default_browser: cfg.default_browser || 'chrome',
    default_os_type: cfg.default_os_type || 'macos',
    skip_verify: !!cfg.skip_verify,
    default_count: cfg.default_count || 1,
    default_concurrency: cfg.default_concurrency || 1,
    default_email_mode: cfg.default_email_mode || 'mailtm',
    default_delay: cfg.default_delay || 0,
    default_step_delay: cfg.default_step_delay || 0,
    auto_export: !!cfg.auto_export,
    auto_import: !!cfg.auto_import,
    import_convert_inflight: cfg.import_convert_inflight || 4,
    import_proxy_mode: cfg.import_proxy_mode || 'registration',
    proxy_pick_mode: cfg.proxy_pick_mode || 'random',
    auto_pause_on_failures: cfg.auto_pause_on_failures !== false,
    auto_pause_wait_minutes: cfg.auto_pause_wait_minutes || 5,
    auto_pause_fail_threshold: cfg.auto_pause_fail_threshold || 10
  })
  if (cfg.batch_config) Object.assign(batch, cfg.batch_config)
  // load 完成后才武装自动保存：否则填充服务端值的那一刻会被当成用户改动，
  // 用本地默认值把服务端配置覆盖掉（skip_verify 就是这样被写成 false 的）。
  autosave.arm()
}

async function save() {
  saving.value = true
  msg.value = ''
  try {
    // explicit payload so paid keys are always sent when filled
    await grok.saveConfig({
      captcha_provider: form.captcha_provider,
      ezsolver_url: form.ezsolver_url,
      ezsolver_timeout_sec: form.ezsolver_timeout_sec,
      ezsolver_max_concurrency: form.ezsolver_max_concurrency,
      ezsolver_api_key: form.ezsolver_api_key,
      ezsolver_captcha_retry: form.ezsolver_captcha_retry,
      auralith_url: form.auralith_url,
      auralith_api_key: form.auralith_api_key,
      auralith_timeout_sec: form.auralith_timeout_sec,
      auralith_max_concurrency: form.auralith_max_concurrency,
      auralith_retry: form.auralith_retry,
      yes_captcha_key: form.yes_captcha_key,
      next_captcha_key: form.next_captcha_key,
      captcha_proxy_mode: form.captcha_proxy_mode,
      default_browser: form.default_browser,
      default_os_type: form.default_os_type,
      skip_verify: form.skip_verify,
      default_count: form.default_count,
      default_concurrency: form.default_concurrency,
      default_email_mode: form.default_email_mode,
      default_delay: form.default_delay,
      default_step_delay: form.default_step_delay,
      auto_export: form.auto_export,
      auto_import: form.auto_import,
      import_convert_inflight: form.import_convert_inflight,
      import_proxy_mode: form.import_proxy_mode,
      proxy_pick_mode: form.proxy_pick_mode,
      auto_pause_on_failures: form.auto_pause_on_failures,
      auto_pause_wait_minutes: form.auto_pause_wait_minutes,
      auto_pause_fail_threshold: form.auto_pause_fail_threshold,
      batch_config: { ...batch }
    })
    msg.value = '全部设置已保存（打码通道 / 注册默认值 / 入库 / 批次调优）'
    await reload()
  } catch (e:any) {
    msg.value = e?.response?.data?.error || e.message
  } finally { saving.value = false }
}

// ── 打码通道：免费引擎一行 / 收费 Key 一行 ──
// VeloraTurn 与 EzSolver 同一套 HTTP 协议，共用同一组参数（端口不同）。
// as const 是必需的：模板里用 form[freeEngine.url] 动态取字段，
// 没有它这些字段名会被推成宽 string，无法索引 form（TS7053）。
const FREE_ENGINES = [
  { id: 'ezsolver', name: 'EzSolver', port: ':8192', url: 'ezsolver_url', timeout: 'ezsolver_timeout_sec', conc: 'ezsolver_max_concurrency', retry: 'ezsolver_captcha_retry', key: 'ezsolver_api_key', placeholder: 'http://127.0.0.1:8192' },
  { id: 'veloraturn', name: 'VeloraTurn', port: ':8193', url: 'ezsolver_url', timeout: 'ezsolver_timeout_sec', conc: 'ezsolver_max_concurrency', retry: 'ezsolver_captcha_retry', key: 'ezsolver_api_key', placeholder: 'http://127.0.0.1:8193' },
  { id: 'auralith', name: 'Auralith', port: ':8194', url: 'auralith_url', timeout: 'auralith_timeout_sec', conc: 'auralith_max_concurrency', retry: 'auralith_retry', key: 'auralith_api_key', placeholder: 'http://127.0.0.1:8194' },
] as const
const isFree = computed(() => FREE_ENGINES.some((e) => e.id === form.captcha_provider))
const freeEngine = computed(() => FREE_ENGINES.find((e) => e.id === form.captcha_provider) || FREE_ENGINES[0])
const freeWorkers = computed(() => {
  const c = form.captcha_provider === 'auralith' ? form.auralith_max_concurrency : form.ezsolver_max_concurrency
  return effectiveWorkers(c, form.default_concurrency)
})

// 引擎实时状态（来自插件中心）：切换通道前就能看出引擎有没有在跑、
// 跑在 Docker 还是本机——避免选了一个没部署的引擎直接开跑注册。
const engineStatus = ref<any[]>([])
let statusTimer: any = null
async function loadEngineStatus() {
  try {
    const data = await pluginsApi.listPlugins()
    engineStatus.value = data?.status || []
  } catch { /* 后端未就绪时静默，状态保持上次值 */ }
}
function es(id: string) {
  return engineStatus.value.find((s: any) => s.id === id) || {}
}
function engineState(id: string): { cls: string; text: string } {
  const s = es(id)
  if (s.healthy) return { cls: 'ok', text: s.mode === 'docker' ? 'Docker 运行中' : '运行中' }
  if (s.running) return { cls: 'run', text: '启动中…' }
  if (s.mode === 'docker') return { cls: 'warn', text: '容器未运行' }
  return { cls: 'off', text: '未启动' }
}


// 全部设置自动保存：没有保存按钮，改动 600ms 后落盘，右下角提示结果。
// 同时监听 form 与 batch —— 两者都在同一个 save() 载荷里。
const autosave = useAutoSave(
  { form, batch },
  save,
  { okMessage: '设置已保存', label: '设置' },
)

const saveLabel = computed(() => ({
  idle: '已同步',
  dirty: '未保存',
  saving: '保存中',
  saved: '已保存',
  error: '保存失败',
}[autosave.state.value]))

onMounted(() => {
  reload()
  loadEngineStatus()
  statusTimer = setInterval(loadEngineStatus, 5000)
})
onUnmounted(() => {
  if (statusTimer) clearInterval(statusTimer)
})
// 与代理池页同因：防抖 600ms 内切走导航，watcher 已随组件停掉，
// 未落盘的改动就没了。离开前强制 flush 一次。
onBeforeUnmount(async () => {
  await autosave.flush()
})
</script>

<template>
  <div class="vhead">
    <div class="vhead-title">
      <div class="kicker">Settings</div>
      <h2>设置 / 打码</h2>
    </div>
    <div class="stat-chips">
      <span class="schip is-accent"><span class="k">当前通道</span><b>{{ form.captcha_provider }}</b></span>
      <span class="schip" :class="yesReady ? 'is-ok' : ''"><span class="k">Yes</span><b>{{ yesReady ? '已配置' : '未配置' }}</b></span>
      <span class="schip" :class="nextReady ? 'is-ok' : ''"><span class="k">Next</span><b>{{ nextReady ? '已配置' : '未配置' }}</b></span>
    </div>
    <div class="spacer" />
    <div class="vhead-actions">
      <!-- 本页无保存按钮（全自动保存），必须给出状态锚点 + 手动兜底 -->
      <span class="savestate" :class="'is-' + autosave.state.value">
        <i class="sdot" />{{ saveLabel }}
      </span>
      <button class="btn btn-secondary btn-sm"
              :disabled="autosave.state.value === 'saving'" @click="autosave.saveNow()">
        {{ autosave.state.value === 'saving' ? '保存中…' : '立即保存' }}
      </button>
    </div>
  </div>

  <p v-if="msg" class="vbanner info">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="m20 6-11 11-5-5"/></svg>
    <span>{{ msg }}</span>
  </p>

  <!-- 打码通道：免费引擎一行 + 收费 Key 一行，不再堆卡片 -->
  <div class="vcard">
    <div class="vcard-head">
      <span class="t">打码通道</span>
      <div class="spacer" />
      <span class="note">点选引擎即切换通道 · 免费引擎状态实时显示</span>
    </div>

    <!-- 免费自建引擎：一行三个，点卡片切换 -->
    <div class="ch-row">
      <div class="ch-row-label">免费自建</div>
      <div class="ch-grid ch-grid-3">
        <button v-for="p in FREE_ENGINES" :key="p.id" type="button" class="ch-cell"
          :class="{ 'is-on': form.captcha_provider === p.id }" @click="form.captcha_provider = p.id"
          :title="es(p.id).message || p.name">
          <span class="ch-name">{{ p.name }}</span>
          <span class="ch-state" :class="engineState(p.id).cls">
            <i class="ch-dot" />{{ engineState(p.id).text }}
          </span>
          <span class="ch-meta"><span class="ch-port mono">{{ p.port }}</span><span class="ch-tag">免费</span></span>
        </button>
      </div>
    </div>

    <!-- 免费引擎参数：当前选中引擎的参数共占一行（紧凑，不另开卡片） -->
    <div v-if="isFree" class="ch-params">
      <div class="ch-params-grid">
        <label class="vfield">
          <span class="lb">服务 URL</span>
          <input class="input input-sm mono" v-model="form[freeEngine.url]" :placeholder="freeEngine.placeholder" />
        </label>
        <label class="vfield">
          <span class="lb">超时 (秒)</span>
          <input class="input input-sm" type="number" min="10" v-model.number="form[freeEngine.timeout]" />
        </label>
        <label class="vfield">
          <span class="lb">并发上限 (0=跟随注册)</span>
          <input class="input input-sm" type="number" min="0" v-model.number="form[freeEngine.conc]" placeholder="0" />
        </label>
        <label class="vfield">
          <span class="lb">失败重试</span>
          <input class="input input-sm" type="number" min="1" v-model.number="form[freeEngine.retry]"
            :placeholder="freeEngine.id === 'auralith' ? '2' : '5'" />
        </label>
        <label class="vfield">
          <span class="lb">API Key（可选）</span>
          <input class="input input-sm mono" type="password" v-model="form[freeEngine.key]" placeholder="本机/内网留空" />
        </label>
      </div>
      <span class="note">
        按当前默认并发 {{ form.default_concurrency }} 计算，实际会开
        <b>{{ freeWorkers }}</b> 个打码浏览器（注册页选别的并发时按那个值算）。
        {{ freeEngine.id === 'veloraturn' ? 'VeloraTurn 与 EzSolver 共用此组参数，端口不同。' : '' }}
      </span>
    </div>

    <!-- 收费 Key：一行两个，点名称切换通道 -->
    <div class="ch-row">
      <div class="ch-row-label">收费 Key</div>
      <div class="ch-grid ch-grid-2">
        <div class="ch-cell ch-cell-input" :class="{ 'is-on': form.captcha_provider === 'yescaptcha' }">
          <div class="ch-head">
            <button type="button" class="ch-pick" @click="form.captcha_provider = 'yescaptcha'">
              <span class="ch-name">YesCaptcha</span>
              <span class="ch-state" :class="yesReady ? 'ok' : 'off'">
                <i class="ch-dot" />{{ yesReady ? 'Key 已配置' : '收费 · 需 Key' }}
              </span>
            </button>
            <a class="btn btn-ghost btn-sm" href="https://yescaptcha.com" target="_blank" rel="noopener">官网 ↗</a>
          </div>
          <div class="vinput-group">
            <input class="input input-sm mono" :type="showYes ? 'text' : 'password'" v-model="form.yes_captcha_key"
              placeholder="粘贴 YesCaptcha ClientKey" autocomplete="off" />
            <button type="button" class="icon-btn" :title="showYes ? '隐藏' : '显示'" @click="showYes = !showYes">
              <svg v-if="showYes" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
              <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M3 3l18 18"/><path d="M10.6 5.2A9.8 9.8 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-2.4 3.3M6.5 6.7A17 17 0 0 0 2 12s3.6 7 10 7a9.6 9.6 0 0 0 4-.85"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
            </button>
          </div>
        </div>

        <div class="ch-cell ch-cell-input" :class="{ 'is-on': form.captcha_provider === 'nextcaptcha' }">
          <div class="ch-head">
            <button type="button" class="ch-pick" @click="form.captcha_provider = 'nextcaptcha'">
              <span class="ch-name">NextCaptcha</span>
              <span class="ch-state" :class="nextReady ? 'ok' : 'off'">
                <i class="ch-dot" />{{ nextReady ? 'Key 已配置' : '收费 · 需 Key' }}
              </span>
            </button>
            <a class="btn btn-ghost btn-sm" href="https://nextcaptcha.com" target="_blank" rel="noopener">官网 ↗</a>
          </div>
          <div class="vinput-group">
            <input class="input input-sm mono" :type="showNext ? 'text' : 'password'" v-model="form.next_captcha_key"
              placeholder="粘贴 NextCaptcha clientKey" autocomplete="off" />
            <button type="button" class="icon-btn" :title="showNext ? '隐藏' : '显示'" @click="showNext = !showNext">
              <svg v-if="showNext" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
              <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M3 3l18 18"/><path d="M10.6 5.2A9.8 9.8 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-2.4 3.3M6.5 6.7A17 17 0 0 0 2 12s3.6 7 10 7a9.6 9.6 0 0 0 4-.85"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
            </button>
          </div>
        </div>
      </div>
    </div>

    <p v-if="form.captcha_provider === 'yescaptcha' && !yesReady" class="vbanner bad" style="margin-top:12px">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 9v4M12 17v.01"/><circle cx="12" cy="12" r="9"/></svg>
      <span>已选 YesCaptcha 但未填 Key，注册会失败。请在下方填入并保存。</span>
    </p>
    <p v-else-if="form.captcha_provider === 'nextcaptcha' && !nextReady" class="vbanner bad" style="margin-top:12px">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 9v4M12 17v.01"/><circle cx="12" cy="12" r="9"/></svg>
      <span>已选 NextCaptcha 但未填 Key，注册会失败。请在下方填入并保存。</span>
    </p>
    <p v-else-if="isFree && !es(form.captcha_provider).healthy" class="vbanner bad" style="margin-top:12px">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 9v4M12 17v.01"/><circle cx="12" cy="12" r="9"/></svg>
      <span>{{ freeEngine.name }} 当前未就绪（{{ es(form.captcha_provider).message || '引擎未启动' }}）。
        请到「Docker 管理 → 打码引擎」一键部署，或到「插件中心」本地启动。</span>
    </p>
  </div>

  <!-- 注册默认值 -->
  <div class="vcard">
    <div class="vcard-head">
      <span class="t">注册默认值</span>
      <div class="spacer" />
    </div>
    <div class="vform" style="grid-template-columns:repeat(auto-fit,minmax(190px,1fr))">
      <label class="vfield">
        <span class="lb">打码出口</span>
        <select class="input input-sm" v-model="form.captcha_proxy_mode">
          <option value="direct">direct · 直连</option>
          <option value="registration">registration · 跟随注册代理</option>
          <option value="global">global · 打码池轮转</option>
          <option value="selected">selected · 打码池首个</option>
        </select>
      </label>
      <label class="vfield">
        <span class="lb">浏览器指纹</span>
        <select class="input input-sm" v-model="form.default_browser">
          <option value="chrome">chrome</option>
          <option value="edge">edge</option>
          <option value="firefox">firefox</option>
          <option value="safari">safari</option>
        </select>
      </label>
      <label class="vfield">
        <span class="lb">系统指纹</span>
        <select class="input input-sm" v-model="form.default_os_type">
          <option value="macos">macos</option>
          <option value="windows">windows</option>
          <option value="linux">linux</option>
        </select>
      </label>
      <div class="vfield">
        <span class="lb">邮箱验证</span>
        <label class="vfield-inline" style="padding-top:2px">
          <input type="checkbox" v-model="form.skip_verify" />
          <span>默认跳过验证</span>
        </label>
      </div>
    </div>
    <div class="vform" style="grid-template-columns:repeat(auto-fit,minmax(150px,1fr));margin-top:10px">
      <label class="vfield">
        <span class="lb">默认数量</span>
        <input class="input input-sm" type="number" min="1" v-model.number="form.default_count" />
      </label>
      <label class="vfield">
        <span class="lb">默认并发</span>
        <input class="input input-sm" type="number" min="1" v-model.number="form.default_concurrency" />
      </label>
      <label class="vfield">
        <span class="lb">默认邮箱源</span>
        <select class="input input-sm" v-model="form.default_email_mode">
          <option value="mailtm">mailtm · 临时邮箱</option>
          <option value="edu">edu · CF Worker 域名池</option>
          <option value="outlook">outlook · 自备凭证</option>
        </select>
      </label>
      <label class="vfield">
        <span class="lb">账号间隔 (秒)</span>
        <input class="input input-sm" type="number" min="0" v-model.number="form.default_delay" />
      </label>
      <label class="vfield">
        <span class="lb">步进延迟 (秒)</span>
        <input class="input input-sm" type="number" min="0" v-model.number="form.default_step_delay" />
      </label>
      <div class="vfield">
        <span class="lb">自动导出</span>
        <label class="vfield-inline" style="padding-top:2px">
          <input type="checkbox" v-model="form.auto_export" />
          <span>成功后自动导出</span>
        </label>
      </div>
    </div>

    <details class="vhelp" style="margin-top:12px">
      <summary>打码出口怎么选</summary>
      <div class="vhelp-body">
        打码出口只影响<b>发给打码引擎的代理</b>，注册主链路的出口独立由代理池决定。<br />
        <code>registration</code> 让打码与注册同出口，风控特征最一致，默认推荐；<br />
        <code>direct</code> 适合打码引擎在本机、代理带宽紧张的情况；<br />
        <code>global</code> / <code>selected</code> 用「代理设置 → 打码出口」里单独配的池子。
      </div>
    </details>
  </div>

  <!-- 入库（SSO→OAuth） -->
  <div class="vsplit-wide">
    <div class="vcard">
      <div class="vcard-head">
        <span class="t">入库转换</span>
        <span class="vtag" :class="form.auto_import ? 'ok' : ''">{{ form.auto_import ? '注册后自动' : '手动' }}</span>
      </div>
      <label class="vfield-inline">
        <input type="checkbox" v-model="form.auto_import" />
        <span>注册成功后自动执行 SSO→OAuth 并标记已入库</span>
      </label>
      <div class="vform vform-2" style="margin-top:9px">
        <label class="vfield">
          <span class="lb">转换并发</span>
          <input class="input input-sm" type="number" min="1" max="16" v-model.number="form.import_convert_inflight" />
        </label>
        <label class="vfield">
          <span class="lb">入库出口</span>
          <select class="input input-sm" v-model="form.import_proxy_mode">
            <option value="registration">registration · 跟随注册代理</option>
            <option value="direct">direct · 直连</option>
          </select>
        </label>
      </div>
      <details class="vhelp" style="margin-top:10px">
        <summary>入库失败怎么排</summary>
        <div class="vhelp-body">
          转换常在注册之后一段时间才跑，注册代理可能已过期 —— 走它会全失败。
          代理侧失败会自动直连回退一次（不消耗重试次数），限流按 attempt²×2s 退避最多 3 次，
          设备流永久拒绝立即停止。更细的出口池在<b>代理池 → 出口设置</b>里配。
        </div>
      </details>
    </div>

    <div class="vcard">
      <div class="vcard-head">
        <span class="t">风控避让</span>
        <span class="vtag" :class="form.auto_pause_on_failures ? 'ok' : ''">
          {{ form.auto_pause_on_failures ? '已开启' : '已关闭' }}
        </span>
      </div>
      <label class="vfield-inline">
        <input type="checkbox" v-model="form.auto_pause_on_failures" />
        <span>连续失败达阈值后自动暂停一段时间</span>
      </label>
      <div class="vform vform-2" style="margin-top:9px">
        <label class="vfield">
          <span class="lb">连续失败阈值</span>
          <input class="input input-sm" type="number" min="1" v-model.number="form.auto_pause_fail_threshold" />
        </label>
        <label class="vfield">
          <span class="lb">暂停时长 (分钟)</span>
          <input class="input input-sm" type="number" min="1" v-model.number="form.auto_pause_wait_minutes" />
        </label>
      </div>
      <label class="vfield" style="margin-top:9px">
        <span class="lb">注册代理选择顺序</span>
        <select class="input input-sm" v-model="form.proxy_pick_mode">
          <option value="random">random · 每次开跑随机打乱</option>
          <option value="round_robin">round_robin · 按列表顺序</option>
        </select>
      </label>
    </div>
  </div>

  <!-- 批次调优 -->
  <div class="vcard">
    <div class="vcard-head">
      <span class="t">批次调优</span>
      <div class="spacer" />
      <span class="note">代理淘汰 / 粘性复用 / 速率节奏</span>
    </div>

    <div class="vform" style="grid-template-columns:repeat(auto-fit,minmax(158px,1fr))">
      <label class="vfield">
        <span class="lb">失败几次换代理</span>
        <input class="input input-sm" type="number" min="1" v-model.number="batch.max_proxy_fails" />
      </label>
      <label class="vfield">
        <span class="lb">限流冷却 (分钟)</span>
        <input class="input input-sm" type="number" min="-1" v-model.number="batch.proxy_cooldown_minutes" />
      </label>
      <label class="vfield">
        <span class="lb">每槽成功上限</span>
        <input class="input input-sm" type="number" min="1" v-model.number="batch.max_per_ip" />
      </label>
      <label class="vfield">
        <span class="lb">同槽最小间隔 (ms)</span>
        <input class="input input-sm" type="number" min="0" v-model.number="batch.min_proxy_interval" />
      </label>
      <label class="vfield">
        <span class="lb">工作集余量</span>
        <input class="input input-sm" type="number" min="0" v-model.number="batch.proxy_working_headroom" />
      </label>
      <label class="vfield">
        <span class="lb">起跑错峰 (秒)</span>
        <input class="input input-sm" type="number" min="0" v-model.number="batch.stagger_sec" />
      </label>
      <label class="vfield">
        <span class="lb">OTP 轮询 (ms)</span>
        <input class="input input-sm" type="number" min="100" v-model.number="batch.otp_poll_ms" />
      </label>
      <label class="vfield">
        <span class="lb">速率容量</span>
        <input class="input input-sm" type="number" min="1" v-model.number="batch.rate_capacity" />
      </label>
      <label class="vfield">
        <span class="lb">速率基线</span>
        <input class="input input-sm" type="number" min="1" v-model.number="batch.rate_base" />
      </label>
      <label class="vfield">
        <span class="lb">速率下限</span>
        <input class="input input-sm" type="number" min="1" v-model.number="batch.rate_min" />
      </label>
      <!-- 「WARP 轮换 / 每 N 个轮换」已从界面移除：
           这两项在 batch.go 里从未被读取（grep warp 零命中），配了不生效。
           留一个假开关比没有更糟——用户会以为 WARP 在按账号轮换。
           后端 BatchConfig 字段保留以兼容老配置文件。
           WARP 出口现在作为代理池条目参与注册，轮换由代理池的选择顺序决定。 -->
    </div>

    <div class="row" style="margin-top:10px;gap:16px">
      <label class="vfield-inline">
        <input type="checkbox" v-model="batch.human_delay" />
        <span>拟人延迟（步骤间随机停顿）</span>
      </label>
      <label class="vfield-inline">
        <input type="checkbox" v-model="batch.proxy_precheck_all" />
        <span>启动预检整池（默认只检并发数那几个）</span>
      </label>
    </div>

    <details class="vhelp" style="margin-top:11px">
      <summary>这些参数分别管什么</summary>
      <div class="vhelp-body">
        <ul>
          <li><b>失败几次换代理</b> —— 同一代理<b>连续</b>失败到这个次数后触发处置（成功即归零）。
            处置分三种：<br />
            ① 验证已不可用 → 从工作集移除，并从储备补位；<br />
            ② 验证仍可用且重连换到了新出口 IP → 保留继续用（动态住宅代理走这条）；<br />
            ③ <b>被上游限流且重连后出口 IP 没变</b>（固定出口代理）→ 按下面的「限流冷却」挂起避让。</li>
          <li><b>限流冷却</b> —— 上面第 ③ 种情况挂起多久，到期自动放回工作集，期间从储备补位顶上。
            限流通常是临时的，直接淘汰会白扔掉可用代理。日志标记为
            <code>[代理限流]</code>（与 <code>[代理失败]</code> 区分）。
            填 <code>-1</code> 关闭冷却（恢复旧行为：一直重连重试同一个被限的 IP）。
            整池都在冷却时不会停跑，会继续用最早到期的那个 —— 绝不会退化成无代理直连。</li>
          <li><b>每槽成功上限</b> —— 一个代理槽累计成功多少个账号后强制重连换出口 IP（动态住宅常靠新 TCP 换 IP）。
            设太低会频繁重连拖慢速度。</li>
          <li><b>同槽最小间隔</b> —— 同一代理两次使用之间至少等多久，0 = 不限。</li>
          <li><b>工作集余量</b> —— 在并发数之上多留几个代理槽；0 = 严格按并发数取用。</li>
          <li><b>起跑错峰</b> —— 多并发时各 worker 之间的启动间隔，避免同一瞬间打上游。</li>
          <li><b>OTP 轮询</b> —— 查收验证码邮件的轮询间隔。</li>
          <li><b>速率容量 / 基线 / 下限</b> —— 自适应限速的令牌桶参数，遇到风控自动降到下限。</li>
          <li><b>WARP 出口</b> —— 不在这里配。WARP 实例创建后会自动并入
            <b>代理池</b>（列表里带 <code>WARP</code> 标记），和手动添加的代理一样
            参与注册出口轮转，可单独启停。删除请到「WARP 代理」页删容器。</li>
          <li><b>拟人延迟</b> —— 步骤之间加随机停顿，更像真人但更慢。</li>
          <li><b>启动预检整池</b> —— 恢复旧行为。预检要经每个代理各发一次请求，
            住宅代理按量计费时不建议开。</li>
        </ul>
      </div>
    </details>
  </div>
</template>

<style scoped>
/* ── 打码通道：免费一行 / 收费一行（紧凑块，不是卡片堆） ── */
.ch-row {
  display: grid;
  grid-template-columns: 84px minmax(0, 1fr);
  gap: 12px;
  align-items: start;
  margin-top: 10px;
}
.ch-row-label {
  font-size: 11px;
  font-weight: 700;
  color: var(--ink-3);
  padding-top: 9px;
  letter-spacing: .02em;
  white-space: nowrap;
}
.ch-grid { display: grid; gap: 8px; }
.ch-grid-3 { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.ch-grid-2 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.ch-cell {
  border: 1px solid var(--line);
  border-radius: 11px;
  background: color-mix(in srgb, var(--ink) 2.5%, transparent);
  padding: 9px 11px;
  display: flex;
  flex-direction: column;
  gap: 5px;
  cursor: pointer;
  text-align: left;
  font: inherit;
  color: inherit;
  transition: border-color .15s, box-shadow .15s;
}
.ch-cell:hover { border-color: color-mix(in srgb, var(--accent) 45%, var(--line)); }
.ch-cell.is-on {
  border-color: var(--accent);
  box-shadow: 0 0 0 1px var(--accent) inset, 0 4px 14px color-mix(in srgb, var(--accent) 16%, transparent);
}
.ch-cell-input { cursor: default; }
.ch-name { font-size: 13px; font-weight: 720; letter-spacing: -0.01em; }
.ch-state {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--ink-3);
  white-space: nowrap;
}
.ch-state.ok { color: #2f7d4f; }
.ch-state.warn { color: #97671b; }
.ch-state.run { color: var(--accent); }
.ch-dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; flex: none; }
.ch-state.ok .ch-dot { box-shadow: 0 0 0 3px rgba(47, 125, 79, .14); }
.ch-state.warn .ch-dot { box-shadow: 0 0 0 3px rgba(151, 103, 27, .14); }
.ch-meta { display: flex; align-items: center; gap: 7px; }
.ch-port { font-size: 10.5px; color: var(--ink-3); }
.ch-tag {
  font-size: 9.5px;
  font-weight: 700;
  color: #2f7d4f;
  background: rgba(47, 125, 79, .1);
  border: 1px solid rgba(47, 125, 79, .28);
  border-radius: 999px;
  padding: 1px 7px;
}
.ch-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; }
.ch-pick {
  border: 0;
  background: transparent;
  padding: 0;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  gap: 4px;
  align-items: flex-start;
  text-align: left;
  font: inherit;
  color: inherit;
}
/* 免费引擎参数：当前引擎共占一行，虚线分组标注归属 */
.ch-params {
  margin-top: 10px;
  padding: 10px 12px;
  border: 1px dashed var(--line-2, var(--line));
  border-radius: 11px;
  background: color-mix(in srgb, var(--ink) 2%, transparent);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.ch-params-grid {
  display: grid;
  grid-template-columns: minmax(150px, 1.6fr) 88px 158px 84px minmax(130px, 1fr);
  gap: 9px;
}
@media (max-width: 1100px) {
  .ch-params-grid { grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); }
  .ch-grid-3 { grid-template-columns: 1fr; }
  .ch-grid-2 { grid-template-columns: 1fr; }
}
</style>
