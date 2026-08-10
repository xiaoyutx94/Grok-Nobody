<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import * as home from '@/api/home'
import { confirmBox } from '@/utils/confirm'
import { useAutoSave } from '@/utils/autosave'
import * as plugins from '@/api/plugins'
import { getProxyPool } from '@/api/grok'

const caps = ref<any>({})
const warps = ref<any[]>([])
const poolCount = ref(0)
const msg = ref('')
const busy = ref('')
const deployCount = ref(1)
const usePlus = ref(false)
const customKey = ref('')
const probing = ref(false)
const autoSaving = ref(false)
const keyMeta = ref<any>(null)
const globalProxy = ref({
  enabled: false,
  url: '',
  apply_to_warp: true,
  apply_to_app: true,
  chain_register: false
})
const gpBusy = ref(false)
let timer: any
let saveTimer: any

const activeLicense = computed(() => (usePlus.value ? (customKey.value || '').trim() : ''))
const runningCount = computed(() => warps.value.filter((w) => w.status === 'running').length)

function statusText(w: any) {
  if (w?.status_label) return w.status_label
  const s = String(w?.status || '')
  if (s === 'running') return '运行中'
  if (s === 'exited' || s === 'dead') return '已停止'
  if (s === 'missing') return '不存在'
  if (s === 'docker_unavailable') return 'Docker不可用'
  if (s === 'docker_down') return 'Docker未启动'
  if (s === 'starting' || s === 'restarting' || s === 'created') return '启动中'
  return s || '未知'
}
function statusClass(w: any) {
  const s = String(w?.status || '')
  if (s === 'running') return 'run'
  if (s === 'missing' || s === 'exited' || s === 'dead' || s === 'docker_down' || s === 'docker_unavailable') return 'bad'
  return ''
}
function ipNote(w: any) {
  const m = String(w?.message || '')
  if (w?.public_ip) {
    if (m === 'via_warp_socks' || m === 'via_warp_container_socks') return 'WARP出口IP'
    if (m === 'via_container_proxy') return 'WARP出口IP'
    if (m === 'via_proxy' || m === 'ip_ok' || m === 'ip_refreshed' || m === 'recreated') return 'WARP出口IP'
    if (m === 'container_egress') return '容器直出(非WARP，忽略)'
    return m || 'WARP出口IP'
  }
  if (m === 'warp_tunnel_pending' || m === 'wg_handshake_timeout') return 'WARP隧道未就绪(需总代理/等握手)'
  if (m === 'waiting_ip') return '分配/探测中…'
  if (m === 'ip_probe_failed') return '探测失败'
  return m || '等待中'
}

async function reloadBase() {
  caps.value = await home.getCapabilities()
  warps.value = await home.listWarp()
  try {
    const pool: any = await getProxyPool()
    poolCount.value = (pool?.urls || []).length
  } catch {
    poolCount.value = 0
  }
}

async function reloadKeys() {
  const cat: any = await home.getWarpKeys(false)
  const sel = String(cat?.selected_key || '').trim()
  if (sel) {
    usePlus.value = true
    customKey.value = sel
  } else {
    usePlus.value = false
    customKey.value = ''
  }
  const infos = (cat?.custom_infos || []) as any[]
  keyMeta.value = sel ? (infos.find((x) => x.key === sel) || null) : null
}

async function reload() {
  try {
    await reloadBase()
    await reloadKeys()
    try {
      const gp: any = await home.getGlobalProxy()
      if (gp) globalProxy.value = {
        enabled: !!gp.enabled,
        url: gp.url || '',
        apply_to_warp: gp.apply_to_warp !== false,
        apply_to_app: gp.apply_to_app !== false,
        chain_register: !!gp.chain_register
      }
    } catch { /* ignore */ }
    // 服务端配置填充完毕后才武装自动保存，避免用本地默认值覆盖服务端
    gpAutosave.arm()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  }
}

