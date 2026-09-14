'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('jsdom');
const { teardownDOM, requireFresh } = require('./dom_helper.test_util');

const SCHEDULE_HTML = fs.readFileSync(path.join(__dirname, 'admin_schedule.html'), 'utf8');

// admin_schedule.js reads its schedule id from window.location.pathname at
// load time, so (unlike most other pages) it needs a jsdom instance
// constructed with a specific URL rather than dom_helper.test_util's fixed
// 'http://localhost/' -- built locally here rather than by changing that
// shared helper (other pages' tests rely on its current, simpler signature).
function setupScheduleDOM(scheduleID) {
  const dom = new JSDOM(SCHEDULE_HTML, { url: 'http://localhost/admin/schedule/' + encodeURIComponent(scheduleID) });
  global.window = dom.window;
  global.document = dom.window.document;
  global.navigator = dom.window.navigator;
  return dom;
}

function baseSchedule(overrides) {
  return Object.assign({
    seed_urls: ['http://example.com'],
    max_pages: 20,
    respect_robots: true,
    link_scope: 'domain',
    allowed_domains: ['a.example'],
    blocked_domains: ['b.example'],
    follow_indexed_domains: false,
    use_sitemap: false,
    prioritize_unindexed: false,
    renderer: 'none',
    user_agent: 'custom-agent',
    cookie: 'session=abc',
    basic_auth_user: 'user',
    basic_auth_pass: 'pass',
    fetch_timeout_seconds: 10,
    min_text_length: 50,
    crawl_delay_ms: 250,
    max_response_kb: 5120,
    interval_minutes: 60,
    max_runs: 5,
    enabled: true,
    created_at: '2026-01-02T03:04:05Z',
    run_count: 3,
    next_run_at: '2026-01-02T04:00:00Z',
    last_run_at: '2026-01-02T03:00:00Z',
  }, overrides);
}

function loadFixture(scheduleID, fetchImpl) {
  setupScheduleDOM(scheduleID || 'sched-1');
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => baseSchedule() }));
  return requireFresh('./admin_schedule.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('load() applies the fetched schedule to the form and reveals it', async () => {
  loadFixture('sched-1', async (url) => {
    assert.equal(url, '/admin/api/schedules/sched-1');
    return { ok: true, json: async () => baseSchedule() };
  });
  await flush();
  assert.equal(document.getElementById('schedule-title').textContent, 'http://example.com');
  assert.equal(document.getElementById('schedule-form').hidden, false);
  assert.equal(document.getElementById('schedule-max-pages').value, '20');
  assert.equal(document.getElementById('schedule-user-agent').value, 'custom-agent');
  assert.equal(document.getElementById('schedule-enabled').checked, true);
  const meta = document.getElementById('schedule-meta').textContent;
  assert.equal(meta.includes('Runs so far: 3'), true);
});

test('load() URL-decodes the schedule id from the path', async () => {
  loadFixture('sched with spaces', async (url) => {
    assert.equal(url, '/admin/api/schedules/sched%20with%20spaces');
    return { ok: true, json: async () => baseSchedule() };
  });
  await flush();
});

test('load() shows "Not found" and the error message on failure', async () => {
  loadFixture('sched-1', async () => ({ ok: false, status: 404, text: async () => 'no such schedule' }));
  await flush();
  assert.equal(document.getElementById('schedule-title').textContent, 'Not found');
  assert.equal(document.getElementById('schedule-status').textContent.includes('no such schedule'), true);
});

test('requestBody reflects the current form values', async () => {
  loadFixture();
  await flush();
  document.getElementById('schedule-max-pages').value = '99';
  document.getElementById('schedule-seed-urls').value = 'http://a\nhttp://b';
  document.getElementById('schedule-enabled').checked = false;
  const { requestBody } = require('./admin_schedule.js');
  const body = requestBody();
  assert.deepEqual(body.seed_urls, ['http://a', 'http://b']);
  assert.equal(body.max_pages, 99);
  assert.equal(body.enabled, false);
});

test('requestBody falls back to 20 max_pages and 0 for other blank numeric fields', async () => {
  loadFixture();
  await flush();
  for (const id of [
    'schedule-max-pages', 'schedule-fetch-timeout', 'schedule-interval',
    'schedule-min-text-length', 'schedule-delay', 'schedule-max-response', 'schedule-max-runs',
  ]) {
    document.getElementById(id).value = '';
  }
  const { requestBody } = require('./admin_schedule.js');
  const body = requestBody();
  assert.equal(body.max_pages, 20);
  assert.equal(body.fetch_timeout_seconds, 0);
  assert.equal(body.interval_minutes, 0);
  assert.equal(body.min_text_length, 0);
  assert.equal(body.crawl_delay_ms, 0);
  assert.equal(body.max_response_kb, 0);
  assert.equal(body.max_runs, 0);
});

