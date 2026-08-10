<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import * as edu from '@/api/edu'
import { confirmBox } from '@/utils/confirm'

// ── 邮箱来源：edu（CF 临时邮箱）/ icloud 已迁移至独立导航 IcloudView ──

type Zone = {
  id?: string
  name: string
  status?: string
  email_routing_enabled?: boolean
  email_routing_status?: string
  has_catch_all?: boolean
  catch_all_worker?: string
  probe_error?: string
}

type CFAcc = {
  id: string
  name?: string
  cf_account_id: string
  cf_api_token?: string
  callback_url?: string
  notes?: string
  is_default?: boolean
  created_at?: string
  updated_at?: string
  last_used_at?: string
}

const store = ref<any>({ workers: [] })
const docs = ref<any>({ links: [], prereqs: [], token_perms: [], steps: [] })
const showDocs = ref(false)
const showPerms = ref(true)

// multi CF accounts
const cfStore = ref<{ accounts: CFAcc[]; active_id?: string }>({ accounts: [] })
const activeId = ref('')
const editingId = ref<string | null>(null) // null = new
const showAccountForm = ref(false)
const formBusy = ref(false)
const form = reactive({
  name: '',
  cf_account_id: '',
  cf_api_token: '',
  callback_url: '',
  notes: '',
  is_default: false
})
const showToken = ref(false)

// active resolved secrets (from raw endpoint)
const accountId = ref('')
const apiToken = ref('')
const callbackUrl = ref('')

const zones = ref<Zone[]>([])
const zoneFilter = ref('')
const zoneStatusFilter = ref<'all' | 'not_in_pool' | 'in_pool' | 'cf_open' | 'not_open'>('all')
const zonesLoading = ref(false)
const zonesError = ref('')
const domain = ref('')
const provName = ref('')
const namePrefix = ref('')
const batchSelected = ref<Set<string>>(new Set())
const provisioning = ref(false)
const batchRunning = ref(false)
const importRunning = ref(false)
const lastResult = ref<any>(null)
const genDomain = ref('')
const genCount = ref(5)
const genUseEduSubdomain = ref(true)
const genAddrs = ref<string[]>([])
const workerForm = reactive({
  name: '', domain: '', worker_url: '', api_key: '', enabled: true, use_edu_subdomain: false
})
const busy = ref(false)
const msg = ref('')

const accounts = computed(() => cfStore.value.accounts || [])
const activeAccount = computed(() => accounts.value.find((a) => a.id === activeId.value) || null)
const poolDomains = computed(() => new Set((store.value.workers || []).map((w: any) => String(w.domain || '').toLowerCase())))

function isInPool(z: Zone): boolean {
  return poolDomains.value.has(String(z.name || '').toLowerCase())
}
function isCfOpen(z: Zone): boolean {
  return !!(z.email_routing_enabled || z.has_catch_all)
}
function isProbeUnknown(z: Zone): boolean {
  return z.email_routing_status === 'unknown' || !!z.probe_error
}
function zoneOpenKind(z: Zone): 'in_pool' | 'cf_open' | 'unknown' | 'not_open' {
  if (isInPool(z)) return 'in_pool'
  if (isCfOpen(z)) return 'cf_open'
  if (isProbeUnknown(z)) return 'unknown'
  return 'not_open'
}
function zoneStatusLabel(z: Zone): string {
  const k = zoneOpenKind(z)
  if (k === 'in_pool') return '已在池'
  if (k === 'cf_open') return 'CF已开通'
  if (k === 'unknown') return '状态未知'
  return '未开通'
}
function zoneStatusClass(z: Zone): string {
  const k = zoneOpenKind(z)
  if (k === 'in_pool') return 'pill pill-ok'
  if (k === 'cf_open') return 'pill pill-warn'
  if (k === 'unknown') return 'pill pill-unk'
  return 'pill pill-muted'
}
function zoneStatusTitle(z: Zone): string {
  const parts: string[] = []
  if (isInPool(z)) parts.push('EDU 池')
  if (z.email_routing_enabled) parts.push(`routing=${z.email_routing_status || 'on'}`)
  if (z.has_catch_all) parts.push(`catch_all${z.catch_all_worker ? '=' + z.catch_all_worker : ''}`)
  if (z.probe_error) parts.push(`probe: ${z.probe_error}`)
  else if (z.email_routing_status === 'unknown') parts.push('routing=unknown')
  return parts.join(' · ') || '未开通'
}
function zoneOptionSuffix(z: Zone): string {
  const k = zoneOpenKind(z)
  if (k === 'in_pool') return ' · 已在池'
  if (k === 'cf_open') return ' · CF已开通'
  if (k === 'unknown') return ' · 未知'
  return ''
}
function shortId(id: string) {
  const s = String(id || '')
  if (s.length <= 12) return s
  return s.slice(0, 6) + '…' + s.slice(-4)
}

const filteredZones = computed(() => {
  const q = zoneFilter.value.trim().toLowerCase()
  let list = zones.value || []
  if (q) list = list.filter((z) => String(z.name || '').toLowerCase().includes(q))
  const f = zoneStatusFilter.value
  if (f === 'not_in_pool') list = list.filter((z) => !isInPool(z))
  else if (f === 'in_pool') list = list.filter((z) => isInPool(z))
  else if (f === 'cf_open') list = list.filter((z) => isCfOpen(z) && !isInPool(z))
  else if (f === 'not_open') list = list.filter((z) => !isInPool(z) && !isCfOpen(z) && !isProbeUnknown(z))
  return list
})

