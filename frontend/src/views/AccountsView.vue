<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import * as grok from '@/api/grok'
import * as gw from '@/api/gateway'
import { confirmBox } from '@/utils/confirm'
import { copyWithToast, toast } from '@/utils/clipboard'
import AccountTestModal from '@/components/AccountTestModal.vue'
import AccountDetailModal from '@/components/AccountDetailModal.vue'
import AccountRowMenu from '@/components/AccountRowMenu.vue'

const PAGE_SIZE = 50
type Filter = 'all' | 'imported' | 'pending' | 'oauth' | 'failed'

const accounts = ref<any[]>([])
const selected = ref<string[]>([])
const loading = ref(false)
const exporting = ref(false)
const importing = ref(false)
// 入库进度（异步转换时轮询填充；null = 不显示面板）
const progress = ref<grok.ImportProgress | null>(null)
let progressTimer: any = null
const busyId = ref('')
const q = ref('')
const page = ref(1)
const filter = ref<Filter>('all')
const revealed = reactive<Record<string, boolean>>({})

// 网关分组：筛选 + 批量移动
const groups = ref<gw.GatewayGroup[]>([])
const groupFilter = ref('')
const showMove = ref(false)
const moveTarget = ref('')

const testTarget = ref<any | null>(null)
const detailTarget = ref<any | null>(null)
const menu = reactive<{ acc: any | null; pos: { top: number; left: number } | null }>({ acc: null, pos: null })

const stats = computed(() => {
  const list = accounts.value
  return {
    total: list.length,
    imported: list.filter((a) => a.imported).length,
    pending: list.filter((a) => !a.imported).length,
    oauth: list.filter((a) => a.access_token).length,
    failed: list.filter((a) => a.last_test_status === 'fail').length
  }
})

const filtered = computed(() => {
  let list = accounts.value
  if (filter.value === 'imported') list = list.filter((a) => a.imported)
  else if (filter.value === 'pending') list = list.filter((a) => !a.imported)
  else if (filter.value === 'oauth') list = list.filter((a) => a.access_token)
  else if (filter.value === 'failed') list = list.filter((a) => a.last_test_status === 'fail')
  if (groupFilter.value) list = list.filter((a) => a.group_id === groupFilter.value)

  const s = q.value.trim().toLowerCase()
  if (!s) return list
  // SSO 不再显示在列表，但保留可搜索
  return list.filter(
    (a) =>
      String(a.email || '').toLowerCase().includes(s) ||
      String(a.sso || '').toLowerCase().includes(s) ||
      String(a.note || '').toLowerCase().includes(s) ||
      String(a.proxy || '').toLowerCase().includes(s) ||
      String(a.status || '').toLowerCase().includes(s)
  )
})

const totalPages = computed(() => Math.max(1, Math.ceil(filtered.value.length / PAGE_SIZE)))
const pageItems = computed(() => {
  const p = Math.min(page.value, totalPages.value)
  return filtered.value.slice((p - 1) * PAGE_SIZE, (p - 1) * PAGE_SIZE + PAGE_SIZE)
})
const allSelectedOnPage = computed(
  () => pageItems.value.length > 0 && pageItems.value.every((a) => selected.value.includes(a.id))
)

watch([q, filter, filtered], () => {
  if (page.value > totalPages.value) page.value = totalPages.value
  if (page.value < 1) page.value = 1
})

async function reload() {
  loading.value = true
  try {
    accounts.value = await grok.listAccounts()
    // 丢弃已不存在账号的选中态，避免批量操作打到空 ID
    const live = new Set(accounts.value.map((a) => a.id))
    selected.value = selected.value.filter((id) => live.has(id))
    if (page.value > totalPages.value) page.value = totalPages.value
    // 网关分组列表（筛选/移动用）
    try {
      const g = await gw.getGroups()
      groups.value = g.groups
    } catch { /* 网关不可用时分组功能静默降级 */ }
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || '加载失败', 'bad')
  } finally {
    loading.value = false
  }
}

