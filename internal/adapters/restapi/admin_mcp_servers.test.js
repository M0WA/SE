'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const MCP_SERVERS_HTML = fs.readFileSync(path.join(__dirname, 'admin_mcp_servers.html'), 'utf8');

function baseServer(overrides) {
  return Object.assign({
    id: 'mcp_web',
    name: 'web',
    transport: 'stdio',
    command: '/usr/bin/searchengine-mcp-web',
    args: [],
    base_url: '',
    has_api_key: false,
    enabled: true,
    prompt: '',
    gated_by_web_search: false,
  }, overrides);
}

function loadFixture(fetchImpl) {
  setupDOM(MCP_SERVERS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => [] }));
  return requireFresh('./admin_mcp_servers.js');
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
  assert.equal(document.getElementById('servers-status').textContent.includes('No MCP servers'), true);
  assert.equal(document.getElementById('servers-table').querySelector('table'), null);
});

test('renderServers builds a row per server with its fields', () => {
  const { renderServers } = loadFixture();
  renderServers([baseServer(), baseServer({ id: 'mcp_remote', name: 'remote', transport: 'http', enabled: false })]);
  const text = document.getElementById('servers-table').textContent;
  assert.equal(text.includes('web'), true);
  assert.equal(text.includes('remote'), true);
  assert.equal(document.getElementById('servers-status').textContent, '');
});

test('renderServers renders an "Edit" link to the per-server subpage', () => {
  const { renderServers } = loadFixture();
  renderServers([baseServer()]);
  const editLink = document.querySelector('#servers-table a.text-button');
  assert.equal(editLink.textContent, 'Edit');
  assert.equal(editLink.getAttribute('href'), '/admin/mcp-servers/mcp_web');
});

test('renderServers shows transport and enabled columns', () => {
  const { renderServers } = loadFixture();
  renderServers([baseServer({ transport: 'http', enabled: false })]);
  const cells = Array.from(document.querySelectorAll('#servers-table td')).map((td) => td.textContent);
  assert.equal(cells.includes('http'), true);
  assert.equal(cells.includes('no'), true);
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
  assert.equal(gotURL, '/admin/api/mcp-servers/mcp_web');
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
