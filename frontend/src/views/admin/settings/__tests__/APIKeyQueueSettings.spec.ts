import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import APIKeyQueueSettings from '../APIKeyQueueSettings.vue'

const { getSettings, saveSettings } = vi.hoisted(() => ({ getSettings: vi.fn(), saveSettings: vi.fn() }))
vi.mock('@/api/admin/apiKeyQueue', () => ({ getAPIKeyQueueSettings: getSettings, updateAPIKeyQueueSettings: saveSettings }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('API Key queue panel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getSettings.mockResolvedValue({ enabled: false, max_waiting: 6, timeout_seconds: 60, configured: false })
    saveSettings.mockImplementation(async value => ({ ...value, configured: true }))
  })

  it('saves a global policy without requiring a Key ID or changing its execution limit', async () => {
    const wrapper = mount(APIKeyQueueSettings)
    await flushPromises()
    await wrapper.get('#api-key-queue-enabled').trigger('click')
    await wrapper.get('#api-key-queue-max').setValue(8)
    await wrapper.get('.btn-primary').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith({ enabled: true, max_waiting: 8, timeout_seconds: 60 })
    expect(wrapper.text()).toContain('admin.settings.apiKeyQueue.saved')
    expect(wrapper.text()).not.toContain('admin.settings.apiKeyQueue.legacy')
    await wrapper.get('#api-key-queue-enabled').trigger('click')
    await wrapper.get('.btn-primary').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenLastCalledWith({ enabled: false, max_waiting: 8, timeout_seconds: 60 })
  })

  it('rejects invalid limits and reports storage failures without claiming success', async () => {
    const wrapper = mount(APIKeyQueueSettings)
    await flushPromises()
    await wrapper.get('#api-key-queue-max').setValue(101)
    await wrapper.get('.btn-primary').trigger('click')
    expect(saveSettings).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('invalid')
    await wrapper.get('#api-key-queue-max').setValue(6)
    saveSettings.mockRejectedValueOnce(new Error('failed'))
    await wrapper.get('.btn-primary').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('saveFailed')
    expect(wrapper.text()).not.toContain('admin.settings.apiKeyQueue.saved')
  })
})
