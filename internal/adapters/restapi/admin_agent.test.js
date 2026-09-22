'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('jsdom');
const { teardownDOM, requireFresh } = require('./dom_helper.test_util');

const AGENT_HTML = fs.readFileSync(path.join(__dirname, 'admin_agent.html'), 'utf8');

// admin_agent.js reads its agent id (or the literal "new") from
// window.location.pathname at load time, so it needs a jsdom instance
// constructed with a specific URL, same reasoning as
// admin_mcp_server.test.js's setupServerDOM.
function setupAgentDOM(id) {
  const dom = new JSDOM(AGENT_HTML, { url: 'http://localhost/admin/agents/' + encodeURIComponent(id) });
  global.window = dom.window;
  global.document = dom.window.document;
  Object.defineProperty(global, 'navigator', {
    value: dom.window.navigator, configurable: true, writable: true,
  });
  return dom;
}

function baseAgent(overrides) {
  return Object.assign({
    id: 'fact_checker',
    name: 'fact-checker',
    description: 'Verifies claims against sources.',
    system_prompt: 'Be skeptical.',
    mcp_server_ids: ['mcp1'],
    enabled: true,
  }, overrides);
}

const MCP_SERVERS = [
  { id: 'mcp1', name: 'web tools' },
  { id: 'mcp2', name: 'datetime' },
];

// urlSplitFetch routes a GET on the agent itself vs. the global MCP server
// catalog fetch (used to populate the checkbox list) to their own
// implementations -- mirrors admin_mcp_server.test.js's own helper for the
// same "one fixture, two distinct endpoints" reason.
function urlSplitFetch(agentImpl, mcpServersImpl) {
  return async (url) => {
    if (url.includes('/admin/api/mcp-servers')) return mcpServersImpl();
    return agentImpl();
  };
}

function loadFixture(id, fetchImpl) {
  setupAgentDOM(id || 'fact_checker');
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || urlSplitFetch(
    () => ({ ok: true, json: async () => baseAgent() }),
    () => ({ ok: true, json: async () => MCP_SERVERS }),
  );
  return requireFresh('./admin_agent.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('load() applies the fetched agent to the form and reveals it', async () => {
  loadFixture('fact_checker');
  await flush();
  assert.equal(document.getElementById('agent-title').textContent, 'fact-checker');
  assert.equal(document.getElementById('agent-form').hidden, false);
  assert.equal(document.getElementById('agent-name').value, 'fact-checker');
  assert.equal(document.getElementById('agent-description').value, 'Verifies claims against sources.');
  assert.equal(document.getElementById('agent-system-prompt').value, 'Be skeptical.');
  assert.equal(document.getElementById('agent-enabled').checked, true);
  const meta = document.getElementById('agent-meta').textContent;
  assert.equal(meta.includes('fact_checker'), true);
});

test('renders the admin nav rail on load', async () => {
  loadFixture('fact_checker');
  await flush();
  const rail = document.getElementById('admin-rail');
  assert.notEqual(rail.querySelector('a'), null);
});

test('load() shows "Not found" and the error message on failure', async () => {
  loadFixture('fact_checker', urlSplitFetch(
    () => ({ ok: false, status: 404, text: async () => 'no such agent' }),
    () => ({ ok: true, json: async () => MCP_SERVERS }),
  ));
  await flush();
  assert.equal(document.getElementById('agent-title').textContent, 'Not found');
  assert.equal(document.getElementById('agent-status').textContent.includes('no such agent'), true);
  assert.equal(document.getElementById('agent-form').hidden, true);
});

test('"new" mode shows an empty form without fetching the agent, and hides delete', async () => {
  let agentFetched = false;
  loadFixture('new', urlSplitFetch(
    () => { agentFetched = true; return { ok: true, json: async () => baseAgent() }; },
    () => ({ ok: true, json: async () => MCP_SERVERS }),
  ));
  await flush();
  assert.equal(agentFetched, false);
  assert.equal(document.getElementById('agent-title').textContent, 'Add agent');
  assert.equal(document.getElementById('agent-form').hidden, false);
  assert.equal(document.getElementById('agent-enabled').checked, true);
  assert.equal(document.getElementById('agent-delete-btn').hidden, true);
});

test('"new" mode still fetches the global MCP server catalog to populate checkboxes', async () => {
  let mcpServersFetched = false;
  loadFixture('new', urlSplitFetch(
    () => ({ ok: true, json: async () => baseAgent() }),
    () => { mcpServersFetched = true; return { ok: true, json: async () => MCP_SERVERS }; },
  ));
  await flush();
  assert.equal(mcpServersFetched, true);
  assert.equal(document.querySelectorAll('#agent-mcp-servers-list input[type="checkbox"]').length, 2);
});

