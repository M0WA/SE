'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const CHAT_SETTINGS_HTML = fs.readFileSync(path.join(__dirname, 'admin_chat_settings.html'), 'utf8');

function baseChatEndpoint(overrides) {
  return Object.assign({
    base_url: 'http://localhost:8000/v1',
    has_api_key: true,
    model: 'llama-3',
    enabled: true,
    system_prompt: 'You are a helpful assistant.',
    max_context_tokens: 6000,
    web_search_enabled: true,
    web_search_base_url: 'http://127.0.0.1:8888',
    web_search_result_count: 5,
    updated_at: '2026-01-02T03:04:05Z',
  }, overrides);
}

function loadFixture(chatEndpoint, fetchImpl, mcpServers, agents) {
  setupDOM(CHAT_SETTINGS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async (url) => {
    if (url.includes('/admin/api/chat-endpoint')) {
      return { ok: true, json: async () => chatEndpoint || baseChatEndpoint() };
    }
    if (url.includes('/admin/api/agents')) {
      return { ok: true, json: async () => agents || [] };
    }
    if (url.includes('/admin/api/mcp-servers')) {
      return { ok: true, json: async () => mcpServers || [] };
    }
    return { ok: true, json: async () => ({}) };
  });
  return requireFresh('./admin_chat_settings.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('loadChatEndpoint populates every chat field from the GET response', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-enabled').checked, true);
  assert.equal(document.getElementById('chat-base-url').value, 'http://localhost:8000/v1');
  assert.equal(document.getElementById('chat-model').value, 'llama-3');
  assert.equal(document.getElementById('chat-system-prompt').value, 'You are a helpful assistant.');
  assert.equal(document.getElementById('chat-max-context-tokens').value, '6000');
  assert.equal(document.getElementById('chat-web-search-enabled').checked, true);
  assert.equal(document.getElementById('chat-web-search-base-url').value, 'http://127.0.0.1:8888');
  assert.equal(document.getElementById('chat-web-search-result-count').value, '5');
});

test('loadAgentOptions populates the select with every agent, keeping "(none)" first', async () => {
  loadFixture(undefined, undefined, undefined, [
    { id: 'researcher', name: 'Researcher' },
    { id: 'fact_checker', name: 'Fact Checker' },
  ]);
  await new Promise((resolve) => setTimeout(resolve, 0));
  const select = document.getElementById('chat-default-agent');
  const options = Array.from(select.options).map((o) => [o.value, o.textContent]);
  assert.deepEqual(options, [
    ['', '(none)'],
    ['researcher', 'Researcher'],
    ['fact_checker', 'Fact Checker'],
  ]);
});

test('loadChatEndpoint selects the stored default_agent_id once options are populated', async () => {
  loadFixture(baseChatEndpoint({ default_agent_id: 'fact_checker' }), undefined, undefined, [
    { id: 'researcher', name: 'Researcher' },
    { id: 'fact_checker', name: 'Fact Checker' },
  ]);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-default-agent').value, 'fact_checker');
});

