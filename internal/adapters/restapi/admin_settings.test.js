'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const SETTINGS_HTML = fs.readFileSync(path.join(__dirname, 'admin_settings.html'), 'utf8');

const FULL_SETTINGS = {
  tuning: { alpha: 0.5, k1: 1.2, b: 0.75, pagerank_weight: 0 },
  operational: {
    title_weight: 2,
    fetch_timeout_seconds: 8,
    user_agent: 'se-bot',
    default_max_pages: 20,
    min_text_length: 50,
    crawl_delay_ms: 250,
    max_response_kb: 5120,
    max_retained_crawl_jobs: 200,
    default_renderer: 'none',
    link_scope: 'domain',
    default_top_k: 10,
    semantic_candidate_pool_size: 200,
    ann_search_enabled: true,
    embedding_provider: 'hash',
    embedding_http_base_url: '',
    embedding_http_model: '',
    embedding_http_dimensions: 128,
    embedding_http_api_key_set: false,
    embedding_recompute_rate_limit_per_second: 5,
    max_document_versions: 5,
    db_max_open_conns: 25,
    db_max_idle_conns: 25,
    db_conn_max_lifetime_minutes: 5,
    fuzzy_match_enabled: true,
    fuzzy_max_edit_distance: 2,
    pagerank_recompute_interval_minutes: 60,
    session_ttl_hours: 12,
  },
};

const EMPTY_OVERRIDES = { blocked_terms: [], blocked_domains: [], boosted_terms: {}, boosted_domains: {} };

function loadFixture() {
  setupDOM(SETTINGS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async (url) => {
    if (url.includes('/admin/api/settings')) return { ok: true, json: async () => FULL_SETTINGS };
    if (url.includes('/admin/api/overrides')) return { ok: true, json: async () => EMPTY_OVERRIDES };
    return { ok: true, json: async () => ({}) };
  };
  return requireFresh('./admin_settings.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('applySettings fills every field, applying the renderer/link-scope fallbacks', () => {
  const { applySettings } = loadFixture();
  applySettings(FULL_SETTINGS);
  assert.equal(document.getElementById('alpha').value, '0.5');
  assert.equal(document.getElementById('title-weight').value, '2');
  assert.equal(document.getElementById('default-renderer').value, 'none');
  assert.equal(document.getElementById('default-link-scope').value, 'domain');
  assert.equal(document.getElementById('ann-search-enabled').checked, true);
  assert.equal(document.getElementById('session-ttl').value, '12');
});

test('applySettings falls back to "none"/"domain" when renderer/link_scope are unset', () => {
  const { applySettings } = loadFixture();
  const s = JSON.parse(JSON.stringify(FULL_SETTINGS));
  s.operational.default_renderer = '';
  s.operational.link_scope = '';
  applySettings(s);
  assert.equal(document.getElementById('default-renderer').value, 'none');
  assert.equal(document.getElementById('default-link-scope').value, 'domain');
});

test('applySettings shows the HTTP embedding fields and "no key" hint for the hash provider', () => {
  const { applySettings } = loadFixture();
  applySettings(FULL_SETTINGS);
  assert.equal(document.getElementById('embedding-provider').value, 'hash');
  assert.equal(document.getElementById('embedding-http-fields').hidden, true);
  assert.equal(document.getElementById('embedding-http-api-key').value, '');
  assert.equal(document.getElementById('embedding-http-api-key-hint').textContent, 'No key currently configured.');
});

test('applySettings reveals the HTTP embedding fields and never fills in the API key, even when one is configured', () => {
  const { applySettings } = loadFixture();
  const s = JSON.parse(JSON.stringify(FULL_SETTINGS));
  s.operational.embedding_provider = 'http';
  s.operational.embedding_http_base_url = 'http://localhost:11434/v1';
  s.operational.embedding_http_model = 'nomic-embed-text';
  s.operational.embedding_http_dimensions = 768;
  s.operational.embedding_http_api_key_set = true;
  applySettings(s);
  assert.equal(document.getElementById('embedding-http-fields').hidden, false);
  assert.equal(document.getElementById('embedding-http-base-url').value, 'http://localhost:11434/v1');
  assert.equal(document.getElementById('embedding-http-model').value, 'nomic-embed-text');
  assert.equal(document.getElementById('embedding-http-dimensions').value, '768');
  assert.equal(document.getElementById('embedding-recompute-rate-limit').value, '5');
  assert.equal(document.getElementById('embedding-http-api-key').value, '');
  assert.equal(
    document.getElementById('embedding-http-api-key-hint').textContent,
    'A key is currently configured. Leave blank to keep it, or type a new one to replace it.',
  );
});

test('toggleEmbeddingHTTPFields shows/hides the HTTP fields based on the select value', () => {
  const { toggleEmbeddingHTTPFields } = loadFixture();
  const select = document.getElementById('embedding-provider');
  select.value = 'http';
  toggleEmbeddingHTTPFields();
  assert.equal(document.getElementById('embedding-http-fields').hidden, false);
  select.value = 'hash';
  toggleEmbeddingHTTPFields();
  assert.equal(document.getElementById('embedding-http-fields').hidden, true);
});

test('changing the embedding provider select toggles the HTTP fields visibility', () => {
  loadFixture();
  const select = document.getElementById('embedding-provider');
  select.value = 'http';
  select.dispatchEvent(new window.Event('change'));
  assert.equal(document.getElementById('embedding-http-fields').hidden, false);
});

test('the embedding API key field starts readonly and becomes editable on focus', () => {
  loadFixture();
  const input = document.getElementById('embedding-http-api-key');
  assert.equal(input.hasAttribute('readonly'), true);
  input.dispatchEvent(new window.Event('focus'));
  assert.equal(input.hasAttribute('readonly'), false);
});

test('saveSettings posts a blank embedding API key by default, leaving the stored key untouched', async () => {
  const { saveSettings } = loadFixture();
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.embedding_provider, 'hash');
  assert.equal(gotBody.operational.embedding_http_api_key, '');
});

