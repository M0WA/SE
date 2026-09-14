'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const PAGERANK_HTML = fs.readFileSync(path.join(__dirname, 'admin_pagerank.html'), 'utf8');

function baseStats(overrides) {
  return Object.assign({
    total_docs: 10,
    min_pagerank: 0.001,
    max_pagerank: 0.05,
    avg_pagerank: 0.01,
    recompute_in_progress: false,
    last_recomputed_at: '2026-01-02T03:04:05Z',
    last_recompute_documents: 10,
    last_recompute_iterations: 20,
    last_recompute_final_delta: 0.0000001,
    last_recompute_duration_ms: 42,
    damping: 0.85,
    max_iterations: 100,
    epsilon: 0.0001,
    pagerank_weight: 0,
    recompute_interval_minutes: 60,
  }, overrides);
}

function loadFixture(fetchImpl) {
  setupDOM(PAGERANK_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => baseStats() }));
  return requireFresh('./admin_pagerank.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('load() renders stats fetched from the server on success', async () => {
  loadFixture(async () => ({ ok: true, json: async () => baseStats({ total_docs: 7 }) }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const text = document.getElementById('pagerank-stats').textContent;
  assert.equal(text.includes('7'), true);
});

test('load() reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db down' }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('pagerank-stats').textContent.includes('db down'), true);
});

test('renderStats renders distribution, config, and a "no recompute yet" message', () => {
  const { renderStats } = loadFixture();
  renderStats(baseStats({ last_recomputed_at: '' }));
  assert.equal(document.getElementById('pagerank-stats').textContent.includes('0.05'), true);
  assert.equal(document.getElementById('pagerank-last-run').textContent, 'No recompute has run yet on this instance.');
  const configText = document.getElementById('pagerank-config').textContent;
  assert.equal(configText.includes('0.85'), true);
  assert.equal(configText.includes('60 min'), true);
});

test('renderStats renders the last recompute summary when one has run', () => {
  const { renderStats } = loadFixture();
  renderStats(baseStats());
  const text = document.getElementById('pagerank-last-run').textContent;
  assert.equal(text.includes('10'), true);
  assert.equal(text.includes('20'), true);
  assert.equal(text.includes('42 ms'), true);
});

test('renderStats shows "Recomputing…" and disables the button while in progress', () => {
  const { renderStats } = loadFixture();
  renderStats(baseStats({ recompute_in_progress: true }));
  assert.equal(document.getElementById('pagerank-last-run').textContent.includes('Recomputing…'), true);
  assert.equal(document.getElementById('recompute-btn').disabled, true);
});

test('renderStats schedules exactly one poll while recompute is in progress, and clears it once done', () => {
  const originalSetTimeout = global.setTimeout;
  const originalClearTimeout = global.clearTimeout;
  const scheduled = [];
  let cleared = 0;
  global.setTimeout = (fn, ms) => { scheduled.push(ms); return 'fake-timer'; };
  global.clearTimeout = () => { cleared++; };
  try {
    const { renderStats } = loadFixture();
    renderStats(baseStats({ recompute_in_progress: true }));
    renderStats(baseStats({ recompute_in_progress: true }));
    assert.deepEqual(scheduled, [2000]);
    renderStats(baseStats({ recompute_in_progress: false }));
    assert.equal(cleared, 1);
  } finally {
    global.setTimeout = originalSetTimeout;
    global.clearTimeout = originalClearTimeout;
  }
});

test('the poll timer actually re-fetches stats once it fires', async () => {
  let fetchCount = 0;
  const { renderStats } = loadFixture(async () => {
    fetchCount++;
    return { ok: true, json: async () => baseStats({ recompute_in_progress: false }) };
  });
  await new Promise((resolve) => setTimeout(resolve, 0)); // let the module's own load-time load() settle
  fetchCount = 0;

  const originalSetTimeout = global.setTimeout;
  let capturedFn = null;
  try {
    global.setTimeout = (fn, ms) => { capturedFn = fn; return 'fake-timer'; };
    renderStats(baseStats({ recompute_in_progress: true }));
    assert.notEqual(capturedFn, null);
  } finally {
    global.setTimeout = originalSetTimeout;
  }
  capturedFn(); // simulate the real timer firing
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(fetchCount, 1);
});

test('clicking Recompute now posts a recompute request and shows the result', async () => {
  let postedURL;
  loadFixture(async (url) => {
    if (url === '/admin/api/pagerank/recompute') {
      postedURL = url;
      return {
        ok: true,
        json: async () => ({
          duration_ms: 55, documents: 10, iterations: 12,
          final_delta: 0.000002, min_pagerank: 0.001, max_pagerank: 0.06, avg_pagerank: 0.02,
        }),
      };
    }
    return { ok: true, json: async () => baseStats() };
  });
  document.getElementById('recompute-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(postedURL, '/admin/api/pagerank/recompute');
  assert.equal(document.getElementById('recompute-status').textContent, 'Done in 55 ms.');
  assert.equal(document.getElementById('recompute-result').textContent.includes('12'), true);
});

test('clicking Recompute now reports the error and stops the button loading on failure', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/pagerank/recompute') {
      return { ok: false, status: 500, text: async () => 'recompute failed' };
    }
    return { ok: true, json: async () => baseStats() };
  });
  document.getElementById('recompute-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('recompute-status').textContent, 'Could not recompute: recompute failed');
  assert.equal(document.getElementById('recompute-btn').disabled, false);
});