const zoneStats = computed(() => {
  const all = zones.value || []
  let inPool = 0, cfOpen = 0, unknown = 0, notOpen = 0
  for (const z of all) {
    const k = zoneOpenKind(z)
    if (k === 'in_pool') inPool++
    else if (k === 'cf_open') cfOpen++
    else if (k === 'unknown') unknown++
    else notOpen++
  }
  return { total: all.length, inPool, cfOpen, unknown, notOpen }
})

async function loadCFAccounts() {
  const st = await edu.listCFAccounts()
  cfStore.value = { accounts: st?.accounts || [], active_id: st?.active_id || '' }
  // migrate legacy localStorage single cred once
  if (!cfStore.value.accounts.length) {
    await migrateLegacyLocalCreds()
  }
  const prefer = cfStore.value.active_id || cfStore.value.accounts.find((a) => a.is_default)?.id || cfStore.value.accounts[0]?.id || ''
  if (prefer) await selectAccount(prefer, false)
  else {
    activeId.value = ''
    accountId.value = ''
    apiToken.value = ''
    callbackUrl.value = ''
  }
}

async function migrateLegacyLocalCreds() {
  const saved = localStorage.getItem('umbraforge.edu.creds')
  if (!saved) return
  try {
    const j = JSON.parse(saved)
    if (!j.accountId || !j.apiToken) return
    await edu.upsertCFAccount({
      name: '迁移账号',
      cf_account_id: j.accountId,
      cf_api_token: j.apiToken,
      callback_url: j.callbackUrl || '',
      is_default: true
    })
    localStorage.removeItem('umbraforge.edu.creds')
    const st = await edu.listCFAccounts()
    cfStore.value = { accounts: st?.accounts || [], active_id: st?.active_id || '' }
    msg.value = '已迁移本地浏览器凭证到永久账号库'
  } catch {}
}

async function selectAccount(id: string, announce = true) {
  activeId.value = id
  try {
    await edu.activateCFAccount(id)
  } catch {}
  const raw = await edu.getCFAccountRaw(id)
  accountId.value = raw?.cf_account_id || ''
  apiToken.value = raw?.cf_api_token || ''
  callbackUrl.value = raw?.callback_url || ''
  const st = await edu.listCFAccounts()
  cfStore.value = { accounts: st?.accounts || [], active_id: st?.active_id || id }
  zones.value = []
  if (announce) msg.value = `已切换账号：${raw?.name || shortId(raw?.cf_account_id || id)}`
}

function openNewAccount() {
  editingId.value = null
  Object.assign(form, { name: '', cf_account_id: '', cf_api_token: '', callback_url: '', notes: '', is_default: accounts.value.length === 0 })
  showToken.value = true
  showAccountForm.value = true
}

function openEditAccount(a: CFAcc) {
  editingId.value = a.id
  Object.assign(form, {
    name: a.name || '',
    cf_account_id: a.cf_account_id || '',
    cf_api_token: a.cf_api_token || '', // masked
    callback_url: a.callback_url || '',
    notes: a.notes || '',
    is_default: !!a.is_default
  })
  showToken.value = false
  showAccountForm.value = true
}

function cancelForm() {
  showAccountForm.value = false
  editingId.value = null
}

async function saveAccount() {
  if (!form.cf_account_id.trim()) {
    msg.value = 'Account ID 不能为空'
    return
  }
  if (!editingId.value && !form.cf_api_token.trim()) {
    msg.value = '新建账号需要填写 API Token'
    return
  }
  formBusy.value = true
  try {
    const body: any = {
      name: form.name.trim(),
      cf_account_id: form.cf_account_id.trim(),
      cf_api_token: form.cf_api_token,
      callback_url: form.callback_url.trim(),
      notes: form.notes.trim(),
      is_default: form.is_default
    }
    if (editingId.value) body.id = editingId.value
    const out = await edu.upsertCFAccount(body)
    showAccountForm.value = false
    await loadCFAccounts()
    if (out?.id) await selectAccount(out.id, false)
    msg.value = editingId.value ? '账号已更新（永久保存）' : '账号已添加（永久保存）'
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    formBusy.value = false
  }
}

async function removeAccount(a: CFAcc) {
  if (!await confirmBox(`删除账号「${a.name || a.cf_account_id}」？此操作不可恢复。`)) return
  try {
    await edu.deleteCFAccount(a.id)
    if (activeId.value === a.id) {
      activeId.value = ''
      accountId.value = ''
      apiToken.value = ''
      callbackUrl.value = ''
      zones.value = []
    }
    await loadCFAccounts()
    msg.value = '账号已删除'
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  }
}

async function reload() {
  store.value = await edu.listEdu()
  docs.value = await edu.eduDocs()
  await loadCFAccounts()
}

