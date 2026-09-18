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
    max_concurrent_crawls: 3,
    default_renderer: 'none',
    link_scope: 'domain',
    url_alias_www_enabled: true,
    content_dedup_enabled: false,
    content_dedup_method: 'exact',
    content_dedup_simhash_max_distance: 3,
    content_dedup_interval_minutes: 120,
    default_top_k: 10,
    semantic_candidate_pool_size: 200,
    ann_search_enabled: true,
    embedding_hash_enabled: true,
    embedding_search_weights: { hash: 1 },
    embedding_title_weight: 0.3,
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

const CHAT_ENDPOINT = {
  enabled: true,
  base_url: 'http://localhost:8000/v1',
  model: 'llama-3',
  has_api_key: true,
  rag_enabled: true,
  rag_result_count: 5,
};

function loadFixture(endpoints, chatEndpoint) {
  setupDOM(SETTINGS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async (url) => {
    if (url.includes('/admin/api/settings')) return { ok: true, json: async () => FULL_SETTINGS };
    if (url.includes('/admin/api/overrides')) return { ok: true, json: async () => EMPTY_OVERRIDES };
    if (url.includes('/admin/api/embeddings/endpoints')) return { ok: true, json: async () => endpoints || [] };
    if (url.includes('/admin/api/chat-endpoint')) return { ok: true, json: async () => chatEndpoint || CHAT_ENDPOINT };
    return { ok: true, json: async () => ({}) };
  };
  return requireFresh('./admin_settings.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  // applySettings kicks off loadEmbeddingSearchWeights as fire-and-forget
  // (it doesn't block rendering the rest of the form on that fetch) --
  // flush before tearing down so that pending promise settles against
  // *this* test's own fetch mock/DOM, rather than rejecting asynchronously
  // once the next test has already replaced both.
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('applySettings fills every field, applying the renderer/link-scope fallbacks', () => {
  const { applySettings } = loadFixture();
  applySettings(FULL_SETTINGS);
  assert.equal(document.getElementById('alpha').value, '0.5');
  assert.equal(document.getElementById('title-weight').value, '2');
  assert.equal(document.getElementById('embedding-title-weight').value, '0.3');
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

test('applySettings checks the hash-enabled box and populates the title weight', async () => {
  const { applySettings } = loadFixture();
  applySettings(FULL_SETTINGS);
  await flush();
  assert.equal(document.getElementById('embedding-hash-enabled').checked, true);
  assert.equal(document.getElementById('embedding-title-weight').value, '0.3');
});

// applySettings shows 0 in the title-weight field, not a blank -- 0 here is
// a real, meaningful "disabled" value (see the field's own doc comment in
// admin.go) that must round-trip visibly.
test('applySettings shows a zero embedding title weight as "0", not blank', () => {
  const { applySettings } = loadFixture();
  const s = JSON.parse(JSON.stringify(FULL_SETTINGS));
  s.operational.embedding_title_weight = 0;
  applySettings(s);
  assert.equal(document.getElementById('embedding-title-weight').value, '0');
});

test('saveSettings posts the configured embedding title weight', async () => {
  const { saveSettings } = loadFixture();
  await flush();
  document.getElementById('embedding-title-weight').value = '0.6';
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.embedding_title_weight, 0.6);
});

test('saveSettings posts 0 for an unparseable embedding title weight field', async () => {
  const { saveSettings } = loadFixture();
  await flush();
  document.getElementById('embedding-title-weight').value = '';
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.embedding_title_weight, 0);
});

test('saveSettings posts the checked state of the hash-enabled checkbox and the collected weights', async () => {
  const { saveSettings, weightInputID } = loadFixture([{ id: 'ionos', name: 'IONOS', enabled: true }]);
  await flush();
  document.getElementById('embedding-hash-enabled').checked = false;
  document.getElementById(weightInputID('hash')).value = '0';
  document.getElementById(weightInputID('ionos')).value = '2.5';
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.embedding_hash_enabled, false);
  assert.deepEqual(gotBody.operational.embedding_search_weights, { ionos: 2.5 });
});

test('loadEmbeddingSearchWeights offers a hash weight input when no endpoints are configured', async () => {
  const { loadEmbeddingSearchWeights } = loadFixture();
  await flush();
  await loadEmbeddingSearchWeights({ hash: 1 });
  const inputs = Array.from(document.querySelectorAll('#embedding-search-weights input[data-provider]'));
  assert.deepEqual(inputs.map((i) => i.dataset.provider), ['hash']);
  assert.equal(inputs[0].value, '1');
});

test('loadEmbeddingSearchWeights lists a weight input for every enabled endpoint, not disabled ones', async () => {
  const { loadEmbeddingSearchWeights } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/admin/api/embeddings/endpoints')) {
      return {
        ok: true,
        json: async () => [
          { id: 'ionos', name: 'IONOS bge-m3', enabled: true },
          { id: 'local', name: 'Local Ollama', enabled: false },
        ],
      };
    }
    return { ok: true, json: async () => ({}) };
  };
  await loadEmbeddingSearchWeights({ hash: 1, ionos: 0.5 });
  const inputs = Array.from(document.querySelectorAll('#embedding-search-weights input[data-provider]'));
  assert.deepEqual(inputs.map((i) => i.dataset.provider), ['hash', 'ionos']);
  assert.equal(inputs[0].value, '1');
  assert.equal(inputs[1].value, '0.5');
});

