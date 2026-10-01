import { test, expect } from '@playwright/test';
import { login, getClientConfig } from './helpers';

test.describe('Attributes', () => {
  test('redirects to login when not authenticated', async ({ page }) => {
    await page.goto('/attributes');
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });

  test('shows the attribute discovery filters and a scan note', async ({ page, request }) => {
    const hasE2EKey = !!process.env.E2E_API_KEY;
    const hasOIDCCreds = !!process.env.E2E_OIDC_EMAIL && !!process.env.E2E_OIDC_PASSWORD;
    const cfg = await getClientConfig(request);
    test.skip(
      !!cfg.oidc?.enabled && !hasE2EKey && !hasOIDCCreds,
      'Set E2E_API_KEY (preferred) or E2E_OIDC_EMAIL+E2E_OIDC_PASSWORD to run authenticated tests',
    );

    await login(page, request);

    await page.goto('/attributes');
    await expect(page.getByRole('heading', { name: 'Attributes' })).toBeVisible({ timeout: 10000 });
    await expect(page.getByRole('combobox', { name: 'Time range' })).toHaveValue('24h');
    await expect(page.getByRole('combobox', { name: 'Span name' })).toBeVisible();
    await expect(page.getByRole('combobox', { name: 'Sampling' })).toBeVisible();

    await page.getByRole('combobox', { name: 'Time range' }).selectOption('7d');
    await expect(page).toHaveURL(/range=7d/);
    await expect(page.getByTestId('scan-note')).toBeVisible({ timeout: 15000 });

    // The trace health page links here.
    await page.goto('/trace-health');
    await page.getByRole('link', { name: 'Attributes' }).click();
    await expect(page).toHaveURL(/\/attributes/);
  });
});
