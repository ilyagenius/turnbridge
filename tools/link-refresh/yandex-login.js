// yandex-login.js — One-time interactive login to save Yandex cookies
// Usage: node yandex-login.js
// Opens a visible browser window. Log in manually, then press Enter in terminal.

const { chromium } = require('playwright');
const path = require('path');
const readline = require('readline');

const COOKIES_PATH = path.join(__dirname, 'yandex-cookies.json');

(async () => {
  const browser = await chromium.launch({ headless: false });
  const context = await browser.newContext({
    locale: 'ru-RU',
    userAgent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36',
  });
  const page = await context.newPage();

  await page.goto('https://passport.yandex.ru/auth');

  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  await new Promise(resolve => {
    rl.question('Log in in the browser, then press Enter here... ', resolve);
  });
  rl.close();

  await context.storageState({ path: COOKIES_PATH });
  console.log('Cookies saved to', COOKIES_PATH);

  await browser.close();
})();
