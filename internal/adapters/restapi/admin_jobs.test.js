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
  // admin_jobs.js's own exports, plus admin.js's shared helpers (icon
  // glyphs/SVGs, setIconLabel) it relies on as ambient globals -- same
  // relationship as e.g. buildTable/getJSON, just not re-exported by this
  // page's own module.exports.
  return Object.assign({}, adminHelpers, requireFresh('./admin_jobs.js'));
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

test('formatSpeed renders an em-dash before a job has been polled at all', () => {
  const { formatSpeed } = loadFixture();
  assert.equal(formatSpeed({ id: 'never-seen', pages_crawled: 0 }), '—');
});

test('formatSpeed renders an em-dash after only one poll (no delta yet)', () => {
  const { formatSpeed, updateJobSpeeds } = loadFixture();
  updateJobSpeeds([{ id: 'j1', pages_crawled: 10 }], 1000);
  assert.equal(formatSpeed({ id: 'j1' }), '—');
});

test('formatSpeed computes pages/sec from the delta between two polls, not pages_crawled/started_at', () => {
  const { formatSpeed, updateJobSpeeds } = loadFixture();
  updateJobSpeeds([{ id: 'j1', pages_crawled: 10 }], 1000);
  updateJobSpeeds([{ id: 'j1', pages_crawled: 30 }], 3000);
  assert.equal(formatSpeed({ id: 'j1' }), '10.00/s');
});

test('formatSpeed survives a job resuming after a restart: pages_crawled is a lifetime counter that can jump hugely between polls without inflating the rate, since the calculation never looks at started_at', () => {
  const { formatSpeed, updateJobSpeeds } = loadFixture();
  // Simulate: job had accumulated 17979 pages before a crawl-server
  // restart; started_at gets reset on resume (irrelevant here, since
  // updateJobSpeeds never reads it), and two polls 2s apart see the
  // lifetime counter continue climbing by a normal amount.
  updateJobSpeeds([{ id: 'j1', pages_crawled: 17979 }], 1000);
  updateJobSpeeds([{ id: 'j1', pages_crawled: 17981 }], 3000);
  assert.equal(formatSpeed({ id: 'j1' }), '1.00/s');
});

test('updateJobSpeeds tracks multiple jobs independently', () => {
  const { formatSpeed, updateJobSpeeds } = loadFixture();
  updateJobSpeeds([{ id: 'a', pages_crawled: 0 }, { id: 'b', pages_crawled: 100 }], 1000);
  updateJobSpeeds([{ id: 'a', pages_crawled: 5 }, { id: 'b', pages_crawled: 105 }], 2000);
  assert.equal(formatSpeed({ id: 'a' }), '5.00/s');
  assert.equal(formatSpeed({ id: 'b' }), '5.00/s');
});

test('clearEndedJobs sends a DELETE to the jobs collection and reports how many were removed', async () => {
  const { clearEndedJobs } = loadFixture();
  let gotMethod, gotURL;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/crawl/jobs') && opts && opts.method === 'DELETE') {
      gotMethod = opts.method;
      gotURL = url;
      return { ok: true, json: async () => ({ removed: 3 }) };
    }
    return { ok: true, json: async () => [] };
  };
  await clearEndedJobs();
  assert.equal(gotMethod, 'DELETE');
  assert.equal(gotURL, '/admin/api/crawl/jobs');
  assert.equal(document.getElementById('jobs-status').textContent, 'Cleared 3 ended jobs.');
});

test('clearEndedJobs singularizes the status message for exactly one removed job', async () => {
  const { clearEndedJobs } = loadFixture();
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/crawl/jobs') && opts && opts.method === 'DELETE') {
      return { ok: true, json: async () => ({ removed: 1 }) };
    }
    return { ok: true, json: async () => [] };
  };
  await clearEndedJobs();
  assert.equal(document.getElementById('jobs-status').textContent, 'Cleared 1 ended job.');
});

test('clearEndedJobs alerts on failure', async () => {
  const { clearEndedJobs } = loadFixture();
  const alerts = [];
  global.window.alert = (msg) => alerts.push(msg);
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/crawl/jobs') && opts && opts.method === 'DELETE') {
      return { ok: false, status: 500, json: async () => ({}), text: async () => 'boom' };
    }
    return { ok: true, json: async () => [] };
  };
  await clearEndedJobs();
  assert.equal(alerts.length, 1);
  assert.match(alerts[0], /Could not clear ended jobs/);
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