function scheduleAutoSave() {
  if (saveTimer) clearTimeout(saveTimer)
  saveTimer = setTimeout(() => void autoSave(), 350)
}

async function autoSave() {
  autoSaving.value = true
  try {
    const key = activeLicense.value
    await home.saveWarpKeySelection({
      mode: key ? 'custom' : 'free',
      key,
      remember_custom: false,
    })
    msg.value = key ? '已启用 Warp+（自动保存）' : '已使用免费 WARP'
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    autoSaving.value = false
  }
}

function setFree() {
  usePlus.value = false
  customKey.value = ''
  keyMeta.value = null
  scheduleAutoSave()
}
function setPlus() {
  usePlus.value = true
  scheduleAutoSave()
}
function onKeyInput() {
  usePlus.value = true
  scheduleAutoSave()
}

async function softProbe() {
  const key = activeLicense.value
  if (!key || probing.value) return
  probing.value = true
  try {
    keyMeta.value = await home.probeWarpKey(key)
    msg.value = keyMeta.value?.ok ? 'Key 可用' : (keyMeta.value?.error || 'Key 探测完成')
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    probing.value = false
  }
}

async function saveGlobalProxy() {
  gpBusy.value = true
  msg.value = ''
  try {
    const out: any = await home.saveGlobalProxy({ ...globalProxy.value })
    globalProxy.value = {
      enabled: !!out.enabled,
      url: out.url || '',
      apply_to_warp: out.apply_to_warp !== false,
      apply_to_app: out.apply_to_app !== false,
      chain_register: !!out.chain_register
    }
    msg.value = out.enabled
      ? '总代理已启用：WARP 容器出网将经此代理，注册仍用 WARP 出口 IP'
      : '总代理已关闭'
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    gpBusy.value = false
  }
}


// 总代理是纯配置项：改动自动落盘，不再需要「保存总代理」按钮。
// saveGlobalProxy 内部会用服务端返回值回填 globalProxy，所以必须靠 mute
// 抑制回填带来的二次触发（否则 watch → save → 回填 → watch 无限循环）。
const gpAutosave = useAutoSave(
  globalProxy,
  saveGlobalProxy,
  { okMessage: '总代理已保存', label: '总代理' },
)

async function deploy() {
  busy.value = 'deploy'
  msg.value = ''
  try {
    await autoSave()
    const lic = activeLicense.value
    const created = await home.deployWarp(deployCount.value, lic)
    msg.value = lic
      ? `已部署 ${created?.length || deployCount.value} 个 Warp+，并写入代理池`
      : `已部署 ${created?.length || deployCount.value} 个免费 WARP，并写入代理池`
    await reloadBase()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    busy.value = ''
  }
}

async function restartAll() {
  busy.value = 'restart'
  msg.value = ''
  try {
    await home.restartWarpAll()
    msg.value = '已尝试启动/重启全部 WARP 容器，并同步代理池'
    await reloadBase()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    busy.value = ''
  }
}

async function syncPool() {
  busy.value = 'sync'
  msg.value = ''
  try {
    const data: any = await home.syncWarpProxyPool()
    msg.value = `已同步到代理池 · 新增 ${data?.added ?? 0} · 池内共 ${data?.total_pool ?? 0}`
    await reloadBase()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    busy.value = ''
  }
}

async function refreshAll() {
  busy.value = 'refresh'
  try {
    await home.refreshWarp('all')
    msg.value = '已刷新全部出口 IP'
    await reloadBase()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    busy.value = ''
  }
}

async function refreshOne(name: string) {
  busy.value = name
  try {
    await home.refreshWarp(name)
    await reloadBase()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    busy.value = ''
  }
}