test('saveSettings posts a typed embedding API key and the HTTP provider fields', async () => {
  const { saveSettings } = loadFixture();
  document.getElementById('embedding-provider').value = 'http';
  document.getElementById('embedding-http-base-url').value = 'https://api.example.com/v1';
  document.getElementById('embedding-http-model').value = 'text-embedding-3-small';
  document.getElementById('embedding-http-dimensions').value = '1536';
  const keyInput = document.getElementById('embedding-http-api-key');
  keyInput.removeAttribute('readonly');
  keyInput.value = 'sk-new-key';
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.embedding_provider, 'http');
  assert.equal(gotBody.operational.embedding_http_base_url, 'https://api.example.com/v1');
  assert.equal(gotBody.operational.embedding_http_model, 'text-embedding-3-small');
  assert.equal(gotBody.operational.embedding_http_dimensions, 1536);
  assert.equal(gotBody.operational.embedding_http_api_key, 'sk-new-key');
});

test('saveSettings posts 0 for an unparseable embedding dimensions field', async () => {
  const { saveSettings } = loadFixture();
  document.getElementById('embedding-http-dimensions').value = '';
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.embedding_http_dimensions, 0);
});

test('saveSettings posts the configured embedding recompute rate limit', async () => {
  const { saveSettings } = loadFixture();
  document.getElementById('embedding-recompute-rate-limit').value = '20';
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.embedding_recompute_rate_limit_per_second, 20);
});

test('saveSettings posts 0 for an unparseable embedding recompute rate limit field', async () => {
  const { saveSettings } = loadFixture();
  document.getElementById('embedding-recompute-rate-limit').value = '';
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.embedding_recompute_rate_limit_per_second, 0);
});

test('loadSettings applies the fetched settings on success', async () => {
  const { loadSettings } = loadFixture();
  document.getElementById('alpha').value = '0';
  await loadSettings();
  assert.equal(document.getElementById('alpha').value, '0.5');
});

