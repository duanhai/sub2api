import { apiClient } from '../client'

export interface RequestDetailLoggingPolicy {
  enabled: boolean
  mode: 'raw' | 'dual' | 'structured'
  body_limit_kb: number
  source: string
}

export interface RequestDetailLoggingSettings extends RequestDetailLoggingPolicy {
  path: string
  configured: boolean
  active: boolean
  runtime_error?: string
  dropped: number
  write_errors: number
}

export async function getRequestDetailLogging(): Promise<RequestDetailLoggingSettings> {
  const { data } = await apiClient.get<RequestDetailLoggingSettings>('/admin/settings/request-detail-logging')
  return data
}

export async function updateRequestDetailLogging(policy: RequestDetailLoggingPolicy): Promise<RequestDetailLoggingSettings> {
  const { data } = await apiClient.put<RequestDetailLoggingSettings>('/admin/settings/request-detail-logging', policy)
  return data
}
