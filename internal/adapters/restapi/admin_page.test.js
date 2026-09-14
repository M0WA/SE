'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const OVERVIEW_HTML = fs.readFileSync(path.join(__dirname, 'admin.html'), 'utf8');

// neutralFetch answers every endpoint admin_page.js's own top-level
// loadOverview() call touches, with empty-but-valid data -- so requiring
// the module doesn't race a test's own fetch mock (see flush()) or throw
// once teardownDOM() has already removed `document`.
async function neutralFetch(url) {
  if (url.includes('/vocabulary')) return { ok: true, json: async () => ({ vocabulary_size: 0 }) };
  if (url.includes('/documents/overview')) return { ok: true, json: async () => ({ total_domains: 0, top_domains: [], age_buckets: [] }) };
  return { ok: true, json: async () => ({ total_docs: 0, avg_doc_len: 0, driver: 'sqlite' }) };
}

function loadFixture() {
  setupDOM(OVERVIEW_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = neutralFetch;
  return requireFresh('./admin_page.js');
}

// flush lets a chain of already-scheduled microtasks (module-load-time
// async calls, in particular) fully settle before assertions or teardown --
// a single macrotask tick is enough, since microtasks always drain before it.
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
