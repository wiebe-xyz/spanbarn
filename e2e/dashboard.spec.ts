import { test, expect } from '@playwright/test';

test.describe('Dashboard', () => {
  test('redirects to login when not authenticated', async ({ page }) => {
    // The app's API client redirects to /login on 401 responses.
    // Navigating to the root without a session triggers this redirect
    // once the dashboard tries to fetch services.
    await page.goto('/');
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });

  test('services page renders after login', async ({ page }) => {
    await page.goto('/login');

    // Log in with default credentials
    await page.fill('input#username', 'admin');
    await page.fill('input#password', 'admin');
    await page.click('button[type="submit"]');

    // Should redirect to the services page (root)
    await expect(page).toHaveURL(/^\/$|\/$/,  { timeout: 10000 });

    // The dashboard sidebar should be visible with navigation items
    await expect(page.locator('text=Services')).toBeVisible();
    await expect(page.locator('text=Traces')).toBeVisible();
    await expect(page.locator('text=Dependencies')).toBeVisible();
  });
});
