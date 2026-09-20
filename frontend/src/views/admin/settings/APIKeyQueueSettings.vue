<template>
  <section class="card" aria-labelledby="api-key-queue-title">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 id="api-key-queue-title" class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.apiKeyQueue.title') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.apiKeyQueue.description') }}</p>
    </div>
    <div class="space-y-4 p-6">
      <p v-if="loading" role="status">{{ t('common.loading') }}</p>
      <template v-else-if="loaded">
        <p v-if="!settings.configured" class="text-sm text-amber-700 dark:text-amber-400">{{ t('admin.settings.apiKeyQueue.legacy') }}</p>
        <div class="flex items-center justify-between gap-4">
          <label for="api-key-queue-enabled">{{ t('admin.settings.apiKeyQueue.enabled') }}</label>
          <Toggle id="api-key-queue-enabled" v-model="settings.enabled" :disabled="saving" />
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label for="api-key-queue-max" class="mb-2 block text-sm">{{ t('admin.settings.apiKeyQueue.maxWaiting') }}</label>
            <input id="api-key-queue-max" v-model.number="settings.max_waiting" type="number" min="1" max="100" step="1" class="input w-full" :disabled="saving" />
          </div>
          <div>
            <label for="api-key-queue-timeout" class="mb-2 block text-sm">{{ t('admin.settings.apiKeyQueue.timeout') }}</label>
            <input id="api-key-queue-timeout" v-model.number="settings.timeout_seconds" type="number" min="1" max="60" step="1" class="input w-full" :disabled="saving" />
          </div>
        </div>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.apiKeyQueue.effect') }}</p>
        <button type="button" class="btn btn-primary" :disabled="saving" @click="save">{{ t('admin.settings.apiKeyQueue.save') }}</button>
      </template>
      <button v-else type="button" class="btn btn-secondary" @click="load">{{ t('admin.settings.apiKeyQueue.retry') }}</button>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <p v-if="saved" role="status" class="text-sm text-green-600">{{ t('admin.settings.apiKeyQueue.saved') }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import { getAPIKeyQueueSettings, updateAPIKeyQueueSettings } from '@/api/admin/apiKeyQueue'

const { t } = useI18n()
const settings = ref({ enabled: false, max_waiting: 6, timeout_seconds: 60, configured: false })
const loading = ref(true)
const loaded = ref(false)
const saving = ref(false)
const saved = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    settings.value = await getAPIKeyQueueSettings()
    loaded.value = true
  } catch {
    error.value = t('admin.settings.apiKeyQueue.loadFailed')
  } finally {
    loading.value = false
  }
}

async function save() {
  saved.value = false
  error.value = ''
  const { enabled, max_waiting, timeout_seconds } = settings.value
  if (!Number.isInteger(max_waiting) || max_waiting < 1 || max_waiting > 100 ||
      !Number.isInteger(timeout_seconds) || timeout_seconds < 1 || timeout_seconds > 60) {
    error.value = t('admin.settings.apiKeyQueue.invalid')
    return
  }
  saving.value = true
  try {
    settings.value = await updateAPIKeyQueueSettings({ enabled, max_waiting, timeout_seconds })
    saved.value = true
  } catch {
    error.value = t('admin.settings.apiKeyQueue.saveFailed')
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
