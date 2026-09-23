'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const EMBEDDINGS_HTML = fs.readFileSync(path.join(__dirname, 'admin_embeddings.html'), 'utf8');

function baseSettings(operationalOverrides) {
  return {
    operational: Object.assign({
      embedding_hash_enabled: true,
      embedding_search_weights: { hash: 1 },
      embedding_title_weight: 0.3,
      embedding_recompute_concurrency: 4,
    }, operationalOverrides),
  };
}

function baseEmbeddingStatus(overrides) {
  return Object.assign({
    total_docs: 10,
    in_progress: false,
    last_run_at: null,
    documents: 0,
    failed: 0,
    duration_ms: 0,
  }, overrides);
}

function loadFixture(fetchImpl) {
  setupDOM(EMBEDDINGS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async (url) => {
    if (url === '/admin/api/settings') return { ok: true, json: async () => baseSettings() };
    if (url === '/admin/api/embeddings/endpoints') return { ok: true, json: async () => [] };
    return { ok: true, json: async () => baseEmbeddingStatus() };
  });
  return requireFresh('./admin_embeddings.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('load() fetches config and recompute status on page load', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/settings') return { ok: true, json: async () => baseSettings() };
    if (url === '/admin/api/embeddings/endpoints') return { ok: true, json: async () => [] };
    return { ok: true, json: async () => baseEmbeddingStatus({ total_docs: 42 }) };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-recompute-summary').textContent.includes('42'), true);
});

function kvValuesByKey(container) {
  const values = {};
  container.querySelectorAll('.kv-row').forEach((row) => {
    values[row.querySelector('.k').textContent] = row.querySelector('.v').textContent;
  });
  return values;
}

test('providerLabel names the hash provider', () => {
  const { providerLabel } = loadFixture();
  assert.equal(providerLabel('hash', []), 'Hash (dependency-free)');
});

test('providerLabel names a matching endpoint by name and id', () => {
  const { providerLabel } = loadFixture();
  const endpoints = [{ id: 'ionos_bge_m3', name: 'IONOS bge-m3' }];
  assert.equal(providerLabel('ionos_bge_m3', endpoints), 'IONOS bge-m3 (ionos_bge_m3)');
});

test('providerLabel falls back to the raw id when no endpoint matches (e.g. a deleted one)', () => {
  const { providerLabel } = loadFixture();
  assert.equal(providerLabel('gone', []), 'gone');
});

test('activeSearchWeightsLabel shows "none" when nothing is weighted', () => {
  const { activeSearchWeightsLabel } = loadFixture();
  assert.equal(activeSearchWeightsLabel({}, []), 'none');
  assert.equal(activeSearchWeightsLabel({ hash: 0 }, []), 'none');
});

test('activeSearchWeightsLabel lists every positively-weighted provider with its weight', () => {
  const { activeSearchWeightsLabel } = loadFixture();
  const endpoints = [{ id: 'ionos', name: 'IONOS bge-m3' }];
  assert.equal(
    activeSearchWeightsLabel({ hash: 0.3, ionos: 0.7, unused: 0 }, endpoints),
    'Hash (dependency-free) (0.3), IONOS bge-m3 (ionos) (0.7)',
  );
});

test('renderConfig shows only hash enabled when no endpoints are configured', () => {
  const { renderConfig } = loadFixture();
  renderConfig(baseSettings().operational, []);
  const values = kvValuesByKey(document.getElementById('embeddings-config'));
  assert.equal(values['Enabled providers'], 'hash');
  assert.equal(values['Active for search'], 'Hash (dependency-free) (1)');
  assert.equal(values['HTTP endpoints configured'], '0');
  assert.equal(values['Title weight'].includes('0.3'), true);
});

test('renderConfig lists every enabled endpoint by name alongside hash', () => {
  const { renderConfig } = loadFixture();
  const endpoints = [
    { id: 'ionos', name: 'IONOS bge-m3', enabled: true },
    { id: 'local', name: 'Local Ollama', enabled: false },
  ];
  renderConfig(baseSettings({ embedding_search_weights: { ionos: 1 } }).operational, endpoints);
  const values = kvValuesByKey(document.getElementById('embeddings-config'));
  assert.equal(values['Enabled providers'], 'hash, IONOS bge-m3');
  assert.equal(values['Active for search'], 'IONOS bge-m3 (ionos) (1)');
  assert.equal(values['HTTP endpoints configured'], '2');
});

