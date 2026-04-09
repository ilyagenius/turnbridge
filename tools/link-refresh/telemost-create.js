// telemost-create.js — Creates a Telemost room using Playwright headless browser
// Usage: node telemost-create.js
// Output: prints the room URL to stdout

const { chromium } = require('playwright');

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    locale: 'ru-RU',
    userAgent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36',
  });
  const page = await context.newPage();

  try {
    // Go to Telemost main page
    await page.goto('https://telemost.yandex.ru/', { waitUntil: 'networkidle', timeout: 30000 });

    // Click "Create meeting" button
    const createBtn = page.locator('a[href*="/j/"], button:has-text("Создать"), a:has-text("Создать видеовстречу")').first();
    await createBtn.waitFor({ timeout: 15000 });
    await createBtn.click();

    // Wait for navigation to the room URL
    await page.waitForURL(/telemost\.yandex\.ru\/j\//, { timeout: 15000 });

    const url = page.url();
    // Output just the clean URL
    const match = url.match(/https:\/\/telemost\.yandex\.ru\/j\/\d+/);
    if (match) {
      console.log(match[0]);
    } else {
      console.log(url.split('?')[0]);
    }
  } catch (err) {
    console.error('ERROR:', err.message);
    process.exit(1);
  } finally {
    await browser.close();
  }
})();
