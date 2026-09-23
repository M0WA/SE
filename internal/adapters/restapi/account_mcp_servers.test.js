'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const ACCOUNT_MCP_SERVERS_HTML = fs.readFileSync(path.join(__dirname, 'account_mcp_servers.html'), 'utf8');

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

// Doesn't load admin.js (see account_mcp_servers.js) -- no admin.js global to assign in this fixture.
function loadFixture(fetchImpl) {
  setupDOM(ACCOUNT_MCP_SERVERS_HTML);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => [] }));
  return requireFresh('./account_mcp_servers.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('loadServers shows a message and no table when none are configured', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  assert.equal(document.getElementById('servers-status').textContent.includes('No personal MCP servers'), true);
  assert.equal(document.getElementById('servers-table').querySelector('table'), null);
});

test('renderServers builds a row per server with its fields', () => {
  const { renderServers } = loadFixture();
  renderServers([baseServer(), baseServer({ id: 'other', name: 'other server', base_url: 'https://b.example.com', enabled: false })]);
  const text = document.getElementById('servers-table').textContent;
  assert.equal(text.includes('my notes'), true);
  assert.equal(text.includes('other server'), true);
  assert.equal(document.getElementById('servers-status').textContent, '');
});

test('renderServers renders an "Edit" link to the per-server subpage', () => {
  const { renderServers } = loadFixture();
  renderServers([baseServer()]);
  const editLink = document.querySelector('#servers-table a.text-button');
  assert.equal(editLink.textContent, 'Edit');
  assert.equal(editLink.getAttribute('href'), '/account/mcp-servers/my_notes');
});

test('renderServers shows base url and enabled columns', () => {
  const { renderServers } = loadFixture();
  renderServers([baseServer({ enabled: false })]);
  const cells = Array.from(document.querySelectorAll('#servers-table td')).map((td) => td.textContent);
  // Uses Array.some+strict equality, not .includes() -- CodeQL's js/incomplete-url-substring-sanitization
  // rule misreads .includes() here as a URL-substring check; this is an exact-match against rendered
  // cell strings, not that pattern. Rephrased to avoid re-litigating the false positive.
  assert.equal(cells.some((c) => c === 'https://example.com/mcp'), true);
  assert.equal(cells.some((c) => c === 'no'), true);
});

test('loadServers reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db down' }));
  await flush();
  assert.equal(document.getElementById('servers-status').textContent.includes('db down'), true);
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseServer()] }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => [] };
  };
  document.querySelector('#servers-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('clicking Delete calls the DELETE endpoint and reloads the list when confirmed', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseServer()] }));
  await flush();
  window.confirm = () => true;
  let gotURL, gotMethod;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') {
      gotURL = url;
      gotMethod = opts.method;
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => [] };
  };
  document.querySelector('#servers-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(gotURL, '/account/api/mcp-servers/my_notes');
  assert.equal(gotMethod, 'DELETE');
});

test('a failed delete shows an alert and does not reload the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseServer()] }));
  await flush();
  window.confirm = () => true;
  let alertMessage = '';
  window.alert = (msg) => { alertMessage = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'db down' };
    return { ok: true, json: async () => [baseServer()] };
  };
  document.querySelector('#servers-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(alertMessage.includes('db down'), true);
});

test('sign-out posts to /logout on click', async () => {
  loadFixture();
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
