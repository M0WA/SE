'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('jsdom');
const { teardownDOM, requireFresh } = require('./dom_helper.test_util');

const SERVER_HTML = fs.readFileSync(path.join(__dirname, 'admin_mcp_server.html'), 'utf8');

// Reads server id (or "new") from window.location.pathname at load time, so needs a jsdom
// instance with a specific URL -- same as admin_user.test.js's setupUserDOM.
function setupServerDOM(id) {
  const dom = new JSDOM(SERVER_HTML, { url: 'http://localhost/admin/mcp-servers/' + encodeURIComponent(id) });
  global.window = dom.window;
  global.document = dom.window.document;
  Object.defineProperty(global, 'navigator', {
    value: dom.window.navigator, configurable: true, writable: true,
  });
  return dom;
}

function baseServer(overrides) {
  return Object.assign({
    id: 'mcp_web',
    name: 'web',
    transport: 'stdio',
    command: '/usr/bin/searchengine-mcp-web',
    args: ['--flag'],
    base_url: '',
    has_api_key: false,
    enabled: true,
    prompt: '',
    gated_by_web_search: false,
  }, overrides);
}

function loadFixture(id, fetchImpl) {
  setupServerDOM(id || 'mcp_web');
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => baseServer() }));
  return requireFresh('./admin_mcp_server.js');
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
  loadFixture('mcp_web', async (url) => {
    assert.equal(url, '/admin/api/mcp-servers/mcp_web');
    return { ok: true, json: async () => baseServer({ prompt: 'Prefer the top 3 results.', gated_by_web_search: true }) };
  });
  await flush();
  assert.equal(document.getElementById('server-title').textContent, 'web');
  assert.equal(document.getElementById('server-form').hidden, false);
  assert.equal(document.getElementById('server-name').value, 'web');
  assert.equal(document.getElementById('server-transport').value, 'stdio');
  assert.equal(document.getElementById('server-command').value, '/usr/bin/searchengine-mcp-web');
  assert.equal(document.getElementById('server-args').value, '--flag');
  assert.equal(document.getElementById('server-prompt').value, 'Prefer the top 3 results.');
  assert.equal(document.getElementById('server-gated-by-web-search').checked, true);
  const meta = document.getElementById('server-meta').textContent;
  assert.equal(meta.includes('mcp_web'), true);
});

test('renders the admin nav rail on load', async () => {
  loadFixture('mcp_web');
  await flush();
  const rail = document.getElementById('admin-rail');
  assert.notEqual(rail.querySelector('a'), null);
});

