import { test, expect } from '@playwright/test';
import { login, getClientConfig } from './helpers';

test.describe('Duration heatmap', () => {
  test('redirects to login when not authenticated', async ({ page }) => {
    await page.goto('/heatmap');
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });

  test('loads the heatmap and drag-selects into the comparison', async ({ page, request }) => {
    const hasE2EKey = !!process.env.E2E_API_KEY;
    const hasOIDCCreds = !!process.env.E2E_OIDC_EMAIL && !!process.env.E2E_OIDC_PASSWORD;
    const cfg = await getClientConfig(request);
    test.skip(
      !!cfg.oidc?.enabled && !hasE2EKey && !hasOIDCCreds,
      'Set E2E_API_KEY (preferred) or E2E_OIDC_EMAIL+E2E_OIDC_PASSWORD to run authenticated tests',
    );

    await login(page, request);

    const loaded = page.waitForResponse((r) => r.url().includes('/api/v1/heatmap'));
    await page.goto('/heatmap?range=7d');
    await expect(page.getByRole('heading', { name: 'Duration heatmap' })).toBeVisible({ timeout: 10000 });
    expect((await loaded).status()).toBe(200);

    const grid = page.getByTestId('heatmap-grid');
    // A project without spans in the range shows the empty state and has nothing to drag over.
    if (!(await grid.isVisible({ timeout: 5000 }).catch(() => false))) {
      await expect(page.getByText('No spans in this range.')).toBeVisible();
      return;
    }

    // Drag over the upper half of the grid, the slow spans.
    const box = await grid.boundingBox();
    expect(box).not.toBeNull();
    const { x, y, width, height } = box!;
    await page.mouse.move(x + width * 0.1, y + height * 0.05);
    await page.mouse.down();
    await page.mouse.move(x + width * 0.9, y + height * 0.45, { steps: 5 });
    await expect(page.getByTestId('heatmap-selection')).toBeVisible();
    await page.mouse.up();

    await expect(page).toHaveURL(/\/compare\?.*selection=/);
    const selection = new URL(page.url()).searchParams.get('selection') ?? '';
    expect(selection).toContain('duration_us');
    await expect(page.getByRole('heading', { name: 'Compare attributes' })).toBeVisible();
  });
});
