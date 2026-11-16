// BrandTrace page tracker. The public site key comes from /api/v1/client-config
// (SPANBARN_BRANDTRACE_SITE_KEY). When the server sends no brandtrace block the
// SDK is never loaded and nothing is sent.

import { fetchClientConfig } from './api/clientConfig'

const SCRIPT_ID = 'brandtrace-sdk'

export async function initBrandTrace(): Promise<void> {
  if (typeof document === 'undefined' || document.getElementById(SCRIPT_ID)) return
  try {
    const bt = (await fetchClientConfig()).brandtrace
    if (!bt?.site_key || !bt.sdk_url) return
    const script = document.createElement('script')
    script.id = SCRIPT_ID
    script.async = true
    script.src = bt.sdk_url
    script.setAttribute('data-site-key', bt.site_key)
    document.head.appendChild(script)
  } catch { /* analytics never breaks the page */ }
}