test('loadEmbeddingSearchWeights defaults an unweighted enabled endpoint to 0', async () => {
  const { loadEmbeddingSearchWeights, weightInputID } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/admin/api/embeddings/endpoints')) {
      return { ok: true, json: async () => [{ id: 'ionos', name: 'IONOS', enabled: true }] };
    }
    return { ok: true, json: async () => ({}) };
  };
  await loadEmbeddingSearchWeights({ hash: 1 });
  assert.equal(document.getElementById(weightInputID('ionos')).value, '0');
});

// A weight naming a provider that's since been disabled or deleted is shown
// anyway as a clearly-labeled, disabled (locked) input, so the form doesn't
// silently drop it out from under an admin who hasn't saved yet -- the next
// save still resolves this server-side via domain.ReconcileSearchWeights,
// and collectEmbeddingSearchWeights never resubmits a disabled input.
test('loadEmbeddingSearchWeights keeps a no-longer-enabled provider visible as a locked, stale input', async () => {
  const { loadEmbeddingSearchWeights, collectEmbeddingSearchWeights, weightInputID } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/admin/api/embeddings/endpoints')) return { ok: true, json: async () => [] };
    return { ok: true, json: async () => ({}) };
  };
  await loadEmbeddingSearchWeights({ hash: 1, gone: 0.7 });
  const staleInput = document.getElementById(weightInputID('gone'));
  assert.equal(staleInput.value, '0.7');
  assert.equal(staleInput.disabled, true);
  assert.equal(staleInput.closest('.form-row').textContent.includes('not currently enabled'), true);
  // The stale, disabled "gone" entry is never resubmitted on save; the
  // always-offered, still-enabled "hash" input still is.
  assert.deepEqual(collectEmbeddingSearchWeights(), { hash: 1 });
});

test('loadEmbeddingSearchWeights falls back to hash-only when the endpoints fetch fails', async () => {
  const { loadEmbeddingSearchWeights } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/admin/api/embeddings/endpoints')) throw new Error('network down');
    return { ok: true, json: async () => ({}) };
  };
  await loadEmbeddingSearchWeights({ hash: 1 });
  const inputs = Array.from(document.querySelectorAll('#embedding-search-weights input[data-provider]'));
  assert.deepEqual(inputs.map((i) => i.dataset.provider), ['hash']);
});

