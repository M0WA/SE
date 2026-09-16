'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const CONTENT_DEDUP_HTML = fs.readFileSync(path.join(__dirname, 'admin_content_dedup.html'), 'utf8');

function baseDedupStatus(overrides) {
  return Object.assign({
    in_progress: false,
    last_run_at: null,
    groups_found: 0,
    documents_merged: 0,
    duration_ms: 0,
  }, overrides);
}

function baseGroupsResponse(overrides) {
  return Object.assign({ total: 0, groups: [] }, overrides);
}

function loadFixture(fetchImpl) {
  setupDOM(CONTENT_DEDUP_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async (url) => {
    if (url === '/admin/api/content-dedup') return { ok: true, json: async () => baseDedupStatus() };
    if (url.startsWith('/admin/api/content-dedup/alias-groups')) return { ok: true, json: async () => baseGroupsResponse() };
    return { ok: true, json: async () => ({}) };
  });
  return requireFresh('./admin_content_dedup.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('load() fetches recompute status and merged groups on page load', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/content-dedup') return { ok: true, json: async () => baseDedupStatus() };
    if (url.startsWith('/admin/api/content-dedup/alias-groups')) {
      return { ok: true, json: async () => baseGroupsResponse({ total: 3 }) };
    }
    return { ok: true, json: async () => ({}) };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('dedup-groups-summary').textContent.includes('3'), true);
});

test('renderRecomputeStatus shows "no recompute yet" when this instance has never run one', () => {
  const { renderRecomputeStatus } = loadFixture();
  renderRecomputeStatus(baseDedupStatus());
  assert.equal(document.getElementById('dedup-recompute-status').textContent, 'No recompute has run yet on this instance.');
  assert.equal(document.getElementById('dedup-recompute-result').textContent, '');
});

test('renderRecomputeStatus renders the last run summary when one has run', () => {
  const { renderRecomputeStatus } = loadFixture();
  renderRecomputeStatus(baseDedupStatus({
    last_run_at: '2026-01-02T03:04:05Z', groups_found: 4, documents_merged: 9, duration_ms: 4200,
  }));
  const resultText = document.getElementById('dedup-recompute-result').textContent;
  assert.equal(resultText.includes('4'), true);
  assert.equal(resultText.includes('9'), true);
  assert.equal(resultText.includes('4200'), true);
});

test('renderRecomputeStatus shows "Recomputing…" and disables the button while in progress', () => {
  const { renderRecomputeStatus } = loadFixture();
  renderRecomputeStatus(baseDedupStatus({ in_progress: true }));
  assert.equal(document.getElementById('dedup-recompute-status').textContent.includes('Recomputing…'), true);
  assert.equal(document.getElementById('dedup-recompute-btn').disabled, true);
});

test('renderRecomputeStatus schedules exactly one poll while in progress, and clears it once done', () => {
  const originalSetTimeout = global.setTimeout;
  const originalClearTimeout = global.clearTimeout;
  const scheduled = [];
  let cleared = 0;
  global.setTimeout = (fn, ms) => { scheduled.push(ms); return 'fake-timer'; };
  global.clearTimeout = () => { cleared++; };
  try {
    const { renderRecomputeStatus } = loadFixture();
    renderRecomputeStatus(baseDedupStatus({ in_progress: true }));
    renderRecomputeStatus(baseDedupStatus({ in_progress: true }));
    assert.deepEqual(scheduled, [2000]);
    renderRecomputeStatus(baseDedupStatus({ in_progress: false }));
    assert.equal(cleared, 1);
  } finally {
    global.setTimeout = originalSetTimeout;
    global.clearTimeout = originalClearTimeout;
  }
});

test('loadRecomputeStatus reports the error message on a failed fetch', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/content-dedup') return { ok: false, status: 500, text: async () => 'status store down' };
    if (url.startsWith('/admin/api/content-dedup/alias-groups')) return { ok: true, json: async () => baseGroupsResponse() };
    return { ok: true, json: async () => ({}) };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('dedup-recompute-status').textContent.includes('status store down'), true);
});

