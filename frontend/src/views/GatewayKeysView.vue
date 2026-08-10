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
// 复制视觉反馈：keyId → 'copied'（2s 后清除）
const copiedId = ref('')
let copyTimer: any = null

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

// 复制完整密钥：列表行 → reveal 接口取回 → 剪贴板 → 视觉反馈
async function copyKeyById(id: string) {
  try {
    const r = await gw.revealKey(id)
    await writeClipboard(r.key)
    flashCopied(id)
  } catch (e: any) {
    toast('复制失败: ' + (e?.response?.data?.error || e?.message || e), 'bad')
  }
}

// 创建弹窗复制（key_full 已在内存）
async function copyCreated() {
  if (!created.value?.key_full) return
  await writeClipboard(created.value.key_full)
  flashCopied(created.value.id)
}

function writeClipboard(text: string): Promise<void> {
  // 不弹提示：视觉反馈靠复制按钮/图标变绿（✓ 2s）
  return (navigator.clipboard?.writeText(text) || Promise.reject(new Error('no clipboard'))).catch(() => {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  })
}

function flashCopied(id: string) {
  copiedId.value = id
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = setTimeout(() => { copiedId.value = '' }, 2000)
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
    <div class="key-title">新密钥已生成（点击复制，立即保存）</div>
    <div class="key-line">
      <code class="mono">{{ created.key_full }}</code>
      <button class="btn" :class="{ 'copied': copiedId === created.id }" @click="copyCreated">
        {{ copiedId === created.id ? '✓ 已复制' : '复制密钥' }}
      </button>
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
          <td>
            <div class="key-cell">
              <code class="mono small">{{ k.key }}</code>
              <button class="icon-btn" :class="{ 'copied': copiedId === k.id }"
                :title="copiedId === k.id ? '已复制' : '复制完整密钥'" @click="copyKeyById(k.id)">
                <span v-if="copiedId === k.id" class="copied-mark">✓</span>
                <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" style="width:15px;height:15px"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
              </button>
            </div>
          </td>
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
.key-cell { display: flex; align-items: center; gap: 6px; }
.icon-btn.copied { color: var(--success, #1a7f37); }
.copied-mark { font-weight: 700; font-size: 14px; }
.btn.copied { background: var(--success, #1a7f37); border-color: var(--success, #1a7f37); color: #fff; }
.f-label { display: block; margin: 10px 0 4px; font-size: 13px; opacity: 0.8; }
.f-input { width: 100%; padding: 8px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg); color: var(--text); }
.modal-mask { position: fixed; inset: 0; background: rgba(0,0,0,0.45); display: flex; align-items: center; justify-content: center; z-index: 50; }
.modal { background: var(--bg-card, #fff); border-radius: 12px; padding: 20px; width: 420px; max-width: 92vw; }
.modal-actions { display: flex; gap: 10px; margin-top: 16px; justify-content: flex-end; }
</style>
