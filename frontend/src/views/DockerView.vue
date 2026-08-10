<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import * as dk from '@/api/docker'
import * as pluginsApi from '@/api/plugins'
import * as home from '@/api/home'
import { toast } from '@/utils/clipboard'
import { confirmBox } from '@/utils/confirm'

const rt = ref<dk.DockerRuntime | null>(null)
const containers = ref<dk.DockerContainer[]>([])
const images = ref<dk.DockerImage[]>([])
const disk = ref<any[]>([])
const busy = ref('')
const resizeTask = ref<dk.DockerTask | null>(null)
const showAdvanced = ref(false)
const formCores = ref(0)
const formMemGB = ref(0)

// 打码引擎：一键部署全部 + 单引擎状态
const ENGINES = [
  { id: 'auralith', name: 'Auralith', port: 8194 },
  { id: 'veloraturn', name: 'VeloraTurn', port: 8193 },
  { id: 'ezsolver', name: 'EzSolver', port: 8192 },
]
const captcha = ref<any[]>([])
const deployTask = ref<dk.DockerTask | null>(null)
const installTask = ref<dk.DockerTask | null>(null)

let poll: any = null
let deployPoll: any = null
let installPoll: any = null

// errText 取出后端真正的失败原因。
//
// axios 错误的 e.message 只是 HTTP 层的 "Request failed with status code 500"，
// 后端的可操作提示在 e.response.data.error 里。本页原来 8 处 catch 全用
// e?.message，于是所有失败都显示成一句无用的 500 —— 用户看不到「安装不完整，
// 请重新安装」这类关键信息，只能干瞪眼。
function errText(e: any, fallback = '操作失败'): string {
  return (
    e?.response?.data?.error ||
    e?.response?.data?.message ||
    e?.message ||
    (typeof e === 'string' ? e : '') ||
    fallback
  )
}

const backendLabel = computed(() => {
  switch (rt.value?.backend) {
    case 'colima': return 'Colima'
    case 'docker-desktop': return 'Docker Desktop'
    case 'native-linux': return 'Linux 原生'
    default: return '未识别'
  }
})

// 每个打码槽约 20 秒解一次 Turnstile → 每分钟约 3 次
const curRate = computed(() => (rt.value?.cur_slots ?? 0) * 3)
const memGB = (mb?: number) => (mb ? Math.round(mb / 1024) : 0)
const corePct = computed(() => {
  if (!rt.value?.host_cores) return 0
  return Math.min(100, Math.round((rt.value.vm_cores / rt.value.host_cores) * 100))
})

async function load() {
  try {
    rt.value = await dk.getRuntime()
    if (!formCores.value && rt.value) {
      formCores.value = rt.value.vm_cores || rt.value.rec_cores
      formMemGB.value = memGB(rt.value.vm_mem_mb || rt.value.rec_mem_mb)
    }
  } catch (e: any) {
    toast('读取 Docker 运行时失败：' + errText(e), 'bad')
  }
  await Promise.all([loadContainers(), loadImages(), loadDisk(), loadCaptcha()])
}

async function loadContainers() {
  try {
    containers.value = (await dk.listContainers()) || []
  } catch {
    containers.value = []
  }
}
async function loadImages() {
  try {
    images.value = (await dk.listImages()) || []
  } catch {
    images.value = []
  }
}
async function loadDisk() {
  try {
    disk.value = (await dk.getDiskUsage())?.rows || []
  } catch {
    disk.value = []
  }
}

// ── 打码引擎：三个引擎共用一体容器（umbraforge-captcha）──
async function loadCaptcha() {
  try {
    const data = await pluginsApi.listPlugins()
    captcha.value = data?.status || []
  } catch {
    captcha.value = []
  }
}
function capSt(id: string) {
  return captcha.value.find((s: any) => s.id === id) || {}
}
function capState(id: string): { cls: string; text: string } {
  const s = capSt(id)
  if (s.healthy) return { cls: 'ok', text: s.mode === 'docker' ? 'Docker' : '本机' }
  if (s.running) return { cls: 'run', text: '启动中' }
  if (s.mode === 'docker') return { cls: 'warn', text: '容器停止' }
  return { cls: 'off', text: '未启动' }
}
// 一体容器在容器列表里的行（用于整体启停/删除）
const combinedContainer = computed(() =>
  containers.value.find((c) => c.name === 'umbraforge-captcha') || null,
)
const combinedRunning = computed(() => combinedContainer.value?.state === 'running')
const healthyCount = computed(() => ENGINES.filter((p) => capSt(p.id).healthy).length)

async function deployAll() {
  if (!rt.value?.daemon_ok) return
  busy.value = 'deploy-all'
  try {
    deployTask.value = await dk.deployAllCaptcha()
    startPollingDeploy()
  } catch (e: any) {
    toast(errText(e), 'bad')
    busy.value = ''
  }
}
function startPollingDeploy() {
  stopPollingDeploy()
  deployPoll = setInterval(async () => {
    try {
      const tasks = await dk.getDockerTasks()
      const t = tasks['docker-deploy:all']
      if (t) deployTask.value = t
      if (t?.done) {
        stopPollingDeploy()
        busy.value = ''
        toast(t.ok ? '打码引擎部署完成' : '部署失败：' + t.message, t.ok ? 'ok' : 'bad')
        await Promise.all([load(), loadCaptcha()])
      }
    } catch { /* 部署期间接口短暂不可用，继续轮询 */ }
  }, 1500)
}
function stopPollingDeploy() {
  if (deployPoll) { clearInterval(deployPoll); deployPoll = null }
}