// 批量移动到网关分组（A 组 → B 组）
async function doMove() {
  if (!selected.value.length) return
  if (!moveTarget.value) {
    toast('请选择目标分组', 'bad')
    return
  }
  const target = groups.value.find((g) => g.id === moveTarget.value)
  try {
    const r = await gw.moveAccounts({
      ids: selected.value,
      group_id: target?.id || '',
      group_name: target?.name || ''
    })
    toast(`已移动 ${r.moved} 个账号到「${target?.name || '未分组'}」`)
    selected.value = []
    showMove.value = false
    moveTarget.value = ''
    await reload()
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || '移动失败', 'bad')
  }
}

/** 就地替换一行，避免整表重载导致滚动位置和选中态抖动。 */
function patchRow(acc: any) {
  if (!acc?.id) return
  const i = accounts.value.findIndex((a) => a.id === acc.id)
  if (i >= 0) accounts.value[i] = { ...accounts.value[i], ...acc }
  if (detailTarget.value?.id === acc.id) detailTarget.value = accounts.value[i] || detailTarget.value
  if (testTarget.value?.id === acc.id) testTarget.value = accounts.value[i] || testTarget.value
}

async function refreshRow(id: string) {
  try {
    patchRow(await grok.getAccount(id))
  } catch {
    await reload()
  }
}

function toggleAllPage() {
  const ids = pageItems.value.map((a) => a.id)
  if (allSelectedOnPage.value) {
    const drop = new Set(ids)
    selected.value = selected.value.filter((id) => !drop.has(id))
  } else {
    const set = new Set(selected.value)
    ids.forEach((id) => set.add(id))
    selected.value = [...set]
  }
}
function toggleOne(id: string) {
  selected.value = selected.value.includes(id)
    ? selected.value.filter((x) => x !== id)
    : [...selected.value, id]
}
function clearSelection() {
  selected.value = []
}

// ---------- 批量 ----------
async function doDelete() {
  if (!selected.value.length) return
  if (!(await confirmBox(`删除 ${selected.value.length} 个账号？此操作不可恢复。`))) return
  try {
    await grok.deleteAccounts(selected.value)
    toast(`已删除 ${selected.value.length} 个账号`)
    selected.value = []
    await reload()
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || '删除失败', 'bad')
  }
}

async function doClear() {
  if (!(await confirmBox('清空全部账号库？不可恢复。'))) return
  try {
    await grok.clearAccounts()
    selected.value = []
    page.value = 1
    toast('账号库已清空')
    await reload()
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || '清空失败', 'bad')
  }
}

// 入库转换（真转换）：SSO→OAuth，成功标已入库，失败保留可重试。
//
// 走异步接口 + 轮询：并发 4、每个账号最多 3 次 attempt²×2s 退避重试，
// 23 个最坏要几分钟。同步等待时界面只有一个转圈按钮，看不出跑到第几个、
// 也分不清是在退避还是卡死。
async function doImport() {
  const ids = selected.value.length
    ? selected.value
    : accounts.value.filter((a) => !a.imported).map((a) => a.id)
  if (!ids.length) {
    toast('没有待入库的账号', 'bad')
    return
  }
  importing.value = true
  try {
    progress.value = await grok.convertAccountsAsync(ids)
    startProgressPoll()
  } catch (e: any) {
    importing.value = false
    toast(e?.response?.data?.error || e.message || '入库失败', 'bad')
  }
}

function stageLabel(stage: string): string {
  return ({
    preparing: '准备中',
    converting: '转换中',
    saving: '写入账号库',
    done: '已完成',
    failed: '失败',
    idle: '空闲',
  } as Record<string, string>)[stage] || stage
}

function startProgressPoll() {
  stopProgressPoll()
  progressTimer = setInterval(async () => {
    try {
      const p = await grok.getImportProgress()
      progress.value = p
      if (!p.running) {
        stopProgressPoll()
        importing.value = false
        toast(p.message || '入库完成', p.stage === 'failed' || p.failed > 0 ? 'bad' : 'ok')
        await reload()
        // 完成后保留面板几秒，让用户看清结果再收起
        setTimeout(() => { if (!progress.value?.running) progress.value = null }, 6000)
      }
    } catch { /* 轮询失败不打断任务，下一拍再试 */ }
  }, 800)
}
function stopProgressPoll() {
  if (progressTimer) { clearInterval(progressTimer); progressTimer = null }
}
async function doExport(format: string) {
  exporting.value = true
  try {
    const res = await grok.exportSaveAs(format, true)
    if (res?.cancelled) {
      toast('已取消导出')
      return
    }
    toast(`已导出到 ${res.path}`)
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || '导出失败', 'bad')
  } finally {
    exporting.value = false
  }
}

