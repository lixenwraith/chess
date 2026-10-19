// Playwright configuration for the web client's end-to-end tests. The server
// must already run with -serve and PostgreSQL (test/run-test-server.sh with
// WEB_PORT, or any -dev -serve server): CHESS_E2E_WEB names the client's
// origin and CHESS_E2E_API the API's. CHESS_E2E_CHROMIUM points at an
// installed Chromium binary when `npx playwright install chromium` is not an
// option.
'use strict';
const { defineConfig } = require('@playwright/test');

const chromium = process.env.CHESS_E2E_CHROMIUM;

module.exports = defineConfig({
    testDir: '.',
    testMatch: '*.spec.js',
    timeout: 120_000,
    expect: { timeout: 15_000 },
    fullyParallel: false,
    workers: 1,
    retries: 0,
    reporter: process.env.CI ? 'github' : 'list',
    use: {
        baseURL: process.env.CHESS_E2E_WEB || 'http://localhost:9090',
        viewport: { width: 980, height: 480 },
        permissions: ['clipboard-read', 'clipboard-write'],
        acceptDownloads: true,
        trace: 'retain-on-failure',
        launchOptions: chromium ? { executablePath: chromium } : {},
    },
});
