import { test, expect, type Page } from '@playwright/test';
import { login, getClientConfig } from './helpers';

const errorFilter = JSON.stringify({ match: 'and', filters: [{ key: 'status', op: '=', value: 'error' }] });

const panels = [
  {
    title: 'Error count by url.path',
    url: `/analyze?run=1&range=24h&group_by=url.path&calc=count&filter=${encodeURIComponent(errorFilter)}`,
  },
  {
    title: 'P95 by http.request.method',
    url: '/analyze?run=1&range=24h&group_by=http.request.method&calc=p95&view=chart',
  },
  {
    title: 'COUNT by user_agent.original',
    url: '/analyze?run=1&range=24h&group_by=user_agent.original&calc=count',
  },
];

async function saveToBoard(page: Page, boardName: string, title: string, url: string) {
  await page.goto(url);
  // The button shows once the query has a result, an empty one included.
  await page.getByRole('button', { name: 'Save to board' }).click({ timeout: 20000 });
  await page.getByRole('combobox', { name: 'Board' }).selectOption({ label: boardName });
  await page.getByLabel('Panel title').fill(title);
  await page.getByRole('button', { name: 'Add panel' }).click();
  await expect(page.getByRole('status')).toContainText(`Saved to ${boardName}`);
}

test.describe('Boards', () => {
  test('redirects to login when not authenticated', async ({ page }) => {
    await page.goto('/boards');
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });

  test('saves three query panels to a board that keeps one time range', async ({ page, request }) => {
    const hasE2EKey = !!process.env.E2E_API_KEY;
    const hasOIDCCreds = !!process.env.E2E_OIDC_EMAIL && !!process.env.E2E_OIDC_PASSWORD;
    const cfg = await getClientConfig(request);
    test.skip(
      !!cfg.oidc?.enabled && !hasE2EKey && !hasOIDCCreds,
      'Set E2E_API_KEY (preferred) or E2E_OIDC_EMAIL+E2E_OIDC_PASSWORD to run authenticated tests',
    );

    await login(page, request);

    const boardName = `E2E board ${Date.now()}`;
    await page.goto('/boards');
    await expect(page.getByRole('heading', { name: 'Boards' })).toBeVisible({ timeout: 10000 });
    await page.getByLabel('New board name').fill(boardName);
    await page.getByRole('button', { name: 'Create board' }).click();
    await expect(page.getByRole('heading', { name: boardName })).toBeVisible({ timeout: 10000 });
    const boardUrl = page.url();
    await expect(page.getByText('This board has no panels')).toBeVisible();

    for (const p of panels) {
      await saveToBoard(page, boardName, p.title, p.url);
    }

    await page.goto(boardUrl);
    const cards = page.getByTestId('panel');
    await expect(cards).toHaveCount(3, { timeout: 15000 });
    await expect(cards.locator('h2')).toHaveText(panels.map((p) => p.title));

    // One range for every panel, saved with the board.
    const range = page.getByRole('combobox', { name: 'Time range' });
    await expect(range).toHaveValue('24h');
    await range.selectOption('1h');
    await page.getByRole('combobox', { name: 'Refresh interval' }).selectOption('60');

    await page.reload();
    await expect(cards).toHaveCount(3, { timeout: 15000 });
    await expect(page.getByRole('combobox', { name: 'Time range' })).toHaveValue('1h');
    await expect(page.getByRole('combobox', { name: 'Refresh interval' })).toHaveValue('60');

    // The first and third panels are tables, the second a chart. A project with no
    // spans in the window shows the empty table, otherwise rows.
    await expect(cards.nth(0).getByRole('table')).toBeVisible({ timeout: 15000 });
    await expect(cards.nth(1).getByRole('button', { name: 'Show table' })).toBeVisible();
    await expect(cards.nth(2).getByRole('table')).toBeVisible();

    // Reordering sticks.
    await page.getByRole('button', { name: `Move ${panels[0].title} later` }).click();
    await page.reload();
    await expect(cards.locator('h2')).toHaveText([panels[1].title, panels[0].title, panels[2].title], { timeout: 15000 });

    // Clean up.
    page.once('dialog', (d) => d.accept());
    await page.getByRole('button', { name: 'Delete board' }).click();
    await expect(page).toHaveURL(/\/boards\?project=/);
    await expect(page.getByRole('link', { name: new RegExp(boardName) })).toHaveCount(0);
  });
});