async function loadZones(silent = false) {
  if (!activeId.value) {
    if (!silent) msg.value = '请先添加并选择一个 Cloudflare 账号'
    return
  }
  // refresh raw secrets
  try {
    const raw = await edu.getCFAccountRaw(activeId.value)
    accountId.value = raw?.cf_account_id || ''
    apiToken.value = raw?.cf_api_token || ''
    callbackUrl.value = raw?.callback_url || ''
  } catch (e: any) {
    if (!silent) msg.value = e?.response?.data?.error || e.message
    return
  }
  if (!accountId.value.trim() || !apiToken.value.trim()) {
    if (!silent) msg.value = '当前账号缺少 Account ID 或 Token，请编辑补全'
    return
  }
  zonesLoading.value = true
  zonesError.value = ''
  try {
    const data = await edu.listZones({
      cf_account_id: accountId.value.trim(),
      cf_api_token: apiToken.value.trim(),
      probe_email_routing: true
    })
    zones.value = Array.isArray(data) ? data : (data?.zones || [])
    if (!silent) {
      msg.value = `账号 ${activeAccount.value?.name || shortId(accountId.value)} · 域名 ${zones.value.length} · 已在池 ${zoneStats.value.inPool} · CF已开通未入池 ${zoneStats.value.cfOpen}`
    }
    if (zones.value.length === 1 && !domain.value) domain.value = zones.value[0].name
  } catch (e: any) {
    zonesError.value = e?.response?.data?.error || e.message
    if (!silent) msg.value = zonesError.value
  } finally {
    zonesLoading.value = false
  }
}

function toggleBatch(name: string) {
  const s = new Set(batchSelected.value)
  if (s.has(name)) s.delete(name)
  else s.add(name)
  batchSelected.value = s
}
function selectNotInPool() {
  const s = new Set<string>()
  for (const z of filteredZones.value) if (!isInPool(z)) s.add(z.name)
  batchSelected.value = s
}
function selectNotOpen() {
  const s = new Set<string>()
  for (const z of filteredZones.value) {
    if (!isInPool(z) && !isCfOpen(z) && !isProbeUnknown(z)) s.add(z.name)
  }
  batchSelected.value = s
}
function selectCfOpenNotInPool() {
  const s = new Set<string>()
  for (const z of filteredZones.value) {
    if (isCfOpen(z) && !isInPool(z)) s.add(z.name)
  }
  batchSelected.value = s
}
function clearBatch() { batchSelected.value = new Set() }

async function ensureCreds(): Promise<boolean> {
  if (!activeId.value) {
    msg.value = '请先选择 Cloudflare 账号'
    return false
  }
  const raw = await edu.getCFAccountRaw(activeId.value)
  accountId.value = raw?.cf_account_id || ''
  apiToken.value = raw?.cf_api_token || ''
  callbackUrl.value = raw?.callback_url || ''
  if (!accountId.value || !apiToken.value) {
    msg.value = '当前账号凭证不完整'
    return false
  }
  return true
}

async function provisionOne() {
  if (!domain.value) return
  if (!(await ensureCreds())) return
  provisioning.value = true
  msg.value = ''
  try {
    lastResult.value = await edu.provision({
      domain: domain.value,
      name: provName.value || domain.value,
      cf_account_id: accountId.value,
      cf_api_token: apiToken.value,
      callback_url: callbackUrl.value,
      sync_edu: true
    })
    msg.value = lastResult.value?.ok ? '开通成功并已同步 EDU 池' : (lastResult.value?.error || '开通失败')
    await reload()
    await loadZones(true)
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    provisioning.value = false
  }
}

async function provisionBatch() {
  if (!batchSelected.value.size) return
  if (!(await ensureCreds())) return
  const targets = [...batchSelected.value].filter((name) => !poolDomains.value.has(name.toLowerCase()))
  if (!targets.length) {
    msg.value = '所选域名均已在 EDU 池，无需重复入池'
    return
  }
  batchRunning.value = true
  msg.value = ''
  try {
    const res = await edu.provisionBatch({
      domains: targets,
      cf_account_id: accountId.value,
      cf_api_token: apiToken.value,
      callback_url: callbackUrl.value,
      name_prefix: namePrefix.value
    })
    const skipped = batchSelected.value.size - targets.length
    msg.value = `批量完成 ok=${res.ok} fail=${res.fail}` + (skipped ? ` · 跳过已在池 ${skipped}` : '')
    batchSelected.value = new Set()
    await reload()
    await loadZones(true)
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    batchRunning.value = false
  }
}




function currentZone() {
  return zones.value.find((z) => z.name === domain.value) || ({ name: domain.value } as Zone)
}
function singlePrimaryLabel() {
  if (provisioning.value || importRunning.value) return '处理中…'
  const z = currentZone()
  if (isInPool(z)) return '重新开通并同步'
  if (isCfOpen(z)) return '仅入池（已开通，不重开）'
  return '一键开通并入池'
}
async function handleSingleDomain() {
  const z = currentZone()
  if (isCfOpen(z) && !isInPool(z)) return importOne()
  return provisionOne()
}

async function importOne() {
  if (!domain.value) return
  if (!(await ensureCreds())) return
  importRunning.value = true
  msg.value = ''
  try {
    lastResult.value = await edu.importOpen({
      domain: domain.value,
      name: provName.value || domain.value,
      cf_account_id: accountId.value,
      cf_api_token: apiToken.value,
      callback_url: callbackUrl.value
    })
    msg.value = lastResult.value?.ok ? '已直接入池（未重新开通 CF）' : (lastResult.value?.error || '入池失败')
    await reload()
    await loadZones(true)
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    importRunning.value = false
  }
}