test('loadAgentOptions leaves just "(none)" on a failed fetch, without blocking the rest of the page', async () => {
  loadFixture(baseChatEndpoint(), async (url) => {
    if (url.includes('/admin/api/agents')) return { ok: false, status: 500, text: async () => 'db down' };
    if (url.includes('/admin/api/chat-endpoint')) return { ok: true, json: async () => baseChatEndpoint() };
    return { ok: true, json: async () => [] };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  const select = document.getElementById('chat-default-agent');
  assert.equal(select.options.length, 1);
  assert.equal(select.options[0].value, '');
  assert.equal(document.getElementById('chat-base-url').value, 'http://localhost:8000/v1');
});

test('loadAgentOptions clears previously-populated options before repopulating', async () => {
  const { loadAgentOptions } = loadFixture(undefined, undefined, undefined, [{ id: 'researcher', name: 'Researcher' }]);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-default-agent').options.length, 2);
  global.fetch = async (url) => {
    if (url.includes('/admin/api/agents')) return { ok: true, json: async () => [] };
    return { ok: true, json: async () => ({}) };
  };
  await loadAgentOptions();
  assert.equal(document.getElementById('chat-default-agent').options.length, 1);
});

test('loadChatEndpoint defaults web search off when nothing is stored', async () => {
  loadFixture(baseChatEndpoint({ web_search_enabled: false, web_search_base_url: '', web_search_result_count: 0 }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-web-search-enabled').checked, false);
  assert.equal(document.getElementById('chat-web-search-base-url').value, '');
  assert.equal(document.getElementById('chat-web-search-result-count').value, '0');
});

test('loadChatEndpoint never populates the API key field, even when one is stored', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-api-key').value, '');
  assert.equal(document.getElementById('chat-api-key').placeholder, 'Leave blank to keep the current key');
  assert.equal(document.getElementById('chat-clear-api-key').disabled, false);
});

test('loadChatEndpoint disables "remove stored key" when nothing is stored', async () => {
  loadFixture(baseChatEndpoint({ has_api_key: false }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-api-key').placeholder, '');
  assert.equal(document.getElementById('chat-clear-api-key').disabled, true);
});

test('loadChatEndpoint defaults max-context-tokens to 0 when unset', async () => {
  loadFixture(baseChatEndpoint({ max_context_tokens: 0 }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-max-context-tokens').value, '0');
});

test('loadChatEndpoint defaults system prompt to empty when unset', async () => {
  loadFixture(baseChatEndpoint({ system_prompt: '' }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-system-prompt').value, '');
});

test('loadChatEndpoint reports an error message on a failed fetch', async () => {
  loadFixture(undefined, async () => ({ ok: false, status: 500, text: async () => 'chat endpoint down' }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-settings-status').textContent.includes('chat endpoint down'), true);
});

test('saveChatEndpoint PATCHes every field, including max_context_tokens', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('chat-enabled').checked = true;
  document.getElementById('chat-base-url').value = 'http://localhost:9000/v1';
  document.getElementById('chat-model').value = 'gpt-oss';
  document.getElementById('chat-api-key').value = 'sk-new-key';
  document.getElementById('chat-system-prompt').value = 'Answer tersely.';
  document.getElementById('chat-max-context-tokens').value = '8000';
  document.getElementById('chat-web-search-enabled').checked = true;
  document.getElementById('chat-web-search-base-url').value = 'http://127.0.0.1:8888';
  document.getElementById('chat-web-search-result-count').value = '3';

  let gotURL, gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/chat-endpoint') && opts && opts.method === 'PATCH') {
      gotURL = url;
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => baseChatEndpoint({ base_url: 'http://localhost:9000/v1' }) };
    }
    return { ok: true, json: async () => baseChatEndpoint() };
  };

  const { saveChatEndpoint } = requireFresh('./admin_chat_settings.js');
  await saveChatEndpoint();

  assert.equal(gotURL, '/admin/api/chat-endpoint');
  assert.equal(gotBody.base_url, 'http://localhost:9000/v1');
  assert.equal(gotBody.model, 'gpt-oss');
  assert.equal(gotBody.api_key, 'sk-new-key');
  assert.equal(gotBody.system_prompt, 'Answer tersely.');
  assert.equal(gotBody.max_context_tokens, 8000);
  assert.equal(gotBody.web_search_enabled, true);
  assert.equal(gotBody.web_search_base_url, 'http://127.0.0.1:8888');
  assert.equal(gotBody.web_search_result_count, 3);
  assert.equal(document.getElementById('chat-settings-status').textContent, 'Saved.');
});

// This test deliberately does NOT re-require the module mid-test (unlike
// most other save tests in this file) -- admin_chat_settings.js's own
// module-load-time loadAgentOptions() clears the select's options
// synchronously (before its repopulating fetch resolves), which would
// race against and wipe out a value set on the original module's DOM
// right before a requireFresh() call. Reusing the original module's own
// saveChatEndpoint (global.fetch is still looked up dynamically at call
// time, so reassigning it here still works) avoids that race entirely.
test('saveChatEndpoint includes the selected default_agent_id', async () => {
  const chatSvc = loadFixture(undefined, undefined, undefined, [{ id: 'fact_checker', name: 'Fact Checker' }]);
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('chat-default-agent').value = 'fact_checker';

  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/chat-endpoint') && opts && opts.method === 'PATCH') {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => baseChatEndpoint({ default_agent_id: 'fact_checker' }) };
    }
    return { ok: true, json: async () => baseChatEndpoint() };
  };
  await chatSvc.saveChatEndpoint();
  assert.equal(gotBody.default_agent_id, 'fact_checker');
});

test('saveChatEndpoint sends clear_api_key when the "remove stored key" box is checked', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('chat-clear-api-key').checked = true;

  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/chat-endpoint') && opts && opts.method === 'PATCH') {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => baseChatEndpoint({ has_api_key: false }) };
    }
    return { ok: true, json: async () => baseChatEndpoint() };
  };
  const { saveChatEndpoint } = requireFresh('./admin_chat_settings.js');
  await saveChatEndpoint();
  assert.equal(gotBody.clear_api_key, true);
});

test('saveChatEndpoint defaults max_context_tokens to 0 for an unparseable value', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('chat-max-context-tokens').value = 'abc';

  let gotBody;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/chat-endpoint') && opts && opts.method === 'PATCH') {
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => baseChatEndpoint() };
    }
    return { ok: true, json: async () => baseChatEndpoint() };
  };
  const { saveChatEndpoint } = requireFresh('./admin_chat_settings.js');
  await saveChatEndpoint();
  assert.equal(gotBody.max_context_tokens, 0);
});