async function doCopyExport(format: string) {
  try {
    await grok.copyExport(format)
    toast(`已复制 ${format} 到剪贴板`)
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || '复制失败', 'bad')
  }
}

// ---------- 单账号 ----------
function credLine(a: any) {
  return [a.email, a.password, a.sso, a.sso_rw, a.access_token, a.refresh_token]
    .map((v) => v || '')
    .join('----')
}

async function onMenuAction(name: string, acc: any) {
  switch (name) {
    case 'test':
      testTarget.value = acc
      break
    case 'detail':
      detailTarget.value = acc
      break
    case 'copy-email':
      await copyWithToast(acc.email, '邮箱')
      break
    case 'copy-password':
      await copyWithToast(acc.password, '密码')
      break
    case 'copy-sso':
      await copyWithToast(acc.sso, 'SSO')
      break
    case 'copy-token':
      await copyWithToast(acc.access_token, 'Access Token')
      break
    case 'copy-line':
      await copyWithToast(credLine(acc), '整行凭据')
      break
    case 'verify':
      await verifyOne(acc)
      break
    case 'convert':
      await convertOne(acc)
      break
    case 'toggle-imported':
      await toggleImported(acc)
      break
    case 'delete':
      await deleteOne(acc)
      break
  }
}

async function verifyOne(acc: any) {
  busyId.value = acc.id
  try {
    const res = await grok.verifyAccount(acc.id, false)
    toast(`凭证可用 · ${res?.models ?? 0} 个模型`)
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || '校验失败', 'bad')
  } finally {
    busyId.value = ''
    await refreshRow(acc.id)
  }
}

async function convertOne(acc: any) {
  const label = acc.access_token ? '重取凭证' : '入库转换'
  if (acc.access_token && !(await confirmBox('重新用 SSO 换取 OAuth 凭证？现有 access_token 会被覆盖。'))) return
  busyId.value = acc.id
  try {
    const updated = await grok.refreshAccountOAuth(acc.id, false)
    patchRow(updated)
    toast(`${label}成功`)
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || `${label}失败`, 'bad')
  } finally {
    busyId.value = ''
  }
}

async function toggleImported(acc: any) {
  busyId.value = acc.id
  try {
    patchRow(await grok.updateAccount(acc.id, { imported: !acc.imported }))
    toast(acc.imported ? '已标记未入库' : '已标记已入库')
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || '操作失败', 'bad')
  } finally {
    busyId.value = ''
  }
}

async function deleteOne(acc: any) {
  if (!(await confirmBox(`删除账号 ${acc.email || acc.id}？此操作不可恢复。`))) return
  try {
    await grok.deleteAccounts([acc.id])
    accounts.value = accounts.value.filter((a) => a.id !== acc.id)
    selected.value = selected.value.filter((id) => id !== acc.id)
    if (page.value > totalPages.value) page.value = totalPages.value
    toast('已删除')
  } catch (e: any) {
    toast(e?.response?.data?.error || e.message || '删除失败', 'bad')
  }
}

function openMenu(e: MouseEvent, acc: any) {
  const MENU_W = 208
  const MENU_H = 430
  const pad = 8
  let left = e.clientX
  let top = e.clientY
  if (left + MENU_W + pad > window.innerWidth) left = window.innerWidth - MENU_W - pad
  if (top + MENU_H + pad > window.innerHeight) top = Math.max(pad, window.innerHeight - MENU_H - pad)
  menu.acc = acc
  menu.pos = { top, left }
}
function closeMenu() {
  menu.acc = null
  menu.pos = null
}