async function importBatch() {
  if (!batchSelected.value.size) return
  if (!(await ensureCreds())) return
  // only CF-open, not already in pool
  const targets = [...batchSelected.value].filter((name) => {
    const z = zones.value.find((x) => x.name === name)
    if (!z) return !poolDomains.value.has(name.toLowerCase())
    return isCfOpen(z) && !isInPool(z)
  })
  if (!targets.length) {
    msg.value = '没有可「仅入池」的域名：请选择状态为 CF已开通 且未在池的域名'
    return
  }
  importRunning.value = true
  msg.value = ''
  try {
    const res = await edu.importOpenBatch({
      domains: targets,
      cf_account_id: accountId.value,
      cf_api_token: apiToken.value,
      callback_url: callbackUrl.value,
      name_prefix: namePrefix.value
    })
    const skipped = batchSelected.value.size - targets.length
    msg.value = `仅入池完成 ok=${res.ok} fail=${res.fail}` + (skipped ? ` · 跳过 ${skipped}（非已开通或已在池）` : '')
    batchSelected.value = new Set()
    await reload()
    await loadZones(true)
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    importRunning.value = false
  }
}

async function smartBatch() {
  // CF已开通 → 仅入池；未开通 → 全量开通；已在池跳过
  if (!batchSelected.value.size) return
  if (!(await ensureCreds())) return
  const toImport: string[] = []
  const toProvision: string[] = []
  for (const name of batchSelected.value) {
    const z = zones.value.find((x) => x.name === name)
    if (!z) {
      toProvision.push(name)
      continue
    }
    if (isInPool(z)) continue
    if (isCfOpen(z)) toImport.push(name)
    else if (!isProbeUnknown(z)) toProvision.push(name)
  }
  if (!toImport.length && !toProvision.length) {
    msg.value = '所选域名均已在池或状态未知，无需处理'
    return
  }
  batchRunning.value = true
  importRunning.value = true
  msg.value = ''
  try {
    let ok = 0, fail = 0
    if (toImport.length) {
      const res = await edu.importOpenBatch({
        domains: toImport,
        cf_account_id: accountId.value,
        cf_api_token: apiToken.value,
        callback_url: callbackUrl.value,
        name_prefix: namePrefix.value
      })
      ok += res.ok || 0
      fail += res.fail || 0
    }
    if (toProvision.length) {
      const res = await edu.provisionBatch({
        domains: toProvision,
        cf_account_id: accountId.value,
        cf_api_token: apiToken.value,
        callback_url: callbackUrl.value,
        name_prefix: namePrefix.value
      })
      ok += res.ok || 0
      fail += res.fail || 0
    }
    msg.value = `处理完成 入池/开通 ok=${ok} fail=${fail}（已开通→仅入池 ${toImport.length}，未开通→开通 ${toProvision.length}）`
    batchSelected.value = new Set()
    await reload()
    await loadZones(true)
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    batchRunning.value = false
    importRunning.value = false
  }
}

async function saveWorker() {
  busy.value = true
  try {
    await edu.upsertWorker({ ...workerForm })
    Object.assign(workerForm, { name: '', domain: '', worker_url: '', api_key: '', enabled: true, use_edu_subdomain: false })
    await reload()
    msg.value = 'Worker 已保存'
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    busy.value = false
  }
}
async function toggleWorker(w: any) {
  try {
    await edu.upsertWorker({ ...w, enabled: !w.enabled })
    await reload()
    msg.value = !w.enabled ? '已启用' : '已停用'
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  }
}
async function toggleUseEdu(w: any) {
  try {
    await edu.upsertWorker({ ...w, use_edu_subdomain: !w.use_edu_subdomain })
    await reload()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  }
}
async function removeWorker(id: string) {
  if (!await confirmBox('删除该 Worker？')) return
  await edu.deleteWorker(id)
  await reload()
}
async function gen() {
  genAddrs.value = await edu.generateAddresses({
    domain: genDomain.value || domain.value,
    count: genCount.value,
    use_edu_subdomain: genUseEduSubdomain.value
  }) || []
}
async function copyText(t: string) {
  await navigator.clipboard.writeText(t)
  msg.value = '已复制'
}

watch(activeId, () => { /* zones cleared in selectAccount */ })

onMounted(() => { reload() })
</script>

