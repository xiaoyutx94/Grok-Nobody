import { api, unwrap } from './client'

/** Docker 运行时（虚拟机）规格快照。 */
export interface DockerRuntime {
  backend: 'colima' | 'docker-desktop' | 'native-linux' | 'unknown'
  present: boolean
  daemon_ok: boolean
  resizable: boolean
  /** 容器实际可用的核数/内存 —— macOS/Windows 上这是虚拟机配额，不是真机规格 */
  vm_cores: number
  vm_mem_mb: number
  host_cores: number
  host_mem_mb: number
  /** 按真机规格自动算出的推荐值 */
  rec_cores: number
  rec_mem_mb: number
  /** 打码槽位：直接决定每分钟能注册几个 */
  cur_slots: number
  rec_slots: number
  undersized: boolean
  /** 推荐值是否真的高于当前 —— 用户手动加过配额时推荐值会更低，不能当成「优化」展示 */
  rec_is_upgrade: boolean
  /** 当前配额已高于推荐值 */
  above_recommended: boolean
  /** 「与主机共享内存」档的配额上限（MB）= 宿主 3/4 */
  share_mem_mb: number
  /** 底层有内存气球（vz）：配额只是上限，实占跟用量走、空闲归还宿主。
   *  false（qemu / Docker Desktop）时配额会被实打实占住。 */
  mem_balloon: boolean
  /** 虚拟化类型：vz / qemu / 空 */
  vm_type?: string
  /** 本程序能否直接把 Docker 拉起来（Docker Desktop / colima） */
  startable: boolean
  /** 安装不完整：有 docker 命令但找不到运行时本体（多为安装中断残骸） */
  broken: boolean
  /** 宿主虚拟化前置条件（Windows：BIOS VT-x/AMD-V + WSL2）。false 时 Docker 起不来 */
  virt_ok: boolean
  /** virt_ok=false 时的可操作原因 */
  virt_reason?: string
  /** Docker Desktop 本体是否装着 —— 区分「没装」与「装了但起不来」 */
  desktop_installed: boolean
  /** vm_cores/vm_mem_mb 是否为真实探测值。false = 回落估算，界面应显示「—」 */
  vm_specs_known: boolean
  message?: string
}

export interface DockerContainer {
  id: string
  name: string
  image: string
  state: string
  status: string
  ports: string
  /** 是否由 UmbraForge 创建 —— 只有托管容器允许删除 */
  managed: boolean
  plugin?: string
}

export interface DockerImage {
  id: string
  repo: string
  tag: string
  size: string
  managed: boolean
}

export interface DockerTask {
  key: string
  running: boolean
  stage: string
  message: string
  log: string[] | null
  done: boolean
  ok: boolean
  /** RFC3339。用于判断「刚刚完成」——切走期间跑完的任务回来还要能看到结果 */
  started_at?: string
  updated_at?: string
}

const B = '/api/v1/admin/docker'

export const getRuntime = async () => unwrap<DockerRuntime>(await api.get(`${B}/runtime`))

/**
 * 一键部署全部打码引擎（Auralith + VeloraTurn + EzSolver 装进同一个容器，
 * 端口 8192/8193/8194）。异步任务，轮询 getDockerTasks 的 'docker-deploy:all'。
 */
export const deployAllCaptcha = async () =>
  unwrap<DockerTask>(await api.post(`${B}/captcha/deploy-all`, {}, { timeout: 30_000 }))

/** 装了 Docker 但守护进程连不上时启动/修复虚拟机（colima 裸 start/restart，不覆写规格）。 */
export const startVM = async () =>
  unwrap<{ message: string }>(await api.post(`${B}/vm/start`, {}, { timeout: 300_000 }))

/**
 * cores/mem_mb 传 0 表示用推荐值。异步任务，轮询 plugins/docker-task 看进度。
 *
 * share=true 走「与主机共享内存」档：配额取宿主 3/4。colima+vz 有内存气球，
 * 该配额只是上限——VM 实占跟真实用量走、空闲归还宿主，而不是把固定几 G 切走。
 */
export const applyRuntime = async (cores = 0, mem_mb = 0, share = false) =>
  unwrap<DockerTask>(await api.post(`${B}/runtime/apply`, { cores, mem_mb, share }))

export const listContainers = async () =>
  unwrap<DockerContainer[]>(await api.get(`${B}/containers`))

export const containerAction = async (name: string, action: 'start' | 'stop' | 'restart' | 'remove') =>
  unwrap(await api.post(`${B}/containers/action`, { name, action }))

export const listImages = async () => unwrap<DockerImage[]>(await api.get(`${B}/images`))

export const removeImage = async (ref: string, allow_public = false) =>
  unwrap(await api.post(`${B}/images/remove`, { ref, allow_public }))

export const getDiskUsage = async () => unwrap<{ rows: any[] }>(await api.get(`${B}/disk`))

export const pruneDocker = async () => unwrap(await api.post(`${B}/prune`))

/** 卸载 Docker（异步任务，轮询 plugins/docker-task 的 docker-install 看进度）。 */
export const uninstallDocker = async (removeAllContainers = false) =>
  unwrap<DockerTask>(await api.post(`${B}/uninstall`, { remove_all_containers: removeAllContainers }))

/** Docker 相关异步任务进度（装 Docker / 部署容器 / 调整规格共用一张表）。 */
export const getDockerTasks = async () =>
  unwrap<Record<string, DockerTask>>(await api.get('/api/v1/admin/plugins/docker-task'))
