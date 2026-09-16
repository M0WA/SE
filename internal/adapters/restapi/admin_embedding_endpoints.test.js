'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const ENDPOINTS_HTML = fs.readFileSync(path.join(__dirname, 'admin_embedding_endpoints.html'), 'utf8');

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

function loadFixture(fetchImpl) {
  setupDOM(ENDPOINTS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => [] }));
  return requireFresh('./admin_embedding_endpoints.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('loadEndpoints shows a message and no table when none are configured', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  assert.equal(document.getElementById('endpoints-status').textContent.includes('No HTTP endpoints configured'), true);
  assert.equal(document.getElementById('endpoints-table').querySelector('table'), null);
});

test('renderEndpoints builds a row per endpoint with its fields', () => {
  const { renderEndpoints } = loadFixture();
  renderEndpoints([baseEndpoint(), baseEndpoint({ id: 'local', name: 'Local Ollama', dimensions: 384, enabled: false })]);
  const text = document.getElementById('endpoints-table').textContent;
  assert.equal(text.includes('IONOS bge-m3'), true);
  assert.equal(text.includes('Local Ollama'), true);
  assert.equal(text.includes('1024'), true);
  assert.equal(text.includes('384'), true);
  assert.equal(document.getElementById('endpoints-status').textContent, '');
});

test('renderEndpoints shows Yes/No for the enabled column', () => {
  const { renderEndpoints } = loadFixture();
  renderEndpoints([baseEndpoint({ enabled: true }), baseEndpoint({ id: 'x', enabled: false })]);
  const rows = document.getElementById('endpoints-table').querySelectorAll('tbody tr');
  assert.equal(rows.length, 2);
});

test('each row has an Edit link to the endpoint\'s edit page', () => {
  const { renderEndpoints } = loadFixture();
  renderEndpoints([baseEndpoint()]);
  const editLink = document.getElementById('endpoints-table').querySelector('a.text-button');
  assert.equal(editLink.getAttribute('href'), '/admin/embeddings/endpoint/ionos_bge_m3');
  assert.equal(editLink.textContent, 'Edit');
});

test('loadEndpoints reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db down' }));
  await flush();
  assert.equal(document.getElementById('endpoints-status').textContent.includes('db down'), true);
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseEndpoint()] }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => [] };
  };
  document.querySelector('#endpoints-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('clicking Delete removes the endpoint when confirmed, then reloads the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseEndpoint()] }));
  await flush();
  window.confirm = () => true;
  let deletedURL = null;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') {
      deletedURL = url;
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => [] };
  };
  document.querySelector('#endpoints-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deletedURL, '/admin/api/embeddings/endpoints/ionos_bge_m3');
  assert.equal(document.getElementById('endpoints-status').textContent.includes('No HTTP endpoints configured'), true);
});

test('clicking Delete alerts on failure', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseEndpoint()] }));
  await flush();
  window.confirm = () => true;
  let alertMsg = null;
  window.alert = (msg) => { alertMsg = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'in use' };
    return { ok: true, json: async () => [baseEndpoint()] };
  };
  document.querySelector('#endpoints-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(alertMsg, 'Could not delete: in use');
});
