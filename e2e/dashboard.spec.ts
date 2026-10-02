import { test, expect } from '@playwright/test';
import { login, getClientConfig } from './helpers';

test.describe('Dashboard', () => {
  test('redirects to login when not authenticated', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });

  test('landing page is the dashboard and services moved to /services', async ({ page, request }) => {
    const hasE2EKey = !!process.env.E2E_API_KEY;
    const hasOIDCCreds = !!process.env.E2E_OIDC_EMAIL && !!process.env.E2E_OIDC_PASSWORD;
    const cfg = await getClientConfig(request);
    test.skip(
      !!cfg.oidc?.enabled && !hasE2EKey && !hasOIDCCreds,
      'Set E2E_API_KEY (preferred) or E2E_OIDC_EMAIL+E2E_OIDC_PASSWORD to run authenticated tests',
    );

    await login(page, request);

    await page.goto('/');
    for (const title of [
      'Trace Counts by Service',
      'Trace Counts by HTTP Status Code',
      'Trace Duration Heatmap',
      'Duration Heatmap',
      'Duration by Service',
      'Duration by Name',
    ]) {
      await expect(page.getByRole('region', { name: title, exact: true })).toBeVisible({ timeout: 10000 });
    }
    await expect(page.getByRole('combobox', { name: 'Time range' })).toHaveValue('24h');

    await page.goto('/services');
    await expect(page.getByRole('heading', { name: 'Services' }).first()).toBeVisible();
  });

  test('a zoomed window and filter chips can be linked, widened and cleared', async ({ page, request }) => {
    const hasE2EKey = !!process.env.E2E_API_KEY;
    const hasOIDCCreds = !!process.env.E2E_OIDC_EMAIL && !!process.env.E2E_OIDC_PASSWORD;
    const cfg = await getClientConfig(request);
    test.skip(
      !!cfg.oidc?.enabled && !hasE2EKey && !hasOIDCCreds,
      'Set E2E_API_KEY (preferred) or E2E_OIDC_EMAIL+E2E_OIDC_PASSWORD to run authenticated tests',
    );

    await login(page, request);

    const to = Date.now() - 3600_000;
    const from = to - 600_000;
    await page.goto(`/?from=${from}&to=${to}&service=e2e-service&min_us=1000`);

    const bar = page.getByRole('group', { name: 'Active zoom and filters' });
    await expect(bar).toContainText('Zoomed', { timeout: 10000 });
    await expect(page.getByRole('combobox', { name: 'Time range' })).toHaveValue('custom');
    await expect(page.getByRole('button', { name: 'Remove service = e2e-service' })).toBeVisible();
    await expect(page.getByRole('button', { name: /^Remove duration/ })).toBeVisible();
    for (const title of ['Trace Counts by Service', 'Trace Duration Heatmap', 'Duration by Service']) {
      await expect(page.getByRole('region', { name: title, exact: true })).toBeVisible();
    }

    await page.getByRole('button', { name: 'Zoom out' }).click();
    await expect(page).toHaveURL(/from=\d+&to=\d+/);

    await page.getByRole('button', { name: 'Remove service = e2e-service' }).click();
    await expect(page.getByRole('button', { name: 'Remove service = e2e-service' })).toHaveCount(0);

    await page.getByRole('button', { name: 'Reset zoom' }).click();
    await expect(page.getByRole('combobox', { name: 'Time range' })).toHaveValue('24h');
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
