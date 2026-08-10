import { isRef, ref, unref, watch, type Ref } from 'vue'
import { toast } from './clipboard'

/**
 * 安全序列化：调用方可能传 reactive 对象、ref、或两者混装的字面量。
 * 直接 JSON.stringify 一个 ref 会撞上 Vue 内部的 dep/computed 循环引用而抛错。
 */
function serialize(source: unknown): string {
  const unwrap = (v: any): any => {
    const raw = isRef(v) ? unref(v) : v
    if (raw === null || typeof raw !== 'object') return raw
    if (Array.isArray(raw)) return raw.map(unwrap)
    const out: Record<string, any> = {}
    for (const k of Object.keys(raw)) out[k] = unwrap(raw[k])
    return out
  }
  try {
    return JSON.stringify(unwrap(source))
  } catch {
    return ''
  }
}

/**
 * 表单自动保存（防抖）。
 *
 * 两个必须处理的坑：
 *
 * 1. **不能在首屏加载时触发**。表单的初始值是本地默认值，`load()` 把服务端配置
 *    填进来的那一刻会触发 watch；此时若立刻回写，等于用本地默认值覆盖服务端配置。
 *    实测踩过：`skip_verify` 表单初值是 false，被回写后注册尾部的 device OAuth
 *    验活重新打开，每个账号多花约 13.5 秒。所以必须由调用方在 load 完成后
 *    显式 `arm()`。
 *
 * 2. **失败必须让用户看见**。去掉保存按钮后没有「保存中/已保存」的视觉锚点，
 *    静默失败会让用户以为已落盘，重启后配置全丢。所以失败要弹红色提示。
 */
/** 保存状态。给界面一个确定的视觉锚点——去掉保存按钮后，用户无法判断
 *  「我改的到底存没存」，只能靠一闪而过的 toast，这正是自动保存最让人不安的地方。 */
export type AutoSaveState = 'idle' | 'dirty' | 'saving' | 'saved' | 'error'

export interface AutoSaveHandle {
  /** load() 完成后调用，之后的改动才会触发保存 */
  arm: () => void
  /** 立即落盘（跳过防抖），用于离开页面前 */
  flush: () => Promise<void>
  /** 用户手动触发保存（无论有无改动都写一次），供「立即保存」按钮用 */
  saveNow: () => Promise<void>
  /** 当前保存状态，供界面显示「未保存/保存中/已保存/失败」 */
  state: Ref<AutoSaveState>
  /**
   * 执行一段「程序性写入」而不触发保存。
   *
   * 必须有这个：保存成功后通常要 reload() 拉回服务端结果，而 reload 会改动
   * 被监听的对象 → 再次触发保存 → 又 reload …… 形成无限循环。
   */
  mute: <T>(fn: () => Promise<T> | T) => Promise<T>
}

export function useAutoSave(
  source: object,
  save: () => Promise<any>,
  opts: { delay?: number; okMessage?: string; label?: string } = {},
): AutoSaveHandle {
  const delay = opts.delay ?? 600
  const okMessage = opts.okMessage ?? '已保存'
  let timer: any = null
  let armed = false
  let pending = false
  let muted = 0
  // baseline 是 arm() 时刻的序列化快照。
  // 只靠 armed 布尔量不够：Vue 的 watch 默认在 nextTick 才刷新，而 arm() 是在
  // load() 末尾同步调用的，于是 load 自己那次 Object.assign 触发的回调会在
  // armed=true 之后才到 —— 表现为「一进页面就自动保存一次」。实测这一次回写
  // 把 skip_verify 写成了 false，注册尾部的 device OAuth 验活重新打开，
  // 每个账号多花约 13.5 秒。所以还要比对内容：与 baseline 相同就不是用户改动。
  let baseline = ''
  const snapshot = () => serialize(source)
  const state = ref<AutoSaveState>('idle')

  async function run() {
    timer = null
    pending = false
    state.value = 'saving'
    // 保存本身可能回填数据（如 save 内部 reload），期间不再触发新的保存
    muted++
    try {
      await save()
      state.value = 'saved'
      toast(okMessage)
    } catch (e: any) {
      state.value = 'error'
      const detail = e?.response?.data?.error || e?.message || String(e)
      toast(`${opts.label ? opts.label + ' ' : ''}保存失败：${detail}`, 'bad')
    } finally {
      // 等一拍再解除，让 watch 的本轮回调先跑完
      setTimeout(() => { muted = Math.max(0, muted - 1) }, 0)
    }
  }

  watch(
    () => serialize(source),
    () => {
      if (!armed) return
      const now = snapshot()
      if (now === baseline) return // load 填充引起的回调，不是用户改动
      baseline = now
      // muted>0（一次保存进行中）不吞改动：照常排队，等 run() 结束后
      // timer 触发再保存。旧实现直接 return，保存窗口内的用户编辑会
      // 被静默丢弃——「添加了代理却从没落盘」的根因之一。
      pending = true
      state.value = 'dirty'
      if (timer) clearTimeout(timer)
      timer = setTimeout(run, delay)
    },
    { deep: true },
  )

  return {
    state,
    arm: () => {
      baseline = snapshot()
      armed = true
      state.value = 'idle'
    },
    // saveNow 无条件写一次。「立即保存」按钮要的是确定性：用户点了就该落盘，
    // 不能因为「内容和 baseline 一样」而什么都不做——那样按钮看着像没反应。
    saveNow: async () => {
      if (timer) { clearTimeout(timer); timer = null }
      baseline = snapshot()
      pending = true
      await run()
    },
    flush: async () => {
      if (timer) { clearTimeout(timer); timer = null }
      // 不能只信 pending 标志：Vue 的 watch 默认在 nextTick 才刷新，
      // 「改完最后一个字符立刻点导航」时 watcher 回调还没跑过，pending 仍是
      // false —— 旧实现在这里直接空转返回，改动随组件卸载一起消失。
      // 改成与 baseline 比对内容：只要真有差异就落盘，与 watcher 时序无关。
      if (!armed) return
      const now = snapshot()
      if (!pending && now === baseline) return
      baseline = now
      pending = true
      await run()
    },
    mute: async <T>(fn: () => Promise<T> | T): Promise<T> => {
      muted++
      try {
        return await fn()
      } finally {
        setTimeout(() => { muted = Math.max(0, muted - 1) }, 0)
      }
    },
  }
}