// 安装 Docker（未安装时）：异步任务，轮询 docker-install 看进度
async function installDocker() {
  busy.value = 'install-docker'
  try {
    installTask.value = await pluginsApi.dockerInstall()
    startPollingInstall()
  } catch (e: any) {
    toast(errText(e), 'bad')
    busy.value = ''
  }
}
function startPollingInstall() {
  stopPollingInstall()
  installPoll = setInterval(async () => {
    try {
      const tasks = await dk.getDockerTasks()
      const t = tasks['docker-install']
      if (t) installTask.value = t
      if (t?.done) {
        stopPollingInstall()
        busy.value = ''
        toast(t.ok ? 'Docker 已就绪' : 'Docker 安装失败：' + t.message, t.ok ? 'ok' : 'bad')
        await maybeCleanInstallers(t.ok ? t.installer_files : undefined)
        await load()
      }
    } catch { /* 安装期间接口短暂不可用，继续轮询 */ }
  }, 1500)
}
function stopPollingInstall() {
  if (installPoll) { clearInterval(installPoll); installPoll = null }
}

// 卸载成功后弹窗询问是否删除残留的安装器下载文件。
async function maybeCleanInstallers(files: any[] | undefined) {
  if (!Array.isArray(files) || files.length === 0) return
  const totalMB = files.reduce((s, f) => s + Number(f?.size_mb || 0), 0)
  const ok = await confirmBox(
    `Docker 卸载完成。发现 ${files.length} 个已下载的安装文件（共 ${totalMB.toFixed(0)} MB），是否删除以释放空间？`
  )
  if (!ok) return
  try {
    const r: any = await home.cleanDockerInstallers(false)
    toast(`已删除 ${r?.removed || 0} 个安装文件（释放 ${Number(r?.freed_mb || 0).toFixed(0)} MB）`, 'ok')
  } catch (e: any) {
    toast('清理安装文件失败：' + (e?.message || e), 'bad')
  }
}

// deployBlockedReason 「一键部署全部」为什么点不了。
// 打码容器必须跑在 Docker 里，所以守护进程未就绪时它一定是禁用的 ——
// 但要把「下一步该做什么」讲清楚，而不是留一个没有解释的灰按钮。
const deployBlockedReason = computed(() => {
  const r = rt.value
  if (!r) return '正在探测 Docker…'
  if (r.daemon_ok) return ''
  if (!r.present) return '需要先安装 Docker：打码容器必须跑在 Docker 里'
  if (!r.virt_ok) return r.virt_reason || '宿主虚拟化未启用，Docker 无法启动'
  if (r.broken) return 'Docker 安装不完整，请点「重新安装 Docker」'
  return '需要先启动 Docker（点右上角「启动 Docker」）'
})

// 卸载 Docker。复用 docker-install 任务槽展示进度（两者互斥，不会并发）。
async function uninstall() {
  const ok = await confirmBox(
    '卸载 Docker？\n\n' +
    '会先移除本程序创建的打码容器，再卸载 Docker 本体（Windows 需在 UAC 提示中确认）。\n' +
    '你的其它容器与数据卷不受影响。卸载后可随时点「安装 Docker」重装。'
  )
  if (!ok) return
  busy.value = 'uninstall'
  try {
    installTask.value = await dk.uninstallDocker(false)
    startPollingInstall()
  } catch (e: any) {
    toast(errText(e), 'bad')
    busy.value = ''
  }
}

// 装了 Docker 但守护进程连不上：裸 colima start/restart（不覆写 VM 规格）
async function startVM() {
  busy.value = 'vm-start'
  try {
    const r = await dk.startVM()
    toast(r?.message || 'Docker 已就绪', 'ok')
    await load()
  } catch (e: any) {
    toast(errText(e), 'bad')
  } finally {
    busy.value = ''
  }
}

async function applySpecs(useRecommended: boolean) {
  if (!rt.value) return
  const cores = useRecommended ? 0 : formCores.value
  const memMB = useRecommended ? 0 : formMemGB.value * 1024
  const target = useRecommended
    ? `${rt.value.rec_cores} 核 / ${memGB(rt.value.rec_mem_mb)}GB`
    : `${cores} 核 / ${formMemGB.value}GB`
  const ok = await confirmBox(
    `将 Docker 虚拟机调整为 ${target}？\n\n` +
    `虚拟机会重启，期间所有容器不可用（重启策略为 always / unless-stopped 的会自动回来）。\n` +
    `整个过程通常 1~3 分钟。`
  )
  if (!ok) return
  busy.value = 'resize'
  try {
    resizeTask.value = await dk.applyRuntime(cores, memMB)
    startPollingTask()
  } catch (e: any) {
    toast('调整失败：' + errText(e), 'bad')
    busy.value = ''
  }
}