function shortProxy(u: string) {
  const s = String(u || '')
  if (!s) return '—'
  if (s.length <= 28) return s
  return s.slice(0, 14) + '…' + s.slice(-10)
}
function modeLabel(m: string) {
  if (m === 'custom') return '独立'
  if (m === 'none') return '直连'
  return '默认'
}
async function applyUpstream(w: any, recreate = true) {
  busy.value = 'up-' + w.name
  msg.value = ''
  try {
    const mode = w.upstream_proxy_mode || 'inherit'
    await home.updateWarpUpstream(w.name, {
      upstream_proxy_mode: mode,
      upstream_proxy: mode === 'custom' ? (w.upstream_proxy || '') : '',
      recreate
    })
    msg.value = recreate
      ? `${w.name} 上游已保存并重建（${modeLabel(mode)}）`
      : `${w.name} 上游已保存`
    await reloadBase()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    busy.value = ''
  }
}
async function applyDefaultToAll() {
  if (!warps.value.length) return
  if (!await confirmBox('全部实例改为「跟随默认总代理」并重建？')) return
  busy.value = 'up-all'
  msg.value = ''
  try {
    for (const w of warps.value) {
      await home.updateWarpUpstream(w.name, { upstream_proxy_mode: 'inherit', recreate: true })
    }
    msg.value = '已全部设为跟随默认总代理并重建'
    await reloadBase()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message
  } finally {
    busy.value = ''
  }
}
async function removeOne(name: string) {
  if (!await confirmBox(`删除 ${name}？`)) return
  await home.removeWarp(name)
  await reloadBase()
}

async function installDocker() {
  busy.value = 'install-docker'
  msg.value = ''
  try {
    const data: any = await plugins.ensureDocker()
    msg.value = data?.message || 'Docker 安装完成，请启动 Docker Desktop 后重试'
    await reloadBase()
  } catch (e: any) {
    msg.value = e?.response?.data?.error || e.message || 'Docker 安装失败'
    await reloadBase()
  } finally {
    busy.value = ''
  }
}

async function uninstallDocker() {
  if (!await confirmBox('将卸载本机 Docker，并清理 Grok-Nobody WARP 容器。是否继续？')) return
  const all = await confirmBox('是否删除 Docker 中全部容器？\n确定=全部；取消=仅 umbra-warp')
  busy.value = 'uninstall-docker'
  msg.value = ''
  try {
    const data: any = await home.uninstallDocker(!!all)
    msg.value = data?.hint || (data?.steps || []).join(' · ') || 'Docker 卸载完成'
    await maybeCleanInstallers(data?.installer_files)
    await reloadBase()
  } catch (e: any) {
    const data = e?.response?.data?.data
    msg.value = (e?.response?.data?.error || e.message) + (data?.steps ? `；${(data.steps || []).join(' · ')}` : '')
    await reloadBase()
  } finally {
    busy.value = ''
  }
}

// 卸载成功后弹窗询问是否删除残留的安装器下载文件。
async function maybeCleanInstallers(files: any[] | undefined) {
  if (!Array.isArray(files) || files.length === 0) return
  const totalMB = files.reduce((s, f) => s + Number(f?.size_mb || 0), 0)
  const ok = await confirmBox(
    `卸载完成。发现 ${files.length} 个已下载的 Docker 安装文件（共 ${totalMB.toFixed(0)} MB），是否删除以释放空间？`
  )
  if (!ok) return
  try {
    const r: any = await home.cleanDockerInstallers(false)
    msg.value += ` · 已删除 ${r?.removed || 0} 个安装文件（释放 ${Number(r?.freed_mb || 0).toFixed(0)} MB）`
  } catch (e: any) {
    msg.value += ` · 清理安装文件失败：${e?.message || e}`
  }
}

onMounted(async () => {
  await reload()
  timer = setInterval(async () => {
    try { await reloadBase() } catch { /* ignore */ }
  }, 3000)
})
onUnmounted(() => {
  clearInterval(timer)
  if (saveTimer) clearTimeout(saveTimer)
})
</script>

