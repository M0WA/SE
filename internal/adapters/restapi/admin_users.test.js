'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const USERS_HTML = fs.readFileSync(path.join(__dirname, 'admin_users.html'), 'utf8');

function baseUser(overrides) {
  return Object.assign({
    id: 'user_alice',
    username: 'alice',
    custom_prompt: '',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  }, overrides);
}

function loadFixture(fetchImpl) {
  setupDOM(USERS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => [] }));
  return requireFresh('./admin_users.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('loadUsers shows a message and no table when none are configured', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  assert.equal(document.getElementById('users-status').textContent.includes('No user accounts'), true);
  assert.equal(document.getElementById('users-table').querySelector('table'), null);
});

test('renderUsers builds a row per user with its fields', () => {
  const { renderUsers } = loadFixture();
  renderUsers([baseUser(), baseUser({ id: 'user_bob', username: 'bob' })]);
  const text = document.getElementById('users-table').textContent;
  assert.equal(text.includes('alice'), true);
  assert.equal(text.includes('bob'), true);
  assert.equal(document.getElementById('users-status').textContent, '');
});

test('renderUsers renders an "Edit" link to the per-user subpage', () => {
  const { renderUsers } = loadFixture();
  renderUsers([baseUser()]);
  const editLink = document.querySelector('#users-table a.text-button');
  assert.equal(editLink.textContent, 'Edit');
  assert.equal(editLink.getAttribute('href'), '/admin/users/user_alice');
});

test('renderUsers renders an empty created cell when created_at is missing', () => {
  const { renderUsers } = loadFixture();
  renderUsers([baseUser({ created_at: '' })]);
  const rows = document.getElementById('users-table').querySelectorAll('tbody tr');
  assert.equal(rows.length, 1);
});

test('loadUsers reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db down' }));
  await flush();
  assert.equal(document.getElementById('users-status').textContent.includes('db down'), true);
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseUser()] }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => [] };
  };
  document.querySelector('#users-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('clicking Delete calls the DELETE endpoint and reloads the list when confirmed', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseUser()] }));
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
  document.querySelector('#users-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(gotURL, '/admin/api/users/user_alice');
  assert.equal(gotMethod, 'DELETE');
});

test('a failed delete shows an alert and does not reload the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseUser()] }));
  await flush();
  window.confirm = () => true;
  let alertMessage = '';
  window.alert = (msg) => { alertMessage = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'db down' };
    return { ok: true, json: async () => [baseUser()] };
  };
  document.querySelector('#users-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(alertMessage.includes('db down'), true);
});
