<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as grok from '@/api/grok'
import { confirmBox } from '@/utils/confirm'
import { toast } from '@/utils/clipboard'
import { useAutoSave } from '@/utils/autosave'

// source: '' = 手动添加；'warp' = WARP 实例自动并入（生命周期由容器决定，
// 不允许在本页删除——删了条目容器还在跑，用户会以为已移除）
interface Entry { url: string; note?: string; enabled: boolean; source?: string }
interface TestResult { url: string; ok: boolean; ip?: string; latency_ms?: number; error?: string }

const entries = ref<Entry[]>([])
const msg = ref('')
const saving = ref(false)
const testing = ref(false)
const selected = ref<Set<string>>(new Set())
const results = ref<Record<string, TestResult>>({})
// 文本区：批量添加输入（解析 url[#|备注]，URL 编码备注自动解码）。
// draft 是批量添加文本区的内容。用 localStorage 兜住：
//
// 它只是「待添加」的草稿，不该进服务端代理池；但它也不能一切走导航就消失——
// 用户粘进几十行代理、还没点「批量添加」就切了个标签，回来一片空白，
// 从用户视角看就是「添加的代理都没了」。这是本页丢数据的三条路径之一
// （另两条：首屏拉取失败导致 autosave 未武装、卸载时未 flush 未落盘改动）。
const DRAFT_KEY = 'umbraforge.proxypool.draft'
const draft = ref(localStorage.getItem(DRAFT_KEY) || '')
watch(draft, (v) => {
  try {
    if (v) localStorage.setItem(DRAFT_KEY, v)
    else localStorage.removeItem(DRAFT_KEY)
  } catch { /* 隐私模式/配额满：草稿保不住不影响主流程 */ }
})

const enabledCount = computed(() => entries.value.filter(e => e.enabled).length)

// 出口设置：注册 / 打码 / 导入三条链路。后端接口早就有，前端此前从未调用，
// 导致 captcha_mode=global|selected 依赖的打码出口池、以及入库出口池无法配置。
const routing = ref<grok.ProxySettings>({
  register_urls: [],
  captcha_mode: 'registration',
  captcha_urls: [],
  import_mode: 'direct',
  import_urls: []
})
const captchaDraft = ref('')
const importDraft = ref('')
const routingBusy = ref(false)

const MODE_LABELS: Record<string, string> = {
  direct: 'direct · 直连',
  registration: 'registration · 跟随注册代理',
  global: 'global · 用本页专属池轮转',
  selected: 'selected · 用本页专属池首个'
}
// 只有 global/selected 才真正读取专属池
const captchaNeedsPool = computed(() => ['global', 'selected'].includes(routing.value.captcha_mode))
const importNeedsPool = computed(() => ['global', 'selected'].includes(routing.value.import_mode))

function linesOf(text: string) {
  return text.split(/\n+/).map(s => s.trim()).filter(Boolean)
}

async function reloadRouting() {
  try {
    const ps = await grok.getProxySettings()
    routing.value = {
      register_urls: ps.register_urls || [],
      captcha_mode: ps.captcha_mode || 'registration',
      captcha_urls: ps.captcha_urls || [],
      import_mode: ps.import_mode || 'direct',
      import_urls: ps.import_urls || []
    }
    captchaDraft.value = routing.value.captcha_urls.join('\n')
    importDraft.value = routing.value.import_urls.join('\n')
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  }
}

async function saveRouting() {
  routingBusy.value = true
  try {
    // register_urls 由代理池列表本身维护，这里不覆盖它
    const saved = await grok.saveProxySettings({
      captcha_mode: routing.value.captcha_mode,
      captcha_urls: linesOf(captchaDraft.value),
      import_mode: routing.value.import_mode,
      import_urls: linesOf(importDraft.value)
    })
    routing.value = {
      register_urls: saved.register_urls || [],
      captcha_mode: saved.captcha_mode,
      captcha_urls: saved.captcha_urls || [],
      import_mode: saved.import_mode,
      import_urls: saved.import_urls || []
    }
    captchaDraft.value = routing.value.captcha_urls.join('\n')
    importDraft.value = routing.value.import_urls.join('\n')
    msg.value = `出口设置已保存（打码 ${routing.value.captcha_mode} · 入库 ${routing.value.import_mode}）`
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    routingBusy.value = false
  }
}

