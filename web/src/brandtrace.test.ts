import { beforeEach, describe, expect, it, vi } from 'vitest'

function stubConfig(body: unknown): void {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => body }))
}

describe('initBrandTrace', () => {
  beforeEach(() => {
    vi.resetModules()
    document.head.innerHTML = ''
  })

  it('adds no script when the config has no brandtrace block', async () => {
    stubConfig({})
    const { initBrandTrace } = await import('./brandtrace')
    await initBrandTrace()
    expect(document.getElementById('brandtrace-sdk')).toBeNull()
  })

  it('adds no script for an empty site key', async () => {
    stubConfig({ brandtrace: { site_key: '', sdk_url: 'https://bt.example/sdk.js' } })
    const { initBrandTrace } = await import('./brandtrace')
    await initBrandTrace()
    expect(document.getElementById('brandtrace-sdk')).toBeNull()
  })

  it('loads the SDK once with the site key', async () => {
    stubConfig({ brandtrace: { site_key: 'k1', sdk_url: 'https://bt.example/sdk.js' } })
    const { initBrandTrace } = await import('./brandtrace')
    await initBrandTrace()
    await initBrandTrace()
    const tags = document.querySelectorAll('script#brandtrace-sdk')
    expect(tags).toHaveLength(1)
    expect(tags[0].getAttribute('src')).toBe('https://bt.example/sdk.js')
    expect(tags[0].getAttribute('data-site-key')).toBe('k1')
  })
})
