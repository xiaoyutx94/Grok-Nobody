<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import * as grok from '@/api/grok'
import { copyWithToast, toast } from '@/utils/clipboard'

const props = defineProps<{ account: any | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved', acc: any): void }>()

const saving = ref(false)
const errorMsg = ref('')
const reveal = reactive<Record<string, boolean>>({})
// 用量查询（sub2api 同款 billing 探测）
const usageLoading = ref(false)
const usage = ref<any | null>(null)
const usageError = ref('')

async function queryUsage() {
  const acc = props.account
  if (!acc?.id || !acc.access_token) {
    usageError.value = '该账号无 OAuth 凭证，无法查询用量'
    return
  }
  usageLoading.value = true
  usageError.value = ''
  usage.value = null
  try {
    const data = await grok.fetchAccountUsage(acc.id, true)
    usage.value = { ...(data.billing || {}), ...(data.quota || {}) }
  } catch (e: any) {
    usageError.value = e?.response?.data?.error || e?.message || '用量查询失败'
  } finally {
    usageLoading.value = false
  }
}

function fmtPct(p: any): string {
  if (p == null) return ''
  return `${Number(p).toFixed(1)}%`
}
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

        <!-- 用量查询（sub2api 同款 billing 探测：周 credits + 月额度） -->
        <div class="usage-box">
          <div class="usage-head">
            <span class="usage-title">用量查询</span>
            <button class="btn btn-ghost btn-xs" :disabled="usageLoading" @click="queryUsage">
              {{ usageLoading ? '查询中…' : usage ? '重新查询' : '查询用量' }}
            </button>
          </div>
          <div v-if="usageError" class="usage-err">{{ usageError }}</div>
          <div v-else-if="usage" class="usage-body">
            <div class="usage-row" v-if="usage.plan">
              <span class="usage-k">套餐</span><b>{{ usage.plan }}</b>
            </div>
            <div class="usage-row" v-if="usage.tokens?.remaining != null || usage.tokens?.limit != null">
              <span class="usage-k">Tokens</span>
              <span class="mono">剩 {{ usage.tokens?.remaining ?? '?' }} / {{ usage.tokens?.limit ?? '?' }}<template v-if="usage.tokens?.reset_at">（重置 {{ usage.tokens.reset_at.slice(5, 16).replace('T', ' ') }}）</template></span>
            </div>
            <div class="usage-row" v-if="usage.requests?.remaining != null || usage.requests?.limit != null">
              <span class="usage-k">请求</span>
              <span class="mono">{{ usage.requests?.remaining ?? '?' }} / {{ usage.requests?.limit ?? '?' }}</span>
            </div>
            <div class="usage-row" v-if="usage.period_type || usage.usage_percent != null">
              <span class="usage-k">周期</span>
              <b>{{ usage.period_type || '—' }}{{ usage.usage_percent != null ? ` · 已用 ${fmtPct(usage.usage_percent)}` : '' }}</b>
            </div>
            <div class="usage-row" v-if="usage.period_start || usage.period_end">
              <span class="usage-k">窗口</span>
              <span class="mono">{{ (usage.period_start || '—').slice(0, 16) }} ~ {{ (usage.period_end || '—').slice(0, 16) }}</span>
            </div>
            <div class="usage-row" v-if="usage.monthly_limit_cents || usage.used_cents">
              <span class="usage-k">金额</span>
              <span class="mono">已用 ${{ ((usage.used_cents || 0) / 100).toFixed(2) }} / ${{ ((usage.monthly_limit_cents || 0) / 100).toFixed(2) }}</span>
            </div>
            <div class="usage-row" v-if="usage.retry_after_seconds">
              <span class="usage-k">限流</span><b class="usage-warn">重置 {{ usage.retry_after_seconds }}s 后</b>
            </div>
            <div class="usage-row" v-if="usage.subscription_tier || usage.entitlement_status">
              <span class="usage-k">订阅</span>
              <span>{{ usage.subscription_tier || '' }}{{ usage.subscription_tier && usage.entitlement_status ? ' · ' : '' }}{{ usage.entitlement_status || '' }}</span>
            </div>
            <div class="usage-prods" v-if="usage.product_usage?.length">
              <div v-for="pu in usage.product_usage" :key="pu.product" class="usage-row">
                <span class="usage-k">{{ pu.product }}</span>
                <span v-if="pu.usage_percent != null">{{ fmtPct(pu.usage_percent) }}</span>
              </div>
            </div>
            <div v-if="usage.error_code || usage.error_message" class="usage-err">
              额度信号：{{ usage.error_code || '' }} {{ usage.error_message || '' }}
            </div>
            <div v-if="usage.partial" class="usage-note">部分窗口查询失败（周/月任一不可用）</div>
          </div>
          <div v-else class="usage-note">查询账号在 cli-chat-proxy 的用量（周 credits 与月度额度）</div>
        </div>
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

<style scoped>
.usage-box {
  margin-top: 14px;
  padding: 10px 12px;
  border: 1px solid var(--line, rgba(127, 127, 127, 0.25));
  border-radius: 10px;
  background: color-mix(in srgb, var(--panel, #fff) 60%, transparent);
}
.usage-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
}
.usage-title {
  font-size: 12px;
  font-weight: 700;
}
.usage-body {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
}
.usage-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
}
.usage-k {
  min-width: 44px;
  color: var(--ink-3, #888);
  font-size: 11px;
}
.usage-note {
  font-size: 11px;
  color: var(--ink-3, #888);
}
.usage-err {
  font-size: 11px;
  color: #d33;
}
</style>
