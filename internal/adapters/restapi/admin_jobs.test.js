'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const JOBS_HTML = fs.readFileSync(path.join(__dirname, 'admin_jobs.html'), 'utf8');

function loadFixture() {
  setupDOM(JOBS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async (url) => {
    if (url.includes('/admin/api/schedules')) return { ok: true, json: async () => [] };
    if (url.includes('/admin/api/crawl/jobs')) return { ok: true, json: async () => [] };
    return { ok: true, json: async () => ({}) };
  };
  return requireFresh('./admin_jobs.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('capitalize upper-cases only the first letter', () => {
  const { capitalize } = loadFixture();
  assert.equal(capitalize('queued'), 'Queued');
  assert.equal(capitalize('a'), 'A');
});

test('formatDuration renders an em-dash when the job has not started', () => {
  const { formatDuration } = loadFixture();
  assert.equal(formatDuration(null, null), '—');
});

test('formatDuration renders seconds for a short-running job', () => {
  const { formatDuration } = loadFixture();
  const start = new Date('2026-01-01T00:00:00Z').toISOString();
  const end = new Date('2026-01-01T00:00:05.5Z').toISOString();
  assert.equal(formatDuration(start, end), '5.5s');
});

test('formatDuration renders minutes+seconds once past a minute', () => {
  const { formatDuration } = loadFixture();
  const start = new Date('2026-01-01T00:00:00Z').toISOString();
  const end = new Date('2026-01-01T00:01:05Z').toISOString();
  assert.equal(formatDuration(start, end), '1m 5s');
});

test('formatMs renders an em-dash for a falsy value', () => {
  const { formatMs } = loadFixture();
  assert.equal(formatMs(0), '—');
  assert.equal(formatMs(null), '—');
});

test('formatMs renders milliseconds under a second, seconds at/above it', () => {
  const { formatMs } = loadFixture();
  assert.equal(formatMs(250), '250ms');
  assert.equal(formatMs(1500), '1.5s');
});

test('filterJobs returns every job unchanged for a blank pattern', () => {
  const { filterJobs } = loadFixture();
  const jobs = [{ request: { seed_urls: ['http://a'] }, status: 'done' }];
  assert.equal(filterJobs(jobs, ''), jobs);
});

test('filterJobs matches against the seed summary or status, case-insensitively', () => {
  const { filterJobs } = loadFixture();
  const jobs = [
    { request: { seed_urls: ['http://example.com'] }, status: 'done' },
    { request: { seed_urls: ['http://other.test'] }, status: 'failed' },
  ];
  assert.deepEqual(filterJobs(jobs, 'EXAMPLE'), [jobs[0]]);
  assert.deepEqual(filterJobs(jobs, 'failed'), [jobs[1]]);
});

test('filterJobs returns the unfiltered list (not an error) for an invalid regex', () => {
  const { filterJobs } = loadFixture();
  const jobs = [{ request: { seed_urls: ['http://a'] }, status: 'done' }];
  assert.deepEqual(filterJobs(jobs, '('), jobs);
});

test('filterPages matches url, status, title, or error', () => {
  const { filterPages } = loadFixture();
  const pages = [
    { url: 'http://a/x', status: 'indexed', title: 'Hello world' },
    { url: 'http://a/y', status: 'fetch_failed', error: 'timeout' },
  ];
  assert.deepEqual(filterPages(pages, 'hello'), [pages[0]]);
  assert.deepEqual(filterPages(pages, 'timeout'), [pages[1]]);
});

test('filterCrawls matches against the seed summary', () => {
  const { filterCrawls } = loadFixture();
  const crawls = [{ seed_urls: ['http://example.com'] }, { seed_urls: ['http://other.test'] }];
  assert.deepEqual(filterCrawls(crawls, 'other'), [crawls[1]]);
});

test('crawlRecurrenceCell shows "once" for a non-recurring schedule', () => {
  const { crawlRecurrenceCell } = loadFixture();
  const td = crawlRecurrenceCell({ recurring: false });
  assert.equal(td.textContent, 'once');
});

test('crawlRecurrenceCell shows the interval, plus a run count once max_runs is capped', () => {
  const { crawlRecurrenceCell } = loadFixture();
  const uncapped = crawlRecurrenceCell({ recurring: true, interval_minutes: 30, max_runs: 0, run_count: 4 });
  assert.equal(uncapped.textContent, '30 min');
  const capped = crawlRecurrenceCell({ recurring: true, interval_minutes: 30, max_runs: 10, run_count: 4 });
  assert.equal(capped.textContent, '30 min (4/10 runs)');
});

test('crawlLinkScopeCell renders the human label for a known scope, falling back to the raw value otherwise', () => {
  const { crawlLinkScopeCell, LINK_SCOPE_LABELS } = loadFixture();
  assert.equal(crawlLinkScopeCell({ link_scope: 'host' }).textContent, LINK_SCOPE_LABELS.host);
  assert.equal(crawlLinkScopeCell({ link_scope: '' }).textContent, LINK_SCOPE_LABELS['']);
  assert.equal(crawlLinkScopeCell({ link_scope: 'something-unknown' }).textContent, 'something-unknown');
});
