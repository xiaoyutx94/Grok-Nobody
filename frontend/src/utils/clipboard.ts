// WKWebView 里 navigator.clipboard 在非安全上下文/无用户手势时会 reject，
// 所以统一走这里：优先 async API，失败回落 execCommand，再失败才报错。
import { reactive } from 'vue'

export interface ToastState {
  visible: boolean
  message: string
  kind: 'ok' | 'bad'
}

export const toastState = reactive<ToastState>({ visible: false, message: '', kind: 'ok' })

let toastTimer: ReturnType<typeof setTimeout> | null = null

export function toast(message: string, kind: 'ok' | 'bad' = 'ok') {
  toastState.message = message
  toastState.kind = kind
  toastState.visible = true
  if (toastTimer) clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toastState.visible = false }, 1800)
}

/** 复制文本；成功返回 true。text 为空视为无事可做（返回 false，不弹提示）。 */
export async function copyText(text: string): Promise<boolean> {
  const value = String(text ?? '')
  if (!value) return false
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value)
      return true
    }
  } catch {
    // 落到 execCommand
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = value
    // 避免滚动跳动 + 不被 user-select:none 影响
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-1000px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    ta.setSelectionRange(0, value.length)
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

/** 复制并弹 toast，label 用于提示文案（如「邮箱」「密码」）。 */
export async function copyWithToast(text: string, label: string): Promise<boolean> {
  const value = String(text ?? '')
  if (!value) {
    toast(`${label}为空，无可复制内容`, 'bad')
    return false
  }
  const ok = await copyText(value)
  toast(ok ? `已复制${label}` : `复制${label}失败`, ok ? 'ok' : 'bad')
  return ok
}
