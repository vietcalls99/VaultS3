import { apiFetch } from './client'

export interface VersionStatus {
  current: string
  latest?: string
  updateAvailable: boolean
  checkedAt?: number
  error?: string
}

export function getVersion(): Promise<VersionStatus> {
  return apiFetch<VersionStatus>('/version')
}
