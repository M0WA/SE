'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const AGENTS_HTML = fs.readFileSync(path.join(__dirname, 'admin_agents.html'), 'utf8');

function baseAgent(overrides) {
  return Object.assign({
    id: 'fact_checker',
    name: 'fact-checker',
    description: 'Verifies claims against sources.',
    system_prompt: 'Be skeptical.',
    mcp_server_ids: [],
    enabled: true,
  }, overrides);
}

function loadFixture(fetchImpl) {
  setupDOM(AGENTS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => [] }));
  return requireFresh('./admin_agents.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('loadAgents shows a message and no table when none are configured', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  assert.equal(document.getElementById('agents-status').textContent.includes('No agents'), true);
  assert.equal(document.getElementById('agents-table').querySelector('table'), null);
});

test('renderAgents builds a row per agent with its fields', () => {
  const { renderAgents } = loadFixture();
  renderAgents([baseAgent(), baseAgent({ id: 'summarizer', name: 'summarizer', enabled: false })]);
  const text = document.getElementById('agents-table').textContent;
  assert.equal(text.includes('fact-checker'), true);
  assert.equal(text.includes('summarizer'), true);
  assert.equal(document.getElementById('agents-status').textContent, '');
});

test('renderAgents renders an "Edit" link to the per-agent subpage', () => {
  const { renderAgents } = loadFixture();
  renderAgents([baseAgent()]);
  const editLink = document.querySelector('#agents-table a.text-button');
  assert.equal(editLink.textContent, 'Edit');
  assert.equal(editLink.getAttribute('href'), '/admin/agents/fact_checker');
});

test('renderAgents shows the description and enabled columns', () => {
  const { renderAgents } = loadFixture();
  renderAgents([baseAgent({ enabled: false })]);
  const cells = Array.from(document.querySelectorAll('#agents-table td')).map((td) => td.textContent);
  assert.equal(cells.includes('Verifies claims against sources.'), true);
  assert.equal(cells.includes('no'), true);
});

test('loadAgents reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db down' }));
  await flush();
  assert.equal(document.getElementById('agents-status').textContent.includes('db down'), true);
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseAgent()] }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => [] };
  };
  document.querySelector('#agents-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('clicking Delete calls the DELETE endpoint and reloads the list when confirmed', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseAgent()] }));
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
  document.querySelector('#agents-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(gotURL, '/admin/api/agents/fact_checker');
  assert.equal(gotMethod, 'DELETE');
});

test('a failed delete shows an alert and does not reload the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseAgent()] }));
  await flush();
  window.confirm = () => true;
  let alertMessage = '';
  window.alert = (msg) => { alertMessage = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'db down' };
    return { ok: true, json: async () => [baseAgent()] };
  };
  document.querySelector('#agents-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(alertMessage.includes('db down'), true);
});
