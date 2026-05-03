import { test, expect } from '@playwright/test';

test.describe('Login page', () => {
  test('loads the login page', async ({ page }) => {
    await page.goto('/login');
    await expect(page.locator('text=SpanBarn')).toBeVisible();
    await expect(page.locator('text=Distributed tracing dashboard')).toBeVisible();
  });

  test('has username and password fields', async ({ page }) => {
    await page.goto('/login');
    await expect(page.locator('input#username')).toBeVisible();
    await expect(page.locator('input#password')).toBeVisible();
    await expect(page.locator('button[type="submit"]')).toBeVisible();
    await expect(page.locator('button[type="submit"]')).toHaveText('Sign in');
  });

  test('shows error for invalid credentials', async ({ page }) => {
    await page.goto('/login');
    await page.fill('input#username', 'wrong-user');
    await page.fill('input#password', 'wrong-pass');
    await page.click('button[type="submit"]');

    // The API returns 401, which the login form catches and displays
    await expect(page.locator('text=Invalid username or password')).toBeVisible({ timeout: 10000 });
  });
});
