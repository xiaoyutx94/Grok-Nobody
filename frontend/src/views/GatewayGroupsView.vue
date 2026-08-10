<script setup lang="ts">
import { onMounted, ref } from 'vue'
import * as gw from '@/api/gateway'
import * as grok from '@/api/grok'
import { confirmBox } from '@/utils/confirm'
import { toast } from '@/utils/clipboard'

const groups = ref<gw.GatewayGroup[]>([])
const stats = ref<Record<string, gw.GroupStats>>({})
const services = ref<gw.UpstreamService[]>([])
const pool = ref<grok.ProxyEntry[]>([])
const busy = ref(true)

const editing = ref<gw.GatewayGroup | null>(null)
const showForm = ref(false)
const form = ref({ name: '', desc: '', service_id: '', proxy_ids: [] as string[] })

async function load() {
  busy.value = true
  try {
    const [g, s, p] = await Promise.all([
      gw.getGroups(),
      gw.getServices(),
      grok.getProxyPool()
    ])
    groups.value = g.groups
    stats.value = g.stats
    services.value = s.services
    pool.value = p.entries?.filter((e) => e.enabled) ?? []
  } catch (e: any) {
    toast('加载失败: ' + (e?.response?.data?.error || e?.message || e))
  } finally {
    busy.value = false
  }
}

function openCreate() {
  editing.value = null
  form.value = {
    name: '',
    desc: '',
    service_id: services.value.find((s) => s.enabled)?.id || '',
    proxy_ids: []
  }
  showForm.value = true
}

function openEdit(g: gw.GatewayGroup) {
  editing.value = g
  form.value = { name: g.name, desc: g.desc || '', service_id: g.service_id || '', proxy_ids: [...(g.proxy_ids || [])] }
  showForm.value = true
}

function toggleProxy(url: string) {
  const i = form.value.proxy_ids.indexOf(url)
  if (i >= 0) form.value.proxy_ids.splice(i, 1)
  else form.value.proxy_ids.push(url)
}

// 代理备注（地区标识）：从代理池条目匹配
function proxyNote(url: string): string {
  const e = pool.value.find((p) => p.url === url)
  return e?.note || ''
}

// 分组绑定代理的展示摘要（url + 备注）
function proxySummary(ids: string[]): string {
  if (!ids?.length) return ''
  return ids.map((u) => {
    const n = proxyNote(u)
    return n ? `${u}（${n}）` : u
  }).join('\n')
}

