import { test, expect } from '@playwright/test';
import { login, getClientConfig } from './helpers';

test.describe('Trace health', () => {
  test('redirects to login when not authenticated', async ({ page }) => {
    await page.goto('/trace-health');
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });

  test('shows the four structural views and the trace list filter', async ({ page, request }) => {
    const hasE2EKey = !!process.env.E2E_API_KEY;
    const hasOIDCCreds = !!process.env.E2E_OIDC_EMAIL && !!process.env.E2E_OIDC_PASSWORD;
    const cfg = await getClientConfig(request);
    test.skip(
      !!cfg.oidc?.enabled && !hasE2EKey && !hasOIDCCreds,
      'Set E2E_API_KEY (preferred) or E2E_OIDC_EMAIL+E2E_OIDC_PASSWORD to run authenticated tests',
    );

    await login(page, request);

    await page.goto('/trace-health');
    await expect(page.getByRole('heading', { name: 'Trace health' })).toBeVisible({ timeout: 10000 });
    for (const name of ['Orphan spans', 'Rootless traces', 'Single-span traces', 'Span names']) {
      await expect(page.getByRole('tab', { name })).toBeVisible();
    }
    await expect(page.getByRole('combobox', { name: 'Time range' })).toHaveValue('24h');

    await page.getByRole('tab', { name: 'Span names' }).click();
    await expect(page.getByRole('tab', { name: 'Span names' })).toHaveAttribute('aria-selected', 'true');
    await expect(page).toHaveURL(/tab=names/);

    // The trace list carries the same filter and links here.
    await page.goto('/traces?structure=rootless');
    await expect(page.getByRole('combobox', { name: 'Structure' })).toHaveValue('rootless');
    await page.getByRole('button', { name: 'Trace health' }).click();
    await expect(page).toHaveURL(/\/trace-health/);
  });
});