function startPollingTask() {
  stopPollingTask()
  poll = setInterval(async () => {
    try {
      const tasks = await dk.getDockerTasks()
      const t = tasks['docker-vm-resize']
      if (t) resizeTask.value = t
      if (t?.done) {
        stopPollingTask()
        busy.value = ''
        toast(t.ok ? t.message : '调整失败：' + t.message, t.ok ? 'ok' : 'bad')
        await load()
      }
    } catch { /* 虚拟机重启期间接口会短暂不可用，继续轮询 */ }
  }, 3000)
}
function stopPollingTask() {
  if (poll) { clearInterval(poll); poll = null }
}

async function act(c: dk.DockerContainer, action: 'start' | 'stop' | 'restart' | 'remove') {
  if (action === 'remove') {
    const ok = await confirmBox(`删除容器 ${c.name}？此操作不可撤销。`)
    if (!ok) return
  }
  busy.value = c.name + action
  try {
    await dk.containerAction(c.name, action)
    toast(`${c.name} 已${({ start: '启动', stop: '停止', restart: '重启', remove: '删除' })[action]}`)
    await Promise.all([loadContainers(), loadCaptcha()])
  } catch (e: any) {
    toast(errText(e), 'bad')
  } finally {
    busy.value = ''
  }
}

async function delImage(im: dk.DockerImage) {
  const ref = im.repo + ':' + im.tag
  const ok = await confirmBox(
    im.managed
      ? `删除固化镜像 ${ref}？\n下次部署打码容器会重新在容器内安装 Chromium（约 3~8 分钟）。`
      : `删除公共镜像 ${ref}？\n如果其它项目在用会被一并影响。`
  )
  if (!ok) return
  busy.value = 'img' + ref
  try {
    await dk.removeImage(ref, !im.managed)
    toast('已删除 ' + ref)
    await Promise.all([loadImages(), loadDisk()])
  } catch (e: any) {
    toast(errText(e), 'bad')
  } finally {
    busy.value = ''
  }
}

async function prune() {
  const ok = await confirmBox('清理未使用的容器、镜像和网络？\n不会删除数据卷（你的数据库数据安全）。')
  if (!ok) return
  busy.value = 'prune'
  try {
    const r: any = await dk.pruneDocker()
    toast(r?.message ? String(r.message).slice(0, 80) : '清理完成')
    await Promise.all([loadContainers(), loadImages(), loadDisk()])
  } catch (e: any) {
    toast(errText(e), 'bad')
  } finally {
    busy.value = ''
  }
}

// 切走再切回来时恢复正在跑的任务进度。
//
// 任务状态本来就活在后端（plugins.Center.tasks），但 installTask/deployTask/
// resizeTask 是组件内的 ref —— 切换导航会卸载组件、销毁 ref 并停掉轮询，
// 切回来只剩一个空白页面，用户以为安装/部署断了（实际后端还在跑）。
// 这里在挂载时拉一次任务表，把仍在运行的接回来并续上轮询。
// dismissed 用户手动关掉的已完成任务 key，不再回显。
// 用 key 而不是清空 ref：轮询/恢复逻辑随时可能把同一个任务再塞回来。
const dismissed = ref<Set<string>>(new Set())
function dismissTask(key: string) {
  dismissed.value = new Set([...dismissed.value, key])
}

// visibleTasks 进度面板要显示的任务：进行中的，加上「刚刚结束」且未被关掉的。
// 显示已结束的那部分是必需的 —— 任务在用户切走期间跑完时，
// 回到页面必须能看到成功还是失败，否则等于什么都没发生。
const visibleTasks = computed(() =>
  [deployTask.value, installTask.value, resizeTask.value].filter(
    (t): t is dk.DockerTask =>
      !!t && !dismissed.value.has(t.key) && (!t.done || justFinished(t)),
  ),
)

// justFinished 任务是否「刚刚结束」（默认 3 分钟内）。
// 切走期间跑完的任务也要能看到结果 —— 否则回到页面一片空白，
// 用户不知道到底装成了没有。
function justFinished(t: dk.DockerTask | undefined, withinMs = 180_000): boolean {
  if (!t?.done || !t.updated_at) return false
  const ts = Date.parse(t.updated_at)
  return Number.isFinite(ts) && Date.now() - ts < withinMs
}

async function restoreRunningTasks() {
  try {
    const tasks = await dk.getDockerTasks()
    const restore = (
      t: dk.DockerTask | undefined,
      target: typeof installTask,
      busyKey: string,
      resume: () => void,
    ) => {
      if (!t) return
      if (!t.done) {
        target.value = t
        busy.value = busyKey
        resume()
      } else if (justFinished(t)) {
        // 已结束：只回显结果，不再启动轮询（否则会立刻又 toast 一次）
        target.value = t
      }
    }
    restore(tasks['docker-install'], installTask, 'install-docker', startPollingInstall)
    restore(tasks['docker-deploy:all'], deployTask, 'deploy-all', startPollingDeploy)
    restore(tasks['docker-vm-resize'], resizeTask, 'resize', startPollingTask)
  } catch { /* 后端未就绪时静默，下次进页面再试 */ }
}