test('loadSettings reports the error message on a failed fetch', async () => {
  const mod = loadFixture();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'settings down' });
  await mod.loadSettings();
  assert.equal(document.getElementById('settings-status').textContent.includes('settings down'), true);
});

test('saveSettings posts parsed tuning/operational values and re-applies the response', async () => {
  const { saveSettings } = loadFixture();
  document.getElementById('alpha').value = '0.6';
  document.getElementById('title-weight').value = '3';
  document.getElementById('ann-search-enabled').checked = false;
  document.getElementById('user-agent').value = 'custom-agent';
  let gotBody;
  global.fetch = async (url, opts) => {
    gotBody = JSON.parse(opts.body);
    const applied = JSON.parse(JSON.stringify(FULL_SETTINGS));
    applied.tuning.alpha = gotBody.tuning.alpha;
    return { ok: true, json: async () => applied };
  };
  await saveSettings();
  assert.equal(gotBody.tuning.alpha, 0.6);
  assert.equal(gotBody.operational.title_weight, 3);
  assert.equal(gotBody.operational.ann_search_enabled, false);
  assert.equal(gotBody.operational.user_agent, 'custom-agent');
  assert.equal(document.getElementById('alpha').value, '0.6');
});

test('applyOverrides fills blocked/boosted textareas from the response', () => {
  const { applyOverrides } = loadFixture();
  applyOverrides({
    blocked_terms: ['spam', 'scam'],
    blocked_domains: ['spammy.example'],
    boosted_terms: { official: 1.5 },
    boosted_domains: {},
  });
  assert.equal(document.getElementById('blocked-terms').value, 'spam\nscam');
  assert.equal(document.getElementById('blocked-domains').value, 'spammy.example');
  assert.equal(document.getElementById('boosted-terms').value, 'official 1.5');
  assert.equal(document.getElementById('boosted-domains').value, '');
});

test('loadOverrides applies the fetched overrides on success', async () => {
  const { loadOverrides } = loadFixture();
  global.fetch = async () => ({
    ok: true,
    json: async () => ({ blocked_terms: ['x'], blocked_domains: [], boosted_terms: {}, boosted_domains: {} }),
  });
  await loadOverrides();
  assert.equal(document.getElementById('blocked-terms').value, 'x');
});

test('loadOverrides reports the error message on a failed fetch', async () => {
  const { loadOverrides } = loadFixture();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'overrides down' });
  await loadOverrides();
  assert.equal(document.getElementById('settings-status').textContent.includes('overrides down'), true);
});

test('saveOverrides posts the parsed textarea contents', async () => {
  const { saveOverrides } = loadFixture();
  document.getElementById('blocked-terms').value = 'spam\nscam';
  document.getElementById('blocked-domains').value = 'spammy.example';
  document.getElementById('boosted-terms').value = 'official 1.5';
  document.getElementById('boosted-domains').value = '';
  let gotBody;
  global.fetch = async (url, opts) => {
    gotBody = JSON.parse(opts.body);
    return { ok: true, json: async () => gotBody };
  };
  await saveOverrides();
  assert.deepEqual(gotBody.blocked_terms, ['spam', 'scam']);
  assert.deepEqual(gotBody.blocked_domains, ['spammy.example']);
  assert.deepEqual(gotBody.boosted_terms, { official: 1.5 });
  assert.deepEqual(gotBody.boosted_domains, {});
});

test('factorsToText renders one "word factor" line per entry, empty for none', () => {
  const { factorsToText } = loadFixture();
  assert.equal(factorsToText({ official: 1.5, trusted: 2 }), 'official 1.5\ntrusted 2');
  assert.equal(factorsToText({}), '');
  assert.equal(factorsToText(undefined), '');
});

test('parseFactorLines parses "word factor" pairs and skips malformed lines', () => {
  const { parseFactorLines } = loadFixture();
  assert.deepEqual(
    parseFactorLines('official 1.5\nsingleword\nbad factor abc\ntrusted domain 2.0'),
    { official: 1.5, 'trusted domain': 2.0 },
  );
});