test('collectEmbeddingSearchWeights omits weights left at or parsed as 0 or below', async () => {
  const { loadEmbeddingSearchWeights, collectEmbeddingSearchWeights, weightInputID } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/admin/api/embeddings/endpoints')) {
      return { ok: true, json: async () => [{ id: 'ionos', name: 'IONOS', enabled: true }] };
    }
    return { ok: true, json: async () => ({}) };
  };
  await loadEmbeddingSearchWeights({ hash: 0, ionos: 1.5 });
  document.getElementById(weightInputID('hash')).value = '-1';
  assert.deepEqual(collectEmbeddingSearchWeights(), { ionos: 1.5 });
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

test('applySettings checks the url-alias-www-enabled box', () => {
  const { applySettings } = loadFixture();
  applySettings(FULL_SETTINGS);
  assert.equal(document.getElementById('url-alias-www-enabled').checked, true);
});

test('saveSettings posts an unchecked url-alias-www-enabled box as false', async () => {
  const { saveSettings } = loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('url-alias-www-enabled').checked = false;
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.url_alias_www_enabled, false);
});

test('applySettings fills the content-dedup fields from the response', () => {
  const { applySettings } = loadFixture();
  applySettings(FULL_SETTINGS);
  assert.equal(document.getElementById('content-dedup-enabled').checked, false);
  assert.equal(document.getElementById('content-dedup-method').value, 'exact');
  assert.equal(document.getElementById('content-dedup-simhash-max-distance').value, '3');
  assert.equal(document.getElementById('content-dedup-interval').value, '120');
});

test('saveSettings posts the edited content-dedup fields', async () => {
  const { saveSettings } = loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('content-dedup-enabled').checked = true;
  document.getElementById('content-dedup-method').value = 'simhash';
  document.getElementById('content-dedup-simhash-max-distance').value = '5';
  document.getElementById('content-dedup-interval').value = '90';
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.content_dedup_enabled, true);
  assert.equal(gotBody.operational.content_dedup_method, 'simhash');
  assert.equal(gotBody.operational.content_dedup_simhash_max_distance, 5);
  assert.equal(gotBody.operational.content_dedup_interval_minutes, 90);
});

test('applySettings fills the concurrent-crawls field', () => {
  const { applySettings } = loadFixture();
  applySettings(FULL_SETTINGS);
  assert.equal(document.getElementById('max-concurrent-crawls').value, '3');
});

test('saveSettings posts the edited concurrent-crawls field', async () => {
  const { saveSettings } = loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('max-concurrent-crawls').value = '8';
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/settings')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => FULL_SETTINGS };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveSettings();
  assert.equal(gotBody.operational.max_concurrent_crawls, 8);
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
    return { ok: true, json: async () => [] };
  };
  document.getElementById('settings-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(settingsPosted, true);
  assert.equal(overridesPosted, true);
  assert.equal(document.getElementById('settings-status').textContent, 'Saved.');
  void mod;
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
    return { ok: true, json: async () => [] };
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
    return { ok: true, json: async () => [] };
  };
  document.getElementById('settings-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(overridesPosted, true);
  const text = document.getElementById('settings-status').textContent;
  assert.equal(text.includes('settings: settings write failed'), true);
  assert.equal(text.includes('overrides:'), false);
});

test('loadChatEndpoint populates every chat field from the GET response', async () => {
  const { loadChatEndpoint } = loadFixture();
  await loadChatEndpoint();
  assert.equal(document.getElementById('chat-enabled').checked, true);
  assert.equal(document.getElementById('chat-base-url').value, 'http://localhost:8000/v1');
  assert.equal(document.getElementById('chat-model').value, 'llama-3');
  assert.equal(document.getElementById('chat-rag-enabled').checked, true);
  assert.equal(document.getElementById('chat-rag-result-count').value, '5');
});

test('loadChatEndpoint leaves the API key field blank even when has_api_key is true, and enables the clear checkbox', async () => {
  const { loadChatEndpoint } = loadFixture();
  await loadChatEndpoint();
  assert.equal(document.getElementById('chat-api-key').value, '');
  assert.equal(document.getElementById('chat-api-key').placeholder, 'Leave blank to keep the current key');
  assert.equal(document.getElementById('chat-clear-api-key').disabled, false);
});

