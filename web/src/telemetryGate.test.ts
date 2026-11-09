import { describe, expect, it } from 'vitest'
import { createTelemetryGate, spanStatusFor, TELEMETRY_PATH } from './telemetryGate'

describe('spanStatusFor', () => {
  it('treats a 401 as an expected answer', () => {
    expect(spanStatusFor(401)).toBe('OK')
  })
  it('keeps other client and server errors as errors', () => {
    expect(spanStatusFor(403)).toBe('ERROR')
    expect(spanStatusFor(404)).toBe('ERROR')
    expect(spanStatusFor(500)).toBe('ERROR')
  })
  it('marks 2xx and 3xx as OK', () => {
    expect(spanStatusFor(200)).toBe('OK')
    expect(spanStatusFor(304)).toBe('OK')
  })
})

describe('createTelemetryGate', () => {
  it('sends by default', () => {
    expect(createTelemetryGate().canSend()).toBe(true)
  })
  it('stops after the telemetry endpoint answers 401', () => {
    const gate = createTelemetryGate()
    gate.observe(TELEMETRY_PATH, 401)
    expect(gate.canSend()).toBe(false)
  })
  it('ignores a 401 from other paths', () => {
    const gate = createTelemetryGate()
    gate.observe('/api/v1/me', 401)
    expect(gate.canSend()).toBe(true)
  })
  it('ignores other failures of the telemetry endpoint', () => {
    const gate = createTelemetryGate()
    gate.observe(TELEMETRY_PATH, 429)
    gate.observe(TELEMETRY_PATH, 503)
    expect(gate.canSend()).toBe(true)
  })
  it('resumes after a successful sign-in', () => {
    const gate = createTelemetryGate()
    gate.observe(TELEMETRY_PATH, 401)
    gate.observe('/api/v1/login', 200)
    expect(gate.canSend()).toBe(true)
  })
  it('resumes after a successful session check', () => {
    const gate = createTelemetryGate()
    gate.observe(TELEMETRY_PATH, 401)
    gate.observe('/api/v1/me', 200)
    expect(gate.canSend()).toBe(true)
  })
  it('stays stopped on a failed sign-in', () => {
    const gate = createTelemetryGate()
    gate.observe(TELEMETRY_PATH, 401)
    gate.observe('/api/v1/login', 401)
    expect(gate.canSend()).toBe(false)
  })
})
