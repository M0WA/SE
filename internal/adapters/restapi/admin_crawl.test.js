'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const CRAWL_HTML = fs.readFileSync(path.join(__dirname, 'crawl.html'), 'utf8');

// admin_crawl.js is a plain page script, not a module -- it expects admin.js's
// helpers (wireSignOut, getJSON, postJSON, normalizeURL, parseLines,
// setButtonLoading) as ambient globals, the same way <script src="/admin.js">
// loading before it in the real page does. It also unconditionally runs its
// own bootstrap (wireSignOut/showCurrentDefaults) at load time -- loadFixture
// sets a fetch mock that satisfies showCurrentDefaults' settings GET before
// requiring it, the same way the real page's own initial load would.
function settingsPayload(overrides) {
  return {
    tuning: { alpha: 0.5, k1: 1.2, b: 0.75, pagerank_weight: 0.1 },
    operational: {
      user_agent: 'ua', fetch_timeout_seconds: 8, min_text_length: 50,
      crawl_delay_ms: 250, max_response_kb: 5120,
      ...overrides,
    },
  };
}

function loadFixture(settingsResponse) {
  setupDOM(CRAWL_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async () => ({ ok: true, json: async () => (settingsResponse || settingsPayload()) });
  return requireFresh('./admin_crawl.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('updateSubmitLabel reads "Crawl" when the interval is blank/zero', () => {
  loadFixture();
  const crawlInterval = document.getElementById('crawl-interval');
  const submitBtn = document.querySelector('#crawl-form button[type="submit"]');
  crawlInterval.value = '0';
  crawlInterval.dispatchEvent(new window.Event('input'));
  assert.equal(submitBtn.textContent, 'Crawl');
});

test('updateSubmitLabel reads "Schedule" once a positive interval is entered', () => {
  loadFixture();
  const crawlInterval = document.getElementById('crawl-interval');
  const submitBtn = document.querySelector('#crawl-form button[type="submit"]');
  crawlInterval.value = '30';
  crawlInterval.dispatchEvent(new window.Event('input'));
  assert.equal(submitBtn.textContent, 'Schedule');
});

test('the crawl URL is normalized on blur', () => {
  loadFixture();
  const crawlURL = document.getElementById('crawl-url');
  crawlURL.value = 'example.com/path';
  crawlURL.dispatchEvent(new window.Event('blur'));
  assert.equal(crawlURL.value, 'https://example.com/path');
});

test('blurring an empty crawl URL leaves it blank rather than normalizing', () => {
  loadFixture();
  const crawlURL = document.getElementById('crawl-url');
  crawlURL.value = '   ';
  crawlURL.dispatchEvent(new window.Event('blur'));
  assert.equal(crawlURL.value, '   ');
});

test('the basic-auth fields start readonly and become editable on focus', () => {
  loadFixture();
  const user = document.getElementById('crawl-basic-user');
  const pass = document.getElementById('crawl-basic-pass');
  assert.ok(user.hasAttribute('readonly'));
  assert.ok(pass.hasAttribute('readonly'));
  user.dispatchEvent(new window.Event('focus'));
  pass.dispatchEvent(new window.Event('focus'));
  assert.ok(!user.hasAttribute('readonly'));
  assert.ok(!pass.hasAttribute('readonly'));
});

test('submitting with a blank URL asks for one and never POSTs', async () => {
  loadFixture();
  let posted = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') posted = true;
    return { ok: true, json: async () => settingsPayload() };
  };
  document.getElementById('crawl-url').value = '   ';
  document.getElementById('crawl-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(posted, false);
  assert.equal(document.getElementById('crawl-status').textContent, 'Enter a URL to crawl.');
});

test('submitting a one-off crawl (interval 0) POSTs every field and points to the Jobs page', async () => {
  loadFixture();
  let lastURL, lastBody;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') {
      lastURL = url;
      lastBody = JSON.parse(opts.body);
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => settingsPayload() };
  };
  document.getElementById('crawl-url').value = 'example.com';
  document.getElementById('max-pages').value = '15';
  document.getElementById('crawl-interval').value = '0';
  document.getElementById('crawl-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL, '/admin/api/schedules');
  assert.deepEqual(lastBody.seed_urls, ['https://example.com']);
  assert.equal(lastBody.max_pages, 15);
  assert.equal(lastBody.interval_minutes, 0);
  assert.equal(document.getElementById('crawl-status').textContent, 'Queued — starting within a few seconds. See it on the Jobs page.');
});

test('submitting a repeating crawl reports the interval/max-runs and points to Schedules on the Jobs page', async () => {
  loadFixture();
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') return { ok: true, json: async () => ({}) };
    return { ok: true, json: async () => settingsPayload() };
  };
  document.getElementById('crawl-url').value = 'example.com';
  document.getElementById('crawl-interval').value = '30';
  document.getElementById('crawl-max-runs').value = '5';
  document.getElementById('crawl-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(
    document.getElementById('crawl-status').textContent,
    'Scheduled — repeats every 30 minutes (up to 5 times). See it under Schedules on the Jobs page.',
  );
});

test('a failed crawl submit reports the error on the crawl status line', async () => {
  loadFixture();
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') throw new Error('schedule rejected');
    return { ok: true, json: async () => settingsPayload() };
  };
  document.getElementById('crawl-url').value = 'example.com';
  document.getElementById('crawl-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('crawl-status').textContent, 'Could not start: schedule rejected');
});

test('showCurrentDefaults fills per-crawl override placeholders from a GET', async () => {
  loadFixture(settingsPayload({ user_agent: 'loaded-ua', fetch_timeout_seconds: 9 }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('crawl-user-agent').placeholder, 'Default: loaded-ua');
  assert.equal(document.getElementById('crawl-fetch-timeout').placeholder, 'Default: 9s');
});