test('renderMCPServerCheckboxes checks exactly the given IDs', async () => {
  const { renderMCPServerCheckboxes } = loadFixture('new');
  await flush();
  renderMCPServerCheckboxes(MCP_SERVERS, ['mcp2']);
  const boxes = Array.from(document.querySelectorAll('#agent-mcp-servers-list input[type="checkbox"]'));
  assert.equal(boxes.length, 2);
  assert.equal(boxes.find((b) => b.value === 'mcp1').checked, false);
  assert.equal(boxes.find((b) => b.value === 'mcp2').checked, true);
});

test('renderMCPServerCheckboxes shows a message when no servers are configured', async () => {
  const { renderMCPServerCheckboxes } = loadFixture('new');
  await flush();
  renderMCPServerCheckboxes([], []);
  assert.equal(document.getElementById('agent-mcp-servers-list').textContent.includes('No MCP servers'), true);
});

test('load() checks the boxes matching the agent\'s own mcp_server_ids', async () => {
  loadFixture('fact_checker', urlSplitFetch(
    () => ({ ok: true, json: async () => baseAgent({ mcp_server_ids: ['mcp2'] }) }),
    () => ({ ok: true, json: async () => MCP_SERVERS }),
  ));
  await flush();
  const boxes = Array.from(document.querySelectorAll('#agent-mcp-servers-list input[type="checkbox"]'));
  assert.equal(boxes.find((b) => b.value === 'mcp1').checked, false);
  assert.equal(boxes.find((b) => b.value === 'mcp2').checked, true);
});

test('a failed MCP server catalog fetch shows an error in the checkbox area, without blocking the rest of the form', async () => {
  loadFixture('fact_checker', urlSplitFetch(
    () => ({ ok: true, json: async () => baseAgent() }),
    () => ({ ok: false, status: 500, text: async () => 'db down' }),
  ));
  await flush();
  assert.equal(document.getElementById('agent-mcp-servers-list').textContent.includes('db down'), true);
  assert.equal(document.getElementById('agent-form').hidden, false);
  assert.equal(document.getElementById('agent-name').value, 'fact-checker');
});

test('checkedMCPServerIDs returns only the checked values', async () => {
  const { checkedMCPServerIDs } = loadFixture('fact_checker');
  await flush();
  document.querySelectorAll('#agent-mcp-servers-list input[type="checkbox"]')[1].checked = true;
  const ids = checkedMCPServerIDs();
  assert.equal(ids.includes('mcp1'), true);
  assert.equal(ids.includes('mcp2'), true);
});

test('requestBody includes only the checked MCP server IDs', async () => {
  loadFixture('fact_checker');
  await flush();
  const boxes = document.querySelectorAll('#agent-mcp-servers-list input[type="checkbox"]');
  boxes.forEach((b) => { b.checked = b.value === 'mcp2'; });
  const { requestBody } = require('./admin_agent.js');
  assert.deepEqual(requestBody().mcp_server_ids, ['mcp2']);
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
      return { ok: true, json: async () => baseAgent({ id: 'agent_new', name: 'new' }) };
    }
    return { ok: true, json: async () => MCP_SERVERS };
  };
  document.getElementById('agent-name').value = 'new';
  document.getElementById('agent-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(postedURL, '/admin/api/agents');
  assert.equal(postedBody.name, 'new');
});

test('submitting in edit mode patches the specific agent', async () => {
  let patchedURL = null;
  loadFixture('fact_checker');
  await flush();
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'PATCH') {
      patchedURL = url;
      return { ok: true, json: async () => baseAgent() };
    }
    return { ok: true, json: async () => MCP_SERVERS };
  };
  document.getElementById('agent-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(patchedURL, '/admin/api/agents/fact_checker');
  assert.equal(document.getElementById('agent-form-status').textContent, 'Saved.');
});

test('submitting reports the error message on failure', async () => {
  loadFixture('fact_checker');
  await flush();
  global.fetch = async () => ({ ok: false, status: 400, text: async () => 'name must not be empty' });
  document.getElementById('agent-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(document.getElementById('agent-form-status').textContent, 'Could not save: name must not be empty');
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture('fact_checker');
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('agent-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('clicking Delete removes the agent when confirmed and redirects to the list', async () => {
  let deletedURL = null;
  loadFixture('fact_checker');
  await flush();
  window.confirm = () => true;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') {
      deletedURL = url;
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => baseAgent() };
  };
  document.getElementById('agent-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deletedURL, '/admin/api/agents/fact_checker');
});

test('clicking Delete re-enables the button and alerts on failure', async () => {
  loadFixture('fact_checker');
  await flush();
  window.confirm = () => true;
  let alertMsg = null;
  window.alert = (msg) => { alertMsg = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'in use' };
    return { ok: true, json: async () => baseAgent() };
  };
  document.getElementById('agent-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(document.getElementById('agent-delete-btn').disabled, false);
  assert.equal(alertMsg, 'Could not delete: in use');
});
