'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const OVERVIEW_HTML = fs.readFileSync(path.join(__dirname, 'admin.html'), 'utf8');

// neutralFetch answers every endpoint the module's top-level loadOverview() touches with
// empty-but-valid data, so requiring it doesn't race a test's own mock (see flush()) or throw
// after teardownDOM() removes `document`.
async function neutralFetch(url) {
  if (url.includes('/vocabulary')) return { ok: true, json: async () => ({ vocabulary_size: 0 }) };
  if (url.includes('/documents/overview')) return { ok: true, json: async () => ({ total_domains: 0, top_domains: [], age_buckets: [] }) };
  if (url.includes('/overview/metrics')) return { ok: true, json: async () => ({}) };
  return { ok: true, json: async () => ({ total_docs: 0, avg_doc_len: 0, driver: 'sqlite' }) };
}

function loadFixture() {
  setupDOM(OVERVIEW_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = neutralFetch;
  return requireFresh('./admin_page.js');
}

// flush lets already-scheduled microtasks (module-load-time async calls) settle before
// assertions/teardown -- one macrotask tick suffices, since microtasks always drain first.
function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('buildDonut draws one arc per domain plus the background track', async () => {
  const { buildDonut } = loadFixture();
  await flush();
  const svg = buildDonut([{ host: 'a.example', doc_count: 3 }, { host: 'b.example', doc_count: 1 }], 4);
  const circles = svg.querySelectorAll('circle');
  assert.equal(circles.length, 3); // track + 2 arcs
  assert.equal(circles[1].querySelector('title').textContent, 'a.example — 3 pages');
  assert.equal(circles[2].querySelector('title').textContent, 'b.example — 1 page');
});

test('buildDonut handles a zero total without dividing by zero', async () => {
  const { buildDonut } = loadFixture();
  await flush();
  const svg = buildDonut([{ host: 'a.example', doc_count: 0 }], 0);
  assert.equal(svg.querySelectorAll('circle').length, 2);
});

test('buildDonutLegend renders one row per domain, linking to its documents page', async () => {
  const { buildDonutLegend } = loadFixture();
  await flush();
  const legend = buildDonutLegend([{ host: 'a.example', doc_count: 3 }]);
  const rows = legend.querySelectorAll('.donut-legend-row');
  assert.equal(rows.length, 1);
  const link = rows[0].querySelector('a');
  assert.equal(link.textContent, 'a.example');
  assert.equal(link.getAttribute('href'), '/admin/documents/a.example');
  assert.equal(rows[0].querySelector('.domain-metrics').textContent, '3');
});

test('buildAgeBars scales each bar height by the largest bucket', async () => {
  const { buildAgeBars } = loadFixture();
  await flush();
  const wrap = buildAgeBars([{ label: '<1d', count: 10 }, { label: '<1w', count: 5 }]);
  const cols = wrap.querySelectorAll('.age-bar-col');
  assert.equal(cols.length, 2);
  assert.equal(cols[0].querySelector('.age-bar-count').textContent, '10');
  assert.equal(cols[0].querySelector('.age-bar-label').textContent, '<1d');
  assert.equal(cols[0].querySelector('.age-bar').style.height, '80px');
  assert.equal(cols[1].querySelector('.age-bar').style.height, '40px');
});

test('buildAgeBars floors bar height at 2px for an empty bucket', async () => {
  const { buildAgeBars } = loadFixture();
  await flush();
  const wrap = buildAgeBars([{ label: 'x', count: 0 }]);
  assert.equal(wrap.querySelector('.age-bar').style.height, '2px');
});

test('buildVersionBars labels each bar with its version number', async () => {
  const { buildVersionBars } = loadFixture();
  await flush();
  const wrap = buildVersionBars([{ version: 1, count: 4 }, { version: 2, count: 1 }]);
  const cols = wrap.querySelectorAll('.age-bar-col');
  assert.equal(cols[0].querySelector('.age-bar-label').textContent, 'v1');
  assert.equal(cols[0].querySelector('.age-bar').title, 'Version 1: 4 documents');
  assert.equal(cols[1].querySelector('.age-bar').title, 'Version 2: 1 document');
});

test('buildStoredVersionsBars labels each bar with its stored-version count, singular/plural', async () => {
  const { buildStoredVersionsBars } = loadFixture();
  await flush();
  const wrap = buildStoredVersionsBars([{ stored_versions: 1, doc_count: 5 }, { stored_versions: 2, doc_count: 2 }]);
  const cols = wrap.querySelectorAll('.age-bar-col');
  assert.equal(cols[0].querySelector('.age-bar-label').textContent, '1 version');
  assert.equal(cols[0].querySelector('.age-bar').title, '1 version: 5 documents');
  assert.equal(cols[1].querySelector('.age-bar-label').textContent, '2 versions');
  assert.equal(cols[1].querySelector('.age-bar').title, '2 versions: 2 documents');
});

test('buildJobOutcomeDonut draws one arc per outcome plus the background track', async () => {
  const { buildJobOutcomeDonut } = loadFixture();
  await flush();
  const svg = buildJobOutcomeDonut([{ status: 'done', count: 3 }, { status: 'failed', count: 1 }], 4);
  const circles = svg.querySelectorAll('circle');
  assert.equal(circles.length, 3); // track + 2 arcs
  assert.equal(circles[1].querySelector('title').textContent, 'done — 3 jobs');
  assert.equal(circles[2].querySelector('title').textContent, 'failed — 1 job');
});

test('buildJobOutcomeDonut handles a zero total without dividing by zero', async () => {
  const { buildJobOutcomeDonut } = loadFixture();
  await flush();
  const svg = buildJobOutcomeDonut([{ status: 'done', count: 0 }], 0);
  assert.equal(svg.querySelectorAll('circle').length, 2);
});

test('buildJobOutcomeLegend renders one row per outcome with no link', async () => {
  const { buildJobOutcomeLegend } = loadFixture();
  await flush();
  const legend = buildJobOutcomeLegend([{ status: 'done', count: 3 }, { status: 'failed', count: 1 }]);
  const rows = legend.querySelectorAll('.donut-legend-row');
  assert.equal(rows.length, 2);
  assert.equal(rows[0].querySelector('a'), null);
  assert.equal(rows[0].textContent.includes('done'), true);
  assert.equal(rows[0].querySelector('.domain-metrics').textContent, '3');
});

test('buildThroughputStackBars scales each column by the largest day total, with one segment per outcome', async () => {
  const { buildThroughputStackBars } = loadFixture();
  await flush();
  const daily = [
    { date: '2025-01-01', outcomes: { indexed: 8, fetch_failed: 2 } },
    { date: '2025-01-02', outcomes: { indexed: 5 } },
  ];
  const wrap = buildThroughputStackBars(daily);
  const cols = wrap.querySelectorAll('.age-bar-col');
  assert.equal(cols.length, 2);
  assert.equal(cols[0].querySelector('.age-bar-count').textContent, '10');
  assert.equal(cols[0].querySelector('.age-bar-label').textContent, '01-01');
  assert.equal(cols[0].querySelectorAll('.stack-seg').length, 2);
  assert.equal(cols[0].querySelector('.stack-bar').style.height, '80px');
  assert.equal(cols[1].querySelectorAll('.stack-seg').length, 1);
  assert.equal(cols[1].querySelector('.stack-bar').style.height, '40px');
});

test('buildThroughputStackBars omits a segment entirely for a zero-count outcome', async () => {
  const { buildThroughputStackBars } = loadFixture();
  await flush();
  const wrap = buildThroughputStackBars([{ date: '2025-01-01', outcomes: { indexed: 1, thin_content: 0 } }]);
  assert.equal(wrap.querySelectorAll('.stack-seg').length, 1);
});

test('buildThroughputStackBars floors an empty day at a 2px bar', async () => {
  const { buildThroughputStackBars } = loadFixture();
  await flush();
  const wrap = buildThroughputStackBars([{ date: '2025-01-01', outcomes: {} }]);
  assert.equal(wrap.querySelector('.stack-bar').style.height, '2px');
  assert.equal(wrap.querySelectorAll('.stack-seg').length, 0);
});

test('buildThroughputStackBars still renders a segment for a status outside FETCH_OUTCOME_ORDER, with a fallback opacity, rather than dropping it', async () => {
  const { buildThroughputStackBars } = loadFixture();
  await flush();
  const wrap = buildThroughputStackBars([{ date: '2025-01-01', outcomes: { indexed: 3, some_future_status: 2 } }]);
  const segs = wrap.querySelectorAll('.stack-seg');
  // Both statuses get a segment, keeping the count label and combined flex weight consistent --
  // an unrecognized status never vanishes while still counting toward the bar's height.
  assert.equal(segs.length, 2);
  assert.equal(wrap.querySelector('.age-bar-count').textContent, '5');
  assert.equal(segs[0].title, '2025-01-01 indexed: 3');
  assert.equal(segs[1].title, '2025-01-01 some_future_status: 2');
  assert.equal(segs[1].style.opacity, '0.15');
});

test('buildPageRankHistogram scales each bar by the largest bucket and labels it', async () => {
  const { buildPageRankHistogram } = loadFixture();
  await flush();
  const wrap = buildPageRankHistogram([{ label: '0e+00–1e-01', count: 8 }, { label: '1e-01–2e-01', count: 2 }]);
  const cols = wrap.querySelectorAll('.age-bar-col');
  assert.equal(cols.length, 2);
  assert.equal(cols[0].querySelector('.age-bar-label').textContent, '0e+00–1e-01');
  assert.equal(cols[0].querySelector('.age-bar').title, '0e+00–1e-01: 8 documents');
  assert.equal(cols[0].querySelector('.age-bar').style.height, '80px');
  assert.equal(cols[1].querySelector('.age-bar').style.height, '20px');
});

test('buildLineChart reports no data for an empty series without drawing an svg', async () => {
  const { buildLineChart } = loadFixture();
  await flush();
  const wrap = buildLineChart([], (v) => String(v));
  assert.equal(wrap.querySelector('svg'), null);
  assert.equal(wrap.classList.contains('line-chart-empty'), true);
  assert.equal(wrap.textContent, 'No data in this window.');
});

test('buildLineChart draws one point per entry with a hover title, and first/last date labels', async () => {
  const { buildLineChart } = loadFixture();
  await flush();
  const points = [{ date: '2025-01-01', value: 4 }, { date: '2025-01-02', value: 9 }, { date: '2025-01-03', value: 2 }];
  const wrap = buildLineChart(points, (v) => v + ' docs');
  const svg = wrap.querySelector('svg');
  assert.notEqual(svg, null);
  const dots = svg.querySelectorAll('circle');
  assert.equal(dots.length, 3);
  assert.equal(dots[1].querySelector('title').textContent, '2025-01-02: 9 docs');
  const labels = wrap.querySelectorAll('.line-chart-labels span');
  assert.equal(labels[0].textContent, '2025-01-01');
  assert.equal(labels[1].textContent, '2025-01-03');
});

test('buildLineChart spaces points by actual elapsed days, not by array index, so a multi-day gap shows up as real horizontal space', async () => {
  const { buildLineChart } = loadFixture();
  await flush();
  // A 1-day gap then an 8-day gap: index-based spacing would place the middle dot halfway
  // across; date-based spacing must place it much closer to the first point.
  const points = [{ date: '2025-01-01', value: 1 }, { date: '2025-01-02', value: 2 }, { date: '2025-01-10', value: 3 }];
  const wrap = buildLineChart(points, (v) => String(v));
  const dots = wrap.querySelectorAll('circle');
  const cx = [...dots].map((d) => Number(d.getAttribute('cx')));
  assert.equal(cx[0], 8);
  assert.equal(cx[2], 312);
  // Expected: 8 + (1/9)*304 = 41.8, not the index-based midpoint (160).
  assert.equal(cx[1], 41.8);
});

test('buildLineChart handles a single point without dividing by zero', async () => {
  const { buildLineChart } = loadFixture();
  await flush();
  const wrap = buildLineChart([{ date: '2025-01-01', value: 5 }], (v) => String(v));
  assert.equal(wrap.querySelectorAll('circle').length, 1);
});

test('buildLineChart handles every value being equal (zero span) without dividing by zero', async () => {
  const { buildLineChart } = loadFixture();
  await flush();
  const points = [{ date: '2025-01-01', value: 5 }, { date: '2025-01-02', value: 5 }];
  const wrap = buildLineChart(points, (v) => String(v));
  assert.equal(wrap.querySelectorAll('circle').length, 2);
});

test('loadOverviewMetrics reports an error in tilesEl and leaves chartsEl untouched on fetch failure', async () => {
  const { loadOverviewMetrics } = loadFixture();
  await flush();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'db down' });
  const tilesEl = document.createElement('div');
  const chartsEl = document.createElement('div');
  await loadOverviewMetrics(tilesEl, chartsEl);
  assert.equal(tilesEl.textContent.includes('db down'), true);
  assert.equal(chartsEl.children.length, 0);
});

