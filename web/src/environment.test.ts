import { beforeEach, describe, expect, it } from 'vitest'
import { ALL_ENVIRONMENTS, setEnvironment, withEnvironment } from './environment'

describe('withEnvironment', () => {
  beforeEach(() => setEnvironment('production'))

  it('adds the selected environment to the reads that take one', () => {
    expect(withEnvironment('/api/v1/traces?from=a&to=b')).toBe('/api/v1/traces?from=a&to=b&environment=production')
    expect(withEnvironment('/api/v1/analyze')).toBe('/api/v1/analyze?environment=production')
    setEnvironment('staging')
    expect(withEnvironment('/api/v1/spans/search?project_id=3')).toBe(
      '/api/v1/spans/search?project_id=3&environment=staging',
    )
  })

  it('leaves other endpoints, trace details and explicit environments alone', () => {
    expect(withEnvironment('/api/v1/services?from=a')).toBe('/api/v1/services?from=a')
    expect(withEnvironment('/api/v1/traces/abc123')).toBe('/api/v1/traces/abc123')
    expect(withEnvironment('/api/v1/traces?environment=testing&from=a')).toBe(
      '/api/v1/traces?environment=testing&from=a',
    )
  })

  it('adds nothing when the selection is all', () => {
    setEnvironment(ALL_ENVIRONMENTS)
    expect(withEnvironment('/api/v1/traces?from=a')).toBe('/api/v1/traces?from=a')
  })

  it('keeps the choice for the next visit', () => {
    setEnvironment('testing')
    expect(localStorage.getItem('spanbarn.environment')).toBe('testing')
  })
})