test('renderConfig shows "none" for enabled providers when hash is disabled and no endpoint is enabled', () => {
  const { renderConfig } = loadFixture();
  const endpoints = [{ id: 'local', name: 'Local Ollama', enabled: false }];
  renderConfig(baseSettings({ embedding_hash_enabled: false }).operational, endpoints);
  const values = kvValuesByKey(document.getElementById('embeddings-config'));
  assert.equal(values['Enabled providers'], 'none');
});

test('loadConfig reports the error message on a failed settings fetch', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/settings') return { ok: false, status: 500, text: async () => 'db down' };
    if (url === '/admin/api/embeddings/endpoints') return { ok: true, json: async () => [] };
    return { ok: true, json: async () => baseEmbeddingStatus() };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-config').textContent.includes('db down'), true);
});

test('loadConfig reports the error message on a failed endpoints fetch', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/settings') return { ok: true, json: async () => baseSettings() };
    if (url === '/admin/api/embeddings/endpoints') return { ok: false, status: 500, text: async () => 'endpoints down' };
    return { ok: true, json: async () => baseEmbeddingStatus() };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-config').textContent.includes('endpoints down'), true);
});

test('renderRecomputeStatus shows "no recompute yet" when this instance has never run one', () => {
  const { renderRecomputeStatus } = loadFixture();
  renderRecomputeStatus(baseEmbeddingStatus());
  assert.equal(document.getElementById('embeddings-recompute-status').textContent, 'No recompute has run yet on this instance.');
  assert.equal(document.getElementById('embeddings-recompute-result').textContent, '');
});

test('renderRecomputeStatus renders the last run summary when one has run', () => {
  const { renderRecomputeStatus } = loadFixture();
  renderRecomputeStatus(baseEmbeddingStatus({
    last_run_at: '2026-01-02T03:04:05Z', documents: 8, failed: 2, duration_ms: 4200,
  }));
  const resultText = document.getElementById('embeddings-recompute-result').textContent;
  assert.equal(resultText.includes('8'), true);
  assert.equal(resultText.includes('2'), true);
  assert.equal(resultText.includes('4200'), true);
});

test('renderRecomputeStatus shows "Recomputing…" and disables the button while in progress', () => {
  const { renderRecomputeStatus } = loadFixture();
  renderRecomputeStatus(baseEmbeddingStatus({ in_progress: true }));
  assert.equal(document.getElementById('embeddings-recompute-status').textContent.includes('Recomputing…'), true);
  assert.equal(document.getElementById('embeddings-recompute-btn').disabled, true);
});

test('renderRecomputeStatus shows live progress (count and percentage) while in progress', () => {
  const { renderRecomputeStatus } = loadFixture();
  renderRecomputeStatus(baseEmbeddingStatus({ in_progress: true, total_docs: 200, documents: 50, failed: 0 }));
  const progressText = document.getElementById('embeddings-recompute-progress').textContent;
  assert.equal(progressText.includes('50 / 200'), true);
  assert.equal(progressText.includes('25%'), true);
  // No failures yet -- the "Failed so far" row shouldn't appear.
  assert.equal(progressText.includes('Failed'), false);
});

test('renderRecomputeStatus shows a live failed count once any failures happen mid-run', () => {
  const { renderRecomputeStatus } = loadFixture();
  renderRecomputeStatus(baseEmbeddingStatus({ in_progress: true, total_docs: 200, documents: 50, failed: 3 }));
  const progressText = document.getElementById('embeddings-recompute-progress').textContent;
  assert.equal(progressText.includes('Failed so far'), true);
  assert.equal(progressText.includes('3'), true);
});

test('renderRecomputeStatus clears the live progress display once a run finishes', () => {
  const { renderRecomputeStatus } = loadFixture();
  renderRecomputeStatus(baseEmbeddingStatus({ in_progress: true, total_docs: 200, documents: 50 }));
  renderRecomputeStatus(baseEmbeddingStatus({ in_progress: false, last_run_at: '2026-01-02T03:04:05Z', documents: 200 }));
  assert.equal(document.getElementById('embeddings-recompute-progress').textContent, '');
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
    renderRecomputeStatus(baseEmbeddingStatus({ in_progress: true }));
    renderRecomputeStatus(baseEmbeddingStatus({ in_progress: true }));
    assert.deepEqual(scheduled, [2000]);
    renderRecomputeStatus(baseEmbeddingStatus({ in_progress: false }));
    assert.equal(cleared, 1);
  } finally {
    global.setTimeout = originalSetTimeout;
    global.clearTimeout = originalClearTimeout;
  }
});

