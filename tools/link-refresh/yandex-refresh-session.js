// yandex-refresh-session.js — Refreshes Yandex session cookies via Playwright
// Run via cron every 12h: 0 */12 * * * cd /home/ilya/link-refresh && node yandex-refresh-session.js
//
// Initial setup (one-time, manual):
//   1. node yandex-login.js   (or manually export cookies)
//   2. Login in the browser window that opens
//   3. Cookies saved to yandex-cookies.json
//
// After that, this script keeps the session alive indefinitely.

const { chromium } = require('playwright');
const path = require('path');

const COOKIES_PATH = path.join(__dirname, 'yandex-cookies.json');

(async () => {
  const browser = await chromium.launch({ headless: true });

  let context;
  try {
    context = await browser.newContext({
      storageState: COOKIES_PATH,
      locale: 'ru-RU',
      userAgent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36',
    });
  } catch (err) {
    console.error('ERROR: Cannot load cookies from', COOKIES_PATH);
    console.error('Run yandex-login.js first to create initial session.');
    process.exit(1);
  }

  const page = await context.newPage();

  try {
    // Visit passport to refresh session tokens
    await page.goto('https://passport.yandex.ru', { waitUntil: 'networkidle', timeout: 30000 });
    await page.waitForTimeout(3000);

    // Check we're still logged in
    const url = page.url();
    if (url.includes('/auth') || url.includes('/login')) {
      console.error('ERROR: Session expired — need manual re-login');
      process.exit(1);
    }

    // Save refreshed cookies
    await context.storageState({ path: COOKIES_PATH });
    console.log('Session refreshed OK');
  } catch (err) {
    console.error('ERROR:', err.message);
    process.exit(1);
  } finally {
    await browser.close();
  }
})();
