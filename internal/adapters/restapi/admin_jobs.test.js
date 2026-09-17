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

test('jobDetailDefaultDir starts numeric/recency columns descending, text columns ascending', () => {
  const { jobDetailDefaultDir } = loadFixture();
  assert.equal(jobDetailDefaultDir('fetched_at'), 'desc');
  assert.equal(jobDetailDefaultDir('doc_length'), 'desc');
  assert.equal(jobDetailDefaultDir('links_found'), 'desc');
  assert.equal(jobDetailDefaultDir('duration_ms'), 'desc');
  assert.equal(jobDetailDefaultDir('url'), 'asc');
  assert.equal(jobDetailDefaultDir('status'), 'asc');
  assert.equal(jobDetailDefaultDir('title'), 'asc');
  assert.equal(jobDetailDefaultDir('error'), 'asc');
});

test('jobDetailSortValue reads fetched_at as a timestamp, numeric fields as numbers (0 for falsy), everything else as lowercased text', () => {
  const { jobDetailSortValue } = loadFixture();
  const p = { url: 'HTTP://A', status: 'Indexed', title: 'Hello', doc_length: 12, links_found: 3, duration_ms: 250, fetched_at: '2026-01-02T03:04:05Z', error: 'Timeout' };
  assert.equal(jobDetailSortValue(p, 'fetched_at'), new Date('2026-01-02T03:04:05Z').getTime());
  assert.equal(jobDetailSortValue({}, 'fetched_at'), 0);
  assert.equal(jobDetailSortValue(p, 'doc_length'), 12);
  assert.equal(jobDetailSortValue({}, 'doc_length'), 0);
  assert.equal(jobDetailSortValue(p, 'url'), 'http://a');
  assert.equal(jobDetailSortValue(p, 'status'), 'indexed');
  assert.equal(jobDetailSortValue(p, 'error'), 'timeout');
});

test('sortJobDetailPages sorts newest-fetched-first by default (fetched_at desc)', () => {
  const { sortJobDetailPages } = loadFixture();
  const pages = [
    { url: 'a', fetched_at: '2026-01-01T00:00:00Z' },
    { url: 'b', fetched_at: '2026-01-03T00:00:00Z' },
    { url: 'c', fetched_at: '2026-01-02T00:00:00Z' },
  ];
  const sorted = sortJobDetailPages(pages, 'fetched_at', 'desc');
  assert.deepEqual(sorted.map((p) => p.url), ['b', 'c', 'a']);
});

test('sortJobDetailPages sorts a numeric column ascending or descending on request', () => {
  const { sortJobDetailPages } = loadFixture();
  const pages = [{ url: 'a', doc_length: 30 }, { url: 'b', doc_length: 10 }, { url: 'c', doc_length: 20 }];
  assert.deepEqual(sortJobDetailPages(pages, 'doc_length', 'asc').map((p) => p.url), ['b', 'c', 'a']);
  assert.deepEqual(sortJobDetailPages(pages, 'doc_length', 'desc').map((p) => p.url), ['a', 'c', 'b']);
});

test('sortJobDetailPages sorts a text column case-insensitively', () => {
  const { sortJobDetailPages } = loadFixture();
  const pages = [{ url: 'a', title: 'banana' }, { url: 'b', title: 'Apple' }, { url: 'c', title: 'cherry' }];
  assert.deepEqual(sortJobDetailPages(pages, 'title', 'asc').map((p) => p.url), ['b', 'a', 'c']);
});

test('sortJobDetailPages does not mutate the input array', () => {
  const { sortJobDetailPages } = loadFixture();
  const pages = [{ url: 'a', doc_length: 2 }, { url: 'b', doc_length: 1 }];
  const original = [...pages];
  sortJobDetailPages(pages, 'doc_length', 'asc');
  assert.deepEqual(pages, original);
});

test('buildJobDetailTable renders every column with the "fetched at" header marked as the active, descending sort by default', () => {
  const { buildJobDetailTable } = loadFixture();
  const table = buildJobDetailTable([
    { url: 'https://a', status: 'indexed', title: 'A', doc_length: 100, links_found: 2, duration_ms: 250, fetched_at: '2026-01-01T00:00:00Z', error: '' },
  ]);
  const headers = Array.from(table.querySelectorAll('th')).map((th) => th.textContent);
  assert.deepEqual(headers, ['url', 'status', 'title', 'length', 'links', 'duration', 'fetched at ▼', 'detail']);
  const cells = Array.from(table.querySelectorAll('tbody td')).map((td) => td.textContent);
  assert.equal(cells[0], 'https://a');
  assert.equal(cells[3], '100');
});

test('clicking an inactive column header switches to it at its default direction and re-renders', () => {
  const { loadJobDetail } = loadFixture();
  global.fetch = async (url) => {
    if (url.includes('/admin/api/crawl/jobs/job-1')) {
      return {
        ok: true, json: async () => ({
          id: 'job-1', status: 'done', request: { seed_urls: ['http://a'] },
          pages: [
            { url: 'https://a/1', status: 'indexed', doc_length: 50, fetched_at: '2026-01-01T00:00:00Z' },
            { url: 'https://a/2', status: 'indexed', doc_length: 10, fetched_at: '2026-01-02T00:00:00Z' },
          ],
        }),
      };
    }
    return { ok: true, json: async () => ({}) };
  };
  return loadJobDetail('job-1').then(() => {
    const findHeader = () => Array.from(document.querySelectorAll('#job-detail-table th')).find((th) => th.textContent.startsWith('length'));
    findHeader().dispatchEvent(new window.Event('click'));
    // The click re-renders the whole table (clear + rebuild), so the
    // pre-click header/row elements are now detached -- re-query fresh
    // ones from the rebuilt DOM rather than reusing stale references.
    const rows = Array.from(document.querySelectorAll('#job-detail-table tbody tr'));
    // doc_length defaults to descending on first click: 50 before 10.
    assert.equal(rows[0].querySelector('td').textContent, 'https://a/1');
    assert.equal(rows[1].querySelector('td').textContent, 'https://a/2');
    assert.ok(findHeader().textContent.includes('▼'));
  });
});
