import { test, expect } from '@playwright/test';
import { login, getClientConfig } from './helpers';

test.describe('Compare attributes', () => {
  test('redirects to login when not authenticated', async ({ page }) => {
    await page.goto('/compare');
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });

  test('builds a selection and asks for a comparison', async ({ page, request }) => {
    const hasE2EKey = !!process.env.E2E_API_KEY;
    const hasOIDCCreds = !!process.env.E2E_OIDC_EMAIL && !!process.env.E2E_OIDC_PASSWORD;
    const cfg = await getClientConfig(request);
    test.skip(
      !!cfg.oidc?.enabled && !hasE2EKey && !hasOIDCCreds,
      'Set E2E_API_KEY (preferred) or E2E_OIDC_EMAIL+E2E_OIDC_PASSWORD to run authenticated tests',
    );

    await login(page, request);

    await page.goto('/compare');
    await expect(page.getByRole('heading', { name: 'Compare attributes' })).toBeVisible({ timeout: 10000 });
    await expect(page.getByRole('button', { name: 'Compare' })).toBeDisabled();

    const selection = page.getByRole('group', { name: 'Selection' });
    await selection.getByText('+ Add filter').click();
    await selection.getByLabel('Filter key').fill('kind');
    await selection.getByLabel('Filter value').fill('server');

    const compared = page.waitForResponse((r) => r.url().includes('/api/v1/attributes/compare'));
    await page.getByRole('button', { name: 'Compare' }).click();
    expect((await compared).status()).toBe(200);
    await expect(page).toHaveURL(/selection=/);
    await expect(page.getByTestId('compare-note')).toBeVisible({ timeout: 15000 });

    // The traces page links here with its filters as the selection.
    await page.goto('/traces?service=web');
    await page.getByRole('button', { name: 'Compare attributes' }).click();
    await expect(page).toHaveURL(/\/compare\?.*selection=/);
  });
});
