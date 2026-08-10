<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import * as grok from '@/api/grok'
import { copyWithToast, toast } from '@/utils/clipboard'

const props = defineProps<{ account: any | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved', acc: any): void }>()

const saving = ref(false)
const errorMsg = ref('')
const reveal = reactive<Record<string, boolean>>({})
const form = reactive({
  email: '',
  password: '',
  sso: '',
  sso_rw: '',
  proxy: '',
  base_url: '',
  note: '',
  imported: false
})

watch(
  () => props.account,
  (acc) => {
    errorMsg.value = ''
    Object.keys(reveal).forEach((k) => (reveal[k] = false))
    if (!acc) return
    form.email = acc.email || ''
    form.password = acc.password || ''
    form.sso = acc.sso || ''
    form.sso_rw = acc.sso_rw || ''
    form.proxy = acc.proxy || ''
    form.base_url = acc.base_url || ''
    form.note = acc.note || ''
    form.imported = Boolean(acc.imported)
  },
  { immediate: true }
)

const dirty = computed(() => {
  const a = props.account
  if (!a) return false
  return (
    form.email !== (a.email || '') ||
    form.password !== (a.password || '') ||
    form.sso !== (a.sso || '') ||
    form.sso_rw !== (a.sso_rw || '') ||
    form.proxy !== (a.proxy || '') ||
    form.base_url !== (a.base_url || '') ||
    form.note !== (a.note || '') ||
    form.imported !== Boolean(a.imported)
  )
})

const tokenRows = computed(() => {
  const a = props.account
  if (!a) return []
  return [
    { key: 'access_token', label: 'Access Token', value: a.access_token || '' },
    { key: 'refresh_token', label: 'Refresh Token', value: a.refresh_token || '' },
    { key: 'id_token', label: 'ID Token', value: a.id_token || '' }
  ]
})

function mask(v: string) {
  if (!v) return '-'
  if (v.length <= 12) return '•'.repeat(v.length)
  return v.slice(0, 6) + '…' + v.slice(-4)
}

async function save() {
  if (!props.account?.id) return
  saving.value = true
  errorMsg.value = ''
  try {
    const acc = await grok.updateAccount(props.account.id, {
      email: form.email,
      password: form.password,
      sso: form.sso,
      sso_rw: form.sso_rw,
      proxy: form.proxy,
      base_url: form.base_url,
      note: form.note,
      imported: form.imported
    })
    toast('已保存')
    emit('saved', acc)
  } catch (e: any) {
    errorMsg.value = e?.response?.data?.error || e?.message || String(e)
    toast('保存失败', 'bad')
  } finally {
    saving.value = false
  }
}

const createdAt = computed(() => (props.account?.created_at || '').replace('T', ' ').slice(0, 19))
const updatedAt = computed(() => (props.account?.updated_at || '').replace('T', ' ').slice(0, 19))
</script>

<template>
  <div v-if="account" class="overlay" @click.self="emit('close')">
    <div class="sheet sheet-lg">
      <div class="sheet-head">
        <div>
          <div class="kicker">账号详情 / 编辑</div>
          <h3 class="sheet-title mono">{{ account.email || account.id }}</h3>
        </div>
        <button class="btn btn-icon btn-ghost" data-tip="关闭" @click="emit('close')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><path d="M18 6 6 18M6 6l12 12"/></svg>
        </button>
      </div>

      <p v-if="errorMsg" class="banner is-bad">{{ errorMsg }}</p>

      <div class="detail-meta">
        <span class="pill">创建 {{ createdAt || '-' }}</span>
        <span class="pill">更新 {{ updatedAt || '-' }}</span>
        <span class="pill" :class="account.imported ? 'run' : ''">{{ account.imported ? '已入库' : '未入库' }}</span>
        <span v-if="account.last_test_status" class="pill" :class="account.last_test_status === 'ok' ? 'run' : 'is-bad-pill'">
          上次测试 {{ account.last_test_status === 'ok' ? '通过' : '失败' }}
        </span>
      </div>
      <p v-if="account.last_test_error" class="note" style="word-break:break-all">上次失败原因：{{ account.last_test_error }}</p>

      <div class="gf" style="margin-top:14px">
        <label class="field">
          邮箱
          <div class="ctl input-wrap">
            <input class="input" v-model="form.email" />
            <button class="icon-btn" data-tip="复制" @click="copyWithToast(form.email, '邮箱')">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
            </button>
          </div>
        </label>
        <label class="field">
          密码
          <div class="ctl input-wrap">
            <input class="input" :type="reveal.password ? 'text' : 'password'" v-model="form.password" />
            <button class="icon-btn" :data-tip="reveal.password ? '隐藏' : '显示'" @click="reveal.password = !reveal.password">
              <svg v-if="reveal.password" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
              <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 5.2A9.8 9.8 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-2.4 3.3M6.5 6.7A17 17 0 0 0 2 12s3.6 7 10 7a9.6 9.6 0 0 0 4-.85"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
            </button>
            <button class="icon-btn" data-tip="复制" @click="copyWithToast(form.password, '密码')">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
            </button>
          </div>
        </label>
        <label class="field">
          SSO
          <div class="ctl input-wrap">
            <input class="input mono" :type="reveal.sso ? 'text' : 'password'" v-model="form.sso" />
            <button class="icon-btn" :data-tip="reveal.sso ? '隐藏' : '显示'" @click="reveal.sso = !reveal.sso">
              <svg v-if="reveal.sso" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
              <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 5.2A9.8 9.8 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-2.4 3.3M6.5 6.7A17 17 0 0 0 2 12s3.6 7 10 7a9.6 9.6 0 0 0 4-.85"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
            </button>
            <button class="icon-btn" data-tip="复制" @click="copyWithToast(form.sso, 'SSO')">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
            </button>
          </div>
        </label>
        <label class="field">
          SSO_RW
          <div class="ctl input-wrap">
            <input class="input mono" :type="reveal.sso_rw ? 'text' : 'password'" v-model="form.sso_rw" />
            <button class="icon-btn" :data-tip="reveal.sso_rw ? '隐藏' : '显示'" @click="reveal.sso_rw = !reveal.sso_rw">
              <svg v-if="reveal.sso_rw" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
              <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 5.2A9.8 9.8 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-2.4 3.3M6.5 6.7A17 17 0 0 0 2 12s3.6 7 10 7a9.6 9.6 0 0 0 4-.85"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
            </button>
            <button class="icon-btn" data-tip="复制" @click="copyWithToast(form.sso_rw, 'SSO_RW')">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
            </button>
          </div>
        </label>
        <label class="field">
          代理（留空直连）
          <input class="input ctl mono" v-model="form.proxy" placeholder="socks5://user:pass@host:1080" />
        </label>
        <label class="field">
          Base URL（留空用官方）
          <input class="input ctl mono" v-model="form.base_url" placeholder="https://api.x.ai/v1" />
        </label>
      </div>

      <label class="field" style="margin-top:12px">
        备注
        <input class="input ctl" v-model="form.note" placeholder="给这个账号加个标记" />
      </label>

      <label class="check" style="margin-top:12px">
        <input type="checkbox" v-model="form.imported" />
        <span>标记为已入库</span>
      </label>

      <div class="token-list">
        <div v-for="row in tokenRows" :key="row.key" class="token-row">
          <span class="token-label">{{ row.label }}</span>
          <span class="token-val mono">{{ row.value ? (reveal[row.key] ? row.value : mask(row.value)) : '-' }}</span>
          <button v-if="row.value" class="icon-btn" :data-tip="reveal[row.key] ? '隐藏' : '显示'" @click="reveal[row.key] = !reveal[row.key]">
            <svg v-if="reveal[row.key]" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
            <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 5.2A9.8 9.8 0 0 1 12 5c6.4 0 10 7 10 7a17 17 0 0 1-2.4 3.3M6.5 6.7A17 17 0 0 0 2 12s3.6 7 10 7a9.6 9.6 0 0 0 4-.85"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
          </button>
          <button v-if="row.value" class="icon-btn" data-tip="复制" @click="copyWithToast(row.value, row.label)">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/></svg>
          </button>
        </div>
      </div>

      <div class="sheet-foot">
        <span class="note">{{ dirty ? '有未保存修改' : '无修改' }}</span>
        <div class="spacer" />
        <button class="btn btn-ghost" @click="emit('close')">取消</button>
        <button class="btn btn-primary" :disabled="!dirty || saving" @click="save">{{ saving ? '保存中…' : '保存' }}</button>
      </div>
    </div>
  </div>
</template>