test('clicking Recompute now posts a start request and then polls for status', async () => {
  let postedURL = null;
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url, opts) => {
    if (url === '/admin/api/content-dedup/recompute' && opts && opts.method === 'POST') {
      postedURL = url;
      return { ok: true, json: async () => ({ started: true }) };
    }
    if (url === '/admin/api/content-dedup') {
      return {
        ok: true,
        json: async () => baseDedupStatus({ last_run_at: '2026-01-02T03:04:05Z', groups_found: 2, documents_merged: 5, duration_ms: 123 }),
      };
    }
    if (url.startsWith('/admin/api/content-dedup/alias-groups')) return { ok: true, json: async () => baseGroupsResponse() };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('dedup-recompute-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(postedURL, '/admin/api/content-dedup/recompute');
  assert.equal(document.getElementById('dedup-recompute-result').textContent.includes('2'), true);
  assert.equal(document.getElementById('dedup-recompute-btn').disabled, false);
});

test('clicking Recompute now reports the error and stops the button loading on failure', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url, opts) => {
    if (url === '/admin/api/content-dedup/recompute' && opts && opts.method === 'POST') {
      return { ok: false, status: 409, text: async () => 'a content dedup recompute is already in progress' };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('dedup-recompute-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(
    document.getElementById('dedup-recompute-status').textContent,
    'Could not start recompute: a content dedup recompute is already in progress',
  );
  assert.equal(document.getElementById('dedup-recompute-btn').disabled, false);
});

test('buildGroupsTable renders one row per group with its canonical URL and joined alias URLs', () => {
  const { buildGroupsTable } = loadFixture();
  const table = buildGroupsTable([
    { canonical_id: 'doc-1', canonical_url: 'https://example.com/a', alias_urls: ['https://www.example.com/a', 'https://mirror.example/a'] },
  ]);
  const cells = Array.from(table.querySelectorAll('tbody td')).map((td) => td.textContent);
  assert.deepEqual(cells, ['https://example.com/a', 'https://www.example.com/a, https://mirror.example/a']);
});

test('renderGroupsPager hides the pager when everything fits on one page', () => {
  const { renderGroupsPager } = loadFixture();
  renderGroupsPager(5);
  assert.equal(document.getElementById('dedup-groups-pager').hidden, true);
});

test('renderGroupsPager shows page info and enables Next when more pages remain', () => {
  const { renderGroupsPager } = loadFixture();
  renderGroupsPager(45);
  assert.equal(document.getElementById('dedup-groups-pager').hidden, false);
  assert.equal(document.getElementById('dedup-groups-page-info').textContent, 'Page 1 of 3');
  assert.equal(document.getElementById('dedup-groups-prev').disabled, true);
  assert.equal(document.getElementById('dedup-groups-next').disabled, false);
});

test('loadGroups shows a distinct empty message when nothing has ever been merged', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/content-dedup') return { ok: true, json: async () => baseDedupStatus() };
    if (url.startsWith('/admin/api/content-dedup/alias-groups')) return { ok: true, json: async () => baseGroupsResponse({ total: 0 }) };
    return { ok: true, json: async () => ({}) };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('dedup-groups-table').textContent, 'No documents have been merged yet.');
});

test('loadGroups reports the error message on a failed fetch', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/content-dedup') return { ok: true, json: async () => baseDedupStatus() };
    if (url.startsWith('/admin/api/content-dedup/alias-groups')) return { ok: false, status: 500, text: async () => 'groups store down' };
    return { ok: true, json: async () => ({}) };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('dedup-groups-summary').textContent.includes('groups store down'), true);
});

test('clicking Next/Previous pages through merged groups', async () => {
  const requestedOffsets = [];
  loadFixture(async (url) => {
    if (url === '/admin/api/content-dedup') return { ok: true, json: async () => baseDedupStatus() };
    if (url.startsWith('/admin/api/content-dedup/alias-groups')) {
      const offset = new URL(url, 'http://localhost').searchParams.get('offset');
      requestedOffsets.push(offset);
      return { ok: true, json: async () => baseGroupsResponse({ total: 45 }) };
    }
    return { ok: true, json: async () => ({}) };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('dedup-groups-next').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('dedup-groups-prev').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(requestedOffsets, ['0', '20', '0']);
});