test('loadRecomputeStatus reports the error message on a failed fetch', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/settings') return { ok: true, json: async () => baseSettings() };
    if (url === '/admin/api/embeddings/endpoints') return { ok: true, json: async () => [] };
    return { ok: false, status: 500, text: async () => 'job store down' };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-recompute-status').textContent.includes('job store down'), true);
});

test('clicking Recompute embeddings posts a start request and then polls for status', async () => {
  let postedURL = null;
  loadFixture();
  // Let load-time loadConfig()/loadRecomputeStatus() settle against the fetch stub first --
  // otherwise it can resolve after the click and clobber the status text, since both write the
  // same element (see admin_pagerank.test.js).
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url, opts) => {
    if (url === '/admin/api/embeddings/recompute' && opts && opts.method === 'POST') {
      postedURL = url;
      return { ok: true, json: async () => ({ started: true }) };
    }
    if (url === '/admin/api/embeddings/recompute') {
      return {
        ok: true,
        json: async () => baseEmbeddingStatus({ last_run_at: '2026-01-02T03:04:05Z', documents: 5, failed: 1, duration_ms: 123 }),
      };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('embeddings-recompute-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(postedURL, '/admin/api/embeddings/recompute');
  assert.equal(document.getElementById('embeddings-recompute-result').textContent.includes('5'), true);
  assert.equal(document.getElementById('embeddings-recompute-btn').disabled, false);
});

test('clicking Recompute embeddings reports the error and stops the button loading on failure', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url, opts) => {
    if (url === '/admin/api/embeddings/recompute' && opts && opts.method === 'POST') {
      return { ok: false, status: 409, text: async () => 'an embedding recompute is already in progress' };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('embeddings-recompute-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(
    document.getElementById('embeddings-recompute-status').textContent,
    'Could not start recompute: an embedding recompute is already in progress',
  );
  assert.equal(document.getElementById('embeddings-recompute-btn').disabled, false);
});

test('loadConcurrency populates the concurrency field from settings on page load', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/settings') return { ok: true, json: async () => baseSettings({ embedding_recompute_concurrency: 7 }) };
    if (url === '/admin/api/embeddings/endpoints') return { ok: true, json: async () => [] };
    return { ok: true, json: async () => baseEmbeddingStatus() };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-recompute-concurrency').value, '7');
});

test('loadConcurrency reports the error message on a failed settings fetch', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/settings') return { ok: false, status: 500, text: async () => 'settings down' };
    if (url === '/admin/api/embeddings/endpoints') return { ok: true, json: async () => [] };
    return { ok: true, json: async () => baseEmbeddingStatus() };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(
    document.getElementById('embeddings-recompute-concurrency-status').textContent,
    'Could not load concurrency: settings down',
  );
});

test('clicking the concurrency Save button re-fetches settings, applies the edited value, and posts it back', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  let postedBody = null;
  global.fetch = async (url, opts) => {
    if (url === '/admin/api/settings' && opts && opts.method === 'POST') {
      postedBody = JSON.parse(opts.body);
      return { ok: true, json: async () => postedBody };
    }
    if (url === '/admin/api/settings') return { ok: true, json: async () => baseSettings({ embedding_recompute_concurrency: 4 }) };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('embeddings-recompute-concurrency').value = '9';
  document.getElementById('embeddings-recompute-concurrency-save').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(postedBody.operational.embedding_recompute_concurrency, 9);
  assert.equal(
    document.getElementById('embeddings-recompute-concurrency-status').textContent,
    'Saved. Takes effect on the recompute’s next batch.',
  );
});

test('clicking the concurrency Save button reports the error on a failed save', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url, opts) => {
    if (url === '/admin/api/settings' && opts && opts.method === 'POST') {
      return { ok: false, status: 500, text: async () => 'db down' };
    }
    if (url === '/admin/api/settings') return { ok: true, json: async () => baseSettings() };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('embeddings-recompute-concurrency').value = '9';
  document.getElementById('embeddings-recompute-concurrency-save').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(
    document.getElementById('embeddings-recompute-concurrency-status').textContent,
    'Could not save: db down',
  );
});