<template>
  <div class="vhead">
    <div class="vhead-title">
      <div class="kicker">Mailbox</div>
      <h2>EDU 邮箱</h2>
    </div>
    <div class="stat-chips">
      <span class="schip is-accent"><span class="k">CF 账号</span><b>{{ accounts.length }}</b></span>
      <span class="schip is-ok"><span class="k">EDU 池</span><b>{{ store.workers?.length || 0 }}</b></span>
      <template v-if="zones.length">
        <span class="schip clickable" :class="{ 'is-on': zoneStatusFilter === 'all' }" @click="zoneStatusFilter = 'all'">
          <span class="k">域名</span><b>{{ zoneStats.total }}</b>
        </span>
        <span class="schip clickable is-ok" :class="{ 'is-on': zoneStatusFilter === 'in_pool' }" @click="zoneStatusFilter = 'in_pool'">
          <span class="k">已在池</span><b>{{ zoneStats.inPool }}</b>
        </span>
        <span class="schip clickable is-accent" :class="{ 'is-on': zoneStatusFilter === 'cf_open' }" @click="zoneStatusFilter = 'cf_open'">
          <span class="k">待入池</span><b>{{ zoneStats.cfOpen }}</b>
        </span>
        <span class="schip clickable" :class="{ 'is-on': zoneStatusFilter === 'not_open' }" @click="zoneStatusFilter = 'not_open'">
          <span class="k">未开通</span><b>{{ zoneStats.notOpen }}</b>
        </span>
      </template>
    </div>
    <div class="spacer" />
    <div class="vhead-actions">
      <button class="btn btn-secondary btn-sm" :disabled="!activeId || zonesLoading" @click="loadZones(false)">
        {{ zonesLoading ? '探测中…' : '加载并探测' }}
      </button>
      <button class="btn btn-ghost btn-sm" @click="showDocs = !showDocs">{{ showDocs ? '收起说明' : '使用说明' }}</button>
    </div>
  </div>

  <p v-if="msg" class="vbanner info">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v.01M12 11v5"/></svg>
    <span>{{ msg }}</span>
  </p>
  <p v-if="zonesError" class="vbanner bad">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 9v4M12 17v.01"/><circle cx="12" cy="12" r="9"/></svg>
    <span>{{ zonesError }}</span>
  </p>

  <!-- 邮箱来源（edu / icloud_api）已迁移：iCloud 请使用独立导航「iCloud」页 -->

  <div v-if="showDocs" class="vcard">
    <div class="vcard-head">
      <span class="t">使用说明</span>
      <div class="spacer" />
      <button class="btn btn-ghost btn-sm" @click="showPerms = !showPerms">{{ showPerms ? '收起 Token 权限' : 'Token 权限' }}</button>
      <button class="icon-btn" title="关闭" @click="showDocs = false">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>
      </button>
    </div>
    <div class="vsplit">
      <div>
        <ol class="vnote" style="padding-left:1.2rem;margin:0">
          <li v-for="(s, i) in (docs.steps || [])" :key="i">{{ s }}</li>
          <li>可添加多个 CF 账号，凭证永久存在本机 Application Support，不是浏览器缓存</li>
          <li>切换账号后重新「加载并探测」；已开通未入池可批量入池</li>
        </ol>
        <div class="row" style="margin-top:10px">
          <a v-for="d in (docs.links || [])" :key="d.url" class="btn btn-ghost btn-sm"
            :href="d.url" target="_blank" rel="noopener noreferrer" :title="d.desc">{{ d.title }} ↗</a>
        </div>
      </div>
      <div v-if="showPerms" class="vtable vscroll-md">
        <table>
          <thead><tr><th>权限</th><th>用途</th></tr></thead>
          <tbody>
            <tr v-for="pm in (docs.token_perms || [])" :key="pm.name">
              <td class="mono">{{ pm.name }}</td>
              <td class="note">{{ pm.why }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>

  <!-- 主区：左 CF 账号 + 单域名操作，右 域名列表 -->
  <div class="vsplit-wide">
    <div class="vstack">
      <!-- CF 账号库 -->
      <div class="vcard">
        <div class="vcard-head">
          <span class="t">Cloudflare 账号</span>
          <span class="c">{{ accounts.length }}</span>
          <div class="spacer" />
          <a class="btn btn-ghost btn-sm" href="https://dash.cloudflare.com/" target="_blank" rel="noopener">Dashboard ↗</a>
          <button class="btn btn-primary btn-sm" @click="openNewAccount">＋ 添加</button>
        </div>

        <div v-if="!accounts.length && !showAccountForm" class="vempty" style="padding:22px">
          <div class="t">还没有 CF 账号</div>
          <div class="d">添加后可随时切换使用</div>
          <button class="btn btn-primary btn-sm" style="margin-top:11px" @click="openNewAccount">添加第一个</button>
        </div>

        <div v-else class="acc-list">
          <div v-for="a in accounts" :key="a.id" class="acc-row" :class="{ active: a.id === activeId }"
            @click="selectAccount(a.id)">
            <span class="vdot" :class="a.id === activeId ? 'ok' : ''" />
            <div style="min-width:0;flex:1">
              <div class="acc-name vtrunc">
                {{ a.name || '未命名' }}
                <span v-if="a.is_default && a.id !== activeId" class="vtag" style="margin-left:5px">默认</span>
              </div>
              <div class="mono note vtrunc" :title="a.cf_account_id">{{ shortId(a.cf_account_id) }}</div>
            </div>
            <div class="acc-acts" @click.stop>
              <button class="icon-btn" title="编辑" @click="openEditAccount(a)">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 20h4L20 8l-4-4L4 16z"/></svg>
              </button>
              <button class="icon-btn" title="删除" @click="removeAccount(a)">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M5 7h14M10 4h4M6 7l1 13h10l1-13"/></svg>
              </button>
            </div>
          </div>
        </div>

        <!-- 凭证解析结果：accountId 来自 raw 接口，非空才说明 Token 真的读出来了。
             上面那行只表示「选中了哪个账号」，这里表示「凭证可用」，不是重复信息。 -->
        <div v-if="activeAccount" class="acc-active" :class="{ 'is-bad': !accountId }">
          <span class="vdot" :class="accountId ? 'ok' : 'bad'" />
          <span class="note">{{ accountId ? '凭证已就绪' : '凭证未解析' }}</span>
          <span v-if="accountId" class="mono note vtrunc" :title="accountId">{{ shortId(accountId) }}</span>
          <div class="spacer" />
          <button class="btn btn-ghost btn-sm" @click="openEditAccount(activeAccount)">编辑当前</button>
        </div>

        <div v-if="showAccountForm" class="acc-form">
          <div class="vcard-head" style="margin-bottom:10px">
            <span class="t">{{ editingId ? '编辑账号' : '新建账号' }}</span>
          </div>
          <div class="vform vform-2">
            <label class="vfield">
              <span class="lb">显示名称</span>
              <input class="input input-sm" v-model="form.name" placeholder="主号 / 客户A" />
            </label>
            <label class="vfield">
              <span class="lb">Account ID</span>
              <input class="input input-sm mono" v-model="form.cf_account_id" placeholder="32 位十六进制" />
            </label>
          </div>
          <label class="vfield" style="margin-top:9px">
            <span class="lb">API Token</span>
            <div class="vinput-group">
              <input class="input input-sm mono" v-model="form.cf_api_token" :type="showToken ? 'text' : 'password'"
                :placeholder="editingId ? '留空表示不修改' : 'Bearer Token'" />
              <button type="button" class="icon-btn" :title="showToken ? '隐藏' : '显示'" @click="showToken = !showToken">
                <svg v-if="showToken" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
                <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M3 3l18 18"/><path d="M10.6 5.2A9.8 9.8 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-2.4 3.3M6.5 6.7A17 17 0 0 0 2 12s3.6 7 10 7a9.6 9.6 0 0 0 4-.85"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
              </button>
            </div>
          </label>
          <label class="vfield" style="margin-top:9px">
            <span class="lb">CALLBACK_URL（可选）</span>
            <input class="input input-sm mono" v-model="form.callback_url" placeholder="https://your.host/api/v1/admin/edu-email/callback" />
          </label>
          <label class="vfield" style="margin-top:9px">
            <span class="lb">备注</span>
            <input class="input input-sm" v-model="form.notes" placeholder="可选" />
          </label>
          <label class="vfield-inline" style="margin-top:6px">
            <input type="checkbox" v-model="form.is_default" /><span>设为默认账号</span>
          </label>
          <div class="row" style="margin-top:11px">
            <button class="btn btn-primary btn-sm" :disabled="formBusy" @click="saveAccount">
              {{ formBusy ? '提交中…' : (editingId ? '更新账号' : '添加账号') }}
            </button>
            <button class="btn btn-ghost btn-sm" @click="cancelForm">取消</button>
          </div>
          <p class="note" style="margin-top:8px">写入本机 <code>Application Support/UmbraForge/settings.json</code>，重启后仍在。</p>
        </div>
      </div>

      <!-- 单域名开通 -->
      <div class="vcard">
        <div class="vcard-head">
          <span class="t">单域名开通 / 入池</span>
        </div>
        <label class="vfield">
          <span class="lb">选择域名</span>
          <select class="input input-sm mono" v-model="domain">
            <option value="">— 请选择 —</option>
            <option v-for="z in filteredZones" :key="z.id || z.name" :value="z.name">
              {{ z.name }}{{ zoneOptionSuffix(z) }}
            </option>
          </select>
        </label>
        <div class="vform vform-2" style="margin-top:9px">
          <label class="vfield">
            <span class="lb">名称（可选）</span>
            <input class="input input-sm" v-model="provName" placeholder="可选" />
          </label>
          <label class="vfield">
            <span class="lb">筛选域名</span>
            <input class="input input-sm" v-model="zoneFilter" placeholder="过滤 zones" />
          </label>
        </div>
        <div class="row" style="margin-top:11px">
          <button class="btn btn-primary btn-sm"
            :disabled="provisioning || importRunning || !domain || !activeId" @click="handleSingleDomain">
            {{ singlePrimaryLabel() }}
          </button>
          <button v-if="domain && isCfOpen(currentZone()) && !isInPool(currentZone())"
            class="btn btn-secondary btn-sm" :disabled="provisioning || importRunning || !activeId" @click="provisionOne">
            强制重开
          </button>
        </div>
        <p class="note" style="margin-top:8px">
          <b>CF已开通</b> 默认「仅入池」：只同步 Worker URL / API_KEY 到本地池，不重装 Email Routing。
        </p>
        <div v-if="lastResult" class="res-box">
          <div class="vstate" style="font-weight:700">
            <span class="vdot" :class="lastResult.ok ? 'ok' : 'bad'" />
            {{ lastResult.ok ? '成功' : '失败' }} · {{ lastResult.domain }}
          </div>
          <div v-for="(s, i) in (lastResult.steps || [])" :key="i" class="note" style="margin-top:2px">
            {{ s.ok ? '✓' : '✗' }} {{ s.step }} {{ s.detail || '' }}
          </div>
        </div>
      </div>
    </div>

    <!-- 右：域名列表 + 批量 -->
    <div class="vcard" style="min-width:0">
      <div class="vcard-head">
        <span class="t">域名</span>
        <span class="c">{{ filteredZones.length }}</span>
        <span v-if="batchSelected.size" class="vtag warn">已选 {{ batchSelected.size }}</span>
        <div class="spacer" />
        <select class="input input-sm" style="width:auto" v-model="zoneStatusFilter">
          <option value="all">全部状态</option>
          <option value="not_in_pool">未入库</option>
          <option value="in_pool">已在池</option>
          <option value="cf_open">CF已开通未入池</option>
          <option value="not_open">未开通</option>
        </select>
      </div>

      <div class="row" style="gap:6px;margin-bottom:10px">
        <button class="btn btn-ghost btn-sm" @click="selectNotInPool">选未入库</button>
        <button class="btn btn-ghost btn-sm" @click="selectNotOpen">选未开通</button>
        <button class="btn btn-ghost btn-sm" @click="selectCfOpenNotInPool">选待入池</button>
        <button class="btn btn-ghost btn-sm" :disabled="!batchSelected.size" @click="clearBatch">清空</button>
        <div class="spacer" />
        <input class="input input-sm" style="width:7rem" v-model="namePrefix" placeholder="名称前缀" />
      </div>

      <Transition name="bulk">
        <div v-if="batchSelected.size" class="bulk-bar" style="margin-top:0;margin-bottom:10px">
          <span class="pill run">已选 {{ batchSelected.size }}</span>
          <div class="spacer" />
          <button class="btn btn-secondary btn-sm"
            :disabled="importRunning || batchRunning || !activeId" @click="importBatch">
            {{ importRunning && !batchRunning ? '入池中…' : '仅入池' }}
          </button>
          <button class="btn btn-secondary btn-sm"
            :disabled="batchRunning || importRunning || !activeId" @click="provisionBatch">
            {{ batchRunning && !importRunning ? '开通中…' : '开通' }}
          </button>
          <button class="btn btn-primary btn-sm"
            :disabled="batchRunning || importRunning || !activeId" @click="smartBatch">
            {{ batchRunning || importRunning ? '处理中…' : '智能处理' }}
          </button>
        </div>
      </Transition>

      <div class="vtable vscroll-lg">
        <table>
          <thead>
            <tr>
              <th style="width:36px"></th>
              <th>域名</th>
              <th style="width:104px">状态</th>
              <th>详情</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="z in filteredZones" :key="z.id || z.name" class="zone-row"
              :class="{ selected: batchSelected.has(z.name) }" @click="toggleBatch(z.name)">
              <td @click.stop>
                <input type="checkbox" :checked="batchSelected.has(z.name)" @change="toggleBatch(z.name)" />
              </td>
              <td class="mono">{{ z.name }}</td>
              <td><span :class="zoneStatusClass(z)" :title="zoneStatusTitle(z)">{{ zoneStatusLabel(z) }}</span></td>
              <td class="note vtrunc" style="max-width:16rem" :title="zoneStatusTitle(z)">{{ zoneStatusTitle(z) }}</td>
            </tr>
            <tr v-if="!filteredZones.length">
              <td colspan="4" class="vempty">
                <div class="t">{{ zonesLoading ? '正在探测…' : (activeId ? '还没探测域名' : '先选一个 CF 账号') }}</div>
                <div class="d">{{ activeId ? '点右上角「加载并探测」' : '左侧添加并点选账号' }}</div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>

  <!-- 底部：EDU 池 + 工具 -->
  <div class="vsplit-wide">
    <div class="vcard">
      <div class="vcard-head">
        <span class="t">工具</span>
      </div>
      <details class="vhelp" open>
        <summary>手动添加 Worker</summary>
        <div class="vhelp-body" style="background:transparent;border:0;padding:0;margin-top:9px">
          <div class="vform vform-2">
            <label class="vfield">
              <span class="lb">名称</span>
              <input class="input input-sm" v-model="workerForm.name" placeholder="名称" />
            </label>
            <label class="vfield">
              <span class="lb">域名</span>
              <input class="input input-sm mono" v-model="workerForm.domain" placeholder="example.edu" />
            </label>
            <label class="vfield" style="grid-column:1/-1">
              <span class="lb">Worker URL</span>
              <input class="input input-sm mono" v-model="workerForm.worker_url" placeholder="https://xxx.workers.dev" />
            </label>
            <label class="vfield" style="grid-column:1/-1">
              <span class="lb">API Key</span>
              <input class="input input-sm mono" v-model="workerForm.api_key" placeholder="API Key" />
            </label>
          </div>
          <div class="row" style="margin-top:8px;gap:14px">
            <label class="vfield-inline"><input type="checkbox" v-model="workerForm.enabled" /><span>启用</span></label>
            <label class="vfield-inline"><input type="checkbox" v-model="workerForm.use_edu_subdomain" /><span>用 @edu.域名</span></label>
          </div>
          <button class="btn btn-primary btn-sm" style="margin-top:10px" :disabled="busy" @click="saveWorker">
            添加 Worker
          </button>
        </div>
      </details>

      <details class="vhelp" style="margin-top:10px">
        <summary>生成测试地址</summary>
        <div class="vhelp-body" style="background:transparent;border:0;padding:0;margin-top:9px">
          <div class="vform vform-2">
            <label class="vfield">
              <span class="lb">域名</span>
              <input class="input input-sm mono" v-model="genDomain" :placeholder="domain || 'example.edu'" />
            </label>
            <label class="vfield">
              <span class="lb">数量</span>
              <input class="input input-sm" v-model.number="genCount" type="number" min="1" max="100" />
            </label>
          </div>
          <label class="vfield-inline" style="margin-top:6px">
            <input type="checkbox" v-model="genUseEduSubdomain" /><span>用 @edu.域名</span>
          </label>
          <button class="btn btn-secondary btn-sm" style="margin-top:9px" @click="gen">生成</button>
          <div v-if="genAddrs.length" class="gen-list">
            <button v-for="a in genAddrs" :key="a" class="gen-item mono" :title="'点击复制 ' + a" @click="copyText(a)">
              {{ a }}
            </button>
          </div>
        </div>
      </details>
    </div>

    <div class="vcard" style="min-width:0">
      <div class="vcard-head">
        <span class="t">EDU 池</span>
        <span class="c">{{ store.workers?.length || 0 }}</span>
        <div class="spacer" />
        <span class="note">注册页的邮箱来源</span>
      </div>
      <div class="vtable vscroll-md">
        <table>
          <thead>
            <tr>
              <th>Worker</th>
              <!-- 124px 才够「启用 + @edu」横排；再窄会换行把行高撑成两倍 -->
              <th style="width:124px">状态</th>
              <!-- 「停用」文字按钮 + 3 个图标按钮 ≈ 150px；给 112px 会被
                   td 的 overflow:hidden 直接裁掉按钮 -->
              <th style="width:152px"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="w in store.workers || []" :key="w.id">
              <td style="min-width:0">
                <div style="font-weight:680" class="vtrunc">{{ w.name || w.domain }}</div>
                <div class="mono note vtrunc" :title="w.domain + ' · ' + w.worker_url">
                  {{ w.domain }} · {{ w.worker_url }}
                </div>
              </td>
              <td>
                <!-- 横排：竖排会把行高撑成两倍，列表密度下降 -->
                <div style="display:flex;gap:4px;flex-wrap:wrap">
                  <span class="vtag" :class="w.enabled ? 'ok' : ''">{{ w.enabled ? '启用' : '停用' }}</span>
                  <span v-if="w.use_edu_subdomain" class="vtag warn">@edu</span>
                </div>
              </td>
              <td>
                <div class="vrow-acts">
                  <button class="btn btn-ghost btn-sm" @click="toggleWorker(w)">{{ w.enabled ? '停用' : '启用' }}</button>
                  <button class="icon-btn" :title="w.use_edu_subdomain ? '关闭 @edu' : '开启 @edu'" @click="toggleUseEdu(w)">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M3 12h18M12 3v18"/></svg>
                  </button>
                  <button class="icon-btn" title="复制 Worker URL" @click="copyText(w.worker_url || '')">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
                  </button>
                  <button class="icon-btn" title="从池中移除" @click="removeWorker(w.id)">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M5 7h14M10 4h4M6 7l1 13h10l1-13"/></svg>
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="!(store.workers || []).length">
              <td colspan="3" class="vempty">
                <div class="t">EDU 池是空的</div>
                <div class="d">探测后勾选「待入池」的域名，点「智能处理」</div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* CF 账号：紧凑行式，替代原来的大卡片网格 */
.acc-list { display: flex; flex-direction: column; gap: 5px; max-height: 15rem; overflow: auto; }
.acc-row {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 8px 10px;
  border-radius: 11px;
  border: 1px solid var(--line);
  background: var(--panel-solid);
  cursor: pointer;
  transition: .14s ease;
}
.acc-row:hover { border-color: color-mix(in srgb, var(--accent) 45%, var(--line)); }
.acc-row.active {
  border-color: color-mix(in srgb, var(--accent) 60%, transparent);
  background: color-mix(in srgb, var(--panel-solid) 82%, var(--accent-soft));
}
.acc-name { font-size: 12.5px; font-weight: 700; }
.acc-acts { display: flex; gap: 2px; flex: none; }

.acc-form {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--line);
}

