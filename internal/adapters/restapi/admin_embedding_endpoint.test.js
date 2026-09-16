'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('jsdom');
const { teardownDOM, requireFresh } = require('./dom_helper.test_util');

const ENDPOINT_HTML = fs.readFileSync(path.join(__dirname, 'admin_embedding_endpoint.html'), 'utf8');

// admin_embedding_endpoint.js reads its endpoint id (or the literal "new")
// from window.location.pathname at load time, so it needs a jsdom instance
// constructed with a specific URL, the same reasoning as admin_schedule.test.js.
function setupEndpointDOM(id) {
  const dom = new JSDOM(ENDPOINT_HTML, { url: 'http://localhost/admin/embeddings/endpoint/' + encodeURIComponent(id) });
  global.window = dom.window;
  global.document = dom.window.document;
  Object.defineProperty(global, 'navigator', {
    value: dom.window.navigator, configurable: true, writable: true,
  });
  return dom;
}

function baseEndpoint(overrides) {
  return Object.assign({
    id: 'ionos_bge_m3',
    name: 'IONOS bge-m3',
    base_url: 'https://openai.inference.de-txl.ionos.com/v1',
    has_api_key: true,
    model: 'BAAI/bge-m3',
    dimensions: 1024,
    rate_limit_per_second: 5,
    enabled: true,
    created_at: '2026-01-02T03:04:05Z',
  }, overrides);
}

function loadFixture(id, fetchImpl) {
  setupEndpointDOM(id || 'ionos_bge_m3');
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => baseEndpoint() }));
  return requireFresh('./admin_embedding_endpoint.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('load() applies the fetched endpoint to the form and reveals it', async () => {
  loadFixture('ionos_bge_m3', async (url) => {
    assert.equal(url, '/admin/api/embeddings/endpoints/ionos_bge_m3');
    return { ok: true, json: async () => baseEndpoint() };
  });
  await flush();
  assert.equal(document.getElementById('endpoint-title').textContent, 'IONOS bge-m3');
  assert.equal(document.getElementById('endpoint-form').hidden, false);
  assert.equal(document.getElementById('endpoint-name').value, 'IONOS bge-m3');
  assert.equal(document.getElementById('endpoint-base-url').value, 'https://openai.inference.de-txl.ionos.com/v1');
  assert.equal(document.getElementById('endpoint-model').value, 'BAAI/bge-m3');
  assert.equal(document.getElementById('endpoint-dimensions').value, '1024');
  assert.equal(document.getElementById('endpoint-rate-limit').value, '5');
  assert.equal(document.getElementById('endpoint-enabled').checked, true);
  assert.equal(document.getElementById('endpoint-delete-btn').hidden, false);
  const meta = document.getElementById('endpoint-meta').textContent;
  assert.equal(meta.includes('ionos_bge_m3'), true);
});

// The server never echoes a stored API key's real value back (see
// embeddingEndpointResponse) -- these tests prove the form reflects that.
test('load() never populates the API key field, even when one is set', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  const apiKeyEl = document.getElementById('endpoint-api-key');
  assert.equal(apiKeyEl.value, '');
  assert.equal(apiKeyEl.placeholder, '(unchanged — a key is already set)');
  const clearAPIKey = document.getElementById('endpoint-clear-api-key');
  assert.equal(clearAPIKey.checked, false);
  assert.equal(clearAPIKey.disabled, false);
});

test('load() disables the "remove" checkbox when no key is set', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint({ has_api_key: false }) }));
  await flush();
  assert.equal(document.getElementById('endpoint-api-key').placeholder, '');
  assert.equal(document.getElementById('endpoint-clear-api-key').disabled, true);
});

test('requestBody sends a blank api_key and clear_api_key=false by default', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  const { requestBody } = require('./admin_embedding_endpoint.js');
  const body = requestBody();
  assert.equal(body.api_key, '');
  assert.equal(body.clear_api_key, false);
  assert.equal(body.name, 'IONOS bge-m3');
  assert.equal(body.dimensions, 1024);
  assert.equal(body.rate_limit_per_second, 5);
});