onMounted(async () => {
  await load()
  await restoreRunningTasks()
})
onUnmounted(() => {
  stopPollingTask()
  stopPollingDeploy()
  stopPollingInstall()
})
</script>
<template>
  <div class="dkp">
    <!-- 标题条：状态与全局动作压进一行 -->
    <header class="dkbar">
      <h2>Docker 管理</h2>
      <span class="pill" :class="rt?.daemon_ok ? 'pill-ok' : 'pill-bad'">
        <i class="led" />{{ rt?.daemon_ok ? '运行中' : '未就绪' }}
      </span>
      <span class="pill pill-mute">{{ backendLabel }}</span>
      <span v-if="rt?.daemon_ok" class="pill pill-mute">
        {{ rt?.vm_cores }}核 / {{ memGB(rt?.vm_mem_mb) }}GB
      </span>
      <div class="sp" />
      <!-- 守护进程未就绪时的主动作，三态互斥：
           startable → 能拉起来就「启动 Docker」
           broken    → 安装残骸（有 docker 命令但没运行时本体）→「重新安装」
           其余      → 压根没装 →「安装 Docker」
           旧逻辑只看 rt.present，于是 Windows 上安装失败留下 docker.exe 后，
           界面永远显示「启动虚拟机」且点下去必然 500 —— 死路，没有重装入口。 -->
      <!-- 守护进程未就绪时的主动作。四态互斥，按「用户下一步该干什么」排：
           虚拟化没开   → 不给启动按钮（点了必然失败），只给卸载/重装
           能拉起来     → 启动 Docker
           装了但认不出 → 重新安装
           压根没装     → 安装 Docker -->
      <button v-if="rt && !rt.daemon_ok && rt.startable" class="btn btn-primary btn-xs"
              :disabled="!!busy" @click="startVM">
        {{ busy === 'vm-start' ? '启动中…' : '启动 Docker' }}
      </button>
      <button v-else-if="rt && !rt.daemon_ok && !rt.virt_ok" class="btn btn-ghost btn-xs"
              disabled title="宿主虚拟化未启用，Docker 无法启动">
        无法启动
      </button>
      <button v-else-if="rt && !rt.daemon_ok" class="btn btn-primary btn-xs"
              :disabled="!!busy" @click="installDocker">
        {{ busy === 'install-docker' ? '安装中…' : (rt.broken ? '重新安装 Docker' : '安装 Docker') }}
      </button>
      <!-- 已装好但想重跑安装（修残缺组件）时的兜底入口 -->
      <button v-if="rt && !rt.daemon_ok && (rt.startable || !rt.virt_ok)" class="btn btn-ghost btn-xs"
              :disabled="!!busy" @click="installDocker">
        {{ busy === 'install-docker' ? '安装中…' : '重新安装' }}
      </button>
      <!-- 卸载：Windows 上「装了但起不来」很常见，必须给出路 -->
      <button v-if="rt?.desktop_installed || rt?.broken" class="btn btn-ghost btn-xs"
              :disabled="!!busy" @click="uninstall">
        {{ busy === 'uninstall' ? '卸载中…' : '卸载 Docker' }}
      </button>
      <button v-if="rt?.resizable" class="btn btn-ghost btn-xs"
              @click="showAdvanced = !showAdvanced">规格</button>
      <button class="btn btn-ghost btn-xs" @click="load">刷新</button>
    </header>

    <!-- 守护进程未就绪：一行内说清原因，不再占整块 -->
    <p v-if="rt && !rt.daemon_ok" class="alert" :class="{ bad: rt.broken }">
      <template v-if="!rt.present">未检测到 Docker。安装后即可一键部署全部打码引擎。</template>
      <template v-else>{{ rt.message || 'Docker 守护进程未就绪' }}</template>
    </p>

    <!-- 算力条：本机 → 容器 → 槽位 → 吞吐，一行读完 -->
    <section class="strip" :class="{ 'is-warn': rt?.undersized }">
      <div class="mt">
        <span class="mk">本机</span>
        <span class="mv">{{ rt?.host_cores ?? '—' }}<i>核</i></span>
        <span class="ms">{{ memGB(rt?.host_mem_mb) || '—' }}GB</span>
      </div>
      <span class="arrow">→</span>
      <!-- 守护进程没起来时 vm_cores 是回落估算值，容器压根不存在。
           显示成「容器可用 24 核 / 打码槽位 36」是假数字，必须显示「—」。 -->
      <div class="mt">
        <span class="mk">容器可用</span>
        <template v-if="rt?.vm_specs_known">
          <span class="mv" :class="rt?.undersized ? 'warn' : 'ok'">{{ rt.vm_cores }}<i>核</i></span>
          <span class="ms">{{ memGB(rt.vm_mem_mb) }}GB</span>
        </template>
        <template v-else>
          <span class="mv dim">—</span>
          <span class="ms">Docker 未运行</span>
        </template>
      </div>
      <div class="bar" v-if="rt && rt.host_cores > 0" :title="`容器占用本机 ${corePct}% 的核`">
        <div class="bar-f" :class="rt.undersized ? 'warn' : 'ok'" :style="{ width: corePct + '%' }" />
        <div v-if="rt.resizable" class="bar-m"
             :style="{ left: Math.min(100, (rt.rec_cores / rt.host_cores) * 100) + '%' }"
             :title="`推荐 ${rt.rec_cores} 核`" />
      </div>
      <span class="mpct" v-if="rt && rt.host_cores > 0">{{ corePct }}%</span>
      <div class="vr" />
      <div class="mt">
        <span class="mk">打码槽位</span>
        <span class="mv" :class="rt?.vm_specs_known ? '' : 'dim'">
          {{ rt?.vm_specs_known ? rt.cur_slots : '—' }}<i>槽</i>
        </span>
        <span class="ms">{{ rt?.vm_specs_known ? `约 ${curRate} 个/分钟` : '需先启动 Docker' }}</span>
      </div>
      <div class="sp" />
      <template v-if="rt?.undersized">
        <span class="mnote warn">容器只拿到 {{ rt.vm_cores }}/{{ rt.host_cores }} 核，打码是 CPU 密集型</span>
        <button class="btn btn-primary btn-xs" :disabled="!!busy" @click="applySpecs(true)">
          优化到 {{ rt.rec_cores }}核/{{ memGB(rt.rec_mem_mb) }}GB
        </button>
      </template>
      <span v-else-if="rt?.above_recommended" class="mnote">已高于推荐基准 {{ rt.rec_slots }} 槽</span>
      <span v-else-if="rt?.backend === 'native-linux'" class="mnote">Linux 原生直通，无需调整</span>
      <button v-else-if="rt?.resizable" class="btn btn-ghost btn-xs"
              :disabled="!!busy" @click="applySpecs(true)">按本机优化</button>
    </section>

    <!-- 手动规格：默认收起 -->
    <section v-if="showAdvanced && rt?.resizable" class="sec manual">
      <label class="mf">
        <span>CPU 核数 · 本机 {{ rt?.host_cores }}</span>
        <input class="input input-sm" type="number" min="1" :max="rt?.host_cores" v-model.number="formCores" />
      </label>
      <label class="mf">
        <span>内存 GB · 本机 {{ memGB(rt?.host_mem_mb) }}</span>
        <input class="input input-sm" type="number" min="1" :max="memGB(rt?.host_mem_mb)" v-model.number="formMemGB" />
      </label>
      <span class="mnote">预计 {{ Math.max(1, Math.floor(formCores * 1.5)) }} 槽 · 约
        {{ Math.max(1, Math.floor(formCores * 1.5)) * 3 }} 个/分钟</span>
      <button class="btn btn-primary btn-xs" :disabled="!!busy" @click="applySpecs(false)">应用</button>
    </section>

    <!-- 打码引擎：三引擎一体容器，状态与动作同一行 -->
    <section class="sec">
      <div class="sh">
        <span class="st">打码引擎</span>
        <span class="cnt" :class="healthyCount === ENGINES.length ? 'ok' : ''">{{ healthyCount }}/{{ ENGINES.length }}</span>
        <span class="sn">三引擎同一容器 · 切换通道只切端口，零重部署</span>
        <div class="sp" />
        <template v-if="combinedContainer">
          <button class="btn btn-ghost btn-xs" :disabled="!!busy"
                  @click="act(combinedContainer, combinedRunning ? 'stop' : 'start')">
            {{ combinedRunning ? '停止' : '启动' }}
          </button>
          <button class="btn btn-ghost btn-xs" :disabled="!!busy"
                  @click="act(combinedContainer, 'restart')">重启</button>
        </template>
        <!-- 禁用时必须说明原因：用户截图里这个按钮是灰的，但界面没有任何解释，
             只能猜「是不是坏了」。title 给出确切前置条件。 -->
        <button class="btn btn-primary btn-xs" :disabled="!!busy || !rt?.daemon_ok"
                :title="rt?.daemon_ok ? '' : deployBlockedReason" @click="deployAll">
          {{ busy === 'deploy-all' ? '部署中…' : combinedContainer ? '重新部署' : '一键部署全部' }}
        </button>
      </div>
      <p v-if="!rt?.daemon_ok" class="engs-hint">{{ deployBlockedReason }}</p>
      <div class="engs">
        <div v-for="p in ENGINES" :key="p.id" class="eng" :class="capState(p.id).cls">
          <span class="led" :class="capState(p.id).cls" />
          <span class="en">{{ p.name }}</span>
          <span class="ep mono">:{{ p.port }}</span>
          <span class="es">{{ capState(p.id).text }}</span>
        </div>
      </div>
    </section>

    <!-- 进度：部署 / 安装 / 调规格共用紧凑样式 -->
    <!-- 进度面板：进行中的任务，以及「刚刚结束」的结果（切走期间跑完的也要看得到） -->
    <section v-for="t in visibleTasks" :key="t.key" class="sec prog"
             :class="t.done ? (t.ok ? 'is-ok' : 'is-bad') : ''">
      <div class="sh">
        <span v-if="!t.done" class="spin" />
        <span v-else class="fin" :class="t.ok ? 'ok' : 'bad'">{{ t.ok ? '✓' : '✕' }}</span>
        <span class="st">{{ t.message }}</span>
        <div class="sp" />
        <span class="sn">{{ t.done ? (t.ok ? '已完成' : '失败') : t.stage }}</span>
        <button v-if="t.done" class="btn btn-ghost btn-xs" @click="dismissTask(t.key)">关闭</button>
      </div>
      <div class="logbox" v-if="t.log?.length">
        <div v-for="(l, i) in t.log!.slice(-5)" :key="i" class="logline">{{ l }}</div>
      </div>
    </section>

    <!-- 容器 / 镜像：两列等高，行高统一 -->
    <div class="cols">
      <section class="sec">
        <div class="sh">
          <span class="st">容器</span>
          <span class="cnt">{{ containers.length }}</span>
          <span class="sn">「其它项目」不提供删除</span>
        </div>
        <div class="rows">
          <div v-for="c in containers" :key="c.id" class="row" :class="{ off: c.state !== 'running' }">
            <span class="led" :class="c.state === 'running' ? 'ok' : 'off'" />
            <div class="rmain">
              <div class="rt">
                <span class="mono rn">{{ c.name }}</span>
                <span v-if="c.plugin === 'captcha-all'" class="pill pill-ok xs">打码 · 三合一</span>
                <span v-else-if="c.plugin" class="pill pill-ok xs">打码 · {{ c.plugin }}</span>
                <span v-else-if="c.managed" class="pill pill-mute xs">Grok-Nobody</span>
                <span v-else class="pill pill-warn xs">其它项目</span>
              </div>
              <div class="rs">
                <span class="mono">{{ c.image }}</span><span class="sep">·</span><span>{{ c.status }}</span>
                <template v-if="c.ports"><span class="sep">·</span><span class="mono">{{ c.ports }}</span></template>
              </div>
            </div>
            <div class="racts">
              <button v-if="c.state !== 'running'" class="btn btn-ghost btn-xs"
                      :disabled="!!busy" @click="act(c, 'start')">启动</button>
              <button v-else class="btn btn-ghost btn-xs" :disabled="!!busy" @click="act(c, 'stop')">停止</button>
              <button class="btn btn-ghost btn-xs" :disabled="!!busy" @click="act(c, 'restart')">重启</button>
              <button v-if="c.managed" class="btn btn-danger btn-xs"
                      :disabled="!!busy" @click="act(c, 'remove')">删除</button>
            </div>
          </div>
          <div v-if="!containers.length" class="empty">没有容器，或守护进程未就绪</div>
        </div>
      </section>

      <section class="sec">
        <div class="sh">
          <span class="st">镜像</span>
          <span class="cnt">{{ images.length }}</span>
          <span class="sn">固化镜像已内置 Chromium</span>
        </div>
        <div class="rows">
          <div v-for="im in images" :key="im.id + im.tag" class="row row-noled">
            <div class="rmain">
              <div class="rt">
                <span class="mono rn">{{ im.repo }}<span class="dim">:{{ im.tag }}</span></span>
                <span v-if="im.managed" class="pill pill-ok xs">固化</span>
              </div>
              <div class="rs"><span class="mono">{{ im.size }}</span></div>
            </div>
            <div class="racts">
              <button class="btn btn-danger btn-xs" :disabled="!!busy" @click="delImage(im)">删除</button>
            </div>
          </div>
          <div v-if="!images.length" class="empty">没有镜像</div>
        </div>
      </section>
    </div>

    <!-- 磁盘占用：整行横条，四项等宽铺满 -->
    <section class="sec">
      <div class="dstrip">
        <span class="st">磁盘占用</span>
        <div v-for="(r, i) in disk" :key="i" class="dcell">
          <span class="dtop"><b class="dk">{{ r.Type }}</b><span class="dv mono">{{ r.Size }}</span></span>
          <span class="ds">{{ r.Active }}/{{ r.TotalCount }} 活跃 · 可回收 {{ r.Reclaimable }}</span>
        </div>
        <span v-if="!disk.length" class="sn">暂无数据</span>
        <div class="sp" />
        <span class="sn">不删数据卷</span>
        <button class="btn btn-ghost btn-xs" :disabled="busy === 'prune'" @click="prune">
          {{ busy === 'prune' ? '清理中…' : '清理未使用' }}
        </button>
      </div>
    </section>

    <details class="vhelp">
      <summary>规格与槽位怎么算</summary>
      <div class="vhelp-body">
        macOS / Windows 上容器跑在一个 Linux 虚拟机里，拿到的是<b>虚拟机的配额</b>，
        跟本机规格无关 —— Colima 默认只给 2 核，本机 12 核也用不上。<br>
        一次 Turnstile 挑战约占 <b>1 核 20 秒</b>，所以槽位 ≈ 核数 × 1.5，
        吞吐 ≈ 槽位 × 3 个/分钟：2 核 → 3 槽 → 约 6~9 个/分钟；8 核 → 12 槽 → 约 30+ 个/分钟。<br>
        推荐值取本机 <b>2/3 的核</b>与 <b>1/2 的内存</b>，留余量给系统和浏览器。
        Linux 原生 Docker 没有虚拟机层，容器直接用全部资源，无需设置。<br>
        打码三引擎装在同一个容器（端口 8192/8193/8194 全部映射），
        所以<b>部署一次即可</b>，在「设置 / 打码」里切换通道不需要重新部署。
      </div>
    </details>
  </div>
