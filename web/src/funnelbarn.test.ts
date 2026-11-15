import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const track = vi.fn()
const page = vi.fn()
const ctor = vi.fn()

vi.mock('@funnelbarn/js', () => ({
  FunnelBarnClient: class {
    constructor(opts: unknown) {
      ctor(opts)
    }
    track = track
    page = page
  },
}))

function configure(body: unknown, ok = true): void {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok, json: async () => body }))
}

describe('funnelbarn', () => {
  beforeEach(() => {
    vi.resetModules()
    track.mockClear()
    page.mockClear()
    ctor.mockClear()
    localStorage.clear()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('maps hostnames to environments', async () => {
    const { environmentFor } = await import('./funnelbarn')
    expect(environmentFor('spanbarn.wiebe.xyz')).toBe('production')
    expect(environmentFor('spanbarn-staging.wiebe.xyz')).toBe('staging')
    expect(environmentFor('spanbarn-testing.wiebe.xyz')).toBe('testing')
    expect(environmentFor('localhost')).toBe('development')
  })

  it('creates no client and sends nothing when the config has no funnelbarn block', async () => {
    configure({})
    const { initFunnelBarn, trackConversion, trackConversionOnce } = await import('./funnelbarn')
    await initFunnelBarn()
    trackConversion('signup')
    trackConversionOnce('signup')
    await Promise.resolve()
    expect(ctor).not.toHaveBeenCalled()
    expect(track).not.toHaveBeenCalled()
    expect(localStorage.getItem('fb_once_signup')).toBeNull()
  })

  it('starts the SDK with the recorder on and keeps key pages out of recordings', async () => {
    configure({ funnelbarn: { endpoint: 'https://fb.example/', api_key: 'k', project: 'spanbarn' } })
    const { initFunnelBarn } = await import('./funnelbarn')
    await initFunnelBarn()
    expect(ctor).toHaveBeenCalledWith(
      expect.objectContaining({
        apiKey: 'k',
        endpoint: 'https://fb.example',
        projectName: 'spanbarn',
        recording: true,
        recordExclude: expect.arrayContaining(['/settings*']),
      }),
    )
    expect(page).toHaveBeenCalledTimes(1)
  })

  it('holds an event sent before the config arrives and sends a once-event one time', async () => {
    configure({ funnelbarn: { endpoint: 'https://fb.example', api_key: 'k' } })
    const { initFunnelBarn, trackConversionOnce } = await import('./funnelbarn')
    trackConversionOnce('signup')
    expect(track).not.toHaveBeenCalled()
    await initFunnelBarn()
    await Promise.resolve()
    trackConversionOnce('signup')
    await Promise.resolve()
    expect(track).toHaveBeenCalledTimes(1)
    expect(track).toHaveBeenCalledWith('signup', undefined)
  })
})