test('requestBody reflects a newly typed key and a checked "remove" box', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  document.getElementById('endpoint-api-key').value = 'sk-new';
  document.getElementById('endpoint-clear-api-key').checked = true;
  const { requestBody } = require('./admin_embedding_endpoint.js');
  const body = requestBody();
  assert.equal(body.api_key, 'sk-new');
  assert.equal(body.clear_api_key, true);
});

test('candidateBody reflects the form fields relevant to a connectivity/models probe', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  document.getElementById('endpoint-base-url').value = 'https://new.example/v1';
  document.getElementById('endpoint-model').value = 'new-model';
  const { candidateBody } = require('./admin_embedding_endpoint.js');
  const body = candidateBody();
  assert.equal(body.base_url, 'https://new.example/v1');
  assert.equal(body.model, 'new-model');
  assert.equal(body.dimensions, 1024);
});

// candidateBody includes id in edit mode -- see resolveCandidateAPIKey in
// admin.go, which uses it to fall back to the real stored API key when
// api_key is left blank, since this form never re-populates that field with
// an already-saved endpoint's actual value.
test('candidateBody includes the endpoint id in edit mode', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  const { candidateBody } = require('./admin_embedding_endpoint.js');
  assert.equal(candidateBody().id, 'ionos_bge_m3');
});

test('candidateBody sends a blank id in "new" mode', async () => {
  loadFixture('new');
  await flush();
  const { candidateBody } = require('./admin_embedding_endpoint.js');
  assert.equal(candidateBody().id, '');
});

test('load() shows "Not found" and the error message on failure', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: false, status: 404, text: async () => 'no such endpoint' }));
  await flush();
  assert.equal(document.getElementById('endpoint-title').textContent, 'Not found');
  assert.equal(document.getElementById('endpoint-status').textContent.includes('no such endpoint'), true);
  assert.equal(document.getElementById('endpoint-form').hidden, true);
});

test('"new" mode shows an empty, enabled form without loading or fetching', async () => {
  let fetched = false;
  loadFixture('new', async () => { fetched = true; return { ok: true, json: async () => baseEndpoint() }; });
  await flush();
  assert.equal(fetched, false);
  assert.equal(document.getElementById('endpoint-title').textContent, 'Add endpoint');
  assert.equal(document.getElementById('endpoint-form').hidden, false);
  assert.equal(document.getElementById('endpoint-enabled').checked, true);
  assert.equal(document.getElementById('endpoint-delete-btn').hidden, true);
});

test('submitting in "new" mode posts to the collection endpoint and redirects to the created id', async () => {
  let postedURL = null;
  let postedBody = null;
  loadFixture('new');
  await flush();
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') {
      postedURL = url;
      postedBody = JSON.parse(opts.body);
      return { ok: true, json: async () => baseEndpoint({ id: 'new_endpoint' }) };
    }
    return { ok: true, json: async () => baseEndpoint() };
  };
  document.getElementById('endpoint-name').value = 'New Endpoint';
  document.getElementById('endpoint-base-url').value = 'https://new.example/v1';
  document.getElementById('endpoint-dimensions').value = '4';
  document.getElementById('endpoint-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(postedURL, '/admin/api/embeddings/endpoints');
  assert.equal(postedBody.name, 'New Endpoint');
});

test('submitting in edit mode patches the specific endpoint', async () => {
  let patchedURL = null;
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'PATCH') {
      patchedURL = url;
      return { ok: true, json: async () => baseEndpoint() };
    }
    return { ok: true, json: async () => baseEndpoint() };
  };
  document.getElementById('endpoint-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(patchedURL, '/admin/api/embeddings/endpoints/ionos_bge_m3');
  assert.equal(document.getElementById('endpoint-status').textContent, 'Saved.');
});

test('submitting reports the error message on failure', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  global.fetch = async () => ({ ok: false, status: 400, text: async () => 'dimensions must be positive' });
  document.getElementById('endpoint-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(document.getElementById('endpoint-status').textContent, 'Could not save: dimensions must be positive');
});