<template>
  <div class="vhead">
    <div class="vhead-title">
      <div class="kicker">Network</div>
      <h2>WARP 代理</h2>
    </div>
    <div class="stat-chips">
      <span class="schip" :class="runningCount ? 'is-ok' : ''">
        <span class="k">运行中</span><b>{{ runningCount }}/{{ warps.length }}</b>
      </span>
      <span class="schip is-accent"><span class="k">代理池</span><b>{{ poolCount }}</b></span>
      <span class="schip" :class="caps.docker ? 'is-ok' : 'is-bad'">
        <span class="k">Docker</span><b>{{ caps.docker ? '可用' : '未就绪' }}</b>
      </span>
      <span class="schip"><span class="k">主机</span><b>{{ caps.os }}/{{ caps.arch }}</b></span>
    </div>
    <div class="spacer" />
    <div class="vhead-actions">
      <RouterLink class="btn btn-ghost btn-sm" to="/proxy-pool">代理池</RouterLink>
      <button v-if="!caps.docker" class="btn btn-secondary btn-sm" :disabled="!!busy" @click="installDocker">
        {{ busy === 'install-docker' ? '安装中…' : '安装 Docker' }}
      </button>
      <button v-else class="btn btn-ghost btn-sm" :disabled="!!busy" @click="uninstallDocker">卸载 Docker</button>
    </div>
  </div>

  <p v-if="msg" class="vbanner info">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v.01M12 11v5"/></svg>
    <span>{{ msg }}{{ autoSaving ? ' · 保存中…' : '' }}</span>
  </p>

  <div class="vsplit-wide">
    <!-- 左：部署 + Key + 总代理 -->
    <div class="vstack">
      <div class="vcard">
        <div class="vcard-head">
          <span class="t">部署</span>
          <div class="spacer" />
          <span class="note">部署 → 启动 → 同步池</span>
        </div>
        <div class="row" style="gap:8px">
          <label class="vfield" style="width:78px;flex:none">
            <span class="lb">数量</span>
            <input class="input input-sm" type="number" min="1" max="10" v-model.number="deployCount" />
          </label>
          <div style="flex:1;min-width:0;display:flex;align-items:flex-end;gap:6px;flex-wrap:wrap">
            <button class="btn btn-primary btn-sm" :disabled="!!busy || !caps.docker" @click="deploy">
              {{ busy === 'deploy' ? '部署中…' : '一键部署' }}
            </button>
            <button class="btn btn-secondary btn-sm" :disabled="!!busy || !warps.length" @click="restartAll">
              {{ busy === 'restart' ? '启动中…' : '启动/重启' }}
            </button>
            <button class="btn btn-secondary btn-sm" :disabled="!!busy || !warps.length" @click="syncPool">
              {{ busy === 'sync' ? '同步中…' : '同步到池' }}
            </button>
            <button class="btn btn-ghost btn-sm" :disabled="!!busy || !warps.length" @click="refreshAll">刷新 IP</button>
          </div>
        </div>

        <div class="vseg" style="margin-top:12px">
          <button :class="{ 'is-on': !usePlus }" @click="setFree">免费 WARP</button>
          <button :class="{ 'is-on': usePlus }" @click="setPlus">Warp+</button>
        </div>
        <label class="vfield" style="margin-top:9px">
          <span class="lb">Warp+ Key（可选）</span>
          <div class="vinput-group">
            <input class="input input-sm mono" type="password" autocomplete="off" :disabled="!usePlus"
              placeholder="默认免费，留空即可" v-model="customKey" @input="onKeyInput" />
            <button class="btn btn-ghost btn-sm" :disabled="!activeLicense || probing" @click="softProbe">
              {{ probing ? '探测中' : '探测' }}
            </button>
            <button class="icon-btn" title="清空并改回免费" @click="setFree">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>
            </button>
          </div>
        </label>
        <!-- 固定 2×2：左栏只有 ~430px，auto-fit 会排成 3+1 留下孤儿卡 -->
        <div class="vmetrics" style="margin-top:10px;grid-template-columns:1fr 1fr">
          <div class="vmetric">
            <div class="k">模式</div>
            <div class="v">{{ activeLicense ? 'Warp+' : '免费' }}</div>
          </div>
          <div class="vmetric">
            <div class="k">Key</div>
            <div class="v mono" :title="activeLicense">
              {{ activeLicense ? (activeLicense.slice(0, 6) + '…' + activeLicense.slice(-4)) : '—' }}
            </div>
          </div>
          <div class="vmetric">
            <div class="k">设备</div>
            <div class="v">
              {{ keyMeta?.device_count >= 0 ? (keyMeta.device_count + '/' + (keyMeta.device_limit || 5)) : (keyMeta?.error ? '满' : '—') }}
            </div>
          </div>
          <div class="vmetric">
            <div class="k">额度</div>
            <div class="v" :title="keyMeta?.premium_human || keyMeta?.quota_human || keyMeta?.error || ''">
              {{ activeLicense ? (keyMeta?.premium_human || keyMeta?.quota_human || keyMeta?.error || '待探测') : '免费' }}
            </div>
          </div>
        </div>
      </div>

      <div class="vcard">
        <div class="vcard-head">
          <span class="t">总代理</span>
          <span class="vtag" :class="globalProxy.enabled ? 'ok' : ''">{{ globalProxy.enabled ? '已启用' : '未启用' }}</span>
          <div class="spacer" />
          <span class="note">容器出网前置</span>
        </div>
        <label class="vfield">
          <span class="lb">总代理 URL</span>
          <input class="input input-sm mono" v-model="globalProxy.url"
            placeholder="socks5://127.0.0.1:7890 或 http://user:pass@host:8080" />
        </label>
        <div class="row" style="margin-top:8px;gap:14px">
          <label class="vfield-inline"><input type="checkbox" v-model="globalProxy.enabled" /><span>启用</span></label>
          <label class="vfield-inline"><input type="checkbox" v-model="globalProxy.apply_to_warp" /><span>注入容器</span></label>
          <label class="vfield-inline"><input type="checkbox" v-model="globalProxy.apply_to_app" /><span>管理流量</span></label>
        </div>
        <div class="row" style="margin-top:10px">
          <button class="btn btn-secondary btn-sm" :disabled="!!busy || !warps.length" @click="restartAll">
            按新总代理重启
          </button>
        </div>
        <details class="vhelp" style="margin-top:11px">
          <summary>链路说明（别搞反）</summary>
          <div class="vhelp-body">
            <ol>
              <li>WARP 容器建隧道 / 向 CF 要 IP：<b>经总代理</b>（注入容器 ALL_PROXY）</li>
              <li>探测出口 IP 与注册流量：<b>只经 WARP 本地 socks</b>（127.0.0.1:端口），所以看到的是 <b>WARP IP</b>，不是总代理 IP</li>
              <li>不会用容器直连 IP 冒充 WARP 出口</li>
            </ol>
            <br />
            改完总代理必须「启动/重启」或重新部署，环境变量才会进容器。<br />
            状态显示「不存在」通常是找不到 docker 命令，完全退出 App 再打开。
          </div>
        </details>
      </div>
    </div>

    <!-- 右：实例列表 -->
    <div class="vcard" style="min-width:0">
      <div class="vcard-head">
        <span class="t">实例</span>
        <span class="c">{{ warps.length }}</span>
        <div class="spacer" />
        <button class="btn btn-ghost btn-sm" :disabled="!!busy || !warps.length" @click="applyDefaultToAll">
          全部跟随默认
        </button>
        <button class="btn btn-ghost btn-sm" :disabled="!!busy || !warps.length" @click="restartAll">全部重建</button>
      </div>

      <div class="vtable vscroll-lg">
        <table>
          <thead>
            <tr>
              <!-- 右栏约 704px（1440 窗口）：定宽合计控制在 ~600px，
                   给「实例」列留 ~100px，否则 fixed 布局会溢出 -->
              <th style="min-width:88px">实例</th>
              <th style="width:126px">SOCKS</th>
              <th style="width:84px">状态</th>
              <th style="width:114px">出口 IP</th>
              <th style="width:176px">上游</th>
              <th style="width:92px"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="w in warps" :key="w.id || w.name">
              <td>
                <div class="mono" style="font-weight:700">{{ w.name }}</div>
                <div class="note">:{{ w.port }}<span v-if="w.has_plus"> · Plus</span></div>
              </td>
              <td class="mono vtrunc" :title="w.proxy_url">{{ w.proxy_url }}</td>
              <td>
                <span class="vstate" :title="w.status">
                  <span class="vdot" :class="statusClass(w) === 'run' ? 'ok' : (statusClass(w) === 'bad' ? 'bad' : 'warn')" />
                  {{ statusText(w) }}
                </span>
              </td>
              <td>
                <div class="mono" style="font-weight:660">{{ w.public_ip || '—' }}</div>
                <div class="note vtrunc" :title="ipNote(w)">{{ ipNote(w) }}</div>
              </td>
              <td>
                <select class="input input-sm" v-model="w.upstream_proxy_mode">
                  <option value="inherit">跟随默认</option>
                  <option value="custom">本实例独立</option>
                  <option value="none">直连 CF</option>
                </select>
                <input v-if="w.upstream_proxy_mode === 'custom'" class="input input-sm mono" style="margin-top:4px"
                  v-model="w.upstream_proxy" placeholder="socks5://host:port" />
                <div class="note" style="margin-top:3px">
                  <span v-if="w.upstream_proxy_mode === 'none'">直连</span>
                  <span v-else-if="w.upstream_proxy_mode === 'custom'">{{ shortProxy(w.upstream_proxy) }}</span>
                  <span v-else>{{ globalProxy.enabled ? shortProxy(globalProxy.url) : '默认未启用' }}</span>
                  <span v-if="w.effective_upstream"> · 现 {{ shortProxy(w.effective_upstream) }}</span>
                </div>
              </td>
              <td>
                <div class="vrow-acts">
                  <button class="btn btn-secondary btn-sm" :disabled="!!busy" @click="applyUpstream(w, true)">应用</button>
                  <button class="icon-btn" title="刷新出口 IP" :disabled="!!busy" @click="refreshOne(w.name)">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12a8 8 0 0 1 13.7-5.6L20 8"/><path d="M20 4v4h-4"/><path d="M20 12a8 8 0 0 1-13.7 5.6L4 16"/><path d="M4 20v-4h4"/></svg>
                  </button>
                  <button class="icon-btn" title="删除实例" @click="removeOne(w.name)">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M5 7h14M10 4h4M6 7l1 13h10l1-13"/></svg>
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="!warps.length">
              <td colspan="6" class="vempty">
                <div class="t">还没有 WARP 实例</div>
                <div class="d">先设总代理（如需），再点左侧「一键部署」</div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <details class="vhelp" style="margin-top:11px">
        <summary>使用路径</summary>
        <div class="vhelp-body">
          <ol>
            <li>确认 Docker 可用</li>
            <li>点「一键部署」创建容器</li>
            <li>容器停了点「启动/重启」</li>
            <li>点「同步到池」写入注册代理池</li>
            <li>注册页勾选「使用代理池」</li>
          </ol>
          若 41000/41001 探测失败，先重启容器再同步。<br />
          每行可独立设上游：跟随默认 / 独立 URL / 直连，改完点「应用」立即重建生效。
        </div>
      </details>
    </div>
  </div>
</template>

<style scoped>
.vtable td { vertical-align: top; padding-top: 8px; }
.vtable td .vstate { padding-top: 2px; }
</style>
