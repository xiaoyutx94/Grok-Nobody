<script setup lang="ts">
import { onMounted, ref } from 'vue'
import * as gw from '@/api/gateway'
import { confirmBox } from '@/utils/confirm'
import { toast } from '@/utils/clipboard'

const services = ref<gw.UpstreamService[]>([])
const showForm = ref(false)
const form = ref({ name: '', type: 'grok', base_url: '', enabled: true })
const busy = ref(true)

const TYPE_LABEL: Record<string, string> = {
  grok: 'Grok 反代（grok CLI 指纹）',
  openai: 'OpenAI 兼容上游',
  anthropic: 'Anthropic 兼容上游'
}

async function load() {
  busy.value = true
  try {
    const s = await gw.getServices()
    services.value = s.services
  } finally {
    busy.value = false
  }
}

async function create() {
  if (!form.value.name.trim() || !form.value.base_url.trim()) {
    toast('名称和地址不能为空')
    return
  }
  try {
    await gw.createService(form.value)
    toast('服务已添加')
    showForm.value = false
    form.value = { name: '', type: 'grok', base_url: '', enabled: true }
    await load()
  } catch (e: any) {
    toast('添加失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

async function toggle(s: gw.UpstreamService) {
  try {
    await gw.updateService(s.id, { enabled: !s.enabled })
    toast(s.enabled ? '服务已禁用' : '服务已启用')
    await load()
  } catch (e: any) {
    toast('操作失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

async function remove(s: gw.UpstreamService) {
  if (!(await confirmBox(`删除服务「${s.name}」？`))) return
  try {
    await gw.deleteService(s.id)
    toast('服务已删除')
    await load()
  } catch (e: any) {
    toast('删除失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

onMounted(load)
</script>

<template>
  <div class="page-head">
    <div>
      <h2>服务管理</h2>
      <p class="dim">上游渠道：分组绑定服务决定请求转发到哪里（默认内置 Grok 官方反代）</p>
    </div>
    <button class="btn" @click="showForm = true">+ 添加服务</button>
  </div>

  <div class="card" style="padding: 6px 14px 14px">
    <table class="tbl">
      <thead>
        <tr>
          <th>名称</th>
          <th>类型</th>
          <th>Base URL</th>
          <th>状态</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="s in services" :key="s.id">
          <td>
            {{ s.name }}
            <span v-if="s.builtin" class="pill">内置</span>
          </td>
          <td>{{ TYPE_LABEL[s.type] || s.type }}</td>
          <td><code class="mono small">{{ s.base_url }}</code></td>
          <td><span class="badge" :class="s.enabled ? 'ok' : 'err'">{{ s.enabled ? '启用' : '禁用' }}</span></td>
          <td>
            <button class="icon-btn" :title="s.enabled ? '禁用' : '启用'" @click="toggle(s)">{{ s.enabled ? '⏸' : '▶' }}</button>
            <button v-if="!s.builtin" class="icon-btn" title="删除" @click="remove(s)">🗑</button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-if="busy" class="dim" style="padding: 12px">加载中…</div>
  </div>

  <div v-if="showForm" class="modal-mask" @click.self="showForm = false">
    <div class="modal">
      <h3>添加服务（上游渠道）</h3>
      <label class="f-label">名称</label>
      <input class="f-input" v-model="form.name" placeholder="如：Grok 官方反代" />
      <label class="f-label">类型</label>
      <select class="f-input" v-model="form.type">
        <option value="grok">Grok 反代（注入 grok CLI 官方指纹）</option>
        <option value="openai">OpenAI 兼容上游</option>
        <option value="anthropic">Anthropic 兼容上游</option>
      </select>
      <label class="f-label">Base URL（含 /v1，如 https://cli-chat-proxy.grok.com/v1）</label>
      <input class="f-input mono" v-model="form.base_url" placeholder="https://…/v1" />
      <label class="switch-line">
        <input type="checkbox" v-model="form.enabled" />
        <span>创建后立即启用</span>
      </label>
      <div class="modal-actions">
        <button class="btn" @click="create">添加</button>
        <button class="btn ghost" @click="showForm = false">取消</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tbl { width: 100%; border-collapse: collapse; }
.tbl th, .tbl td { text-align: left; padding: 8px 10px; border-bottom: 1px solid var(--border); }
.tbl th { font-size: 12px; opacity: 0.6; }
.badge.ok { color: var(--success, #1a7f37); }
.badge.err { color: var(--danger, #d33); }
.small { font-size: 12px; }
.switch-line { display: flex; align-items: center; gap: 8px; margin-top: 12px; cursor: pointer; }
.f-label { display: block; margin: 10px 0 4px; font-size: 13px; opacity: 0.8; }
.f-input { width: 100%; padding: 8px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg); color: var(--text); }
.modal-mask { position: fixed; inset: 0; background: rgba(0,0,0,0.45); display: flex; align-items: center; justify-content: center; z-index: 50; }
.modal { background: var(--bg-card, #fff); border-radius: 12px; padding: 20px; width: 440px; max-width: 92vw; }
.modal-actions { display: flex; gap: 10px; margin-top: 16px; justify-content: flex-end; }
</style>
