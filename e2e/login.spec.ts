import { test, expect } from '@playwright/test';
import { getClientConfig } from './helpers';

test.describe('Login page — native auth', () => {
  test.beforeEach(async ({ request, page }) => {
    const cfg = await getClientConfig(request);
    test.skip(!!cfg.oidc?.enabled, 'Instance uses OIDC; native login form is not shown');
    // Pre-load the page so the skip applies before any assertions.
    await page.goto('/login');
  });

  test('loads the login page', async ({ page }) => {
    await expect(page.locator('text=SpanBarn')).toBeVisible();
    await expect(page.locator('text=Distributed tracing dashboard')).toBeVisible();
  });

  test('has username and password fields', async ({ page }) => {
    await expect(page.locator('input#username')).toBeVisible();
    await expect(page.locator('input#password')).toBeVisible();
    await expect(page.locator('button[type="submit"]')).toBeVisible();
    await expect(page.locator('button[type="submit"]')).toHaveText('Sign in');
  });

  test('shows error for invalid credentials', async ({ page }) => {
    await page.fill('input#username', 'wrong-user');
    await page.fill('input#password', 'wrong-pass');
    await page.click('button[type="submit"]');
    await expect(page.locator('text=Invalid username or password')).toBeVisible({ timeout: 10000 });
  });
});

test.describe('Login page — OIDC auth', () => {
  test('redirects /login to the OIDC provider', async ({ page, request }) => {
    const cfg = await getClientConfig(request);
    test.skip(!cfg.oidc?.enabled, 'Instance uses native auth; OIDC redirect does not apply');

    await page.goto('/login');
    await expect(page).toHaveURL(/iam(\.\w+)?\.wiebe\.xyz/, { timeout: 10000 });
  });
});