async function save() {
  if (!form.value.name.trim()) {
    toast('分组名称不能为空')
    return
  }
  try {
    if (editing.value) {
      await gw.updateGroup(editing.value.id, form.value)
      toast('分组已更新')
    } else {
      await gw.createGroup(form.value)
      toast('分组已创建')
    }
    showForm.value = false
    await load()
  } catch (e: any) {
    toast('保存失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

async function remove(g: gw.GatewayGroup) {
  if (!(await confirmBox(`删除分组「${g.name}」？组内账号不会被删除，但将不再参与网关路由。`))) return
  try {
    await gw.deleteGroup(g.id)
    toast('分组已删除')
    await load()
  } catch (e: any) {
    toast('删除失败: ' + (e?.response?.data?.error || e?.message || e))
  }
}

function svcName(id: string) {
  return services.value.find((s) => s.id === id)?.name || (id ? id : '默认')
}

onMounted(load)
</script>

<template>
  <div class="page-head">
    <div>
      <h2>分组管理</h2>
      <p class="dim">账号池分组：绑定上游服务 + 从代理池选择转发出口代理</p>
    </div>
    <button class="btn" @click="openCreate">+ 新建分组</button>
  </div>

  <div class="card" style="padding: 6px 14px 14px">
    <table class="tbl">
      <thead>
        <tr>
          <th>分组</th>
          <th>绑定服务</th>
          <th>出口代理</th>
          <th>账号（可用/总数）</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="g in groups" :key="g.id">
          <td>
            <div class="g-name">{{ g.name }} <span v-if="g.builtin" class="pill">默认</span></div>
            <div class="dim small">{{ g.desc || '—' }}</div>
          </td>
          <td>{{ svcName(g.service_id || '') }}</td>
          <td>
            <span v-if="!g.proxy_ids?.length" class="dim">直连</span>
            <span v-else class="badge" :title="proxySummary(g.proxy_ids)">{{ g.proxy_ids.length }} 个代理</span>
          </td>
          <td>
            <span class="mono">{{ stats[g.id]?.available ?? 0 }}/{{ stats[g.id]?.total ?? 0 }}</span>
            <span v-if="stats[g.id]?.cooldown" class="dim small"> 冷却 {{ stats[g.id]?.cooldown }}</span>
            <span v-if="stats[g.id]?.forbidden" class="badge err">封禁 {{ stats[g.id]?.forbidden }}</span>
          </td>
          <td>
            <button class="icon-btn" title="编辑" @click="openEdit(g)">✎</button>
            <button v-if="!g.builtin" class="icon-btn" title="删除" @click="remove(g)">🗑</button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-if="busy" class="dim" style="padding: 12px">加载中…</div>
  </div>

  <div v-if="showForm" class="modal-mask" @click.self="showForm = false">
    <div class="modal">
      <h3>{{ editing ? '编辑分组' : '新建分组' }}</h3>
      <label class="f-label">名称</label>
      <input class="f-input" v-model="form.name" placeholder="如：主力组 / 备用组" />
      <label class="f-label">描述</label>
      <input class="f-input" v-model="form.desc" placeholder="可选" />
      <label class="f-label">绑定服务（上游渠道）</label>
      <select class="f-input" v-model="form.service_id">
        <option v-for="s in services" :key="s.id" :value="s.id" :disabled="!s.enabled">
          {{ s.name }}（{{ s.type }}）{{ s.enabled ? '' : '· 已禁用' }}
        </option>
      </select>
      <label class="f-label">转发出口代理（从代理池选择，空 = 直连；备注显示地区）</label>
      <div class="proxy-pick">
        <div v-if="!pool.length" class="dim small">代理池暂无启用代理，分组将直连 grok 官方</div>
        <label v-for="p in pool" :key="p.url" class="proxy-item">
          <input type="checkbox" :checked="form.proxy_ids.includes(p.url)" @change="toggleProxy(p.url)" />
          <span class="mono small">{{ p.url }}</span>
          <span v-if="p.note" class="proxy-note">{{ p.note }}</span>
        </label>
      </div>
      <div class="modal-actions">
        <button class="btn" @click="save">保存</button>
        <button class="btn ghost" @click="showForm = false">取消</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tbl { width: 100%; border-collapse: collapse; }
.tbl th, .tbl td { text-align: left; padding: 8px 10px; border-bottom: 1px solid var(--border); }
.tbl th { font-size: 12px; opacity: 0.6; }
.g-name { font-weight: 600; }
.badge.err { color: var(--danger, #d33); }
.small { font-size: 12px; }
.proxy-pick { max-height: 180px; overflow-y: auto; border: 1px solid var(--border); border-radius: 8px; padding: 6px; }
.proxy-item { display: flex; align-items: center; gap: 8px; padding: 3px 4px; cursor: pointer; font-size: 13px; }
.proxy-note { color: var(--accent, #4f7cff); font-size: 11.5px; background: color-mix(in srgb, var(--accent, #4f7cff) 10%, transparent); padding: 1px 6px; border-radius: 6px; white-space: nowrap; }
.f-label { display: block; margin: 10px 0 4px; font-size: 13px; opacity: 0.8; }
.f-input { width: 100%; padding: 8px; border: 1px solid var(--border); border-radius: 8px; background: var(--bg); color: var(--text); }
.modal-mask { position: fixed; inset: 0; background: rgba(0,0,0,0.45); display: flex; align-items: center; justify-content: center; z-index: 50; }
.modal { background: var(--bg-card, #fff); border-radius: 12px; padding: 20px; width: 460px; max-width: 92vw; max-height: 86vh; overflow-y: auto; }
.modal-actions { display: flex; gap: 10px; margin-top: 16px; justify-content: flex-end; }
</style>