test('loadOverviewMetrics renders tiles and appends a second charts row when tier-2 data is present', async () => {
  const { loadOverviewMetrics } = loadFixture();
  await flush();
  global.fetch = async () => ({
    ok: true,
    json: async () => ({
      running_crawl_jobs: 1,
      queued_crawl_jobs: 2,
      running_jobs: [{ id: 'job-1', seed_urls: ['https://a.example'], pages_crawled: 7 }],
      schedules_enabled: 3,
      schedules_disabled: 1,
      schedules_in_progress: 1,
      schedules_overdue: 1,
      pool: { max_open_connections: 10, in_use: 2, idle: 3, open_connections: 5 },
      job_outcomes: [{ status: 'done', count: 3 }],
      daily_fetch_outcomes: [{ date: '2025-01-01', outcomes: { indexed: 3 } }],
      documents_by_day: [{ date: '2025-01-01', count: 3 }],
      fetch_duration_by_day: [{ date: '2025-01-01', avg_duration_ms: 120 }],
      pagerank_buckets: [{ label: '0e+00–1e-01', count: 3 }],
      pagerank_orphan_threshold: 0.000001,
      pagerank_orphan_count: 1,
      pagerank_orphan_percent: 33.3,
      pagerank_total_docs: 3,
    }),
  });
  const tilesEl = document.createElement('div');
  const chartsEl = document.createElement('div');
  await loadOverviewMetrics(tilesEl, chartsEl);

  const tiles = tilesEl.querySelectorAll('.tile');
  assert.equal(tiles.length > 0, true);
  assert.equal(tilesEl.textContent.includes('Running crawl jobs'), true);
  assert.equal(tilesEl.textContent.includes('2 / 10'), true); // DB connections in use
  assert.equal(tilesEl.textContent.includes('33.3%'), true);

  assert.equal(chartsEl.children.length, 1);
  const blocks = chartsEl.querySelectorAll('.overview-block');
  assert.equal(blocks.length, 6); // running jobs, outcomes, throughput, documents trend, duration trend, pagerank
});