test('toggleCrawlEnabled POSTs to the dedicated toggle endpoint with the flipped value, not a full-schedule PATCH', async () => {
  const { toggleCrawlEnabled } = loadFixture();
  let gotMethod, gotURL, gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/toggle')) {
      gotMethod = opts.method;
      gotURL = url;
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => ({ ok: true }) };
    }
    return { ok: true, json: async () => [] };
  };
  await toggleCrawlEnabled({ id: 'sched-1', enabled: false });
  assert.equal(gotMethod, 'POST');
  assert.equal(gotURL, '/admin/api/schedules/sched-1/toggle');
  assert.deepEqual(gotBody, { enabled: true });
});

test('toggleCrawlEnabled alerts on failure', async () => {
  const { toggleCrawlEnabled } = loadFixture();
  const alerts = [];
  global.window.alert = (msg) => alerts.push(msg);
  global.fetch = async (url, opts) => {
    if (url.includes('/toggle')) {
      return { ok: false, status: 500, json: async () => ({}), text: async () => 'boom' };
    }
    return { ok: true, json: async () => [] };
  };
  await toggleCrawlEnabled({ id: 'sched-1', enabled: true });
  assert.equal(alerts.length, 1);
  assert.match(alerts[0], /Could not update/);
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

test('viewButtonCell shows a View icon button always, and a Cancel icon button (reusing the trash icon) only for an active job', () => {
  const { viewButtonCell, ICON_SVGS, ACTION_GLYPHS } = loadFixture();

  const done = viewButtonCell({ id: 'j1', status: 'done' });
  const doneButtons = done.querySelectorAll('button');
  assert.equal(doneButtons.length, 1);
  assert.equal(doneButtons[0].innerHTML, ICON_SVGS.view);
  assert.equal(doneButtons[0].title, 'View');

  const running = viewButtonCell({ id: 'j2', status: 'running' });
  const runningButtons = running.querySelectorAll('button');
  assert.equal(runningButtons.length, 2);
  // Cancel reuses the same trash icon as a schedule row's Delete button --
  // only the title/aria-label ("Cancel") differ, not the glyph.
  assert.equal(runningButtons[1].innerHTML, ICON_SVGS.delete);
  assert.equal(runningButtons[1].title, 'Cancel');
  assert.notEqual(ACTION_GLYPHS.edit, undefined); // sanity: glyph table still has non-SVG entries
});

test('crawlActionsCell shows Run now/Edit as glyph icons and Delete as the shared trash SVG, with the real names as hover text', () => {
  const { crawlActionsCell, ICON_SVGS, ACTION_GLYPHS } = loadFixture();
  const td = crawlActionsCell({ id: 's1' });
  const buttons = td.querySelectorAll('button');
  const link = td.querySelector('a');
  assert.equal(buttons.length, 2);
  assert.equal(buttons[0].textContent, ACTION_GLYPHS.run);
  assert.equal(buttons[0].title, 'Run now');
  assert.equal(link.textContent, ACTION_GLYPHS.edit);
  assert.equal(link.title, 'Edit');
  assert.equal(link.getAttribute('href'), '/admin/schedule/s1');
  assert.equal(buttons[1].innerHTML, ICON_SVGS.delete);
  assert.equal(buttons[1].title, 'Delete');
});

test('renderCrawls lists enabled as the first column and drops the repeats/links columns', () => {
  const { renderCrawls } = loadFixture();
  renderCrawls([{ id: 's1', seed_urls: ['http://a'], recurring: true, interval_minutes: 30, link_scope: 'host', enabled: true, next_run_at: '', last_run_at: '' }]);
  const headers = Array.from(document.querySelectorAll('#crawls-table th')).map((th) => th.textContent);
  assert.deepEqual(headers, ['enabled', 'seed', 'next run', 'last run', '']);
  const firstRowCells = document.querySelectorAll('#crawls-table tbody tr')[0].children;
  assert.ok(firstRowCells[0].querySelector('input[type="checkbox"]'));
});
