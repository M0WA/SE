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
      embedding_provider: 'hash',
      embedding_http_base_url: '',
      embedding_http_model: '',
      embedding_http_dimensions: 256,
      embedding_http_api_key_set: false,
      embedding_rate_limit_per_second: 5,
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
    return { ok: true, json: async () => baseEmbeddingStatus({ total_docs: 42 }) };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-recompute-summary').textContent.includes('42'), true);
});

test('renderConfig shows the hash provider without http-only fields', () => {
  const { renderConfig } = loadFixture();
  renderConfig(baseSettings());
  const text = document.getElementById('embeddings-config').textContent;
  assert.equal(text.includes('Hash'), true);
  assert.equal(text.includes('Base URL'), false);
  assert.equal(text.includes('req/s'), true);
});

test('renderConfig shows http provider fields including base URL, model, dimensions, and api key state', () => {
  const { renderConfig } = loadFixture();
  renderConfig(baseSettings({
    embedding_provider: 'http',
    embedding_http_base_url: 'https://openai.inference.de-txl.ionos.com/v1',
    embedding_http_model: 'BAAI/bge-m3',
    embedding_http_dimensions: 1024,
    embedding_http_api_key_set: true,
    embedding_rate_limit_per_second: 5,
  }));
  const text = document.getElementById('embeddings-config').textContent;
  assert.equal(text.includes('HTTP'), true);
  assert.equal(text.includes('https://openai.inference.de-txl.ionos.com/v1'), true);
  assert.equal(text.includes('BAAI/bge-m3'), true);
  assert.equal(text.includes('1024'), true);
  assert.equal(text.includes('configured'), true);
});

test('renderConfig shows "(not set)" for an unconfigured http base URL, model, and unconfigured api key', () => {
  const { renderConfig } = loadFixture();
  renderConfig(baseSettings({
    embedding_provider: 'http',
    embedding_http_base_url: '',
    embedding_http_model: '',
    embedding_http_dimensions: 256,
    embedding_http_api_key_set: false,
    embedding_rate_limit_per_second: 5,
  }));
  const text = document.getElementById('embeddings-config').textContent;
  assert.equal(text.includes('(not set)'), true);
  assert.equal(text.includes('not configured'), true);
});

test('loadConfig reports the error message on a failed fetch', async () => {
  loadFixture(async (url) => {
    if (url === '/admin/api/settings') return { ok: false, status: 500, text: async () => 'db down' };
    return { ok: true, json: async () => baseEmbeddingStatus() };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-config').textContent.includes('db down'), true);
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
    return { ok: false, status: 500, text: async () => 'job store down' };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-recompute-status').textContent.includes('job store down'), true);
});

test('clicking Recompute embeddings posts a start request and then polls for status', async () => {
  let postedURL = null;
  loadFixture();
  // Let the module's own load-time loadConfig()/loadRecomputeStatus() settle
  // against loadFixture's default fetch stub first -- otherwise it can
  // resolve after the click below and clobber the status text it's about
  // to set, since both write to the same element (see
  // admin_pagerank.test.js's identical comment on its own poll test).
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

test('clicking List available models renders each returned model', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') {
      return { ok: true, json: async () => ({ models: ['BAAI/bge-m3', 'text-embedding-3-small'] }) };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('embeddings-models-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-models-status').textContent, '2 model(s) available from this endpoint.');
  const resultText = document.getElementById('embeddings-models-result').textContent;
  assert.equal(resultText.includes('BAAI/bge-m3'), true);
  assert.equal(resultText.includes('text-embedding-3-small'), true);
  assert.equal(document.getElementById('embeddings-models-btn').disabled, false);
});

test('clicking List available models shows a message when none are reported', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') return { ok: true, json: async () => ({ models: [] }) };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('embeddings-models-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(
    document.getElementById('embeddings-models-status').textContent.includes('No models reported'),
    true,
  );
});

test('clicking List available models shows the endpoint-reported error', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') return { ok: true, json: async () => ({ error: 'unauthorized' }) };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('embeddings-models-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-models-status').textContent, 'Could not list models: unauthorized');
  assert.equal(document.getElementById('embeddings-models-btn').disabled, false);
});

test('clicking List available models shows the empty message when the models field is missing entirely', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') return { ok: true, json: async () => ({}) };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('embeddings-models-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(
    document.getElementById('embeddings-models-status').textContent.includes('No models reported'),
    true,
  );
});

test('clicking List available models reports a network/fetch failure', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') return { ok: false, status: 500, text: async () => 'internal error' };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('embeddings-models-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('embeddings-models-status').textContent.includes('internal error'), true);
  assert.equal(document.getElementById('embeddings-models-btn').disabled, false);
});