test('loadOverviewMetrics omits the orphan tile and every tier-2 block when there is no data', async () => {
  const { loadOverviewMetrics } = loadFixture();
  await flush();
  global.fetch = async () => ({ ok: true, json: async () => ({}) });
  const tilesEl = document.createElement('div');
  const chartsEl = document.createElement('div');
  await loadOverviewMetrics(tilesEl, chartsEl);
  assert.equal(tilesEl.textContent.includes('Orphan pages'), false);
  assert.equal(chartsEl.children.length, 0);
});

test('loadCorpusOverview reports an error and stops when the overview fetch fails', async () => {
  const { loadCorpusOverview } = loadFixture();
  await flush();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'db down' });
  const statsEl = document.createElement('div');
  const statusEl = document.createElement('div');
  const chartsEl = document.createElement('div');
  await loadCorpusOverview(statsEl, statusEl, chartsEl);
  assert.equal(statusEl.textContent.includes('db down'), true);
  assert.equal(chartsEl.children.length, 0);
});

test('loadCorpusOverview shows a no-documents message when the corpus is empty', async () => {
  const { loadCorpusOverview } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/vocabulary')) return { ok: true, json: async () => ({ vocabulary_size: 0 }) };
    return { ok: true, json: async () => ({ total_domains: 0, top_domains: [], age_buckets: [] }) };
  };
  const statsEl = document.createElement('div');
  const statusEl = document.createElement('div');
  const chartsEl = document.createElement('div');
  await loadCorpusOverview(statsEl, statusEl, chartsEl);
  assert.equal(statusEl.textContent, 'No documents indexed yet.');
  assert.equal(chartsEl.children.length, 0);
});

