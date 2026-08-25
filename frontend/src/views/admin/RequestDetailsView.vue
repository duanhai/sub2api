<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { streamCapturedRequestDetails, type CapturedRequestDetail } from '@/api/admin/ops'

type BodyLimit = 256 | 512
type ConnectionState = 'connecting' | 'connected' | 'disconnected'

const liveRows = ref<CapturedRequestDetail[]>([])
const selected = ref<CapturedRequestDetail | null>(null)
const bodyLimitKB = ref<BodyLimit>(256)
const connectionState = ref<ConnectionState>('disconnected')
const lastHeartbeat = ref<Date | null>(null)
const liveError = ref('')
const key = ref('')
const user = ref('')
const model = ref('')
const status = ref('')
let liveController: AbortController | null = null

const displayedRows = computed(() => liveRows.value.filter(row => {
  if (key.value && String(row.api_key_id || '') !== key.value) return false
  if (user.value && !`${row.user_email || ''} ${row.username || ''}`.toLowerCase().includes(user.value.toLowerCase())) return false
  if (model.value && !String(row.model || '').toLowerCase().includes(model.value.toLowerCase())) return false
  if (status.value && String(row.status_code) !== status.value) return false
  return true
}))

const connectionLabel = computed(() => {
  if (connectionState.value === 'connecting') return '连接中'
  if (connectionState.value === 'disconnected') return '已断开'
  return lastHeartbeat.value ? `已连接 · ${lastHeartbeat.value.toLocaleTimeString()}` : '已连接'
})

const bodyStateLabels: Record<CapturedRequestDetail['body_state'], string> = {
  captured: '已捕获', truncated: '已截断', not_applicable: 'GET 无正文', empty: '空正文', decode_failed: '解码失败'
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '实时请求流连接失败'
}

function stopLive(): void {
  liveController?.abort()
  liveController = null
  connectionState.value = 'disconnected'
}

async function startLive(): Promise<void> {
  stopLive()
  liveError.value = ''
  lastHeartbeat.value = null
  connectionState.value = 'connecting'
  const controller = new AbortController()
  liveController = controller
  try {
    await streamCapturedRequestDetails(bodyLimitKB.value, controller.signal, {
      onConnected: () => { connectionState.value = 'connected'; lastHeartbeat.value = new Date() },
      onHeartbeat: () => { lastHeartbeat.value = new Date() },
      onEvent: detail => {
        lastHeartbeat.value = new Date()
        liveRows.value.unshift(detail)
        if (liveRows.value.length > 200) liveRows.value.length = 200
      }
    })
  } catch (error: unknown) {
    if (!controller.signal.aborted) liveError.value = errorMessage(error)
  } finally {
    if (liveController === controller) { liveController = null; connectionState.value = 'disconnected' }
  }
}

watch(bodyLimitKB, () => { void startLive() })
onMounted(startLive)
onUnmounted(stopLive)
</script>

<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="card flex flex-wrap items-center gap-4 p-5">
        <span class="text-sm font-medium">正文上限</span>
        <div class="inline-flex overflow-hidden rounded border border-gray-200 dark:border-dark-600">
          <button v-for="limit in ([256, 512] as const)" :key="limit" type="button" class="px-4 py-2 text-sm" :class="bodyLimitKB === limit ? 'bg-primary-600 text-white' : 'bg-white text-gray-600 dark:bg-dark-800 dark:text-gray-300'" @click="bodyLimitKB = limit">{{ limit }} KB</button>
        </div>
        <span class="inline-flex items-center gap-2 text-sm" :class="connectionState === 'connected' ? 'text-green-600' : connectionState === 'connecting' ? 'text-amber-600' : 'text-red-600'">
          <span class="h-2 w-2 rounded-full bg-current"></span>{{ connectionLabel }}
        </span>
        <button v-if="connectionState === 'disconnected'" class="btn btn-secondary" @click="startLive">重新连接</button>
        <button class="btn btn-secondary ml-auto" @click="liveRows = []">清空当前屏幕</button>
      </div>

      <div class="card p-5">
        <p v-if="liveError" class="mb-4 text-sm text-red-600">{{ liveError }}</p>
        <div class="mb-4 flex flex-wrap gap-3">
          <input v-model="key" class="input w-40" placeholder="API Key ID">
          <input v-model="user" class="input w-48" placeholder="用户邮箱或昵称">
          <input v-model="model" class="input w-48" placeholder="模型">
          <input v-model="status" class="input w-32" placeholder="状态码">
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead><tr class="border-b text-left text-gray-500"><th class="p-2">时间</th><th>API Key</th><th>用户</th><th>模型</th><th>接口</th><th>状态</th><th>耗时</th><th>正文</th><th></th></tr></thead>
            <tbody><tr v-for="row in displayedRows" :key="row.id" class="border-b"><td class="p-2 whitespace-nowrap">{{ new Date(row.created_at).toLocaleString() }}</td><td>{{ row.api_key_name || `API Key #${row.api_key_id || '-'}` }}</td><td>{{ row.user_email || row.username || `用户 #${row.user_id}` }}</td><td>{{ row.model || '-' }}</td><td>{{ row.method }} {{ row.path }}</td><td>{{ row.status_code }}</td><td>{{ row.duration_ms }} ms</td><td>{{ bodyStateLabels[row.body_state] }}</td><td><button v-if="row.request_body" class="text-primary-600" @click="selected = row">查看</button></td></tr></tbody>
          </table>
        </div>
      </div>
    </div>
    <BaseDialog :show="!!selected" title="实时请求体" width="full" @close="selected = null"><pre class="max-h-[70vh] overflow-auto whitespace-pre-wrap break-words text-xs">{{ selected?.request_body }}</pre></BaseDialog>
  </AppLayout>
</template>
