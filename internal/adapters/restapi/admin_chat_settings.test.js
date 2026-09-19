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
    rag_enabled: true,
    rag_result_count: 5,
    max_context_tokens: 6000,
    updated_at: '2026-01-02T03:04:05Z',
  }, overrides);
}

function loadFixture(chatEndpoint, fetchImpl) {
  setupDOM(CHAT_SETTINGS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async (url) => {
    if (url.includes('/admin/api/chat-endpoint')) {
      return { ok: true, json: async () => chatEndpoint || baseChatEndpoint() };
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
  assert.equal(document.getElementById('chat-rag-enabled').checked, true);
  assert.equal(document.getElementById('chat-rag-result-count').value, '5');
  assert.equal(document.getElementById('chat-max-context-tokens').value, '6000');
});

test('loadChatEndpoint never populates the API key field, even when one is stored', async () => {
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-api-key').value, '');
  assert.equal(document.getElementById('chat-api-key').placeholder, 'Leave blank to keep the current key');
  assert.equal(document.getElementById('chat-clear-api-key').disabled, false);
});

test('loadChatEndpoint disables "remove stored key" and defaults RAG off when nothing is stored', async () => {
  loadFixture(baseChatEndpoint({ has_api_key: false, rag_enabled: false }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-api-key').placeholder, '');
  assert.equal(document.getElementById('chat-clear-api-key').disabled, true);
  assert.equal(document.getElementById('chat-rag-enabled').checked, false);
});

test('loadChatEndpoint defaults max-context-tokens to 0 when unset', async () => {
  loadFixture(baseChatEndpoint({ max_context_tokens: 0 }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('chat-max-context-tokens').value, '0');
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
  document.getElementById('chat-rag-enabled').checked = false;
  document.getElementById('chat-rag-result-count').value = '8';
  document.getElementById('chat-max-context-tokens').value = '8000';

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
  assert.equal(gotBody.rag_enabled, false);
  assert.equal(gotBody.rag_result_count, 8);
  assert.equal(gotBody.max_context_tokens, 8000);
  assert.equal(document.getElementById('chat-settings-status').textContent, 'Saved.');
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

  const status = document.getElementById('chat-settings-status');
  assert.equal(status.textContent.includes('chat endpoint save failed'), true);
  assert.equal(document.getElementById('save-chat-settings-btn').disabled, false);
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
