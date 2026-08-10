<script setup lang="ts">
import { onMounted, ref } from 'vue'
import * as gw from '@/api/gateway'
import { confirmBox } from '@/utils/confirm'
import { toast } from '@/utils/clipboard'

const keys = ref<gw.GatewayAPIKey[]>([])
const groups = ref<gw.GatewayGroup[]>([])
const showForm = ref(false)
const form = ref({ name: '', group_id: '' })
const created = ref<gw.GatewayAPIKey | null>(null)
const busy = ref(true)

async function load() {
  busy.value = true
  try {
    const [k, g] = await Promise.all([gw.getKeys(), gw.getGroups()])
    keys.value = k.keys
    groups.value = g.groups
  } finally {
    busy.value = false
  }
}

async function create() {
  if (!form.value.group_id) {
    toast('请选择分组')
    return
  }
  try {
    created.value = await gw.createKey(form.value)
    showForm.value = false
    form.value = { name: '', group_id: groups.value[0]?.id || '' }
    await load()
  } catch (e: any) {
    toast('生成失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

function copyKey(k: gw.GatewayAPIKey) {
  const text = k.key_full || ''
  if (!text) {
    toast('完整密钥仅在创建时显示一次，无法再次复制')
    return
  }
  toast('密钥已复制')
  navigator.clipboard?.writeText(text).catch(() => {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  })
}

async function toggle(k: gw.GatewayAPIKey) {
  const next = k.status === 'active' ? 'disabled' : 'active'
  try {
    await gw.setKeyStatus(k.id, next)
    toast(next === 'active' ? '密钥已启用' : '密钥已禁用')
    await load()
  } catch (e: any) {
    toast('操作失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

async function remove(k: gw.GatewayAPIKey) {
  if (!(await confirmBox(`删除密钥「${k.name}」？使用该密钥的客户端将立即失效。`))) return
  try {
    await gw.deleteKey(k.id)
    toast('密钥已删除')
    await load()
  } catch (e: any) {
    toast('删除失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

function groupName(id: string) {
  return groups.value.find((g) => g.id === id)?.name || id || '—'
}

onMounted(load)
</script>

<template>
  <div class="page-head">
    <div>
      <h2>API 密钥</h2>
      <p class="dim">为分组生成密钥，客户端用它调用本地网关（Bearer sk-xxx）</p>
    </div>
    <button class="btn" @click="showForm = true">+ 生成密钥</button>
  </div>

  <div v-if="created" class="card key-show">
    <div class="key-title">新密钥已生成（仅显示这一次，请立即复制保存）</div>
    <div class="key-line">
      <code class="mono">{{ created.key_full }}</code>
      <button class="btn" @click="copyKey(created!)">复制</button>
      <button class="btn ghost" @click="created = null">关闭</button>
    </div>
  </div>

  <div class="card" style="padding: 6px 14px 14px">
    <table class="tbl">
      <thead>
        <tr>
          <th>名称</th>
          <th>密钥</th>
          <th>分组</th>
          <th>状态</th>
          <th>创建时间</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="k in keys" :key="k.id">
          <td>{{ k.name }}</td>
          <td><code class="mono small">{{ k.key }}</code></td>
          <td>{{ groupName(k.group_id) }}</td>
          <td><span class="badge" :class="k.status === 'active' ? 'ok' : 'err'">{{ k.status === 'active' ? '启用' : '禁用' }}</span></td>
          <td class="dim small">{{ k.created_at?.slice(0, 19).replace('T', ' ') }}</td>
          <td>
            <button class="icon-btn" :title="k.status === 'active' ? '禁用' : '启用'" @click="toggle(k)">{{ k.status === 'active' ? '⏸' : '▶' }}</button>
            <button class="icon-btn" title="删除" @click="remove(k)">🗑</button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-if="busy" class="dim" style="padding: 12px">加载中…</div>
    <div v-if="!busy && !keys.length" class="dim" style="padding: 12px">还没有密钥，点击右上角「生成密钥」</div>
  </div>

  <div v-if="showForm" class="modal-mask" @click.self="showForm = false">
    <div class="modal">
      <h3>生成 API 密钥</h3>
      <label class="f-label">名称</label>
      <input class="f-input" v-model="form.name" placeholder="如：我的客户端" />
      <label class="f-label">绑定分组</label>
      <select class="f-input" v-model="form.group_id">
        <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</option>
      </select>
      <div class="modal-actions">
        <button class="btn" @click="create">生成</button>
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
.key-show { border: 1px solid var(--success, #1a7f37); padding: 14px; }
.key-title { font-weight: 600; margin-bottom: 8px; }
.key-line { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.key-line code { background: var(--bg-code, rgba(0,0,0,0.05)); padding: 8px 10px; border-radius: 8px; word-break: break-all; }
.f-label { display: block; margin: 10px 0 4px; font-size: 13px; opacity: 0.8; }
.f-input { width: 100%; padding: 8px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg); color: var(--text); }
.modal-mask { position: fixed; inset: 0; background: rgba(0,0,0,0.45); display: flex; align-items: center; justify-content: center; z-index: 50; }
.modal { background: var(--bg-card, #fff); border-radius: 12px; padding: 20px; width: 420px; max-width: 92vw; }
.modal-actions { display: flex; gap: 10px; margin-top: 16px; justify-content: flex-end; }
</style>
