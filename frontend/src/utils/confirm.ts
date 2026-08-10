// WKWebView 不支持 window.confirm（静默返回 false），所有确认操作必须走这里。
// 全局单例：confirmBox(msg) → Promise<boolean>，由 App.vue 挂载的 ConfirmHost 渲染。
import { reactive } from 'vue'

export interface ConfirmState {
  visible: boolean
  message: string
  resolve: ((ok: boolean) => void) | null
}

export const confirmState = reactive<ConfirmState>({
  visible: false,
  message: '',
  resolve: null
})

export function confirmBox(message: string): Promise<boolean> {
  confirmState.message = message
  confirmState.visible = true
  return new Promise<boolean>((resolve) => {
    confirmState.resolve = resolve
  })
}

export function settleConfirm(ok: boolean) {
  const r = confirmState.resolve
  confirmState.visible = false
  confirmState.resolve = null
  if (r) r(ok)
}
