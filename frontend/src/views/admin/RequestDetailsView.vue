<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import {
  getCapturedRequestDetailConfig,
  listCapturedRequestDetails,
  streamCapturedRequestDetails,
  updateCapturedRequestDetailConfig,
  type CapturedRequestDetail,
  type CapturedRequestDetailConfig
} from '@/api/admin/ops'

type ViewMode = 'history' | 'live'
type BodyLimit = 256 | 512

const mode = ref<ViewMode>('history')
const rows = ref<CapturedRequestDetail[]>([])
const liveRows = ref<CapturedRequestDetail[]>([])
const config = ref<CapturedRequestDetailConfig>({ enabled: false, body_preview: false, retention_minutes: 30 })
const loading = ref(false)
const selected = ref<CapturedRequestDetail | null>(null)
const loadError = ref('')
const model = ref('')
const status = ref('')
const key = ref('')
const user = ref('')
const page = ref(1)
const total = ref(0)
const pageSize = ref(50)
const bodyLimitKB = ref<BodyLimit>(256)
const liveConnected = ref(false)
const liveError = ref('')
let liveController: AbortController | null = null

const displayedLiveRows = computed(() => liveRows.value.filter(row => {
  if (key.value && String(row.api_key_id || '') !== key.value) return false
  if (user.value && !`${row.user_email || ''} ${row.username || ''}`.toLowerCase().includes(user.value.toLowerCase())) return false
  return true
}))

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '请求失败'
}

async function loadHistory(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [result, nextConfig] = await Promise.all([
      listCapturedRequestDetails({ page: page.value, page_size: pageSize.value, model: model.value, status_code: status.value, api_key_id: key.value, user: user.value }),
      getCapturedRequestDetailConfig()
    ])
    rows.value = result.items
    total.value = result.total
    config.value = nextConfig
  } catch (error: unknown) {
    loadError.value = errorMessage(error)
  } finally {
    loading.value = false
  }
}

async function saveConfig(): Promise<void> {
  try { config.value = await updateCapturedRequestDetailConfig(config.value) }
  catch (error: unknown) { loadError.value = errorMessage(error) }
}

function search(): void { page.value = 1; void loadHistory() }
function stopLive(): void { liveController?.abort(); liveController = null; liveConnected.value = false }

async function startLive(): Promise<void> {
  stopLive()
  liveError.value = ''
  const controller = new AbortController()
  liveController = controller
  liveConnected.value = true
  try {
    await streamCapturedRequestDetails(bodyLimitKB.value, controller.signal, detail => {
      liveRows.value.unshift(detail)
      if (liveRows.value.length > 200) liveRows.value.length = 200
    })
  } catch (error: unknown) {
    if (!controller.signal.aborted) liveError.value = errorMessage(error)
  } finally {
    if (liveController === controller) { liveController = null; liveConnected.value = false }
  }
}

watch(mode, next => { if (next === 'live') void startLive(); else { stopLive(); void loadHistory() } })
watch(bodyLimitKB, () => { if (mode.value === 'live') void startLive() })
onMounted(loadHistory)
onUnmounted(stopLive)
</script>

<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="flex border-b border-gray-200 dark:border-dark-700">
        <button v-for="item in ([['history', '历史明细'], ['live', '实时请求流']] as const)" :key="item[0]" type="button" class="border-b-2 px-5 py-3 text-sm font-medium" :class="mode === item[0] ? 'border-primary-500 text-primary-600' : 'border-transparent text-gray-500'" @click="mode = item[0]">{{ item[1] }}</button>
      </div>

      <div v-if="mode === 'history'" class="card flex flex-wrap items-center gap-4 p-5">
        <label class="flex items-center gap-2 text-sm"><input v-model="config.enabled" type="checkbox" @change="saveConfig"> 保存历史摘要</label>
        <label class="text-sm">保留分钟 <input v-model.number="config.retention_minutes" class="input ml-2 w-20" type="number" min="5" max="720" @change="saveConfig"></label>
        <button class="btn btn-secondary ml-auto" :disabled="loading" @click="loadHistory">刷新</button>
      </div>

      <div v-else class="card flex flex-wrap items-center gap-4 p-5">
        <span class="text-sm font-medium">正文上限</span>
        <div class="inline-flex overflow-hidden rounded border border-gray-200 dark:border-dark-600">
          <button v-for="limit in ([256, 512] as const)" :key="limit" type="button" class="px-4 py-2 text-sm" :class="bodyLimitKB === limit ? 'bg-primary-600 text-white' : 'bg-white text-gray-600 dark:bg-dark-800 dark:text-gray-300'" @click="bodyLimitKB = limit">{{ limit }} KB</button>
        </div>
        <span class="text-sm" :class="liveConnected ? 'text-green-600' : 'text-gray-500'">{{ liveConnected ? '实时连接中' : '已断开' }}</span>
        <button class="btn btn-secondary ml-auto" @click="liveRows = []">清空当前屏幕</button>
      </div>

      <div class="card p-5">
        <p v-if="mode === 'history' && loadError" class="mb-4 text-sm text-red-600">{{ loadError }}</p>
        <p v-if="mode === 'live' && liveError" class="mb-4 text-sm text-red-600">{{ liveError }}</p>
        <div class="mb-4 flex flex-wrap gap-3">
          <input v-model="user" class="input w-48" placeholder="用户邮箱或昵称" @keyup.enter="search">
          <input v-if="mode === 'history'" v-model="model" class="input w-48" placeholder="模型" @keyup.enter="search">
          <input v-if="mode === 'history'" v-model="status" class="input w-32" placeholder="状态码" @keyup.enter="search">
          <input v-model="key" class="input w-40" placeholder="API Key ID" @keyup.enter="search">
          <button v-if="mode === 'history'" class="btn btn-primary" @click="search">筛选</button>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead><tr class="border-b text-left text-gray-500"><th class="p-2">时间</th><th>API Key</th><th>用户</th><th>模型</th><th>接口</th><th>状态</th><th>耗时</th><th></th></tr></thead>
            <tbody><tr v-for="row in mode === 'live' ? displayedLiveRows : rows" :key="row.id" class="border-b"><td class="p-2 whitespace-nowrap">{{ new Date(row.created_at).toLocaleString() }}</td><td>{{ row.api_key_name || `API Key #${row.api_key_id || '-'}` }}</td><td>{{ row.user_email || row.username || `用户 #${row.user_id}` }}</td><td>{{ row.model || '-' }}</td><td>{{ row.method }} {{ row.path }}</td><td>{{ row.status_code }}</td><td>{{ row.duration_ms }} ms</td><td><button v-if="mode === 'live'" class="text-primary-600" @click="selected = row">查看</button></td></tr></tbody>
          </table>
        </div>
        <Pagination v-if="mode === 'history' && total" :page="page" :total="total" :page-size="pageSize" @update:page="value => { page = value; loadHistory() }" @update:pageSize="value => { pageSize = value; page = 1; loadHistory() }" />
      </div>
    </div>
    <BaseDialog :show="!!selected" title="实时请求体" width="full" @close="selected = null"><pre class="max-h-[70vh] overflow-auto whitespace-pre-wrap break-words text-xs">{{ selected?.request_body || '此请求没有正文' }}</pre></BaseDialog>
  </AppLayout>
</template>
