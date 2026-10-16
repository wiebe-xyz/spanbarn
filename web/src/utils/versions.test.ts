import { describe, it, expect } from 'vitest'
import { p95Change } from '../utils/versions'
import type { VersionSummary } from '../api/types'

const v = (p95Us: number): VersionSummary => ({
  version: 'x',
  firstSeen: '',
  lastSeen: '',
  spanCount: 1,
  errorCount: 0,
  errorRate: 0,
  p50Us: 0,
  p95Us,
  p99Us: 0,
})

describe('p95Change', () => {
  it('shows the rise against the previous version', () => {
    expect(p95Change(v(1200), v(1000))).toBe('+20%')
  })
  it('shows a fall without a plus sign', () => {
    expect(p95Change(v(800), v(1000))).toBe('-20%')
  })
  it('is empty without a previous version or a baseline', () => {
    expect(p95Change(v(800), undefined)).toBe('')
    expect(p95Change(v(800), v(0))).toBe('')
  })
})
