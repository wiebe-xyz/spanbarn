import { describe, expect, it } from 'vitest'
import { generateSpanId, generateTraceId } from '../ids.js'

describe('generateTraceId', () => {
  it('generates a 32 hex character string', () => {
    const id = generateTraceId()
    expect(id).toMatch(/^[0-9a-f]{32}$/)
  })
})

describe('generateSpanId', () => {
  it('generates a 16 hex character string', () => {
    const id = generateSpanId()
    expect(id).toMatch(/^[0-9a-f]{16}$/)
  })
})

describe('uniqueness', () => {
  it('generates unique trace IDs', () => {
    const ids = new Set<string>()
    for (let i = 0; i < 1000; i++) {
      ids.add(generateTraceId())
    }
    expect(ids.size).toBe(1000)
  })

  it('generates unique span IDs', () => {
    const ids = new Set<string>()
    for (let i = 0; i < 1000; i++) {
      ids.add(generateSpanId())
    }
    expect(ids.size).toBe(1000)
  })
})