// Each model renders as a plain .list-item, not a kvRow -- a repeated
// "Model" key column next to every entry would read as a redundant table
// for what is really just a flat list of names.
test('clicking "List available models" renders each returned model as a plain list item', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') {
      return { ok: true, json: async () => ({ models: ['BAAI/bge-m3', 'text-embedding-3-small'] }) };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-list-models-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(document.getElementById('endpoint-models-status').textContent, '2 model(s) available from this endpoint.');
  const resultEl = document.getElementById('endpoint-models-result');
  const items = Array.from(resultEl.querySelectorAll('.list-item'));
  assert.deepEqual(items.map((i) => i.textContent), ['BAAI/bge-m3', 'text-embedding-3-small']);
  assert.equal(resultEl.querySelectorAll('.kv-row').length, 0);
});

// The endpoint edit page never re-populates the API key field with an
// already-saved endpoint's real value, so testing/listing models without
// retyping it must still send the request -- the server (resolveCandidateAPIKey)
// is what falls back to the real stored key via the id these requests carry.
test('clicking "List available models" sends the endpoint id alongside a blank api_key', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  let posted;
  global.fetch = async (url, opts) => {
    if (url === '/admin/api/embeddings/models') {
      posted = JSON.parse(opts.body);
      return { ok: true, json: async () => ({ models: [] }) };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-list-models-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(posted.id, 'ionos_bge_m3');
  assert.equal(posted.api_key, '');
});

test('clicking "List available models" shows the endpoint-reported error', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') return { ok: true, json: async () => ({ error: 'unauthorized' }) };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-list-models-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(document.getElementById('endpoint-models-status').textContent, 'Could not list models: unauthorized');
});

test('clicking "List available models" shows the empty message when none are reported', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') return { ok: true, json: async () => ({ models: [] }) };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-list-models-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(
    document.getElementById('endpoint-models-status').textContent.includes('No models reported'),
    true,
  );
});

test('clicking "List available models" reports a network/fetch failure', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/models') return { ok: false, status: 500, text: async () => 'internal error' };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-list-models-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(document.getElementById('endpoint-models-status').textContent.includes('internal error'), true);
});

test('clicking "Test connection" reports a network/fetch failure', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/test') return { ok: false, status: 500, text: async () => 'internal error' };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-test-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(document.getElementById('endpoint-test-status').textContent.includes('internal error'), true);
});

test('clicking "Test connection" reports success', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/test') return { ok: true, json: async () => ({}) };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-test-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(document.getElementById('endpoint-test-status').textContent, 'Connection succeeded.');
});

test('clicking "Test connection" sends the endpoint id alongside a blank api_key', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  let posted;
  global.fetch = async (url, opts) => {
    if (url === '/admin/api/embeddings/test') {
      posted = JSON.parse(opts.body);
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-test-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(posted.id, 'ionos_bge_m3');
  assert.equal(posted.api_key, '');
});

test('clicking "Test connection" reports a failure', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  global.fetch = async (url) => {
    if (url === '/admin/api/embeddings/test') return { ok: true, json: async () => ({ error: 'connection refused' }) };
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-test-btn').dispatchEvent(new window.Event('click'));
  await flush();
  await flush();
  assert.equal(document.getElementById('endpoint-test-status').textContent, 'Connection failed: connection refused');
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('endpoint-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

// The success path's window.location.href assignment prints a harmless
// "Not implemented: navigation" jsdom console error, same limitation noted
// in admin_schedule.test.js's identical delete test.
test('clicking Delete removes the endpoint when confirmed', async () => {
  let deletedURL = null;
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  window.confirm = () => true;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') {
      deletedURL = url;
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => baseEndpoint() };
  };
  document.getElementById('endpoint-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deletedURL, '/admin/api/embeddings/endpoints/ionos_bge_m3');
});

test('clicking Delete re-enables the button and alerts on failure', async () => {
  loadFixture('ionos_bge_m3', async () => ({ ok: true, json: async () => baseEndpoint() }));
  await flush();
  window.confirm = () => true;
  let alertMsg = null;
  window.alert = (msg) => { alertMsg = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'in use' };
    return { ok: true, json: async () => baseEndpoint() };
  };
  document.getElementById('endpoint-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(document.getElementById('endpoint-delete-btn').disabled, false);
  assert.equal(alertMsg, 'Could not delete: in use');
});
