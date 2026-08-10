<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import * as grok from '@/api/grok'
import { confirmBox } from '@/utils/confirm'

// ── iCloud Privacy Mail 取码平台 ──
// 平台账号登录（cookie 会话）→ Apple 账号 / Hide My Email 随机子邮箱管理
// → 同步到本地邮箱池 → 注册页 email_mode=icloud_api 按账号选号取码。

const baseUrl = ref('')
const apiKey = ref('')
const project = ref('openai')
const panelStatus = ref<any>({ running: false })
const panelBusy = ref(false)

// Apple 官方登录（经内置平台转发）
const appleID = ref('')
const applePassword = ref('')
const twoFAMethod = ref('') // 空=平台默认（受信任设备验证码）
const pendingID = ref('')
const twoFACode = ref('')
const appleStage = ref<'idle' | 'waiting_2fa' | 'done' | 'error'>('idle')
const appleMsg = ref('')
// 收信配置（IMAP 取码登录）：主邮箱 + App 专用密码
const imapEmail = ref('')
const imapAppPassword = ref('')

const busy = ref(false)
const msg = ref('')
const healthText = ref('')

const accounts = ref<any[]>([])
const mailboxes = ref<any[]>([])
const pool = ref<any[]>([])
const selAccounts = ref<Set<string>>(new Set())
const createCount = ref(1)
const createAccountId = ref('')

const poolAvailable = computed(() => pool.value.filter(e => e.status === 'available').length)
const poolUsed = computed(() => pool.value.filter(e => e.status === 'used').length)

function toggleAcc(id: string) {
  const s = new Set(selAccounts.value)
  if (s.has(id)) s.delete(id); else s.add(id)
  selAccounts.value = s
}