test('renderSettingsSummary fills the ranking/title-weight/ANN/fuzzy/session/crawl-default tiles', () => {
  const { renderSettingsSummary } = loadFixture();
  renderSettingsSummary(FULL_SETTINGS);
  assert.equal(document.getElementById('tile-ranking').textContent, '0.5 · 1.2 · 0.75');
  assert.equal(document.getElementById('tile-title-weight').textContent, '2×');
  assert.equal(document.getElementById('tile-ann').textContent, 'enabled');
  assert.equal(document.getElementById('tile-fuzzy').textContent, 'on, d≤2');
  assert.equal(document.getElementById('tile-session').textContent, '12h');
  assert.equal(document.getElementById('tile-crawl-default').textContent, '20 pages');
});

test('renderSettingsSummary shows ANN/fuzzy as off when disabled', () => {
  const { renderSettingsSummary } = loadFixture();
  const s = JSON.parse(JSON.stringify(FULL_SETTINGS));
  s.operational.ann_search_enabled = false;
  s.operational.fuzzy_match_enabled = false;
  renderSettingsSummary(s);
  assert.equal(document.getElementById('tile-ann').textContent, 'disabled');
  assert.equal(document.getElementById('tile-fuzzy').textContent, 'off');
});

test('renderOverridesSummary counts blocked/boosted terms and domains, pluralizing correctly', () => {
  const { renderOverridesSummary } = loadFixture();
  renderOverridesSummary({
    blocked_terms: ['spam'],
    blocked_domains: ['a.example', 'b.example'],
    boosted_terms: { official: 1.5 },
    boosted_domains: {},
  });
  assert.equal(document.getElementById('tile-blocked').textContent, '1 term · 2 domains');
  assert.equal(document.getElementById('tile-boosted').textContent, '1 term · 0 domains');
});

test('renderOverridesSummary treats a missing list/map as empty (0)', () => {
  const { renderOverridesSummary } = loadFixture();
  renderOverridesSummary({});
  assert.equal(document.getElementById('tile-blocked').textContent, '0 terms · 0 domains');
  assert.equal(document.getElementById('tile-boosted').textContent, '0 terms · 0 domains');
});

test('loading settings and overrides together populates every summary tile', async () => {
  const { loadSettings, loadOverrides } = loadFixture();
  await loadSettings();
  await loadOverrides();
  assert.equal(document.getElementById('tile-ranking').textContent, '0.5 · 1.2 · 0.75');
  assert.equal(document.getElementById('tile-blocked').textContent, '0 terms · 0 domains');
});

