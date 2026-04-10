// yandex-refresh-session.js — Refreshes Yandex session cookies via Playwright
// Runs as systemd timer (yandex-session.timer) every 12 hours

const { chromium } = require('playwright');

const COOKIES_PATH = '/opt/turnbridge/yandex-cookies.json';

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
    await page.goto('https://passport.yandex.ru', { waitUntil: 'networkidle', timeout: 30000 });
    await page.waitForTimeout(3000);

    const url = page.url();
    if (url.includes('/auth') || url.includes('/login')) {
      console.error('ERROR: Session expired — need manual re-login');
      process.exit(1);
    }

    await context.storageState({ path: COOKIES_PATH });
    console.log('Session refreshed OK');
  } catch (err) {
    console.error('ERROR:', err.message);
    process.exit(1);
  } finally {
    await browser.close();
  }
})();