async function reload() {
  // 关键：拉取失败也必须 arm()。
  //
  // 旧实现在 getProxyPool() 抛错时直接中断，arm() 永远不执行 → 自动保存
  // 一辈子处于未武装状态 → 之后所有编辑（加代理、改 URL、启停）都被静默丢弃，
  // 界面上毫无提示。首屏一次网络抖动就能让整页变成「只读且不告诉你」。
  // 这是「添加的代理没了」最恶劣的一种成因：用户以为在编辑，其实什么都没存。
  try {
    const pool = await grok.getProxyPool()
    entries.value = (pool.entries?.length ? pool.entries : (pool.urls || []).map(u => ({ url: u, enabled: true })))
  } catch (e: any) {
    msg.value = '读取代理池失败：' + (e?.response?.data?.error || e?.message || e) +
      '（本页仍可编辑，改动会自动保存）'
  }
  await reloadRouting()
  routingAutosave.arm()
  poolAutosave.arm()
}
// parseDraftLine 解析单行（url[#|备注]），URL 编码备注解码——供单行粘贴解析用
function parseDraftLine(l: string) {
  const i = l.search(/[#|]/)
  let url = l.trim(), note = ''
  if (i > 0 && i < l.length - 1) {
    url = l.slice(0, i).trim()
    note = l.slice(i + 1).trim()
  }
  try { note = decodeURIComponent(note) } catch { /* 保持原样 */ }
  return { url, note }
}
async function save() {
  saving.value = true
  try {
    await grok.saveProxyPool({ entries: entries.value })
    msg.value = `已保存 ${entries.value.length} 条代理（启用 ${enabledCount.value}）`
    // reload 会重写 entries（服务端规范化结果），用 mute 包住避免
    // watcher 又触发一次保存形成循环
    await poolAutosave.mute(async () => { await reload() })
  } catch (e:any) {
    msg.value = e?.response?.data?.error || e.message
  } finally { saving.value = false }
}

// 所有修改操作（添加/删除/清空/启用/编辑）都直接落盘，不依赖 watcher：
// 用户原话「删除后也自动保存下，等于就调用下保存功能」——每个动作即保存。
// 批量删除/清空都必须跳过 WARP 条目：它们的生命周期由容器决定。
// 删了条目容器还在跑，用户以为已移除，而下次 WARP 页刷新或新建实例时
// SyncWarpProxyEntries 又会把它加回来 —— 表现为「删不掉」。
const isWarp = (e: Entry) => e.source === 'warp'
async function deleteSelected() {
  if (!selected.value.size) return
  const kept = entries.value.filter(e => selected.value.has(e.url) && isWarp(e)).length
  entries.value = entries.value.filter(e => !selected.value.has(e.url) || isWarp(e))
  selected.value = new Set()
  await save()
  msg.value = kept
    ? `已删除选中项并落盘（保留 ${kept} 个 WARP 出口，请到「WARP 代理」页删除容器）`
    : '已删除选中项并落盘'
}
async function removeOne(i: number) {
  if (isWarp(entries.value[i])) {
    msg.value = 'WARP 出口请到「WARP 代理」页删除容器，删除后会自动从代理池移除'
    return
  }
  entries.value.splice(i, 1)
  await save()
  msg.value = `已删除该行并落盘`
}
async function clearAll() {
  const warps = entries.value.filter(isWarp)
  const tip = warps.length
    ? `清空代理池？会立即落盘，且不可撤销。\n（${warps.length} 个 WARP 出口会保留，需到「WARP 代理」页删容器）`
    : '清空代理池？会立即落盘，且不可撤销'
  if (!await confirmBox(tip)) return
  entries.value = warps
  selected.value = new Set()
  await save()
  msg.value = warps.length
    ? `已清空手动添加的代理并落盘（保留 ${warps.length} 个 WARP 出口）`
    : `代理池已清空并落盘`
}
// 启用开关 / URL / 备注编辑：change 事件（失焦/回车）触发即保存
function saveFromInput() {
  if (saving.value) return
  void save()
}

// 自动保存。分两个 watcher 是因为两者语义不同：
// 出口设置是纯配置，改完即可落盘；代理池列表则包含删除/清空这类破坏性编辑，
// 落盘同样是自动的，但删除操作在 UI 上给了二次确认，避免误删一整池。
const routingAutosave = useAutoSave(
  { m: routing, c: captchaDraft, i: importDraft },
  saveRouting,
  { okMessage: '出口设置已保存', label: '出口设置' },
)
const poolAutosave = useAutoSave(
  entries,
  save,
  { okMessage: '代理池已保存', label: '代理池', delay: 900 },
)

// 批量添加：解析文本区 → 追加到现有列表（按 URL 去重，保留备注），不清空已有条目
async function batchAdd() {
  const lines = draft.value.split(/\n+/).map(x => x.trim()).filter(Boolean)
  if (!lines.length) { msg.value = '文本区没有内容'; return }
  const exist = new Set(entries.value.map(e => e.url))
  let added = 0, skipped = 0
  for (const l of lines) {
    const p = parseDraftLine(l)
    if (!p.url) continue
    if (exist.has(p.url)) { skipped++; continue }
    exist.add(p.url)
    entries.value.push({ url: p.url, note: p.note, enabled: true })
    added++
  }
  draft.value = ''
  // 添加是明确操作，立即落盘（不依赖 watcher 防抖/flush——flush 依赖
  // watcher 的 pending 标志，push 后立即调它时 watcher 还没跑，会空转
  // 不保存，这正是「添加了切走标签就没了」的根因）。
  if (added > 0) {
    await save()
    msg.value = `已添加 ${added} 条${skipped ? `，跳过重复 ${skipped} 条` : ''}（已自动落盘）`
  } else {
    msg.value = skipped ? `全部是重复条目（跳过 ${skipped} 条）` : '没有可添加的代理'
  }
}
async function testOne(e: Entry) {
  if (!e.url.trim()) return
  testing.value = true
  try {
    const rs = await grok.testProxyPool([e.url.trim()])
    rs.forEach((r: TestResult) => { results.value[r.url] = r })
    msg.value = rs[0]?.ok ? `可用（${rs[0].ip || '出口通'} · ${rs[0].latency_ms}ms）` : '不可用'
  } catch (err:any) {
    msg.value = err?.response?.data?.error || err.message
  } finally { testing.value = false }
}
async function runTest() {
  const urls = entries.value.filter(e => e.enabled).map(e => e.url)
  if (!urls.length) { msg.value = '没有启用的代理可检测'; return }
  testing.value = true
  results.value = {}
  msg.value = ''
  try {
    const rs = await grok.testProxyPool(urls)
    rs.forEach((r: TestResult) => { results.value[r.url] = r })
    const ok = rs.filter(r => r.ok).length
    msg.value = `检测完成：${ok}/${rs.length} 可用`
  } catch (e:any) {
    msg.value = e?.response?.data?.error || e.message
  } finally { testing.value = false }
}
function toggleSelect(url: string) {
  const s = new Set(selected.value)
  if (s.has(url)) s.delete(url); else s.add(url)
  selected.value = s
}
function toggleAllOnPage() {
  const all = entries.value.map(e => e.url)
  const allSel = all.every(u => selected.value.has(u))
  selected.value = allSel ? new Set() : new Set(all)
}
function addBlank() {
  entries.value.push({ url: '', enabled: true })
}
// 保存状态（界面常驻显示）。两个 autosave 里任一在保存/失败就以它为准，
// 因为用户关心的是「我这一页的改动安全了吗」，不区分是池子还是出口设置。
const poolState = computed(() => {
  const a = poolAutosave.state.value
  const b = routingAutosave.state.value
  for (const s of ['error', 'saving', 'dirty'] as const) {
    if (a === s || b === s) return s
  }
  return a === 'saved' || b === 'saved' ? 'saved' : 'idle'
})
const saveLabel = computed(() => ({
  idle: '已同步',
  dirty: '未保存',
  saving: '保存中',
  saved: '已保存',
  error: '保存失败',
}[poolState.value]))

async function saveNow() {
  // 直接调页面级保存函数：useAutoSave 只有 arm/flush/mute，
  // 没有 saveNow —— 旧代码调不存在的 saveNow() 会 TypeError，
  // 「立即保存」按钮点了毫无反应。
  await Promise.allSettled([save(), saveRouting()])
}

onMounted(reload)

// 离开本页前把未落盘的改动立刻写掉。
//
// 防抖是 900ms，而「改完最后一个字符就点侧栏导航」远快于此。组件卸载时 Vue 会
// 自动停掉 setup() 里的 watch，此后再没有人触发保存 —— 改动就这么没了。
// 这正是用户报的「切到其它导航再切回来，添加的代理都没了」。
//
// flush() 内部只在有 pending 时才真正保存，所以无改动时零开销。
onBeforeUnmount(async () => {
  await Promise.allSettled([poolAutosave.flush(), routingAutosave.flush()])
})
</script>

<template>
  <div class="vhead">
    <div class="vhead-title">
      <div class="kicker">Proxy Pool</div>
      <h2>代理池</h2>
    </div>
    <div class="stat-chips">
      <span class="schip is-accent"><span class="k">总数</span><b>{{ entries.length }}</b></span>
      <span class="schip is-ok"><span class="k">启用</span><b>{{ enabledCount }}</b></span>
      <span v-if="Object.keys(results).length" class="schip is-ok">
        <span class="k">可用</span><b>{{ Object.values(results).filter(r => r.ok).length }}</b>
      </span>
      <span v-if="Object.values(results).some(r => !r.ok)" class="schip is-bad">
        <span class="k">不可用</span><b>{{ Object.values(results).filter(r => !r.ok).length }}</b>
      </span>
    </div>
    <div class="spacer" />
    <div class="vhead-actions">
      <!-- 保存状态：自动保存最让人不安的地方是「看不出到底存没存」。
           这里给一个常驻锚点，再配一个「立即保存」兜底。 -->
      <span class="savestate" :class="'is-' + poolState">
        <i class="sdot" />{{ saveLabel }}
      </span>
      <button class="btn btn-secondary btn-sm"
              :disabled="poolState === 'saving'" @click="saveNow">
        {{ poolState === 'saving' ? '保存中…' : '立即保存' }}
      </button>
      <button class="btn btn-secondary btn-sm" :disabled="testing" @click="runTest">
        {{ testing ? '检测中…' : '批量检测' }}
      </button>
      <button class="btn btn-secondary btn-sm" @click="reload">重新加载</button>
    </div>
  </div>

  <p v-if="msg" class="vbanner info">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v.01M12 11v5"/></svg>
    <span>{{ msg }}</span>
  </p>

  <div class="vsplit">
    <!-- 左：批量添加 -->
    <div class="vcard">
      <div class="vcard-head">
        <span class="t">批量添加</span>
        <div class="spacer" />
        <span class="note">追加，不覆盖</span>
      </div>
      <textarea
        class="textarea"
        style="min-height:190px;margin:0"
        v-model="draft"
        placeholder="socks5://user:pass@host:1080 #家宽&#10;http://127.0.0.1:7890|备用&#10;socks5://1.2.3.4:1080"
      />
      <div class="row" style="margin-top:10px">
        <button class="btn btn-primary btn-sm" @click="batchAdd">批量添加</button>
        <button class="btn btn-ghost btn-sm" :disabled="!draft" @click="draft = ''">清空输入</button>
        <div class="spacer" />
        <button class="btn btn-ghost btn-sm" @click="addBlank">手动加一行</button>
      </div>
      <details class="vhelp" style="margin-top:12px">
        <summary>格式说明</summary>
        <div class="vhelp-body">
          每行一个代理，支持 <code>socks5://</code> 与 <code>http://</code>。<br />
          备注写在 <code>#</code> 或 <code>|</code> 后面，URL 编码会自动解码：<br />
          <code>socks5://host:1080 #家宽</code><br />
          <code>http://host:7890|备用</code><br /><br />
          添加后自动落盘（约 1 秒防抖），右下角会提示保存结果。
        </div>
      </details>
      <details class="vhelp" style="margin-top:8px">
        <summary>代理怎么被用掉</summary>
        <div class="vhelp-body">
          注册时按<b>并发数</b>取代理：并发 1 就只启用 1 个槽，其余留作储备<b>不预检</b>。
          工作集里的槽被淘汰时才从储备补位 —— 住宅代理按请求/流量计费，
          没必要每次开跑都把整池探一遍。<br />
          「启用」= 参与注册出口候选；「检测」= 连通性 + 出口 IP。<br />
          淘汰阈值、每槽成功上限、粘性复用间隔在<b>设置 / 打码 → 批次调优</b>里配。
        </div>
      </details>
    </div>

    <!-- 右：列表 -->
    <div class="vcard" style="min-width:0">
      <div class="vcard-head">
        <span class="t">代理列表</span>
        <span class="c">{{ entries.length }}</span>
        <div class="spacer" />
        <button class="btn btn-ghost btn-sm" :disabled="!entries.length" @click="toggleAllOnPage">
          {{ entries.length && entries.every(e => selected.has(e.url)) ? '取消全选' : '全选' }}
        </button>
        <button class="btn btn-danger btn-sm" :disabled="!selected.size" @click="deleteSelected">
          删除选中 {{ selected.size || '' }}
        </button>
        <button class="btn btn-ghost btn-sm" :disabled="!entries.length" @click="clearAll">清空</button>
      </div>

      <div class="vtable vscroll-lg">
        <table>
          <thead>
            <tr>
              <th style="width:32px">
                <input
                  type="checkbox"
                  :checked="entries.length > 0 && entries.every(e => selected.has(e.url))"
                  @change="toggleAllOnPage"
                />
              </th>
              <th style="width:40px" title="参与注册出口候选">启</th>
              <th style="min-width:15rem">代理 URL</th>
              <th style="width:118px">备注</th>
              <th style="width:120px">检测</th>
              <!-- 「检测」文字按钮 + ✕ 图标 ≈ 92px；再窄会被挤出单元格 -->
              <th style="width:100px"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(e, i) in entries" :key="i">
              <td>
                <input type="checkbox" :checked="selected.has(e.url)" @change="toggleSelect(e.url)" />
              </td>
              <td><input type="checkbox" :checked="e.enabled" @change="e.enabled = ($event.target as HTMLInputElement).checked; saveFromInput()" /></td>
              <td>
                <!-- WARP 出口的 URL 由容器端口决定，改它只会让条目失效 -->
                <input class="input input-sm mono" v-model="e.url" placeholder="socks5://host:port"
                       :readonly="e.source === 'warp'"
                       :title="e.source === 'warp' ? 'WARP 出口地址由容器决定，不可编辑' : ''"
                       @change="saveFromInput()" />
              </td>
              <td>
                <span v-if="e.source === 'warp'" class="vtag ok warp-tag" title="由 WARP 实例自动并入；到「WARP 代理」页删除容器即会移除">
                  WARP
                </span>
                <input v-else class="input input-sm" v-model="e.note" placeholder="家宽 / 香港" @change="saveFromInput()" />
                <span v-if="e.source === 'warp'" class="note vtrunc warp-note">{{ e.note }}</span>
              </td>
              <td>
                <span v-if="results[e.url]" class="vstate">
                  <span class="vdot" :class="results[e.url].ok ? 'ok' : 'bad'" />
                  <span v-if="results[e.url].ok" class="vtrunc" :title="results[e.url].ip">
                    {{ results[e.url].ip || '通' }}
                  </span>
                  <span v-else>不可用</span>
                  <span v-if="results[e.url].latency_ms" class="note num">{{ results[e.url].latency_ms }}ms</span>
                </span>
                <span v-else class="note">—</span>
              </td>
              <td>
                <div class="vrow-acts">
                  <button class="btn btn-ghost btn-sm" :disabled="testing" @click="testOne(e)">检测</button>
                  <!-- WARP 条目不给删：容器还在跑，删了条目只会造成「以为移除了」的错觉。
                       真正的移除路径是 WARP 页删容器，那边会同步清掉这里。 -->
                  <RouterLink v-if="e.source === 'warp'" class="icon-btn" to="/warp"
                              title="WARP 出口请到「WARP 代理」页删除容器">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M7 17 17 7M9 7h8v8"/></svg>
                  </RouterLink>
                  <button v-else class="icon-btn" title="删除这一行" @click="removeOne(i)">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="!entries.length">
              <td colspan="6" class="vempty">
                <div class="t">代理池是空的</div>
                <div class="d">左侧粘贴代理后点「批量添加」，自动落盘</div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>

  <!-- 出口设置：注册 / 打码 / 入库 三条链路各自的出口。
       后端接口一直存在，但前端此前零调用，所以 global|selected 依赖的
       打码出口池与入库出口池根本没法配。 -->
  <div class="vcard">
    <div class="vcard-head">
      <span class="t">出口设置</span>
      <div class="spacer" />
      <span class="note">注册走上面的代理池；打码与入库可独立指定</span>
    </div>

    <div class="vform vform-2">
      <div>
        <label class="vfield">
          <span class="lb">打码出口</span>
          <select class="input input-sm" v-model="routing.captcha_mode">
            <option v-for="(lb, k) in MODE_LABELS" :key="k" :value="k">{{ lb }}</option>
          </select>
        </label>
        <label v-if="captchaNeedsPool" class="vfield" style="margin-top:9px">
          <span class="lb">打码专属代理池（每行一个）</span>
          <textarea class="textarea" style="min-height:92px;font-size:11.5px" v-model="captchaDraft"
            placeholder="socks5://host:1080" />
        </label>
        <p v-else class="note" style="margin-top:7px">
          当前模式不使用专属池；选 global / selected 才需要填。
        </p>
      </div>

      <div>
        <label class="vfield">
          <span class="lb">入库出口（SSO→OAuth 转换）</span>
          <select class="input input-sm" v-model="routing.import_mode">
            <option v-for="(lb, k) in MODE_LABELS" :key="k" :value="k">{{ lb }}</option>
          </select>
        </label>
        <label v-if="importNeedsPool" class="vfield" style="margin-top:9px">
          <span class="lb">入库专属代理池（每行一个）</span>
          <textarea class="textarea" style="min-height:92px;font-size:11.5px" v-model="importDraft"
            placeholder="socks5://host:1080" />
        </label>
        <p v-else class="note" style="margin-top:7px">
          <b>direct</b> 最稳：注册代理到入库时常已过期，走它反而全失败。
        </p>
      </div>
    </div>

    <details class="vhelp" style="margin-top:11px">
      <summary>三条链路的区别</summary>
      <div class="vhelp-body">
        <ul>
          <li><b>注册出口</b> —— 上面的代理池，按并发数取用</li>
          <li><b>打码出口</b> —— 只影响发给打码引擎的代理。<code>registration</code> 与注册同出口，风控特征最一致</li>
          <li><b>入库出口</b> —— SSO→OAuth 转换用。转换通常发生在注册之后一段时间，
            注册代理可能已失效，所以默认 <code>direct</code>；代理失败时会自动直连回退一次</li>
        </ul>
      </div>
    </details>
  </div>
</template>
