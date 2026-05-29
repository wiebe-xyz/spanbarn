import type { APIRequestContext, Page } from '@playwright/test';

export interface ClientConfig {
  oidc?: { enabled?: boolean; loginURL?: string };
}

export async function getClientConfig(request: APIRequestContext): Promise<ClientConfig> {
  try {
    const res = await request.get('/api/v1/client-config');
    if (!res.ok()) return {};
    return (await res.json()) as ClientConfig;
  } catch {
    return {};
  }
}

// loginE2E calls the E2E session endpoint with the project ingest API key.
// The response sets a session cookie on the browser context directly —
// no OIDC flow or email verification needed. Requires E2E_API_KEY env var.
export async function loginE2E(page: Page) {
  const apiKey = process.env.E2E_API_KEY;
  if (!apiKey) throw new Error('E2E_API_KEY must be set for E2E session login');

  const res = await page.request.post('/api/v1/e2e/session', {
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  if (!res.ok()) {
    const body = await res.text();
    throw new Error(`E2E session endpoint returned ${res.status()}: ${body}`);
  }

  // Session cookie is now active on this browser context.
  await page.goto('/');
  await page.waitForURL(/^\/$|\//, { timeout: 10000 });
}

// loginNative fills the SpanBarn username/password form and submits.
export async function loginNative(
  page: Page,
  username = process.env.E2E_USERNAME ?? 'admin',
  password = process.env.E2E_PASSWORD ?? 'admin',
) {
  await page.goto('/login');
  await page.fill('input#username', username);
  await page.fill('input#password', password);
  await page.click('button[type="submit"]');
  await page.waitForURL(/^\/$|\/dashboard/, { timeout: 10000 });
}

// loginOIDC navigates through the IAM password form and waits for the
// SpanBarn OIDC callback to complete. Requires E2E_OIDC_EMAIL and
// E2E_OIDC_PASSWORD environment variables.
export async function loginOIDC(page: Page) {
  const email = process.env.E2E_OIDC_EMAIL;
  const password = process.env.E2E_OIDC_PASSWORD;
  if (!email || !password) {
    throw new Error('E2E_OIDC_EMAIL and E2E_OIDC_PASSWORD must be set for OIDC login');
  }

  await page.goto('/login');
  await page.waitForURL(/iam(\.\w+)?\.wiebe\.xyz/, { timeout: 10000 });

  // IAM shows magic-link form by default. Fill the shared email field first,
  // then switch to password mode — the email field is shared between both forms
  // and stays visible throughout; _pw_email is always aria-hidden.
  await page.fill('input[name="email"]:not([aria-hidden])', email);
  await page.click('button:has-text("Use password")');
  await page.fill('input#_pw_input', password);
  await page.click('button[type="submit"]:has-text("Sign in")');

  // Wait until the OIDC callback has redirected us back to SpanBarn.
  await page.waitForURL((url) => !url.href.includes('.wiebe.xyz/auth/'), { timeout: 15000 });
}

// login picks the right auth flow: E2E bypass > OIDC > native.
export async function login(page: Page, request: APIRequestContext) {
  if (process.env.E2E_API_KEY) {
    await loginE2E(page);
    return;
  }
  const cfg = await getClientConfig(request);
  if (cfg.oidc?.enabled) {
    await loginOIDC(page);
  } else {
    await loginNative(page);
  }
}