function maskPwd(v: string) {
  if (!v) return '-'
  return '•'.repeat(Math.min(10, Math.max(6, v.length)))
}
function shortTime(v: string) {
  return String(v || '').replace('T', ' ').slice(5, 16)
}
/** 去掉 scheme 与凭据后截断；只有真截断了才加省略号。 */
function shortProxy(v: string) {
  const bare = String(v || '').replace(/^\w+:\/\//, '').replace(/^[^@/]*@/, '')
  return bare.length > 16 ? bare.slice(0, 16) + '…' : bare
}
function goPage(p: number) {
  page.value = Math.min(totalPages.value, Math.max(1, p))
}

onUnmounted(stopProgressPoll)

onMounted(reload)
</script>

<template>
  <section class="card page-head-card">
    <div class="page-head">
      <div>
        <div class="kicker">Accounts</div>
        <h2 class="h1">账号管理</h2>
        <p class="sub">邮箱与密码点一下即复制 · 单账号可测试对话、校验凭证、重取凭证</p>
      </div>
      <div class="row">
        <button class="btn btn-secondary" :disabled="loading" @click="reload">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" style="width:15px;height:15px"><path d="M4 12a8 8 0 0 1 13.7-5.6L20 8"/><path d="M20 4v4h-4"/><path d="M20 12a8 8 0 0 1-13.7 5.6L4 16"/><path d="M4 20v-4h4"/></svg>
          {{ loading ? '刷新中…' : '刷新' }}
        </button>
        <button class="btn btn-primary" :disabled="importing" @click="doImport">
          {{ importing ? '入库转换中…' : '入库转换' }}
        </button>
      </div>
    </div>

    <!-- 5 个筛选项：不能用 .g4（固定 4 列），否则第 5 个掉到第二行只占 1/4 宽 -->
    <div class="stat-strip stat-strip-5">
      <button class="stat stat-btn" :class="{ 'is-on': filter === 'all' }" @click="filter = 'all'">
        <div class="k">全部账号</div>
        <div class="v">{{ stats.total }}</div>
      </button>
      <button class="stat stat-btn" :class="{ 'is-on': filter === 'imported' }" @click="filter = 'imported'">
        <div class="k">已入库</div>
        <div class="v ok">{{ stats.imported }}</div>
      </button>
      <!-- 未入库：筛选逻辑早就写好了（filter==='pending'），但这个 chip 一直没渲染，
           所以点不到。注册时若遇上游限流，那个账号会停在未入库（SSO 还在），
           必须能筛出来一键补入库。 -->
      <button class="stat stat-btn" :class="{ 'is-on': filter === 'pending' }" @click="filter = 'pending'">
        <div class="k">未入库</div>
        <div class="v" :class="stats.pending ? 'warn' : ''">{{ stats.pending }}</div>
      </button>
      <button class="stat stat-btn" :class="{ 'is-on': filter === 'oauth' }" @click="filter = 'oauth'">
        <div class="k">有 OAuth 凭证</div>
        <div class="v">{{ stats.oauth }}</div>
      </button>
      <button class="stat stat-btn" :class="{ 'is-on': filter === 'failed' }" @click="filter = 'failed'">
        <div class="k">上次测试失败</div>
        <div class="v" :class="stats.failed ? 'bad' : ''">{{ stats.failed }}</div>
      </button>
    </div>
  </section>

  <section class="card">
    <div class="acct-toolbar">
      <div class="search-wrap">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg>
        <input class="input" v-model="q" placeholder="搜索邮箱 / SSO / 备注 / 代理" @input="page = 1" />
        <button v-if="q" class="icon-btn" data-tip="清空" @click="q = ''; page = 1">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>
        </button>
      </div>
      <select v-if="groups.length" class="input group-filter" v-model="groupFilter" @change="page = 1" title="按网关分组筛选">
        <option value="">全部分组</option>
        <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</option>
      </select>
      <div class="spacer" />
      <div class="row">
        <span class="note">导出</span>
        <button class="btn btn-secondary btn-sm" :disabled="exporting" title="sub2api「导入数据」用：完整 OAuth 凭证 + 注册代理"
          @click="doExport('sub2api')">sub2api</button>
        <button class="btn btn-secondary btn-sm" :disabled="exporting" title="同上但不带代理"
          @click="doExport('sub2api-noproxy')">sub2api 无代理</button>
        <button class="btn btn-secondary btn-sm" :disabled="exporting" title="new-api 渠道数组（xAI type=48，含 CLI 身份头）"
          @click="doExport('newapi')">newapi</button>
        <button class="btn btn-secondary btn-sm" :disabled="exporting" title="纯 SSO 列表，一行一个"
          @click="doExport('sub2api-sso')">SSO 列表</button>
        <button class="btn btn-secondary btn-sm" :disabled="exporting" @click="doExport('full')">完整凭据</button>
        <button class="btn btn-secondary btn-sm" :disabled="exporting" @click="doExport('csv')">CSV</button>
        <button class="btn btn-ghost btn-sm" @click="doCopyExport('sub2api')">复制 sub2api</button>
        <div class="sep" />
        <button class="btn btn-danger btn-sm" @click="doClear">清空库</button>
      </div>
    </div>

    <Transition name="bulk">
      <div v-if="selected.length || progress" class="bulk-bar">
        <template v-if="selected.length">
          <span class="pill run">已选 {{ selected.length }}</span>
          <button class="btn btn-ghost btn-sm" :disabled="importing" @click="clearSelection">取消选择</button>
        </template>

        <!-- 入库实时进度：转换是分钟级操作（并发 4，每个最多 3 次退避重试），
             这里给出「第几个 / 共几个 / 成功 / 失败 / 当前账号 / 耗时」，
             否则用户只能盯着一个转圈按钮猜是不是卡死了。 -->
        <div v-if="progress" class="imp" :class="'is-' + progress.stage">
          <div class="imp-top">
            <span class="imp-stage">
              <span v-if="progress.running" class="imp-spin" />
              {{ stageLabel(progress.stage) }}
            </span>
            <span class="imp-count">{{ progress.done }}/{{ progress.total }}</span>
            <span v-if="progress.success" class="imp-n ok">成功 {{ progress.success }}</span>
            <span v-if="progress.failed" class="imp-n bad">失败 {{ progress.failed }}</span>
            <span v-if="progress.skipped" class="imp-n">跳过 {{ progress.skipped }}</span>
            <span class="imp-el">{{ (progress.elapsed_ms / 1000).toFixed(0) }}s</span>
          </div>
          <div class="imp-track">
            <div class="imp-fill" :class="progress.failed ? 'has-fail' : ''"
                 :style="{ width: progress.total ? (progress.done / progress.total * 100) + '%' : '0%' }" />
          </div>
          <div class="imp-cur">
            <template v-if="progress.running && progress.current">
              正在转换 <b>{{ progress.current }}</b>
            </template>
            <template v-else>{{ progress.message }}</template>
          </div>
          <div v-if="progress.logs?.length" class="imp-logs">
            <div v-for="(l, i) in progress.logs.slice(-3)" :key="i" class="imp-log">{{ l }}</div>
          </div>
        </div>

        <div class="spacer" />
        <template v-if="selected.length">
          <button class="btn btn-secondary btn-sm" :disabled="importing" @click="showMove = true">移动到分组</button>
          <button class="btn btn-primary btn-sm" :disabled="importing" @click="doImport">
            {{ importing ? '入库转换中…' : '入库转换所选' }}
          </button>
          <button class="btn btn-danger btn-sm" :disabled="importing" @click="doDelete">删除所选</button>
        </template>
      </div>
    </Transition>

    <!-- 分组移动弹窗 -->
    <div v-if="showMove" class="modal-mask" @click.self="showMove = false">
      <div class="modal">
        <h3>移动到分组</h3>
        <p class="note">已选 <b>{{ selected.length }}</b> 个账号 → 目标分组（空 = 移出分组）</p>
        <select class="input" v-model="moveTarget" style="width:100%; margin-top:8px">
          <option value="">（移出分组 / 不参与网关）</option>
          <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</option>
        </select>
        <div class="modal-actions">
          <button class="btn btn-primary" @click="doMove">移动</button>
          <button class="btn btn-ghost" @click="showMove = false">取消</button>
        </div>
      </div>
    </div>

    <div class="table-wrap acct-table">
      <table>
        <thead>
          <tr>
            <th class="col-check"><input type="checkbox" :checked="allSelectedOnPage" @change="toggleAllPage" /></th>
            <th class="col-email">邮箱</th>
            <th class="col-pwd">密码</th>
            <th class="col-cred">凭证</th>
            <th class="col-state">状态</th>
            <th class="col-group">分组</th>
            <th class="col-test">上次测试</th>
            <th class="col-proxy">代理</th>
            <th class="col-time">创建</th>
            <th class="col-act">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in pageItems" :key="a.id" :class="{ 'is-busy': busyId === a.id, 'is-sel': selected.includes(a.id) }">
            <td class="col-check"><input type="checkbox" :checked="selected.includes(a.id)" @change="toggleOne(a.id)" /></td>

            <td class="col-email">
              <button class="cell-copy" :title="a.email ? `点击复制 ${a.email}` : '无邮箱'" @click="copyWithToast(a.email, '邮箱')">
                <span class="mono cell-main">{{ a.email || '-' }}</span>
                <svg class="cell-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
              </button>
              <div v-if="a.note" class="cell-note">{{ a.note }}</div>
            </td>

            <td class="col-pwd">
              <div class="pwd-cell">
                <button class="cell-copy" :title="a.password ? '点击复制密码' : '无密码'" @click="copyWithToast(a.password, '密码')">
                  <span class="mono cell-main">{{ revealed[a.id] ? (a.password || '-') : maskPwd(a.password) }}</span>
                </button>
                <button v-if="a.password" class="icon-btn" :title="revealed[a.id] ? '隐藏密码' : '显示密码'"
                  @click.stop="revealed[a.id] = !revealed[a.id]">
                  <svg v-if="revealed[a.id]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
                  <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 5.2A9.8 9.8 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-2.4 3.3M6.5 6.7A17 17 0 0 0 2 12s3.6 7 10 7a9.6 9.6 0 0 0 4-.85"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
                </button>
              </div>
            </td>

            <td class="col-cred">
              <div class="badges">
                <span class="badge" :class="a.sso ? 'is-on' : ''" title="SSO cookie">SSO</span>
                <span class="badge" :class="a.access_token ? 'is-on' : ''" title="OAuth access token">AT</span>
                <span class="badge" :class="a.refresh_token ? 'is-on' : ''" title="OAuth refresh token">RT</span>
              </div>
            </td>

            <td class="col-state">
              <span class="pill" :class="a.imported ? 'run' : ''">{{ a.imported ? '已入库' : '未入库' }}</span>
            </td>

            <td class="col-group">
              <span v-if="a.group_name" class="badge">{{ a.group_name }}</span>
              <span v-else class="note">未分组</span>
            </td>

            <td class="col-test">
              <div v-if="a.last_test_status" class="test-cell" :title="a.last_test_error || ''">
                <span class="dot" :class="a.last_test_status === 'ok' ? 'dot-ok' : 'dot-bad'" />
                <span>{{ a.last_test_status === 'ok' ? `${a.last_test_ms || 0}ms` : '失败' }}</span>
                <span class="cell-note">{{ shortTime(a.last_test_at) }}</span>
              </div>
              <span v-else class="note">未测试</span>
            </td>

            <td class="col-proxy">
              <span v-if="a.proxy" class="mono note" :title="a.proxy">{{ shortProxy(a.proxy) }}</span>
              <span v-else class="note">直连</span>
            </td>

            <td class="col-time"><span class="note">{{ shortTime(a.created_at) }}</span></td>

            <td class="col-act">
              <div class="act-cell">
                <button class="btn btn-secondary btn-sm" :disabled="busyId === a.id" @click="testTarget = a">测试</button>
                <button class="icon-btn" title="更多操作" @click="openMenu($event, a)">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="5" r="1.4"/><circle cx="12" cy="12" r="1.4"/><circle cx="12" cy="19" r="1.4"/></svg>
                </button>
              </div>
            </td>
          </tr>
          <tr v-if="!pageItems.length">
            <td colspan="10" class="empty-cell">
              <div v-if="loading" class="note">加载中…</div>
              <div v-else>
                <div class="empty-title">{{ accounts.length ? '没有匹配的账号' : '账号库还是空的' }}</div>
                <div class="note">{{ accounts.length ? '换个搜索词或筛选条件' : '去「Grok 注册」跑一批，成功的账号会自动入库' }}</div>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="pager">
      <span class="note">共 {{ filtered.length }} 条 · 每页 {{ PAGE_SIZE }} · 本页 {{ pageItems.length }}</span>
      <div class="spacer" />
      <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="goPage(1)">首页</button>
      <button class="btn btn-secondary btn-sm" :disabled="page <= 1" @click="goPage(page - 1)">上一页</button>
      <span class="pill">{{ page }} / {{ totalPages }}</span>
      <button class="btn btn-secondary btn-sm" :disabled="page >= totalPages" @click="goPage(page + 1)">下一页</button>
      <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages" @click="goPage(totalPages)">末页</button>
    </div>
  </section>

  <AccountTestModal :account="testTarget" @close="testTarget = null" @tested="testTarget && refreshRow(testTarget.id)" />
  <AccountDetailModal :account="detailTarget" @close="detailTarget = null" @saved="patchRow($event); detailTarget = null" />
  <AccountRowMenu :account="menu.acc" :position="menu.pos" @close="closeMenu" @action="onMenuAction" />
</template>

<style scoped>
/* 入库实时进度面板（嵌在批量操作条里） */
.imp {
  flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; gap: 4px;
  padding: 6px 12px; margin: 0 4px;
  border-left: 1px solid var(--line); border-right: 1px solid var(--line);
}
.imp-top { display: flex; align-items: center; gap: 9px; flex-wrap: wrap; font-size: 11.5px; }
.imp-stage { display: inline-flex; align-items: center; gap: 5px; font-weight: 600; }
.imp-count { font-family: ui-monospace, monospace; font-weight: 600; font-size: 12.5px; }
.imp-n { color: var(--muted); }
.imp-n.ok { color: var(--ok); }
.imp-n.bad { color: var(--bad); }
.imp-el { margin-left: auto; color: var(--muted); font-family: ui-monospace, monospace; }
.imp-track {
  height: 5px; border-radius: 999px; overflow: hidden;
  background: color-mix(in srgb, var(--line) 70%, transparent);
}
.imp-fill {
  height: 100%; border-radius: 999px; transition: width .35s ease;
  background: linear-gradient(90deg, color-mix(in srgb, var(--ok) 70%, white), var(--ok));
}
.imp-fill.has-fail {
  background: linear-gradient(90deg, color-mix(in srgb, var(--warn) 70%, white), var(--warn));
}
.imp.is-failed .imp-fill { background: var(--bad); }
.imp-cur {
  font-size: 11px; color: var(--muted);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.imp-cur b { color: var(--fg); font-weight: 600; }
.imp-logs { display: flex; flex-direction: column; gap: 1px; }
.imp-log {
  font-family: ui-monospace, SFMono-Regular, monospace; font-size: 10.5px;
  color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.imp-spin {
  width: 10px; height: 10px; border-radius: 50%; flex: 0 0 auto;
  border: 2px solid var(--line); border-top-color: var(--ok);
  animation: impspin .8s linear infinite;
}
@keyframes impspin { to { transform: rotate(360deg); } }

/* 窄屏：进度面板独占一行，避免把按钮挤出去 */
@media (max-width: 1100px) {
  .bulk-bar { flex-wrap: wrap; }
  .imp { flex-basis: 100%; order: 9; border-left: 0; border-right: 0; padding-left: 0; margin: 4px 0 0; }
}

.group-filter { width: 150px; flex: 0 0 auto; }
.modal-mask { position: fixed; inset: 0; background: rgba(0,0,0,0.45); display: flex; align-items: center; justify-content: center; z-index: 60; }
.modal { background: var(--card, #fff); border-radius: 12px; padding: 20px; width: 400px; max-width: 92vw; box-shadow: 0 12px 40px rgba(0,0,0,0.25); }
.modal-actions { display: flex; gap: 10px; margin-top: 16px; justify-content: flex-end; }
.badge { background: color-mix(in srgb, var(--accent, #4f7cff) 12%, transparent); color: var(--accent, #4f7cff); }
</style>