test('loadCorpusOverview renders domain/age charts, and version charts only when present', async () => {
  const { loadCorpusOverview } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/vocabulary')) return { ok: true, json: async () => ({ vocabulary_size: 42 }) };
    return {
      ok: true,
      json: async () => ({
        total_domains: 2,
        top_domains: [{ host: 'a.example', doc_count: 3 }],
        age_buckets: [{ label: '<1d', count: 3 }],
        version_counts: [{ version: 1, count: 3 }],
        stored_version_counts: [{ stored_versions: 1, doc_count: 3 }],
      }),
    };
  };
  const statsEl = document.createElement('div');
  const statusEl = document.createElement('div');
  const chartsEl = document.createElement('div');
  await loadCorpusOverview(statsEl, statusEl, chartsEl);
  assert.equal(statusEl.textContent, '');
  const blocks = chartsEl.querySelectorAll('.overview-block');
  assert.equal(blocks.length, 4);
  assert.equal(statsEl.textContent.includes('42'), true);
});

test('loadCorpusOverview omits the version blocks when the overview has none', async () => {
  const { loadCorpusOverview } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/vocabulary')) return { ok: true, json: async () => ({ vocabulary_size: 0 }) };
    return {
      ok: true,
      json: async () => ({
        total_domains: 1,
        top_domains: [{ host: 'a.example', doc_count: 1 }],
        age_buckets: [{ label: '<1d', count: 1 }],
        version_counts: [],
        stored_version_counts: [],
      }),
    };
  };
  const statsEl = document.createElement('div');
  const statusEl = document.createElement('div');
  const chartsEl = document.createElement('div');
  await loadCorpusOverview(statsEl, statusEl, chartsEl);
  assert.equal(chartsEl.querySelectorAll('.overview-block').length, 2);
});