test('submitting the form saves both settings and overrides, reporting "Saved." on full success', async () => {
  const mod = loadFixture();
  let settingsPosted = false;
  let overridesPosted = false;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings') && opts.method === 'POST') {
      settingsPosted = true;
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    if (url.includes('/admin/api/overrides') && opts.method === 'POST') {
      overridesPosted = true;
      return { ok: true, json: async () => EMPTY_OVERRIDES };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('settings-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(settingsPosted, true);
  assert.equal(overridesPosted, true);
  assert.equal(document.getElementById('settings-status').textContent, 'Saved.');
  void mod;
});

test('a settings response carrying embedding_test_error reports "Saved, but..." instead of plain "Saved."', async () => {
  loadFixture();
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings') && opts.method === 'POST') {
      return { ok: true, json: async () => ({ ...FULL_SETTINGS, embedding_test_error: 'dial tcp: connection refused' }) };
    }
    if (url.includes('/admin/api/overrides') && opts.method === 'POST') {
      return { ok: true, json: async () => EMPTY_OVERRIDES };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('settings-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const text = document.getElementById('settings-status').textContent;
  assert.equal(text.includes('Saved, but'), true);
  assert.equal(text.includes('embedding provider test failed: dial tcp: connection refused'), true);
});

test('one endpoint failing does not stop the other from being tried, and both errors are reported', async () => {
  loadFixture();
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings') && opts.method === 'POST') {
      return { ok: false, status: 500, text: async () => 'settings write failed' };
    }
    if (url.includes('/admin/api/overrides') && opts.method === 'POST') {
      return { ok: false, status: 500, text: async () => 'overrides write failed' };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('settings-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const text = document.getElementById('settings-status').textContent;
  assert.equal(text.includes('settings: settings write failed'), true);
  assert.equal(text.includes('overrides: overrides write failed'), true);
});

test('a failing settings save still lets the overrides save succeed', async () => {
  loadFixture();
  let overridesPosted = false;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings') && opts.method === 'POST') {
      return { ok: false, status: 500, text: async () => 'settings write failed' };
    }
    if (url.includes('/admin/api/overrides') && opts.method === 'POST') {
      overridesPosted = true;
      return { ok: true, json: async () => EMPTY_OVERRIDES };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('settings-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(overridesPosted, true);
  const text = document.getElementById('settings-status').textContent;
  assert.equal(text.includes('settings: settings write failed'), true);
  assert.equal(text.includes('overrides:'), false);
});

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

test('renderEmbeddingRecomputeStatus shows "no recompute yet" when this instance has never run one', () => {
  const { renderEmbeddingRecomputeStatus } = loadFixture();
  renderEmbeddingRecomputeStatus(baseEmbeddingStatus());
  assert.equal(document.getElementById('embedding-recompute-status').textContent, 'No recompute has run yet on this instance.');
  assert.equal(document.getElementById('embedding-recompute-result').textContent, '');
});

test('renderEmbeddingRecomputeStatus renders the last run summary when one has run', () => {
  const { renderEmbeddingRecomputeStatus } = loadFixture();
  renderEmbeddingRecomputeStatus(baseEmbeddingStatus({
    last_run_at: '2026-01-02T03:04:05Z', documents: 8, failed: 2, duration_ms: 4200,
  }));
  const resultText = document.getElementById('embedding-recompute-result').textContent;
  assert.equal(resultText.includes('8'), true);
  assert.equal(resultText.includes('2'), true);
  assert.equal(resultText.includes('4200'), true);
});

test('renderEmbeddingRecomputeStatus shows "Recomputing…" and disables the button while in progress', () => {
  const { renderEmbeddingRecomputeStatus } = loadFixture();
  renderEmbeddingRecomputeStatus(baseEmbeddingStatus({ in_progress: true }));
  assert.equal(document.getElementById('embedding-recompute-status').textContent.includes('Recomputing…'), true);
  assert.equal(document.getElementById('embedding-recompute-btn').disabled, true);
});

test('renderEmbeddingRecomputeStatus schedules exactly one poll while in progress, and clears it once done', () => {
  const originalSetTimeout = global.setTimeout;
  const originalClearTimeout = global.clearTimeout;
  const scheduled = [];
  let cleared = 0;
  global.setTimeout = (fn, ms) => { scheduled.push(ms); return 'fake-timer'; };
  global.clearTimeout = () => { cleared++; };
  try {
    const { renderEmbeddingRecomputeStatus } = loadFixture();
    renderEmbeddingRecomputeStatus(baseEmbeddingStatus({ in_progress: true }));
    renderEmbeddingRecomputeStatus(baseEmbeddingStatus({ in_progress: true }));
    assert.deepEqual(scheduled, [2000]);
    renderEmbeddingRecomputeStatus(baseEmbeddingStatus({ in_progress: false }));
    assert.equal(cleared, 1);
  } finally {
    global.setTimeout = originalSetTimeout;
    global.clearTimeout = originalClearTimeout;
  }
});

test('clicking Recompute embeddings posts a start request and then polls for status', async () => {
  let postedURL = null;
  loadFixture();
  // Let the module's own load-time loadEmbeddingRecomputeStatus() settle
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
  document.getElementById('embedding-recompute-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(postedURL, '/admin/api/embeddings/recompute');
  assert.equal(document.getElementById('embedding-recompute-result').textContent.includes('5'), true);
  assert.equal(document.getElementById('embedding-recompute-btn').disabled, false);
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
  document.getElementById('embedding-recompute-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(
    document.getElementById('embedding-recompute-status').textContent,
    'Could not start recompute: an embedding recompute is already in progress',
  );
  assert.equal(document.getElementById('embedding-recompute-btn').disabled, false);
});

test('loadEmbeddingModels does not fetch when the provider is not http', async () => {
  let called = false;
  const { loadEmbeddingModels } = loadFixture();
  global.fetch = async () => { called = true; return { ok: true, json: async () => ({ models: ['x'] }) }; };
  await loadEmbeddingModels({ operational: { embedding_provider: 'hash', embedding_http_base_url: 'https://example.com' } });
  assert.equal(called, false);
  assert.equal(document.getElementById('embedding-http-model-options').children.length, 0);
});

test('loadEmbeddingModels does not fetch when the base URL is blank', async () => {
  let called = false;
  const { loadEmbeddingModels } = loadFixture();
  global.fetch = async () => { called = true; return { ok: true, json: async () => ({ models: ['x'] }) }; };
  await loadEmbeddingModels({ operational: { embedding_provider: 'http', embedding_http_base_url: '' } });
  assert.equal(called, false);
});

test('loadEmbeddingModels populates the datalist and hint on success', async () => {
  const { loadEmbeddingModels } = loadFixture();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') {
      return { ok: true, json: async () => ({ models: ['intfloat/e5-large-v2', 'Qwen/Qwen3-VL-Embedding-8B'] }) };
    }
    return { ok: true, json: async () => ({}) };
  };
  await loadEmbeddingModels({ operational: { embedding_provider: 'http', embedding_http_base_url: 'https://example.com/v1' } });
  const options = Array.from(document.getElementById('embedding-http-model-options').children).map((o) => o.value);
  assert.deepEqual(options, ['intfloat/e5-large-v2', 'Qwen/Qwen3-VL-Embedding-8B']);
  assert.equal(document.getElementById('embedding-http-model-hint').textContent, '2 model(s) available from this endpoint.');
});

test('loadEmbeddingModels clears stale options before repopulating', async () => {
  const { loadEmbeddingModels } = loadFixture();
  const optionsEl = document.getElementById('embedding-http-model-options');
  const stale = document.createElement('option');
  stale.value = 'stale-model';
  optionsEl.appendChild(stale);
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') return { ok: true, json: async () => ({ models: ['fresh-model'] }) };
    return { ok: true, json: async () => ({}) };
  };
  await loadEmbeddingModels({ operational: { embedding_provider: 'http', embedding_http_base_url: 'https://example.com/v1' } });
  const options = Array.from(optionsEl.children).map((o) => o.value);
  assert.deepEqual(options, ['fresh-model']);
});

test('loadEmbeddingModels shows the error hint on a soft failure from the server', async () => {
  const { loadEmbeddingModels } = loadFixture();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') return { ok: true, json: async () => ({ error: '401 unauthorized' }) };
    return { ok: true, json: async () => ({}) };
  };
  await loadEmbeddingModels({ operational: { embedding_provider: 'http', embedding_http_base_url: 'https://example.com/v1' } });
  assert.equal(document.getElementById('embedding-http-model-hint').textContent, 'Could not list models: 401 unauthorized');
  assert.equal(document.getElementById('embedding-http-model-options').children.length, 0);
});

test('loadEmbeddingModels shows the error hint on a network failure', async () => {
  const { loadEmbeddingModels } = loadFixture();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') throw new Error('network down');
    return { ok: true, json: async () => ({}) };
  };
  await loadEmbeddingModels({ operational: { embedding_provider: 'http', embedding_http_base_url: 'https://example.com/v1' } });
  assert.equal(document.getElementById('embedding-http-model-hint').textContent, 'Could not list models: network down');
});