test('loadChatEndpoint disables the clear-key checkbox and blanks the placeholder when no key is stored', async () => {
  const { loadChatEndpoint } = loadFixture(null, {
    enabled: false, base_url: '', model: '', has_api_key: false, rag_enabled: false, rag_result_count: 5,
  });
  await loadChatEndpoint();
  assert.equal(document.getElementById('chat-api-key').placeholder, '');
  assert.equal(document.getElementById('chat-clear-api-key').disabled, true);
  assert.equal(document.getElementById('chat-rag-enabled').checked, false);
});

test('loadChatEndpoint reports the error message on a failed fetch', async () => {
  const { loadChatEndpoint } = loadFixture();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'chat endpoint down' });
  await loadChatEndpoint();
  assert.equal(document.getElementById('chat-settings-status').textContent.includes('chat endpoint down'), true);
});

test('saveChatEndpoint PATCHes the entered fields and reports "Saved." on success', async () => {
  const { saveChatEndpoint } = loadFixture();
  await flush();
  document.getElementById('chat-enabled').checked = true;
  document.getElementById('chat-base-url').value = 'http://localhost:9000/v1';
  document.getElementById('chat-model').value = 'gpt-oss';
  document.getElementById('chat-api-key').value = 'sk-new-key';
  document.getElementById('chat-rag-enabled').checked = false;
  document.getElementById('chat-rag-result-count').value = '8';
  let gotURL;
  let gotBody;
  let gotMethod;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/chat-endpoint')) {
      if (opts && opts.method === 'PATCH') {
        gotURL = url;
        gotMethod = opts.method;
        gotBody = JSON.parse(opts.body);
      }
      return { ok: true, json: async () => CHAT_ENDPOINT };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveChatEndpoint();
  assert.equal(gotURL, '/admin/api/chat-endpoint');
  assert.equal(gotMethod, 'PATCH');
  assert.equal(gotBody.base_url, 'http://localhost:9000/v1');
  assert.equal(gotBody.model, 'gpt-oss');
  assert.equal(gotBody.api_key, 'sk-new-key');
  assert.equal(gotBody.enabled, true);
  assert.equal(gotBody.rag_enabled, false);
  assert.equal(gotBody.rag_result_count, 8);
  assert.equal(gotBody.clear_api_key, false);
  assert.equal(document.getElementById('chat-settings-status').textContent, 'Saved.');
});

test('saveChatEndpoint sends clear_api_key when the clear checkbox is checked', async () => {
  const { saveChatEndpoint } = loadFixture();
  await flush();
  document.getElementById('chat-clear-api-key').checked = true;
  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/chat-endpoint')) {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => CHAT_ENDPOINT };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveChatEndpoint();
  assert.equal(gotBody.clear_api_key, true);
});

test('saveChatEndpoint surfaces a server error and leaves the loading state cleared', async () => {
  const { saveChatEndpoint } = loadFixture();
  await flush();
  global.fetch = async (url) => {
    if (url.includes('/admin/api/chat-endpoint')) {
      return { ok: false, status: 500, text: async () => 'chat endpoint save failed' };
    }
    return { ok: true, json: async () => ({}) };
  };
  await saveChatEndpoint();
  const status = document.getElementById('chat-settings-status');
  assert.equal(status.textContent.includes('chat endpoint save failed'), true);
  assert.equal(document.getElementById('save-chat-settings-btn').disabled, false);
});

test('clicking "Save chat settings" invokes saveChatEndpoint', async () => {
  loadFixture();
  await flush();
  let posted = false;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/chat-endpoint') && opts.method === 'PATCH') {
      posted = true;
      return { ok: true, json: async () => CHAT_ENDPOINT };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('save-chat-settings-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(posted, true);
});
