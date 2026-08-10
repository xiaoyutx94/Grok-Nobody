<script setup lang="ts">
import { api } from '@/api/client'
import { toast } from '@/utils/clipboard'

defineEmits<{ (e: 'close'): void }>()

const REPO_URL = 'https://github.com/Nobody2088/Grok-Nobody-'
const QQ_URL = 'https://qm.qq.com/q/AzSsHWBkHK'

async function openExternal(url: string, label: string) {
  try {
    const res = await api.post('/api/v1/admin/misc/open-external', { url })
    if (res.data?.code !== 0) toast('打开失败：' + (res.data?.error || '未知错误'), 'bad')
    else toast('已用系统浏览器打开 ' + label, 'ok')
  } catch (e: any) {
    toast('打开失败：' + (e?.message || String(e)), 'bad')
  }
}
</script>

<template>
  <div class="overlay" @click.self="$emit('close')">
    <div class="sheet" style="max-width: 460px">
      <div class="sheet-head">
        <div>
          <div class="kicker">关于</div>
          <h3 class="sheet-title">Grok-Nobody</h3>
        </div>
        <button class="btn btn-icon btn-ghost" data-tip="关闭" @click="$emit('close')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>
        </button>
      </div>

      <div class="about-hero">
        <img src="/icon.svg" alt="Grok-Nobody" class="about-logo" />
        <div>
          <div class="about-name">Grok-Nobody</div>
          <div class="about-desc">Grok 注册工作台 · 打码引擎 · 代理池 · WARP</div>
        </div>
      </div>

      <div class="about-links">
        <button type="button" class="link-card" @click="openExternal(REPO_URL, 'GitHub 仓库')">
          <span class="link-ico gh">
            <svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.11.79-.25.79-.55 0-.27-.01-1.17-.02-2.12-3.2.7-3.87-1.36-3.87-1.36-.52-1.33-1.28-1.68-1.28-1.68-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.19 1.76 1.19 1.03 1.75 2.69 1.25 3.34.95.1-.74.4-1.25.72-1.54-2.55-.29-5.23-1.28-5.23-5.68 0-1.26.45-2.28 1.19-3.09-.12-.29-.52-1.46.11-3.05 0 0 .97-.31 3.18 1.18a11.1 11.1 0 0 1 5.79 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.19 1.83 1.19 3.09 0 4.41-2.69 5.38-5.25 5.66.41.36.78 1.06.78 2.14 0 1.55-.01 2.79-.01 3.17 0 .31.21.67.8.55A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z"/></svg>
          </span>
          <span class="link-body">
            <span class="link-title">GitHub 仓库</span>
            <span class="link-url">{{ REPO_URL }}</span>
          </span>
          <span class="link-arrow">↗</span>
        </button>

        <button type="button" class="link-card" @click="openExternal(QQ_URL, 'QQ 交流群')">
          <span class="link-ico qq">QQ</span>
          <span class="link-body">
            <span class="link-title">QQ 交流群</span>
            <span class="link-url">点击链接加入群聊【grok/gpt/cc-交流】</span>
          </span>
          <span class="link-arrow">↗</span>
        </button>
      </div>

      <p class="about-foot">© 2026 Nobody2088 · 开源在 GitHub · 问题反馈请加 QQ 群</p>
    </div>
  </div>
</template>

<style scoped>
.about-hero {
  display: flex;
  align-items: center;
  gap: 14px;
  margin: 14px 0 16px;
}
.about-logo {
  width: 56px;
  height: 56px;
  border-radius: 14px;
  box-shadow: 0 2px 10px rgba(15, 23, 42, 0.14);
}
.about-name {
  font-size: 17px;
  font-weight: 700;
  color: var(--ink);
}
.about-name em {
  font-style: normal;
  font-size: 12px;
  font-weight: 600;
  color: var(--brand, #d97706);
  margin-left: 6px;
  opacity: 0.85;
}
.about-desc {
  font-size: 12px;
  color: var(--muted);
  margin-top: 3px;
}
.about-links {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.link-card {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  padding: 12px 14px;
  border: 1px solid var(--line, rgba(127, 127, 127, 0.22));
  border-radius: 12px;
  background: var(--panel, transparent);
  cursor: pointer;
  text-align: left;
  transition: border-color 0.15s, transform 0.15s;
}
.link-card:hover {
  border-color: var(--brand, #d97706);
  transform: translateY(-1px);
}
.link-ico {
  width: 38px;
  height: 38px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.link-ico.gh {
  background: #161b22;
  color: #fff;
}
.link-ico.gh svg {
  width: 22px;
  height: 22px;
}
.link-ico.qq {
  background: linear-gradient(135deg, #25b7ff, #0b9df4);
  color: #fff;
  font-size: 14px;
  font-weight: 800;
  letter-spacing: 0.5px;
}
.link-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.link-title {
  font-size: 13.5px;
  font-weight: 650;
  color: var(--ink);
}
.link-url {
  font-size: 11.5px;
  color: var(--muted);
  word-break: break-all;
  line-height: 1.35;
}
.link-arrow {
  margin-left: auto;
  color: var(--muted);
  font-size: 15px;
  opacity: 0.7;
}
.about-foot {
  margin-top: 14px;
  font-size: 11px;
  color: var(--muted);
  text-align: center;
}
</style>
