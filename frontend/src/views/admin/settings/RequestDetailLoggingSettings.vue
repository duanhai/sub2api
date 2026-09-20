<template>
  <section class="card" aria-labelledby="request-detail-title">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 id="request-detail-title" class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.requestDetailLogging.title') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.requestDetailLogging.description') }}</p>
    </div>
    <div class="space-y-4 p-6">
      <p v-if="loading" role="status">{{ t('common.loading') }}</p>
      <template v-else-if="settings">
        <p v-if="!settings.configured" class="text-sm text-amber-700 dark:text-amber-400">{{ t('admin.settings.requestDetailLogging.legacy') }}</p>
        <div class="flex items-center justify-between gap-4">
          <label for="request-detail-enabled">{{ t('admin.settings.requestDetailLogging.enabled') }}</label>
          <Toggle id="request-detail-enabled" v-model="settings.enabled" :disabled="saving" />
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label for="request-detail-mode" class="mb-2 block text-sm">{{ t('admin.settings.requestDetailLogging.mode') }}</label>
            <select id="request-detail-mode" v-model="settings.mode" class="input w-full" :disabled="saving">
              <option value="raw">{{ t('admin.settings.requestDetailLogging.raw') }}</option>
              <option value="dual">{{ t('admin.settings.requestDetailLogging.dual') }}</option>
              <option value="structured">{{ t('admin.settings.requestDetailLogging.structured') }}</option>
            </select>
          </div>
          <div>
            <label for="request-detail-limit" class="mb-2 block text-sm">{{ t('admin.settings.requestDetailLogging.bodyLimit') }}</label>
            <select id="request-detail-limit" v-model.number="settings.body_limit_kb" class="input w-full" :disabled="saving">
              <option :value="256">256 KB</option>
              <option :value="512">512 KB</option>
            </select>
          </div>
        </div>
        <div>
          <label for="request-detail-source" class="mb-2 block text-sm">{{ t('admin.settings.requestDetailLogging.source') }}</label>
          <input id="request-detail-source" v-model="settings.source" type="text" maxlength="256" class="input w-full" :disabled="saving" />
        </div>
        <div>
          <label for="request-detail-path" class="mb-2 block text-sm">{{ t('admin.settings.requestDetailLogging.path') }}</label>
          <input id="request-detail-path" :value="settings.path" type="text" readonly class="input w-full" />
          <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.requestDetailLogging.pathHint') }}</p>
        </div>
        <p class="text-sm">{{ t(settings.active ? 'admin.settings.requestDetailLogging.active' : 'admin.settings.requestDetailLogging.inactive') }}</p>
        <p v-if="settings.runtime_error" role="alert" class="text-sm text-red-600">{{ settings.runtime_error }}</p>
        <p v-if="settings.dropped || settings.write_errors" role="alert" class="text-sm text-amber-700 dark:text-amber-400">{{ t('admin.settings.requestDetailLogging.failures', { dropped: settings.dropped, errors: settings.write_errors }) }}</p>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.requestDetailLogging.effect') }}</p>
        <div class="flex gap-3">
          <button type="button" class="btn btn-primary" :disabled="saving" @click="save">{{ t('admin.settings.requestDetailLogging.save') }}</button>
          <button type="button" class="btn btn-secondary" :disabled="saving" @click="load">{{ t('admin.settings.requestDetailLogging.reload') }}</button>
        </div>
      </template>
      <button v-else type="button" class="btn btn-secondary" @click="load">{{ t('admin.settings.requestDetailLogging.reload') }}</button>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <p v-if="saved" role="status" class="text-sm text-green-600">{{ t('admin.settings.requestDetailLogging.saved') }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import { getRequestDetailLogging, updateRequestDetailLogging, type RequestDetailLoggingSettings } from '@/api/admin/requestDetailLogging'

const { t } = useI18n()
const settings = ref<RequestDetailLoggingSettings | null>(null)
const loading = ref(true)
const saving = ref(false)
const saved = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  saved.value = false
  error.value = ''
  try {
    settings.value = await getRequestDetailLogging()
  } catch {
    settings.value = null
    error.value = t('admin.settings.requestDetailLogging.loadFailed')
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!settings.value) return
  saved.value = false
  error.value = ''
  const { enabled, mode, body_limit_kb, source } = settings.value
  if (![256, 512].includes(body_limit_kb) || new TextEncoder().encode(source).length > 256 || /[\r\n]/.test(source) || source.includes('\0')) {
    error.value = t('admin.settings.requestDetailLogging.invalid')
    return
  }
  saving.value = true
  try {
    settings.value = await updateRequestDetailLogging({ enabled, mode, body_limit_kb, source })
    saved.value = true
  } catch {
    error.value = t('admin.settings.requestDetailLogging.saveFailed')
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