.acc-active {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-top: 9px;
  padding: 7px 10px;
  border-radius: 10px;
  background: var(--accent-soft);
  border: 1px solid color-mix(in srgb, var(--accent) 22%, transparent);
  font-size: 12px;
  min-width: 0;
}
.acc-active strong { font-size: 12.5px; }
.acc-active .spacer { flex: 1 1 auto; }
.acc-active.is-bad {
  background: color-mix(in srgb, var(--bad) 9%, transparent);
  border-color: color-mix(in srgb, var(--bad) 26%, transparent);
}

.res-box {
  margin-top: 11px;
  padding: 10px 12px;
  border-radius: 11px;
  border: 1px solid var(--line);
  background: color-mix(in srgb, var(--ink) 3%, transparent);
  max-height: 11rem;
  overflow: auto;
}

.zone-row { cursor: pointer; }
.zone-row.selected { background: color-mix(in srgb, var(--accent) 12%, transparent) !important; }

.gen-list { display: flex; flex-direction: column; gap: 4px; margin-top: 9px; max-height: 12rem; overflow: auto; }
.gen-item {
  text-align: left;
  padding: 6px 9px;
  border-radius: 8px;
  border: 1px solid var(--line);
  background: var(--panel-solid);
  color: var(--ink-2);
  font-size: 11.5px;
  cursor: pointer;
  transition: .12s ease;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.gen-item:hover { border-color: color-mix(in srgb, var(--accent) 50%, var(--line)); color: var(--accent); }

/* 域名状态胶囊（保留原语义配色） */
.pill-ok { background: color-mix(in srgb, #16a34a 18%, transparent); color: #15803d; border-color: transparent; }
.pill-warn { background: color-mix(in srgb, #d97706 20%, transparent); color: #b45309; border-color: transparent; }
.pill-unk { background: color-mix(in srgb, #7c3aed 18%, transparent); color: #6d28d9; border-color: transparent; }
.pill-muted { background: color-mix(in srgb, #6b7280 16%, transparent); color: var(--ink-2); border-color: transparent; }
[data-theme="dark-gold"] .pill-ok { color: #86efac; }
[data-theme="dark-gold"] .pill-warn { color: #fcd34d; }
[data-theme="dark-gold"] .pill-unk { color: #c4b5fd; }
</style>
