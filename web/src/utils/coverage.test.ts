import { describe, it, expect } from 'vitest'
import { coverageNote } from './coverage'

describe('coverageNote', () => {
  const end = new Date('2026-10-26T12:20:00Z')

  it('names the covered minutes', () => {
    expect(coverageNote('2026-10-26T12:12:00Z', end)).toContain('last 8 min of the selected range')
  })

  it('switches to hours past two hours', () => {
    expect(coverageNote('2026-10-26T07:20:00Z', end)).toContain('last 5 h of')
  })

  it('never claims less than a minute', () => {
    expect(coverageNote('2026-10-26T12:19:50Z', end)).toContain('last 1 min of')
  })
})