</template>

<style scoped>
/* 密度优先：全页 10px 间距，区块内 6~8px 行距，不留空转的大留白 */
.dkp { display: flex; flex-direction: column; gap: 10px; }
.sp { flex: 1 1 auto; min-width: 4px; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }

/* ── 标题条 ── */
.dkbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.dkbar h2 { margin: 0; font-size: 16px; font-weight: 700; letter-spacing: -.01em; }

/* ── 状态胶囊 ── */
.pill {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 2px 8px; border-radius: 999px; font-size: 11px; font-weight: 500;
  border: 1px solid var(--line); color: var(--ink-3); background: var(--panel-solid);
  white-space: nowrap;
}
.pill.xs { padding: 0 6px; font-size: 10px; }
.pill-ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 35%, transparent); background: color-mix(in srgb, var(--ok) 9%, transparent); }
.pill-bad { color: var(--bad); border-color: color-mix(in srgb, var(--bad) 35%, transparent); background: color-mix(in srgb, var(--bad) 9%, transparent); }
.pill-warn { color: var(--accent); border-color: color-mix(in srgb, var(--accent) 38%, transparent); background: var(--accent-soft); }
.pill-mute { color: var(--ink-3); }
.pill .led { width: 5px; height: 5px; background: currentColor; box-shadow: none; }

