import { apiFetch } from './client'

export interface Snapshot {
  id: string
  bucket: string
  message: string
  createdAt: number
  objects: number
  size: number
}

export interface SnapshotChange {
  key: string
  kind: 'added' | 'removed' | 'modified'
}

export interface SnapshotDiff {
  added: number
  removed: number
  modified: number
  changes: SnapshotChange[]
}

export interface RestoreResult {
  reverted: number
  removed: number
  skipped: number
}

export function listSnapshots(bucket: string): Promise<Snapshot[]> {
  return apiFetch(`/buckets/${bucket}/snapshots`)
}

export function createSnapshot(bucket: string, message: string): Promise<Snapshot> {
  return apiFetch(`/buckets/${bucket}/snapshots`, { method: 'POST', body: JSON.stringify({ message }) })
}

export function diffSnapshot(bucket: string, id: string): Promise<SnapshotDiff> {
  return apiFetch(`/buckets/${bucket}/snapshots/${id}/diff`)
}

export function restoreSnapshot(bucket: string, id: string): Promise<RestoreResult> {
  return apiFetch(`/buckets/${bucket}/snapshots/${id}/restore`, { method: 'POST' })
}

export function deleteSnapshot(bucket: string, id: string): Promise<void> {
  return apiFetch(`/buckets/${bucket}/snapshots/${id}`, { method: 'DELETE' })
}
