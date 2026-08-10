<script setup lang="ts">
import ConfirmHost from './components/ConfirmHost.vue'
import ToastHost from './components/ToastHost.vue'
import AboutModal from './components/AboutModal.vue'
import { onMounted, ref } from 'vue'

const theme = ref<'business-white' | 'dark-gold'>('business-white')
const aboutOpen = ref(false)

function applyTheme(t: 'business-white' | 'dark-gold') {
  theme.value = t
  document.documentElement.setAttribute('data-theme', t)
  localStorage.setItem('umbraforge.theme', t)
}

onMounted(() => {
  const saved = localStorage.getItem('umbraforge.theme') as any
  applyTheme(saved === 'dark-gold' ? 'dark-gold' : 'business-white')
})
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <div class="brand">
        <img src="/icon.svg" alt="Grok-Nobody" />
        <div>
          <h1>Grok-Nobody</h1>
          <p>Grok 注册工作台</p>
        </div>
      </div>
      <div class="spacer" />
      <div class="seg theme-seg" role="group" aria-label="主题切换">
        <button type="button" class="icon-seg" :class="{ 'is-on': theme==='business-white' }"
          title="浅色 · 商务白" aria-label="浅色主题" @click="applyTheme('business-white')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2.5v2.2M12 19.3v2.2M2.5 12h2.2M19.3 12h2.2M4.9 4.9l1.6 1.6M17.5 17.5l1.6 1.6M19.1 4.9l-1.6 1.6M6.5 17.5l-1.6 1.6"/></svg>
        </button>
        <button type="button" class="icon-seg" :class="{ 'is-on': theme==='dark-gold' }"
          title="暗色 · 暗金" aria-label="暗色主题" @click="applyTheme('dark-gold')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20.5 14.2A8.5 8.5 0 0 1 9.8 3.5a8.5 8.5 0 1 0 10.7 10.7Z"/></svg>
        </button>
      </div>
    </header>

    <div class="layout">
      <aside class="sidebar">
        <div class="nav-label">注册</div>
        <RouterLink class="nav-item" to="/"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 10.5 12 3l9 7.5"/><path d="M5 9.5V21h14V9.5"/></svg></span><span>首页监控</span></RouterLink>
        <RouterLink class="nav-item" to="/register"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 4.5 13.5H11L10 22l8.5-11.5H13z"/></svg></span><span>Grok 注册</span></RouterLink>
        <RouterLink class="nav-item" to="/accounts"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="8" r="4"/><path d="M4 21c0-4 3.6-6.5 8-6.5s8 2.5 8 6.5"/></svg></span><span>账号管理</span></RouterLink>
        <RouterLink class="nav-item" to="/chat"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a8 8 0 0 1-8 8H4l2.5-2.5A8 8 0 1 1 21 12Z"/><path d="M8.5 10.5h.01M12 10.5h.01M15.5 10.5h.01"/></svg></span><span>对话</span></RouterLink>

        <div class="nav-label">API 网关</div>
        <RouterLink class="nav-item" to="/gateway"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="7" rx="1.5"/><rect x="3" y="13" width="18" height="7" rx="1.5"/><path d="M7 7.5h.01M7 16.5h.01"/></svg></span><span>网关总览</span></RouterLink>
        <RouterLink class="nav-item" to="/gateway/groups"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M3 12h18M3 18h18"/><path d="M7 4v4M12 10v4M17 16v4"/></svg></span><span>分组管理</span></RouterLink>
        <RouterLink class="nav-item" to="/gateway/keys"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="15" r="4"/><path d="m11 12 9-9"/><path d="M17 5l2 2M14 8l2 2"/></svg></span><span>API 密钥</span></RouterLink>
        <RouterLink class="nav-item" to="/gateway/services"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2 2 7l10 5 10-5-10-5z"/><path d="M2 17l10 5 10-5"/><path d="M2 12l10 5 10-5"/></svg></span><span>服务管理</span></RouterLink>

        <div class="nav-label">网络</div>
        <RouterLink class="nav-item" to="/proxy-pool"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M11 5 7 9l4 4"/><path d="M7 9h10a4 4 0 0 1 0 8"/><path d="M13 19l4-4-4-4"/><path d="M17 15H7a4 4 0 0 1 0-8"/></svg></span><span>代理池</span></RouterLink>
        <RouterLink class="nav-item" to="/warp"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c2.5 2.6 3.6 5.6 3.6 9s-1.1 6.4-3.6 9c-2.5-2.6-3.6-5.6-3.6-9s1.1-6.4 3.6-9"/></svg></span><span>WARP 代理</span></RouterLink>

        <div class="nav-label">资源</div>
        <RouterLink class="nav-item" to="/edu"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/></svg></span><span>EDU 邮箱</span></RouterLink>
        <RouterLink class="nav-item" to="/icloud"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M7 16a4 4 0 1 1 .5-7.96A5.5 5.5 0 0 1 18 9.5 3.5 3.5 0 0 1 17.5 16H7z"/><path d="M12 16v-5"/><path d="M9.5 13.5 12 11l2.5 2.5"/></svg></span><span>iCloud 邮箱</span></RouterLink>
        <RouterLink class="nav-item" to="/plugins"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="4" width="6" height="6" rx="1.5"/><rect x="14" y="4" width="6" height="6" rx="1.5"/><rect x="4" y="14" width="6" height="6" rx="1.5"/><rect x="14" y="14" width="6" height="6" rx="1.5"/></svg></span><span>插件中心</span></RouterLink>
        <RouterLink class="nav-item" to="/docker"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="7" rx="1.5"/><path d="M7 11V8M11 11V6M15 11V8"/><path d="M3 18c2.5 2 6 2 9 0"/></svg></span><span>Docker 管理</span></RouterLink>

        <div class="nav-label">系统</div>
        <RouterLink class="nav-item" to="/settings"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5 5l2 2M17 17l2 2M19 5l-2 2M7 17l-2 2"/></svg></span><span>设置 / 打码</span></RouterLink>
        <RouterLink class="nav-item" to="/guide"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M9.3 9.2a2.8 2.8 0 0 1 5.4 1c0 1.8-2.7 2.2-2.7 3.8"/><path d="M12 17.4h.01"/></svg></span><span>使用说明</span></RouterLink>
        <a class="nav-item" href="javascript:void(0)" @click="aboutOpen = true"><span class="nav-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 8h.01"/><path d="M11 12h1v4h1"/></svg></span><span>关于</span></a>
      </aside>

      <main class="content">
        <div class="content-inner fade">
          <RouterView />
        </div>
      </main>
      <ConfirmHost />
      <ToastHost />
      <AboutModal v-if="aboutOpen" @close="aboutOpen = false" />
    </div>
  </div>
</template>