.alert {
  margin: 0; padding: 7px 11px; border-radius: 9px; font-size: 12px; line-height: 1.5;
  color: var(--accent); background: var(--accent-soft);
  border: 1px solid color-mix(in srgb, var(--accent) 26%, transparent);
}
/* 安装残缺是需要用户动手的状态，用红色区别于普通的「未就绪」 */
.alert.bad {
  color: var(--bad);
  background: color-mix(in srgb, var(--bad) 10%, transparent);
  border-color: color-mix(in srgb, var(--bad) 28%, transparent);
}

/* ── 算力条：一行读完「本机 → 容器 → 槽位 → 吞吐」 ── */
.strip {
  display: flex; align-items: center; gap: 11px; flex-wrap: wrap;
  padding: 9px 13px; border: 1px solid var(--line); border-radius: 11px;
  background: var(--panel-solid);
}
.strip.is-warn { border-color: color-mix(in srgb, var(--accent) 45%, transparent); background: var(--accent-soft); }
.mt { display: flex; align-items: baseline; gap: 5px; white-space: nowrap; }
.mk { font-size: 11px; color: var(--ink-3); }
.mv { font-size: 17px; font-weight: 680; line-height: 1.15; letter-spacing: -.01em; font-variant-numeric: tabular-nums; }
.mv i { font-size: 10.5px; font-weight: 400; font-style: normal; color: var(--ink-3); margin-left: 1px; }
.mv.ok { color: var(--ok); }
.mv.warn { color: var(--accent); }
/* 未知值（Docker 未运行时的「—」）：不要用主色，否则看着像真实数据 */
.mv.dim { color: var(--ink-3); font-weight: 500; }
.engs-hint { margin: 7px 0 0; font-size: 11px; line-height: 1.5; color: var(--ink-3); }
.ms { font-size: 11px; color: var(--ink-3); }
.arrow { color: var(--ink-3); font-size: 12px; }
.vr { width: 1px; align-self: stretch; background: var(--line); margin: 1px 2px; }
.mpct { font-size: 11px; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.mnote { font-size: 11.5px; color: var(--ink-3); }
.mnote.warn { color: var(--accent); }

.bar {
  position: relative; flex: 1 1 90px; min-width: 60px; max-width: 200px; height: 5px;
  border-radius: 999px; background: color-mix(in srgb, var(--ink) 8%, transparent); overflow: hidden;
}
.bar-f { height: 100%; border-radius: 999px; transition: width .3s ease; }
.bar-f.ok { background: var(--ok); }
.bar-f.warn { background: var(--accent); }
.bar-m { position: absolute; top: -2px; width: 2px; height: 9px; background: var(--ink-2); opacity: .55; border-radius: 1px; }

/* ── 区块 ── */
.sec {
  padding: 9px 13px 10px; border: 1px solid var(--line); border-radius: 11px;
  background: var(--panel-solid);
}
.sh { display: flex; align-items: center; gap: 7px; flex-wrap: wrap; margin-bottom: 6px; }
.st { font-size: 12.5px; font-weight: 700; letter-spacing: -.01em; }
.cnt {
  font-size: 10.5px; font-weight: 700; padding: 0 6px; border-radius: 999px;
  color: var(--ink-3); background: color-mix(in srgb, var(--ink) 7%, transparent);
  font-variant-numeric: tabular-nums;
}
.cnt.ok { color: var(--ok); background: color-mix(in srgb, var(--ok) 12%, transparent); }
.sn { font-size: 11px; color: var(--ink-3); }

.manual { display: flex; align-items: flex-end; gap: 10px; flex-wrap: wrap; }
.mf { display: flex; flex-direction: column; gap: 3px; font-size: 11px; color: var(--ink-3); }
.mf .input { width: 132px; }

/* ── 打码引擎：三枚紧凑芯片，按内容收窄而不是铺满整行 ── */
.engs { display: flex; flex-wrap: wrap; gap: 7px; }
.eng {
  flex: 0 1 auto; min-width: 168px;
  display: flex; align-items: center; gap: 6px;
  padding: 5px 9px; border: 1px solid var(--line); border-radius: 9px;
  background: color-mix(in srgb, var(--ink) 2.5%, transparent);
}
.eng.ok { border-color: color-mix(in srgb, var(--ok) 32%, transparent); background: color-mix(in srgb, var(--ok) 7%, transparent); }
.eng.warn { border-color: color-mix(in srgb, var(--accent) 34%, transparent); background: var(--accent-soft); }
.en { font-size: 12px; font-weight: 640; }
.ep { font-size: 10.5px; color: var(--ink-3); }
.es { margin-left: auto; font-size: 10.5px; color: var(--ink-3); }
.eng.ok .es { color: var(--ok); font-weight: 640; }
.eng.warn .es { color: var(--accent); font-weight: 640; }

/* ── 两列布局：两列等高（stretch），列内表格自己滚，不让某列把整行撑高 ── */
.cols { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; }
@media (min-width: 1080px) {
  .cols { grid-template-columns: minmax(0, 1.35fr) minmax(0, 1fr); align-items: stretch; }
}
.cols .sec { display: flex; flex-direction: column; min-width: 0; }

/* ── 行列表 ──
   用 grid 而不是 flex：三栏（灯 / 内容 / 按钮）位置固定，按钮永远不会
   被长端口串（0.0.0.0:5433->5432/tcp, [::]:5433->5432/tcp）挤到下一行。
   这就是截图里 red-postgres / red-redis 那两行比别人高一截的原因。 */
.rows { display: flex; flex-direction: column; overflow-y: auto; max-height: 348px; }
.row {
  display: grid; grid-template-columns: 6px minmax(0, 1fr) auto;
  align-items: center; gap: 9px;
  min-height: 40px; padding: 5px 0; border-bottom: 1px solid var(--line);
}
/* 镜像行没有状态灯，去掉那一栏，否则内容会整体右移 6px 与容器列对不齐 */
.row-noled { grid-template-columns: minmax(0, 1fr) auto; }
.row:last-child { border-bottom: 0; }
.row.off { opacity: .62; }
.rmain { min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.rt { display: flex; align-items: center; gap: 6px; min-width: 0; }
.rn { font-size: 12px; font-weight: 560; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dim { color: var(--ink-3); font-weight: 400; }
/* 整行副信息当成一条单行文本截断：内部不再各自 flex，避免换行改变行高 */
.rs {
  font-size: 10.5px; color: var(--ink-3); line-height: 1.5;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0;
}
.rs .mono { font-size: 10.5px; }
.sep { opacity: .45; margin: 0 4px; }
.racts { display: flex; gap: 4px; flex: 0 0 auto; white-space: nowrap; }
/* 删除是低频不可逆动作：默认幽灵样式，只在悬停时变红 */
.racts :deep(.btn-danger) {
  background: transparent; border-color: color-mix(in srgb, var(--bad) 30%, transparent);
  color: var(--bad); box-shadow: none;
}
.racts :deep(.btn-danger:hover:not(:disabled)) {
  background: color-mix(in srgb, var(--bad) 10%, transparent);
  border-color: color-mix(in srgb, var(--bad) 55%, transparent);
}
.led { width: 6px; height: 6px; border-radius: 50%; flex: 0 0 auto; background: var(--ink-3); }
.led.ok { background: var(--ok); box-shadow: 0 0 0 2.5px color-mix(in srgb, var(--ok) 16%, transparent); }
.led.run { background: var(--accent); box-shadow: 0 0 0 2.5px var(--accent-soft); }
.led.warn { background: var(--accent); }
.led.off { background: var(--ink-3); opacity: .6; }
.empty { padding: 14px 2px; text-align: center; color: var(--ink-3); font-size: 11.5px; }

/* ── 磁盘：整行横条，四项等宽铺满，不再挤在右列 ── */
.dstrip { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.dstrip .st { flex: 0 0 auto; }
.dcell {
  flex: 1 1 132px; min-width: 0;
  display: flex; flex-direction: column; gap: 1px;
  padding: 5px 9px; border: 1px solid var(--line); border-radius: 9px;
  background: color-mix(in srgb, var(--ink) 2.5%, transparent);
}
.dtop { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
.dk { font-size: 10.5px; font-weight: 600; color: var(--ink-3); white-space: nowrap; }
.dv {
  font-size: 13px; font-weight: 680; font-variant-numeric: tabular-nums;
  margin-left: auto; white-space: nowrap;
}
.ds {
  font-size: 10px; color: var(--ink-3);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}

/* ── 进度 ── */
.prog { border-left: 2px solid var(--accent); }
.prog.is-ok { border-left-color: var(--ok); }
.prog.is-bad { border-left-color: var(--bad); }
/* 结束态用符号替掉转圈，尺寸与 .spin 对齐，避免面板高度跳一下 */
.fin {
  width: 11px; height: 11px; flex: 0 0 auto;
  display: grid; place-items: center;
  font-size: 11px; font-weight: 800; line-height: 1;
}
.fin.ok { color: var(--ok); }
.fin.bad { color: var(--bad); }
.spin {
  width: 11px; height: 11px; border-radius: 50%; flex: 0 0 auto;
  border: 2px solid var(--line); border-top-color: var(--accent);
  animation: dkspin .8s linear infinite;
}
@keyframes dkspin { to { transform: rotate(360deg); } }
.logbox { max-height: 104px; overflow: auto; }
.logline {
  font-family: ui-monospace, SFMono-Regular, monospace; font-size: 10.5px;
  color: var(--ink-3); line-height: 1.55; white-space: pre-wrap; word-break: break-all;
}
</style>