async function loadCfg() {
  try {
    const cfg = await grok.getConfig()
    baseUrl.value = cfg.icloud_api_base_url || ''
    apiKey.value = cfg.icloud_api_key ? '***' : ''
    project.value = cfg.icloud_api_project || 'openai'
  } catch { /* 静默 */ }
}
async function saveCfg() {
  busy.value = true
  try {
    await grok.saveConfig({
      icloud_api_base_url: baseUrl.value.trim(),
      icloud_api_key: apiKey.value.trim() || '***',
      icloud_api_project: project.value.trim() || 'openai',
    })
    msg.value = '平台配置已保存'
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally { busy.value = false }
}

// ── 内置平台服务（方案 B）──
async function refreshPanel() {
  try {
    panelStatus.value = await grok.icloudPanelStatus()
    if (panelStatus.value?.running && panelStatus.value?.url && !baseUrl.value) {
      baseUrl.value = panelStatus.value.url
    }
  } catch { /* 静默 */ }
}
async function startPanel() {
  panelBusy.value = true
  try {
    const r = await grok.icloudPanelStart()
    panelStatus.value = r.status
    if (r.url) baseUrl.value = r.url
    msg.value = `内置平台服务已启动：${r.url}`
    await loadAccounts()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally { panelBusy.value = false }
}
async function stopPanel() {
  if (!await confirmBox('停止内置平台服务？Apple 登录态保留在数据目录，重启服务后仍在')) return
  panelBusy.value = true
  try {
    const r = await grok.icloudPanelStop()
    panelStatus.value = r.status
    msg.value = '内置平台服务已停止'
    accounts.value = []
    mailboxes.value = []
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally { panelBusy.value = false }
}

// ── Apple 官方登录 ──
async function appleLogin() {
  const base = baseUrl.value.trim()
  if (!base) { msg.value = '请先启动内置平台服务'; return }
  if (!appleID.value.trim() || !applePassword.value) { msg.value = '请填写 Apple ID 与密码'; return }
  appleStage.value = 'idle'
  appleMsg.value = '登录中…'
  busy.value = true
  try {
    const r = await grok.icloudAppleLoginStart({
      base_url: base,
      apple_id: appleID.value.trim(),
      password: applePassword.value,
      two_factor_method: twoFAMethod.value || undefined,
    })
    if (r?.needs_2fa) {
      pendingID.value = r.pending_id || ''
      appleStage.value = 'waiting_2fa'
      appleMsg.value = `需要 2FA：请在受信任设备允许后，输入 6 位验证码（${r.apple_id || appleID.value}）`
    } else {
      appleStage.value = 'done'
      applePassword.value = ''
      appleMsg.value = 'Apple 登录成功（无需 2FA）'
      await loadAccounts()
    }
  } catch (e: any) {
    appleStage.value = 'error'
    appleMsg.value = e?.response?.data?.error || e.message
  } finally { busy.value = false }
}
async function submit2FA() {
  if (!pendingID.value || twoFACode.value.trim().length < 4) { msg.value = '请输入 6 位验证码'; return }
  busy.value = true
  try {
    await grok.icloudAppleLogin2FA({
      base_url: baseUrl.value.trim(),
      pending_id: pendingID.value,
      code: twoFACode.value.trim(),
    })
    appleStage.value = 'done'
    applePassword.value = ''
    twoFACode.value = ''
    pendingID.value = ''
    appleMsg.value = 'Apple 登录成功（2FA 已验证）'
    // 默认回填主邮箱（IMAP 收信用）
    if (!imapEmail.value && accounts.value[0]) {
      imapEmail.value = accounts.value[0].apple_id || accounts.value[0].label || ''
    }
    await loadAccounts()
  } catch (e: any) {
    appleMsg.value = '2FA 提交失败：' + (e?.response?.data?.error || e.message)
  } finally { busy.value = false }
}

// 保存并检测收信配置：密码框有值时先保存（平台 check 只检测已保存的
// 取码登录，只点检测会报「未保存」——合并成一次点击符合用户直觉）
async function checkIMAP() {
  const base = baseUrl.value.trim()
  if (!base) { msg.value = '请先启动内置平台服务'; return }
  busy.value = true
  try {
    if (imapAppPassword.value.trim()) {
      await grok.icloudSaveIMAPLogin({
        base_url: base,
        account_id: accounts.value[0]?.id,
        email: imapEmail.value.trim() || accounts.value[0]?.apple_id || accounts.value[0]?.label || '',
        app_password: imapAppPassword.value.trim(),
      })
      imapAppPassword.value = ''
      msg.value = '收信配置已保存，正在检测 IMAP…'
    }
    const d = await grok.icloudCheckIMAPLogin({ base_url: base, account_id: accounts.value[0]?.id })
    msg.value = `收信检测：${d?.message || 'IMAP 收信正常，验证码可自动同步'}`
  } catch (e: any) {
    msg.value = '保存/检测失败：' + (e?.response?.data?.error || e.message)
  } finally { busy.value = false }
}

// 自动获取 iCloud 邮箱（用 Apple 登录态调 HME 列表拿转发目标）
async function autoFetchEmail() {
  busy.value = true
  try {
    const d = await grok.icloudForwardTarget()
    if (d?.email) {
      imapEmail.value = d.email
      msg.value = `已自动获取 iCloud 邮箱：${d.email}`
    } else {
      msg.value = '未取到 iCloud 邮箱'
    }
  } catch (e: any) {
    msg.value = '自动获取失败：' + (e?.response?.data?.error || e.message)
  } finally { busy.value = false }
}

// 打开 Apple 生成页（webview 不弹 target=_blank，走后端调系统浏览器）
async function openApplePage() {
  try {
    await grok.icloudOpenApple()
  } catch { /* 静默 */ }
}

// 保存收信配置（主邮箱 + App 专用密码 → 平台 IMAP 取码登录）
async function saveIMAP() {
  const base = baseUrl.value.trim()
  if (!base) { msg.value = '请先启动内置平台服务'; return }
  if (!imapEmail.value.trim() || !imapAppPassword.value.trim()) {
    msg.value = '请填写主 iCloud 邮箱与 App 专用密码'; return
  }
  busy.value = true
  try {
    await grok.icloudSaveIMAPLogin({
      base_url: base,
      account_id: accounts.value[0]?.id,
      email: imapEmail.value.trim(),
      app_password: imapAppPassword.value.trim(),
    })
    imapAppPassword.value = ''
    msg.value = '收信配置已保存：验证码邮件将经 IMAP 自动同步'
  } catch (e: any) {
    msg.value = '保存失败：' + (e?.response?.data?.error || e.message)
  } finally { busy.value = false }
}
// 保存收信配置（主邮箱 + App 专用密码 → 平台 IMAP 取码登录）
async function loadAccounts() {
  if (!baseUrl.value.trim()) return
  try {
    const r = await grok.icloudAccounts(baseUrl.value.trim())
    accounts.value = r.accounts || []
  } catch { accounts.value = [] }
}
async function loadMailboxes() {
  if (!baseUrl.value.trim()) return
  busy.value = true
  try {
    const r = await grok.icloudMailboxes(baseUrl.value.trim())
    mailboxes.value = r.mailboxes || []
    msg.value = `平台邮箱：${mailboxes.value.length} 个`
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally { busy.value = false }
}
async function loadPool() {
  try {
    const p = await grok.icloudPool()
    pool.value = (p as any)?.entries || []
  } catch { pool.value = [] }
}

// 创建 Hide My Email 随机子邮箱（勾选账号各建 createCount 个；未勾选=全部账号）
async function createMailboxes() {
  if (!panelStatus.value?.running) { msg.value = '请先启动内置平台服务'; return }
  busy.value = true
  try {
    const r = await grok.icloudCreateMailboxes({
      base_url: baseUrl.value.trim(),
      account_id: createAccountId.value || undefined,
      count: createCount.value || 1,
    })
    msg.value = `已创建 ${r.created} 个随机子邮箱`
    await loadMailboxes()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally { busy.value = false }
}
async function syncPool() {
  if (!panelStatus.value?.running) { msg.value = '请先启动内置平台服务'; return }
  busy.value = true
  try {
    const r = await grok.icloudSyncPool(baseUrl.value.trim())
    msg.value = `已同步到本地池：新增 ${r.added} 条，共 ${r.total} 条`
    await loadPool()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally { busy.value = false }
}
async function releaseOne(email: string) {
  await grok.icloudPoolRelease(email)
  await loadPool()
  msg.value = `已释放 ${email} 回可用`
}
// 「用完即删」：删除平台 HME（Apple 侧同步删除），数量上限是并发存在数，
// 删除后可再创建 ≈ 无限
async function deleteOne(e: any) {
  if (!await confirmBox(`删除 HME ${e.email}？Apple 侧将同步删除该隐藏邮箱，删除后可重新创建`)) return
  busy.value = true
  try {
    await grok.icloudMailboxDelete({ base_url: baseUrl.value.trim(), id: e.id })
    await loadPool()
    msg.value = `已删除 ${e.email}（Apple 侧同步删除）`
  } catch (err: any) {
    msg.value = err?.response?.data?.error || err.message
  } finally { busy.value = false }
}
async function clearPool() {
  if (!await confirmBox('清空本地邮箱池？注册将从平台重新同步')) return
  await grok.icloudPoolClear()
  pool.value = []
  msg.value = '本地邮箱池已清空'
}

onMounted(async () => {
  await loadCfg()
  await loadPool()
  await refreshPanel()
  // 自动启动内置平台（有配置地址或本地池/登录态数据时）：
  // 重装/重启后无需手动点「启动内置平台」，Apple 登录态自动恢复
  if (!panelStatus.value?.running && (baseUrl.value || pool.value.length)) {
    await startPanel()
    return
  }
  if (baseUrl.value) await loadAccounts()
})
</script>

<template>
  <div class="vhead">
    <div class="vhead-title">
      <div class="kicker">iCloud Privacy Mail</div>
      <h2>iCloud 邮箱</h2>
    </div>
    <div class="stat-chips">
      <span class="schip is-accent"><span class="k">平台账号</span><b>{{ accounts.length }}</b></span>
      <span class="schip is-ok"><span class="k">本地池可用</span><b>{{ poolAvailable }}</b></span>
      <span class="schip"><span class="k">已用</span><b>{{ poolUsed }}</b></span>
    </div>
    <div class="spacer" />
    <div class="vhead-actions">
      <button v-if="!panelStatus.running" class="btn btn-primary btn-sm" :disabled="panelBusy" @click="startPanel">
        {{ panelBusy ? '启动中…' : '启动内置平台' }}
      </button>
      <button v-else class="btn btn-secondary btn-sm" :disabled="panelBusy" @click="stopPanel">停止服务</button>
    </div>
  </div>

  <p v-if="msg" class="vbanner info">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v.01M12 11v5"/></svg>
    <span>{{ msg }}</span>
  </p>

  <div class="vsplit">
    <!-- 左：内置服务 + Apple 官方登录 + 账号 -->
    <div class="vcard">
      <div class="vcard-head">
        <span class="t">Apple 官方登录</span>
        <div class="spacer" />
        <span class="note" :class="{ 'is-ok': panelStatus.running }">
          {{ panelStatus.running ? '服务运行中 ' + (panelStatus.url || '') : '服务未启动' }}
        </span>
      </div>
      <div v-if="!panelStatus.running" class="empty">
        内置 iCloud 平台服务未启动。点击右上角「启动内置平台」——
        服务随桌面版运行，Apple 登录态与邮箱数据保存在本机数据目录。
      </div>
      <template v-else>
        <div class="form-grid" style="grid-template-columns:1.3fr 1.3fr;gap:8px">
          <label class="fld">Apple ID
            <input class="input input-sm" v-model="appleID" placeholder="你的 Apple ID 邮箱" />
          </label>
          <label class="fld">密码
            <input class="input input-sm" v-model="applePassword" type="password" placeholder="只参与登录请求，不落盘" />
          </label>
        </div>
        <div class="row" style="margin-top:8px">
          <button class="btn btn-primary btn-sm" :disabled="busy" @click="appleLogin">登录 Apple</button>
          <span v-if="appleMsg" class="note" :class="{ 'is-bad': appleStage === 'error' }">{{ appleMsg }}</span>
        </div>
        <!-- 2FA 提交 -->
        <div v-if="appleStage === 'waiting_2fa'" class="sub" style="margin-top:10px">
          <div class="sub-head"><span class="sub-t">2FA 验证码</span></div>
          <div class="row">
            <input class="input input-sm" style="width:140px" v-model="twoFACode" placeholder="6 位验证码" maxlength="6" />
            <button class="btn btn-primary btn-sm" :disabled="busy" @click="submit2FA">提交验证码</button>
          </div>
        </div>
        <!-- 收信配置：主 iCloud 邮箱 + App 专用密码（IMAP 取码登录） -->
        <div v-if="accounts.length" class="sub" style="margin-top:10px">
          <div class="sub-head">
            <span class="sub-t">收信配置（IMAP 取码登录）</span>
            <span class="note">平台设计：Apple 登录态只用于创建邮箱，验证码收件走主邮箱 IMAP</span>
          </div>
          <div class="form-grid" style="grid-template-columns:1.3fr 1.3fr auto;gap:8px">
            <label class="fld">主 iCloud 邮箱
              <div class="row" style="gap:6px">
                <input class="input input-sm" v-model="imapEmail" :placeholder="(accounts[0] && (accounts[0].apple_id || accounts[0].label)) || '自动获取…'" />
                <button class="btn btn-ghost btn-sm" type="button" :disabled="busy" @click="autoFetchEmail">自动获取</button>
              </div>
            </label>
            <label class="fld">App 专用密码
              <input class="input input-sm mono" v-model="imapAppPassword" type="password" placeholder="appleid.apple.com 生成，如 xxxx-xxxx-xxxx-xxxx" />
            </label>
            <div class="row" style="align-items:end">
              <button class="btn btn-primary btn-sm" :disabled="busy" @click="checkIMAP">保存并检测</button>
              <a class="btn btn-ghost btn-sm" href="javascript:void(0)" @click="openApplePage">打开 Apple 生成页</a>
            </div>
          </div>
          <p class="hint">App 专用密码：Apple 网页版 → 登录与安全 → App 专用密码 → + 生成一个（仅需此一步手动操作，之后注册全自动）。</p>
        </div>
      </template>
      <hr class="sep" />
      <details class="vhelp">
        <summary>高级：连接外部平台 / 取码参数</summary>
        <div class="vhelp-body">
          <div class="form-grid" style="grid-template-columns:1.5fr 1fr 1fr;gap:8px">
            <label class="fld">平台地址
              <input class="input input-sm" v-model="baseUrl" placeholder="http://127.0.0.1:8787" />
            </label>
            <label class="fld">全局 API Key
              <input class="input input-sm mono" v-model="apiKey" placeholder="星号=保留旧值" />
            </label>
            <label class="fld">项目标识
              <input class="input input-sm" v-model="project" placeholder="openai" />
            </label>
          </div>
          <div class="row" style="margin-top:8px">
            <button class="btn btn-secondary btn-sm" :disabled="busy" @click="saveCfg">保存配置</button>
            <span class="note">地址留空=用内置服务；连接外部平台时填地址+Key</span>
          </div>
        </div>
      </details>
      <div class="vtable vscroll" style="margin-top:10px">
        <table>
          <thead><tr><th style="width:36px">选</th><th>Apple 账号</th><th style="width:90px">邮箱数</th></tr></thead>
          <tbody>
            <tr v-for="a in accounts" :key="a.id">
              <td>
                <input type="checkbox" :checked="selAccounts.has(a.id)" @change="toggleAcc(a.id)" />
              </td>
              <td>
                <span class="mono">{{ a.apple_id || a.label || a.id }}</span>
                <span v-if="a.label" class="note" style="margin-left:6px">{{ a.label }}</span>
              </td>
              <td class="num">{{ (mailboxes.filter(m => m.account_id === a.id)).length }}</td>
            </tr>
            <tr v-if="!accounts.length">
              <td colspan="3" class="vempty">未登录 Apple 或平台无账号 —— 上方登录 Apple 后自动显示</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 右：随机子邮箱 + 本地池 -->
    <div class="vcard" style="min-width:0">
      <div class="vcard-head">
        <span class="t">Hide My Email 随机子邮箱</span>
        <div class="spacer" />
        <button class="btn btn-ghost btn-sm" :disabled="!panelStatus.running || busy" @click="loadMailboxes">同步平台</button>
      </div>
      <div class="row" style="gap:8px;flex-wrap:wrap">
        <label class="fld" style="width:70px">数量
          <input class="input input-sm" v-model.number="createCount" type="number" min="1" max="20" />
        </label>
        <label class="fld" style="flex:1">
          <select class="input input-sm" v-model="createAccountId">
            <option value="">全部账号（各创建 N 个）</option>
            <option v-for="a in accounts" :key="a.id" :value="a.id">{{ a.apple_id || a.label || a.id }}</option>
          </select>
        </label>
        <button class="btn btn-primary btn-sm" :disabled="!panelStatus.running || busy" @click="createMailboxes">创建随机子邮箱</button>
      </div>
      <div class="row" style="margin-top:8px">
        <button class="btn btn-secondary btn-sm" :disabled="!panelStatus.running || busy" @click="syncPool">同步到本地注册池</button>
        <span class="note">注册页选「iCloud 取码平台」后按勾选账号取号</span>
      </div>

      <div class="vtable vscroll-lg" style="margin-top:10px">
        <table>
          <thead><tr><th>邮箱（随机子邮箱）</th><th>账号</th><th style="width:70px">状态</th><th style="width:80px"></th></tr></thead>
          <tbody>
            <tr v-for="e in pool" :key="e.email">
              <td class="mono">{{ e.email }}</td>
              <td class="note">{{ e.account_label || e.account_id || '—' }}</td>
              <td>
                <span class="tag" :class="e.status === 'available' ? 'is-ok' : e.status === 'used' ? 'is-bad' : ''">
                  {{ e.status === 'available' ? '可用' : e.status === 'used' ? '已用' : '禁用' }}
                </span>
              </td>
              <td>
                <button v-if="e.status === 'used'" class="btn btn-ghost btn-xs" @click="releaseOne(e.email)">释放</button>
                <button v-if="e.id" class="btn btn-ghost btn-xs" @click="deleteOne(e)">删除</button>
              </td>
            </tr>
            <tr v-if="!pool.length">
              <td colspan="4" class="vempty">
                本地池为空 —— 登录平台后「同步到本地注册池」，
                <RouterLink class="link" to="/register">注册页</RouterLink> 的邮箱模式选「iCloud 取码平台」即可用。
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="row" style="margin-top:8px">
        <button class="btn btn-ghost btn-sm" :disabled="!pool.length" @click="clearPool">清空本地池</button>
      </div>
    </div>
  </div>
</template>