test('load() shows "Not found" and the error message on failure', async () => {
  loadFixture('mcp_web', async () => ({ ok: false, status: 404, text: async () => 'no such server' }));
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

test('updateTransportVisibility shows stdio fields and hides http fields for "stdio"', async () => {
  const { updateTransportVisibility } = loadFixture('new');
  await flush();
  document.getElementById('server-transport').value = 'stdio';
  updateTransportVisibility();
  assert.equal(document.getElementById('server-command-row').hidden, false);
  assert.equal(document.getElementById('server-args-row').hidden, false);
  assert.equal(document.getElementById('server-base-url-row').hidden, true);
  assert.equal(document.getElementById('server-api-key-row').hidden, true);
});

test('updateTransportVisibility shows http fields and hides stdio fields for "http"', async () => {
  const { updateTransportVisibility } = loadFixture('new');
  await flush();
  document.getElementById('server-transport').value = 'http';
  updateTransportVisibility();
  assert.equal(document.getElementById('server-command-row').hidden, true);
  assert.equal(document.getElementById('server-args-row').hidden, true);
  assert.equal(document.getElementById('server-base-url-row').hidden, false);
  assert.equal(document.getElementById('server-api-key-row').hidden, false);
});

test('changing the transport select live-updates field visibility', async () => {
  loadFixture('new');
  await flush();
  assert.equal(document.getElementById('server-base-url-row').hidden, true);
  document.getElementById('server-transport').value = 'http';
  document.getElementById('server-transport').dispatchEvent(new window.Event('change'));
  assert.equal(document.getElementById('server-base-url-row').hidden, false);
  assert.equal(document.getElementById('server-command-row').hidden, true);
});

test('requestBody parses one argument per line, trimming and dropping blanks', async () => {
  loadFixture('mcp_web', async () => ({ ok: true, json: async () => baseServer() }));
  await flush();
  document.getElementById('server-args').value = '--flag\n \nvalue\n';
  const { requestBody } = require('./admin_mcp_server.js');
  assert.deepEqual(requestBody().args, ['--flag', 'value']);
});

test('requestBody omits api_key when left blank but includes clear_api_key', async () => {
  loadFixture('mcp_web', async () => ({ ok: true, json: async () => baseServer({ has_api_key: true }) }));
  await flush();
  const { requestBody } = require('./admin_mcp_server.js');
  const body = requestBody();
  assert.equal(body.api_key, '');
  assert.equal(body.clear_api_key, false);
});

test('the "remove stored API key" checkbox is disabled until a key is present', async () => {
  loadFixture('mcp_web', async () => ({ ok: true, json: async () => baseServer({ has_api_key: false }) }));
  await flush();
  assert.equal(document.getElementById('server-clear-api-key').disabled, true);

  setupServerDOM('mcp_remote');
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async () => ({ ok: true, json: async () => baseServer({ has_api_key: true }) });
  requireFresh('./admin_mcp_server.js');
  await flush();
  assert.equal(document.getElementById('server-clear-api-key').disabled, false);
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
      return { ok: true, json: async () => baseServer({ id: 'mcp_new', name: 'new' }) };
    }
    return { ok: true, json: async () => baseServer() };
  };
  document.getElementById('server-name').value = 'new';
  document.getElementById('server-command').value = '/usr/bin/foo';
  document.getElementById('server-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(postedURL, '/admin/api/mcp-servers');
  assert.equal(postedBody.name, 'new');
});

test('submitting in edit mode patches the specific server', async () => {
  let patchedURL = null;
  loadFixture('mcp_web', async () => ({ ok: true, json: async () => baseServer() }));
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
  assert.equal(patchedURL, '/admin/api/mcp-servers/mcp_web');
  assert.equal(document.getElementById('server-form-status').textContent, 'Saved.');
});

test('submitting reports the error message on failure', async () => {
  loadFixture('mcp_web', async () => ({ ok: true, json: async () => baseServer() }));
  await flush();
  global.fetch = async () => ({ ok: false, status: 400, text: async () => 'command must not be empty' });
  document.getElementById('server-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(document.getElementById('server-form-status').textContent, 'Could not save: command must not be empty');
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture('mcp_web', async () => ({ ok: true, json: async () => baseServer() }));
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
  loadFixture('mcp_web', async () => ({ ok: true, json: async () => baseServer() }));
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
  assert.equal(deletedURL, '/admin/api/mcp-servers/mcp_web');
});

test('clicking Delete re-enables the button and alerts on failure', async () => {
  loadFixture('mcp_web', async () => ({ ok: true, json: async () => baseServer() }));
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

// urlSplitFetch routes GET (load) and POST (list tools) to separate implementations, since
// loadFixture's default fetchImpl answers every URL identically -- not enough once a test cares
// about the /test probe's response.
function urlSplitFetch(loadImpl, testImpl) {
  return async (url, opts) => {
    if (url.includes('/admin/api/mcp-servers/test')) return testImpl();
    return loadImpl();
  };
}

test('load() automatically lists tools for an existing server, without a button click', async () => {
  let testCalls = 0;
  loadFixture('mcp_web', urlSplitFetch(
    () => ({ ok: true, json: async () => baseServer() }),
    () => { testCalls++; return { ok: true, json: async () => ({ tools: [{ name: 'web_search', description: 'Search the web.' }] }) }; },
  ));
  await flush();
  assert.equal(testCalls, 1);
  assert.equal(document.getElementById('server-list-tools-status').textContent, '1 tool(s) exposed by this server.');
  assert.equal(document.getElementById('server-list-tools-result').textContent, 'web_search — Search the web.');
});

test('listTools reports the server-provided error message', async () => {
  loadFixture('mcp_web', urlSplitFetch(
    () => ({ ok: true, json: async () => baseServer() }),
    () => ({ ok: true, json: async () => ({ error: 'could not connect' }) }),
  ));
  await flush();
  assert.equal(document.getElementById('server-list-tools-status').textContent, 'Could not list tools: could not connect');
  assert.equal(document.getElementById('server-list-tools-result').children.length, 0);
});

test('clicking "Refresh tools" re-runs the probe and replaces the previous result', async () => {
  let testCalls = 0;
  loadFixture('mcp_web', urlSplitFetch(
    () => ({ ok: true, json: async () => baseServer() }),
    () => {
      testCalls++;
      return { ok: true, json: async () => ({ tools: [{ name: 'tool_v' + testCalls, description: '' }] }) };
    },
  ));
  await flush();
  assert.equal(testCalls, 1);
  assert.equal(document.getElementById('server-list-tools-result').textContent, 'tool_v1');

  document.getElementById('server-list-tools-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(testCalls, 2);
  // Replaced, not appended -- exactly one result row after the refresh.
  assert.equal(document.getElementById('server-list-tools-result').children.length, 1);
  assert.equal(document.getElementById('server-list-tools-result').textContent, 'tool_v2');
});

test('listTools shows a plain status message when the server reports zero tools without an error', async () => {
  loadFixture('mcp_web', urlSplitFetch(
    () => ({ ok: true, json: async () => baseServer() }),
    () => ({ ok: true, json: async () => ({ tools: [] }) }),
  ));
  await flush();
  assert.match(document.getElementById('server-list-tools-status').textContent, /No tools reported/);
});

test('candidateBody sends the id (for API-key fallback) plus every field the probe needs', async () => {
  loadFixture('mcp_web', async () => ({ ok: true, json: async () => baseServer({ has_api_key: true }) }));
  await flush();
  document.getElementById('server-args').value = 'a\nb';
  const { candidateBody } = require('./admin_mcp_server.js');
  assert.deepEqual(candidateBody(), {
    id: 'mcp_web', name: 'web', transport: 'stdio',
    command: '/usr/bin/searchengine-mcp-web', args: ['a', 'b'],
    base_url: '', api_key: '',
  });
});

test('candidateBody sends an empty id in "new" mode', async () => {
  loadFixture('new');
  await flush();
  const { candidateBody } = require('./admin_mcp_server.js');
  assert.equal(candidateBody().id, '');
});
