import { apiClient } from '../client'

export interface APIKeyQueueSettings {
  enabled: boolean
  max_waiting: number
  timeout_seconds: number
  configured: boolean
}

export async function getAPIKeyQueueSettings(): Promise<APIKeyQueueSettings> {
  const { data } = await apiClient.get<APIKeyQueueSettings>('/admin/settings/api-key-queue')
  return data
}

export async function updateAPIKeyQueueSettings(settings: Omit<APIKeyQueueSettings, 'configured'>): Promise<APIKeyQueueSettings> {
  const { data } = await apiClient.put<APIKeyQueueSettings>('/admin/settings/api-key-queue', settings)
  return data
}
