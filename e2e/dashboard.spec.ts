import { test, expect } from '@playwright/test';
import { login, getClientConfig } from './helpers';

test.describe('Dashboard', () => {
  test('redirects to login when not authenticated', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });

  test('services page renders after login', async ({ page, request }) => {
    const hasE2EKey = !!process.env.E2E_API_KEY;
    const hasOIDCCreds = !!process.env.E2E_OIDC_EMAIL && !!process.env.E2E_OIDC_PASSWORD;
    const cfg = await getClientConfig(request);
    test.skip(
      !!cfg.oidc?.enabled && !hasE2EKey && !hasOIDCCreds,
      'Set E2E_API_KEY (preferred) or E2E_OIDC_EMAIL+E2E_OIDC_PASSWORD to run authenticated tests',
    );

    await login(page, request);

    await expect(page).toHaveURL(/^\/$|\/$/, { timeout: 10000 });
    await expect(page.locator('text=Services').first()).toBeVisible();
    await expect(page.locator('text=Traces').first()).toBeVisible();
    await expect(page.locator('text=Dependencies').first()).toBeVisible();
  });
});