test('saveChatEndpoint shows an error message and re-enables the button on failure', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));

  global.fetch = async (url) => {
    if (url.includes('/admin/api/chat-endpoint')) {
      return { ok: false, status: 500, text: async () => 'chat endpoint save failed' };
    }
    return { ok: true, json: async () => baseChatEndpoint() };
  };
  const { saveChatEndpoint } = requireFresh('./admin_chat_settings.js');
  await saveChatEndpoint();
  // requireFresh re-executes the module, which auto-invokes its own
  // loadChatEndpoint() (and, via that, loadEnabledServerPrompts()) as a
  // dangling promise this test never awaits directly -- flush it before
  // teardownDOM (in afterEach) deletes global.document, or its tail
  // (renderTokenUsageDonut) can fire against a torn-down DOM.
  await new Promise((resolve) => setTimeout(resolve, 0));

  const status = document.getElementById('chat-settings-status');
  assert.equal(status.textContent.includes('chat endpoint save failed'), true);
  assert.equal(document.getElementById('save-chat-settings-btn').disabled, false);
});

test('renderTokenUsageDonut renders a chart and a legend row per segment, including active MCP server prompts', async () => {
  loadFixture(baseChatEndpoint({ system_prompt: 'x'.repeat(30), max_context_tokens: 1000 }), undefined, [
    { id: 's1', name: 'web', enabled: true, prompt: 'y'.repeat(15) },
    { id: 's2', name: 'disabled_server', enabled: false, prompt: 'z'.repeat(999) },
    { id: 's3', name: 'no_prompt_server', enabled: true, prompt: '' },
  ]);
  await new Promise((resolve) => setTimeout(resolve, 0));

  const container = document.getElementById('chat-token-usage');
  assert.equal(container.querySelectorAll('svg.donut-chart').length, 1);
  const rows = container.querySelectorAll('.donut-legend-row');
  // Global prompt, active MCP server prompts (only s1 counts -- s2 is
  // disabled, s3 has no prompt), and "remaining" since max_context_tokens > 0.
  assert.equal(rows.length, 3);
  assert.match(rows[0].textContent, /Global prompt: \d+/);
  assert.match(rows[1].textContent, /Active MCP server prompts: \d+/);
  assert.match(rows[2].textContent, /Remaining for context\/history: \d+/);
});

test('renderTokenUsageDonut omits the "remaining" segment when no max context tokens is configured', async () => {
  loadFixture(baseChatEndpoint({ system_prompt: 'x'.repeat(30), max_context_tokens: 0 }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const rows = document.getElementById('chat-token-usage').querySelectorAll('.donut-legend-row');
  assert.equal(rows.length, 2);
});

test('editing the system prompt or max context tokens re-renders the donut live', async () => {
  loadFixture(baseChatEndpoint({ system_prompt: '', max_context_tokens: 0 }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-token-usage').querySelectorAll('.donut-legend-row').length, 2);

  document.getElementById('chat-max-context-tokens').value = '500';
  document.getElementById('chat-max-context-tokens').dispatchEvent(new window.Event('input'));
  assert.equal(document.getElementById('chat-token-usage').querySelectorAll('.donut-legend-row').length, 3);

  document.getElementById('chat-system-prompt').value = 'hello';
  document.getElementById('chat-system-prompt').dispatchEvent(new window.Event('input'));
  const globalRow = document.getElementById('chat-token-usage').querySelectorAll('.donut-legend-row')[0];
  assert.match(globalRow.textContent, /Global prompt: [1-9]\d*/);
});

test('renderTokenUsageDonut leaves MCP server prompts out when the servers fetch fails', async () => {
  loadFixture(baseChatEndpoint(), async (url) => {
    if (url.includes('/admin/api/chat-endpoint')) {
      return { ok: true, json: async () => baseChatEndpoint() };
    }
    return { ok: false, status: 500, text: async () => 'mcp servers down' };
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  const rows = document.getElementById('chat-token-usage').querySelectorAll('.donut-legend-row');
  assert.match(rows[1].textContent, /Active MCP server prompts: 0/);
});

test('clicking "Save chat settings" invokes saveChatEndpoint', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  let patched = false;
  global.fetch = async (url, opts) => {
    if (url.includes('/admin/api/chat-endpoint') && opts && opts.method === 'PATCH') {
      patched = true;
      return { ok: true, json: async () => baseChatEndpoint() };
    }
    return { ok: true, json: async () => baseChatEndpoint() };
  };
  document.getElementById('save-chat-settings-btn').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(patched, true);
});
