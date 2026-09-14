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
// own bootstrap (wireSignOut/showCurrentDefaults/loadCrawlerSettings) at load
// time -- loadFixture sets a fetch mock that satisfies the settings GET
// before requiring it, the same way the real page's own initial load would.
function settingsPayload(overrides) {
  return {
    tuning: { alpha: 0.5, k1: 1.2, b: 0.75, pagerank_weight: 0.1 },
    operational: {
      user_agent: 'ua', fetch_timeout_seconds: 8, min_text_length: 50,
      crawl_delay_ms: 250, max_response_kb: 5120,
      default_max_pages: 20, max_retained_crawl_jobs: 200,
      default_renderer: 'none', link_scope: 'domain',
      session_length_minutes: 60, fuzzy_matching: true,
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

test('applyCrawlerSettings populates every field from the operational settings object', () => {
  const { applyCrawlerSettings } = loadFixture();
  applyCrawlerSettings(settingsPayload({
    fetch_timeout_seconds: 12, user_agent: 'my-bot', default_max_pages: 42,
    min_text_length: 100, crawl_delay_ms: 500, max_response_kb: 2048,
    max_retained_crawl_jobs: 50, default_renderer: 'chromium', link_scope: 'tld',
  }));
  assert.equal(document.getElementById('crawler-setting-fetch-timeout').value, '12');
  assert.equal(document.getElementById('crawler-setting-user-agent').value, 'my-bot');
  assert.equal(document.getElementById('crawler-setting-default-max-pages').value, '42');
  assert.equal(document.getElementById('crawler-setting-min-text-length').value, '100');
  assert.equal(document.getElementById('crawler-setting-crawl-delay').value, '500');
  assert.equal(document.getElementById('crawler-setting-max-response-kb').value, '2048');
  assert.equal(document.getElementById('crawler-setting-max-retained-crawl-jobs').value, '50');
  assert.equal(document.getElementById('crawler-setting-default-renderer').value, 'chromium');
  assert.equal(document.getElementById('crawler-setting-default-link-scope').value, 'tld');
});

test('applyCrawlerSettings falls back to "none"/"domain" when renderer/link_scope are unset', () => {
  const { applyCrawlerSettings } = loadFixture();
  const s = settingsPayload();
  delete s.operational.default_renderer;
  delete s.operational.link_scope;
  applyCrawlerSettings(s);
  assert.equal(document.getElementById('crawler-setting-default-renderer').value, 'none');
  assert.equal(document.getElementById('crawler-setting-default-link-scope').value, 'domain');
});

test('loadCrawlerSettings on page load fills the Crawler panel from a GET', async () => {
  loadFixture(settingsPayload({ user_agent: 'loaded-ua', fetch_timeout_seconds: 9 }));
  // loadCrawlerSettings() is fired at load time as a fire-and-forget async
  // call -- flush a microtask/timer turn so its GET resolves before asserting.
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('crawler-setting-user-agent').value, 'loaded-ua');
  assert.equal(document.getElementById('crawler-setting-fetch-timeout').value, '9');
});

test('saving the Crawler settings form merges its 9 fields into the fetched settings and leaves the rest untouched', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));

  let lastPostBody = null;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') {
      lastPostBody = JSON.parse(opts.body);
      return { ok: true, json: async () => settingsPayload({ user_agent: 'new-ua', fetch_timeout_seconds: 15 }) };
    }
    return { ok: true, json: async () => settingsPayload() };
  };

  document.getElementById('crawler-setting-fetch-timeout').value = '15';
  document.getElementById('crawler-setting-user-agent').value = 'new-ua';
  document.getElementById('crawler-setting-default-max-pages').value = '20';
  document.getElementById('crawler-setting-min-text-length').value = '50';
  document.getElementById('crawler-setting-crawl-delay').value = '250';
  document.getElementById('crawler-setting-max-response-kb').value = '5120';
  document.getElementById('crawler-setting-max-retained-crawl-jobs').value = '200';
  document.getElementById('crawler-setting-default-renderer').value = 'none';
  document.getElementById('crawler-setting-default-link-scope').value = 'domain';

  const form = document.getElementById('crawler-settings-form');
  form.dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));

  assert.ok(lastPostBody, 'expected the form submit to POST');
  assert.equal(lastPostBody.operational.user_agent, 'new-ua');
  assert.equal(lastPostBody.operational.fetch_timeout_seconds, 15);
  // Fields this page doesn't own must round-trip unchanged from the GET.
  assert.equal(lastPostBody.operational.session_length_minutes, 60);
  assert.equal(lastPostBody.operational.fuzzy_matching, true);
  assert.deepEqual(lastPostBody.tuning, settingsPayload().tuning);

  const status = document.getElementById('crawler-settings-status');
  assert.equal(status.textContent, 'Saved.');
});

test('a failed load reports an error on the Crawler settings status line', async () => {
  setupDOM(CRAWL_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async () => { throw new Error('network down'); };
  requireFresh('./admin_crawl.js');
  await new Promise((resolve) => setTimeout(resolve, 0));
  const status = document.getElementById('crawler-settings-status');
  assert.equal(status.textContent, 'Could not load settings: network down');
});

test('a failed save reports an error on the Crawler settings status line', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') throw new Error('server exploded');
    return { ok: true, json: async () => settingsPayload() };
  };
  const form = document.getElementById('crawler-settings-form');
  form.dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const status = document.getElementById('crawler-settings-status');
  assert.equal(status.textContent, 'Could not save: server exploded');
});