test('load() applies a minimal schedule: blank optional fields, no next/last run yet', async () => {
  loadFixture('sched-1', async () => ({
    ok: true,
    json: async () => baseSchedule({
      link_scope: '', renderer: '', user_agent: '', cookie: '',
      basic_auth_user: '', basic_auth_pass: '', fetch_timeout_seconds: 0,
      min_text_length: 0, crawl_delay_ms: 0, max_response_kb: 0,
      interval_minutes: 0, max_runs: 0, next_run_at: '', last_run_at: '',
    }),
  }));
  await flush();
  assert.equal(document.getElementById('schedule-link-scope').value, '');
  assert.equal(document.getElementById('schedule-renderer').value, '');
  assert.equal(document.getElementById('schedule-user-agent').value, '');
  assert.equal(document.getElementById('schedule-fetch-timeout').value, '');
  assert.equal(document.getElementById('schedule-max-runs').value, '');
  const meta = document.getElementById('schedule-meta').textContent;
  assert.equal(meta.includes('Next run: —'), true);
  assert.equal(meta.includes('Last run: never'), true);
});

test('submitting the form saves via PATCH and shows Saved on success', async () => {
  let patched = null;
  loadFixture('sched-1', async (url, opts) => {
    if (opts && opts.method === 'PATCH') {
      patched = { url, body: JSON.parse(opts.body) };
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => baseSchedule() };
  });
  await flush();
  document.getElementById('schedule-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  await flush();
  assert.equal(patched.url, '/admin/api/schedules/sched-1');
  assert.equal(document.getElementById('schedule-status').textContent, 'Saved.');
  assert.equal(document.getElementById('schedule-save-btn').disabled, false);
});

test('submitting the form shows an error message on failure', async () => {
  loadFixture('sched-1', async (url, opts) => {
    if (opts && opts.method === 'PATCH') return { ok: false, status: 500, text: async () => 'save failed' };
    return { ok: true, json: async () => baseSchedule() };
  });
  await flush();
  document.getElementById('schedule-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(document.getElementById('schedule-status').textContent, 'Could not save: save failed');
  assert.equal(document.getElementById('schedule-save-btn').disabled, false);
});

test('clicking Run now queues the crawl and reports success', async () => {
  let posted = null;
  loadFixture('sched-1', async (url, opts) => {
    if (opts && opts.method === 'POST') {
      posted = url;
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => baseSchedule() };
  });
  await flush();
  document.getElementById('schedule-run-now-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(posted, '/admin/api/schedules/sched-1/run');
  assert.equal(document.getElementById('schedule-status').textContent.includes('Queued'), true);
});

test('clicking Run now shows an error message on failure', async () => {
  loadFixture('sched-1', async (url, opts) => {
    if (opts && opts.method === 'POST') return { ok: false, status: 500, text: async () => 'could not start' };
    return { ok: true, json: async () => baseSchedule() };
  });
  await flush();
  document.getElementById('schedule-run-now-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(document.getElementById('schedule-status').textContent, 'Could not start: could not start');
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture();
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('schedule-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

// The success path's window.location.href assignment prints a harmless
// "Not implemented: navigation" jsdom console error (real navigation isn't
// supported) -- same jsdom limitation admin.test.js's wireSignOut test
// notes; it doesn't fail this test, only the redirect itself isn't asserted.
test('clicking Delete removes the schedule when confirmed', async () => {
  let deletedURL = null;
  loadFixture('sched-1', async () => ({ ok: true, json: async () => baseSchedule() }));
  await flush();
  window.confirm = () => true;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') {
      deletedURL = url;
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => baseSchedule() };
  };
  document.getElementById('schedule-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deletedURL, '/admin/api/schedules/sched-1');
});

test('clicking Delete re-enables the button and alerts on failure', async () => {
  loadFixture('sched-1');
  await flush();
  window.confirm = () => true;
  let alertMsg = null;
  window.alert = (msg) => { alertMsg = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'in use' };
    return { ok: true, json: async () => baseSchedule() };
  };
  const btn = document.getElementById('schedule-delete-btn');
  btn.dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(btn.disabled, false);
  assert.equal(alertMsg.includes('in use'), true);
});
