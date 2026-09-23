'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('jsdom');
const { teardownDOM, requireFresh } = require('./dom_helper.test_util');

const SERVER_HTML = fs.readFileSync(path.join(__dirname, 'account_mcp_server.html'), 'utf8');

// Reads server id (or "new") from window.location.pathname at load time, same as
// admin_mcp_server.test.js's setupServerDOM.
function setupServerDOM(id) {
  const dom = new JSDOM(SERVER_HTML, { url: 'http://localhost/account/mcp-servers/' + encodeURIComponent(id) });
  global.window = dom.window;
  global.document = dom.window.document;
  Object.defineProperty(global, 'navigator', {
    value: dom.window.navigator, configurable: true, writable: true,
  });
  return dom;
}

function baseServer(overrides) {
  return Object.assign({
    id: 'my_notes',
    name: 'my notes',
    transport: 'http',
    command: '',
    args: [],
    base_url: 'https://example.com/mcp',
    has_api_key: false,
    enabled: true,
    prompt: '',
    gated_by_web_search: false,
  }, overrides);
}

// Doesn't load admin.js (see account_mcp_server.js) -- unlike admin_mcp_server.test.js's fixture,
// no admin.js global or nav rail to assert on.
function loadFixture(id, fetchImpl) {
  setupServerDOM(id || 'my_notes');
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => baseServer() }));
  return requireFresh('./account_mcp_server.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('load() applies the fetched server to the form and reveals it', async () => {
  loadFixture('my_notes', async (url) => {
    assert.equal(url, '/account/api/mcp-servers/my_notes');
    return { ok: true, json: async () => baseServer({ prompt: 'Be terse.', gated_by_web_search: true }) };
  });
  await flush();
  assert.equal(document.getElementById('server-title').textContent, 'my notes');
  assert.equal(document.getElementById('server-form').hidden, false);
  assert.equal(document.getElementById('server-name').value, 'my notes');
  assert.equal(document.getElementById('server-base-url').value, 'https://example.com/mcp');
  assert.equal(document.getElementById('server-prompt').value, 'Be terse.');
  assert.equal(document.getElementById('server-gated-by-web-search').checked, true);
  const meta = document.getElementById('server-meta').textContent;
  assert.equal(meta.includes('my_notes'), true);
});

test('load() shows "Not found" and the error message on failure', async () => {
  loadFixture('my_notes', async () => ({ ok: false, status: 404, text: async () => 'no such server' }));
  await flush();
  assert.equal(document.getElementById('server-title').textContent, 'Not found');
  assert.equal(document.getElementById('server-status').textContent.includes('no such server'), true);
  assert.equal(document.getElementById('server-form').hidden, true);
});

test('"new" mode shows an empty form without fetching, and hides delete', async () => {
  let fetched = false;
  loadFixture('new', async () => { fetched = true; return { ok: true, json: async () => baseServer() }; });
  await flush();
  assert.equal(fetched, false);
  assert.equal(document.getElementById('server-title').textContent, 'Add server');
  assert.equal(document.getElementById('server-form').hidden, false);
  assert.equal(document.getElementById('server-enabled').checked, true);
  assert.equal(document.getElementById('server-delete-btn').hidden, true);
});

test('the "remove stored API key" checkbox is disabled until a key is present', async () => {
  loadFixture('my_notes', async () => ({ ok: true, json: async () => baseServer({ has_api_key: false }) }));
  await flush();
  assert.equal(document.getElementById('server-clear-api-key').disabled, true);

  setupServerDOM('other');
  global.fetch = async () => ({ ok: true, json: async () => baseServer({ has_api_key: true }) });
  requireFresh('./account_mcp_server.js');
  await flush();
  assert.equal(document.getElementById('server-clear-api-key').disabled, false);
});

test('requestBody always sends transport "http"', async () => {
  const { requestBody } = loadFixture('new');
  await flush();
  document.getElementById('server-name').value = 'x';
  document.getElementById('server-base-url').value = 'https://x.example.com';
  assert.equal(requestBody().transport, 'http');
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
      return { ok: true, json: async () => baseServer({ id: 'new_server', name: 'new server' }) };
    }
    return { ok: true, json: async () => baseServer() };
  };
  document.getElementById('server-name').value = 'new server';
  document.getElementById('server-base-url').value = 'https://new.example.com';
  document.getElementById('server-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(postedURL, '/account/api/mcp-servers');
  assert.equal(postedBody.name, 'new server');
  assert.equal(postedBody.transport, 'http');
});

test('submitting in edit mode patches the specific server', async () => {
  let patchedURL = null;
  loadFixture('my_notes', async () => ({ ok: true, json: async () => baseServer() }));
  await flush();
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'PATCH') {
      patchedURL = url;
      return { ok: true, json: async () => baseServer() };
    }
    return { ok: true, json: async () => baseServer() };
  };
  document.getElementById('server-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(patchedURL, '/account/api/mcp-servers/my_notes');
  assert.equal(document.getElementById('server-form-status').textContent, 'Saved.');
});

test('submitting reports the error message on failure', async () => {
  loadFixture('my_notes', async () => ({ ok: true, json: async () => baseServer() }));
  await flush();
  global.fetch = async () => ({ ok: false, status: 400, text: async () => 'base_url must not be empty' });
  document.getElementById('server-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(document.getElementById('server-form-status').textContent, 'Could not save: base_url must not be empty');
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture('my_notes', async () => ({ ok: true, json: async () => baseServer() }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('server-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('clicking Delete removes the server when confirmed and redirects to the list', async () => {
  let deletedURL = null;
  loadFixture('my_notes', async () => ({ ok: true, json: async () => baseServer() }));
  await flush();
  window.confirm = () => true;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') {
      deletedURL = url;
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => baseServer() };
  };
  document.getElementById('server-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deletedURL, '/account/api/mcp-servers/my_notes');
});

test('clicking Delete re-enables the button and alerts on failure', async () => {
  loadFixture('my_notes', async () => ({ ok: true, json: async () => baseServer() }));
  await flush();
  window.confirm = () => true;
  let alertMsg = null;
  window.alert = (msg) => { alertMsg = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'in use' };
    return { ok: true, json: async () => baseServer() };
  };
  document.getElementById('server-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(document.getElementById('server-delete-btn').disabled, false);
  assert.equal(alertMsg, 'Could not delete: in use');
});

test('sign-out posts to /logout on click', async () => {
  loadFixture('my_notes');
  await flush();
  let fetchedURL, fetchedOpts;
  global.fetch = async (url, opts) => {
    fetchedURL = url;
    fetchedOpts = opts;
    return { ok: true };
  };
  document.getElementById('sign-out').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(fetchedURL, '/logout');
  assert.equal(fetchedOpts.method, 'POST');
});
