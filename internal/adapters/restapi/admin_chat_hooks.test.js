'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const HOOKS_HTML = fs.readFileSync(path.join(__dirname, 'admin_chat_hooks.html'), 'utf8');

function baseHook(overrides) {
  return Object.assign({
    id: 'web_search',
    name: 'web_search',
    pattern: '\\[\\[search:(.+?)\\]\\]',
    script: 'web_search.sh',
    enabled: true,
  }, overrides);
}

function loadFixture(fetchImpl) {
  setupDOM(HOOKS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => [] }));
  return requireFresh('./admin_chat_hooks.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('loadHooks shows a message and no table when none are configured', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  assert.equal(document.getElementById('hooks-status').textContent.includes('No chat hooks configured'), true);
  assert.equal(document.getElementById('hooks-table').querySelector('table'), null);
});

test('renderHooks builds a row per hook with its fields', () => {
  const { renderHooks } = loadFixture();
  renderHooks([baseHook(), baseHook({ id: 'other', name: 'other_hook', enabled: false })]);
  const text = document.getElementById('hooks-table').textContent;
  assert.equal(text.includes('web_search'), true);
  assert.equal(text.includes('other_hook'), true);
  assert.equal(text.includes('web_search.sh'), true);
  assert.equal(document.getElementById('hooks-status').textContent, '');
});

test('renderHooks shows Yes/No for the enabled column', () => {
  const { renderHooks } = loadFixture();
  renderHooks([baseHook({ enabled: true }), baseHook({ id: 'x', enabled: false })]);
  const rows = document.getElementById('hooks-table').querySelectorAll('tbody tr');
  assert.equal(rows.length, 2);
  assert.equal(rows[0].textContent.includes('Yes'), true);
  assert.equal(rows[1].textContent.includes('No'), true);
});

test('loadHooks reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db down' }));
  await flush();
  assert.equal(document.getElementById('hooks-status').textContent.includes('db down'), true);
});

test('clicking "Add hook" opens a blank form', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  document.getElementById('add-hook-btn').dispatchEvent(new window.Event('click'));
  assert.equal(document.getElementById('hook-form-panel').hidden, false);
  assert.equal(document.getElementById('hook-form-title').textContent, 'Add hook');
  assert.equal(document.getElementById('hook-name').value, '');
  assert.equal(document.getElementById('hook-enabled').checked, true);
});

test('clicking Edit opens the form pre-filled with that hook\'s fields', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseHook()] }));
  await flush();
  document.querySelector('#hooks-table button.text-button').dispatchEvent(new window.Event('click'));
  assert.equal(document.getElementById('hook-form-panel').hidden, false);
  assert.equal(document.getElementById('hook-form-title').textContent, 'Edit hook');
  assert.equal(document.getElementById('hook-name').value, 'web_search');
  assert.equal(document.getElementById('hook-pattern').value, '\\[\\[search:(.+?)\\]\\]');
  assert.equal(document.getElementById('hook-script').value, 'web_search.sh');
  assert.equal(document.getElementById('hook-enabled').checked, true);
});

test('clicking Cancel hides the form', async () => {
  const { openAddForm, closeForm } = loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  openAddForm();
  assert.equal(document.getElementById('hook-form-panel').hidden, false);
  document.getElementById('hook-cancel-btn').dispatchEvent(new window.Event('click'));
  assert.equal(document.getElementById('hook-form-panel').hidden, true);
  closeForm();
});

test('submitting the form with no hook being edited POSTs a new hook, then reloads the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  document.getElementById('add-hook-btn').dispatchEvent(new window.Event('click'));
  document.getElementById('hook-name').value = 'new_hook';
  document.getElementById('hook-pattern').value = '\\[\\[new:(.+?)\\]\\]';
  document.getElementById('hook-script').value = 'new_hook.sh';
  document.getElementById('hook-enabled').checked = true;

  let gotURL, gotOpts;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') {
      gotURL = url;
      gotOpts = opts;
      return { ok: true, json: async () => baseHook({ id: 'new_hook', name: 'new_hook' }) };
    }
    return { ok: true, json: async () => [baseHook({ id: 'new_hook', name: 'new_hook' })] };
  };
  document.getElementById('hook-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();

  assert.equal(gotURL, '/admin/api/chat-hooks');
  const body = JSON.parse(gotOpts.body);
  assert.equal(body.name, 'new_hook');
  assert.equal(body.pattern, '\\[\\[new:(.+?)\\]\\]');
  assert.equal(body.script, 'new_hook.sh');
  assert.equal(body.enabled, true);
  assert.equal(document.getElementById('hook-form-panel').hidden, true);
});

test('submitting the form while editing PATCHes that hook\'s id, then reloads the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseHook()] }));
  await flush();
  document.querySelector('#hooks-table button.text-button').dispatchEvent(new window.Event('click'));
  document.getElementById('hook-enabled').checked = false;

  let gotURL, gotOpts;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'PATCH') {
      gotURL = url;
      gotOpts = opts;
      return { ok: true, json: async () => baseHook({ enabled: false }) };
    }
    return { ok: true, json: async () => [baseHook({ enabled: false })] };
  };
  document.getElementById('hook-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();

  assert.equal(gotURL, '/admin/api/chat-hooks/web_search');
  const body = JSON.parse(gotOpts.body);
  assert.equal(body.enabled, false);
  assert.equal(document.getElementById('hook-form-panel').hidden, true);
});

test('a failed save shows an error message and re-enables the button, without closing the form', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  document.getElementById('add-hook-btn').dispatchEvent(new window.Event('click'));
  document.getElementById('hook-name').value = 'x';
  document.getElementById('hook-pattern').value = '(.+)';
  document.getElementById('hook-script').value = 'x.sh';

  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') return { ok: false, status: 500, text: async () => 'invalid pattern' };
    return { ok: true, json: async () => [] };
  };
  document.getElementById('hook-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();

  assert.equal(document.getElementById('hook-form-status').textContent.includes('invalid pattern'), true);
  assert.equal(document.getElementById('hook-form-panel').hidden, false);
  assert.equal(document.getElementById('hook-save-btn').disabled, false);
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseHook()] }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => [] };
  };
  document.querySelectorAll('#hooks-table button.text-button')[1].dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('clicking Delete removes the hook when confirmed, then reloads the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseHook()] }));
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
  document.querySelectorAll('#hooks-table button.text-button')[1].dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deletedURL, '/admin/api/chat-hooks/web_search');
  assert.equal(document.getElementById('hooks-status').textContent.includes('No chat hooks configured'), true);
});

test('clicking Delete alerts on failure', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseHook()] }));
  await flush();
  window.confirm = () => true;
  let alertMsg = null;
  window.alert = (msg) => { alertMsg = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'in use' };
    return { ok: true, json: async () => [baseHook()] };
  };
  document.querySelectorAll('#hooks-table button.text-button')[1].dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(alertMsg, 'Could not delete: in use');
});