test('loadCorpusOverview swallows a vocabulary-fetch failure without clearing the rest of the panel', async () => {
  const { loadCorpusOverview } = loadFixture();
  await flush();
  let call = 0;
  global.fetch = async (url) => {
    call++;
    if (url.includes('/vocabulary')) throw new Error('network down');
    return {
      ok: true,
      json: async () => ({ total_domains: 1, top_domains: [{ host: 'a.example', doc_count: 1 }], age_buckets: [] }),
    };
  };
  const statsEl = document.createElement('div');
  const statusEl = document.createElement('div');
  const chartsEl = document.createElement('div');
  await assert.doesNotReject(() => loadCorpusOverview(statsEl, statusEl, chartsEl));
  assert.equal(statusEl.textContent, '');
  assert.equal(call, 2);
});

test('loadOverview wires #stats, #overview-status and #overview-charts from the real page', async () => {
  const { loadOverview } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/vocabulary')) return { ok: true, json: async () => ({ vocabulary_size: 3 }) };
    if (url.includes('/documents/overview')) {
      return {
        ok: true,
        json: async () => ({ total_domains: 1, top_domains: [{ host: 'a.example', doc_count: 1 }], age_buckets: [{ label: '<1d', count: 1 }] }),
      };
    }
    return { ok: true, json: async () => ({ total_docs: 1, avg_doc_len: 1, driver: 'sqlite' }) };
  };
  await loadOverview();
  assert.equal(document.getElementById('overview-status').textContent, '');
  assert.equal(document.getElementById('overview-charts').children.length, 1);
  assert.equal(document.getElementById('stats').textContent.includes('sqlite'), true);
});
