<script setup lang="ts">
import { onBeforeUnmount, onMounted } from 'vue'

const props = defineProps<{
  account: any | null
  position: { top: number; left: number } | null
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'action', name: string, acc: any): void
}>()

function pick(name: string) {
  if (props.account) emit('action', name, props.account)
  emit('close')
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <Teleport to="body">
    <div v-if="account && position">
      <div class="menu-backdrop" @click="emit('close')" @contextmenu.prevent="emit('close')" />
      <div class="row-menu" :style="{ top: position.top + 'px', left: position.left + 'px' }" @click.stop>
        <button class="row-menu-item" @click="pick('test')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M8 5.5v13l10-6.5z"/></svg>
          <span>测试对话</span>
        </button>
        <button class="row-menu-item" :disabled="!account.access_token" @click="pick('verify')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M9 12.5l2.2 2.2L16 10"/><circle cx="12" cy="12" r="9"/></svg>
          <span>校验凭证</span>
        </button>
        <button class="row-menu-item" @click="pick('detail')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 5h16M4 12h16M4 19h10"/></svg>
          <span>详情 / 编辑</span>
        </button>

        <div class="row-menu-sep" />
        <div class="row-menu-label">复制</div>
        <button class="row-menu-item" @click="pick('copy-email')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/></svg>
          <span>邮箱</span>
        </button>
        <button class="row-menu-item" :disabled="!account.password" @click="pick('copy-password')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="10" width="16" height="10" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>
          <span>密码</span>
        </button>
        <button class="row-menu-item" :disabled="!account.sso" @click="pick('copy-sso')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M10.5 13.5 4 20M7 17l-2 2 1.5 1.5L8.5 19"/><circle cx="15.5" cy="8.5" r="5.5"/></svg>
          <span>SSO</span>
        </button>
        <button class="row-menu-item" :disabled="!account.access_token" @click="pick('copy-token')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v18M6 8h12M6 16h12"/></svg>
          <span>Access Token</span>
        </button>
        <button class="row-menu-item" @click="pick('copy-line')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
          <span>整行凭据</span>
        </button>

        <div class="row-menu-sep" />
        <button class="row-menu-item" :disabled="!account.sso" @click="pick('convert')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12a8 8 0 0 1 13.7-5.6L20 8"/><path d="M20 4v4h-4"/><path d="M20 12a8 8 0 0 1-13.7 5.6L4 16"/><path d="M4 20v-4h4"/></svg>
          <span>{{ account.access_token ? '重取凭证' : '入库转换' }}</span>
        </button>
        <button class="row-menu-item" @click="pick('toggle-imported')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16v13H4z"/><path d="M9 11h6"/></svg>
          <span>{{ account.imported ? '标记未入库' : '标记已入库' }}</span>
        </button>
        <div class="row-menu-sep" />
        <button class="row-menu-item is-danger" @click="pick('delete')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M5 7h14M10 4h4M6 7l1 13h10l1-13"/><path d="M10 11v6M14 11v6"/></svg>
          <span>删除账号</span>
        </button>
      </div>
    </div>
  </Teleport>
</template>
