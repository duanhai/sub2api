import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, put } = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, put },
}))

import {
  getTopicSummarySettings,
  updateTopicSummarySettings,
} from '@/api/admin/settings'

describe('admin topic summary settings API', () => {
  beforeEach(() => {
    get.mockReset()
    put.mockReset()
  })

  it('uses a write-only API key contract', async () => {
    const response = {
      enabled: true,
      base_url: 'https://summary.example',
      model: 'gpt-5.6-luna',
      api_key_configured: true,
    }
    const update = {
      enabled: true,
      base_url: 'https://summary.example',
      model: 'gpt-5.6-luna',
      api_key: 'sk-test',
    }
    get.mockResolvedValueOnce({ data: response })
    put.mockResolvedValueOnce({ data: response })

    await expect(getTopicSummarySettings()).resolves.toEqual(response)
    await expect(updateTopicSummarySettings(update)).resolves.toEqual(response)
    expect(get).toHaveBeenCalledWith('/admin/settings/topic-summary')
    expect(put).toHaveBeenCalledWith('/admin/settings/topic-summary', update)
    expect(response).not.toHaveProperty('api_key')
  })
})
