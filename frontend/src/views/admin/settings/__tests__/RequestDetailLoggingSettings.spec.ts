import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RequestDetailLoggingSettings from '../RequestDetailLoggingSettings.vue'

const { getSettings, saveSettings } = vi.hoisted(() => ({ getSettings: vi.fn(), saveSettings: vi.fn() }))
vi.mock('@/api/admin/requestDetailLogging', () => ({ getRequestDetailLogging: getSettings, updateRequestDetailLogging: saveSettings }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const baseline = { enabled: false, mode: 'raw', body_limit_kb: 256, source: '', path: '/legacy/requests.jsonl', configured: false, active: false, dropped: 0, write_errors: 0 }

describe('Request detail logging panel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getSettings.mockResolvedValue({ ...baseline })
    saveSettings.mockImplementation(async value => ({ ...baseline, ...value, configured: true, active: value.enabled }))
  })

  it('shows the legacy path read-only and saves only recording policy', async () => {
    const wrapper = mount(RequestDetailLoggingSettings)
    await flushPromises()
    expect(wrapper.get('#request-detail-path').attributes()).toHaveProperty('readonly')
    expect((wrapper.get('#request-detail-path').element as HTMLInputElement).value).toBe(baseline.path)
    await wrapper.get('#request-detail-enabled').trigger('click')
    await wrapper.get('#request-detail-mode').setValue('dual')
    await wrapper.get('#request-detail-limit').setValue('512')
    await wrapper.get('#request-detail-source').setValue('example.com')
    await wrapper.get('.btn-primary').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith({ enabled: true, mode: 'dual', body_limit_kb: 512, source: 'example.com' })
    expect(wrapper.text()).toContain('requestDetailLogging.saved')
    expect(wrapper.text()).not.toContain('requestDetailLogging.legacy')
  })

  it('prevents saving after load failure and permits retry', async () => {
    getSettings.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mount(RequestDetailLoggingSettings)
    await flushPromises()
    expect(wrapper.find('.btn-primary').exists()).toBe(false)
    expect(wrapper.text()).toContain('loadFailed')
    await wrapper.get('.btn-secondary').trigger('click')
    await flushPromises()
    expect(wrapper.find('.btn-primary').exists()).toBe(true)
  })

  it('validates byte limits and never claims a failed save succeeded', async () => {
    const wrapper = mount(RequestDetailLoggingSettings)
    await flushPromises()
    await wrapper.get('#request-detail-source').setValue('测'.repeat(86))
    await wrapper.get('.btn-primary').trigger('click')
    expect(saveSettings).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('invalid')
    await wrapper.get('#request-detail-source').setValue('valid')
    saveSettings.mockRejectedValueOnce(new Error('permission denied'))
    await wrapper.get('.btn-primary').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('saveFailed')
    expect(wrapper.text()).not.toContain('requestDetailLogging.saved')
  })
})
