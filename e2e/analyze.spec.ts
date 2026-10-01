import { test, expect } from '@playwright/test';
import { login, getClientConfig } from './helpers';

test.describe('Query (group by)', () => {
  test('redirects to login when not authenticated', async ({ page }) => {
    await page.goto('/analyze');
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });

  test('runs a group-by query and shows a table and a chart', async ({ page, request }) => {
    const hasE2EKey = !!process.env.E2E_API_KEY;
    const hasOIDCCreds = !!process.env.E2E_OIDC_EMAIL && !!process.env.E2E_OIDC_PASSWORD;
    const cfg = await getClientConfig(request);
    test.skip(
      !!cfg.oidc?.enabled && !hasE2EKey && !hasOIDCCreds,
      'Set E2E_API_KEY (preferred) or E2E_OIDC_EMAIL+E2E_OIDC_PASSWORD to run authenticated tests',
    );

    await login(page, request);

    await page.goto('/analyze');
    await expect(page.getByRole('heading', { name: 'Query' })).toBeVisible({ timeout: 10000 });
    await expect(page.getByRole('combobox', { name: 'Time range' })).toHaveValue('24h');

    // Nothing runs until the query is submitted.
    await page.getByRole('button', { name: '+ Add group by' }).click();
    await page.getByLabel('Group by key 1', { exact: true }).fill('kind');
    await page.getByRole('button', { name: 'Run query' }).click();
    await expect(page).toHaveURL(/group_by=kind/);
    await expect(page).toHaveURL(/range=24h/);

    // A project with no spans in the window renders the empty state, otherwise rows.
    const table = page.getByRole('table');
    await expect(table).toBeVisible({ timeout: 15000 });
    await expect(table.getByRole('button', { name: 'Count' })).toBeVisible();
    await expect(table.getByRole('button', { name: /P95/ })).toBeVisible();

    await page.getByRole('tab', { name: 'Chart' }).click();
    await expect(page).toHaveURL(/view=chart/);
    await expect(page.getByLabel('Chart calculation')).toBeVisible();

    // The URL is the whole query, so a reload runs it again.
    await page.reload();
    await expect(page.getByRole('tab', { name: 'Chart', selected: true })).toBeVisible({ timeout: 15000 });
  });
});
