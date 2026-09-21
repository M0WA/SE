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

test('clicking "Add user" opens a blank form with username editable', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  document.getElementById('add-user-btn').dispatchEvent(new window.Event('click'));
  assert.equal(document.getElementById('user-form-panel').hidden, false);
  assert.equal(document.getElementById('user-form-title').textContent, 'Add user');
  assert.equal(document.getElementById('user-username').value, '');
  assert.equal(document.getElementById('user-username').disabled, false);
  assert.equal(document.getElementById('user-password').value, '');
  assert.equal(document.getElementById('user-password-label').textContent, 'Password');
});

test('clicking "Reset password" opens the form pre-filled with that user\'s username, locked', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseUser()] }));
  await flush();
  document.querySelector('#users-table button.text-button').dispatchEvent(new window.Event('click'));
  assert.equal(document.getElementById('user-form-panel').hidden, false);
  assert.equal(document.getElementById('user-form-title').textContent, 'Reset password for "alice"');
  assert.equal(document.getElementById('user-username').value, 'alice');
  assert.equal(document.getElementById('user-username').disabled, true);
  assert.equal(document.getElementById('user-password').value, '');
  assert.equal(document.getElementById('user-password-label').textContent, 'New password');
});

test('clicking Cancel hides the form and re-enables the username field', async () => {
  const { openResetForm, closeForm } = loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  openResetForm(baseUser());
  assert.equal(document.getElementById('user-username').disabled, true);
  document.getElementById('user-cancel-btn').dispatchEvent(new window.Event('click'));
  assert.equal(document.getElementById('user-form-panel').hidden, true);
  assert.equal(document.getElementById('user-username').disabled, false);
  closeForm();
});

test('submitting the form with no user being edited POSTs a new user with username and password, then reloads the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  document.getElementById('add-user-btn').dispatchEvent(new window.Event('click'));
  document.getElementById('user-username').value = 'newuser';
  document.getElementById('user-password').value = 'a-strong-password';

  let gotURL, gotOpts;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') {
      gotURL = url;
      gotOpts = opts;
      return { ok: true, json: async () => baseUser({ id: 'user_newuser', username: 'newuser' }) };
    }
    return { ok: true, json: async () => [baseUser({ id: 'user_newuser', username: 'newuser' })] };
  };
  document.getElementById('user-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();

  assert.equal(gotURL, '/admin/api/users');
  const body = JSON.parse(gotOpts.body);
  assert.equal(body.username, 'newuser');
  assert.equal(body.password, 'a-strong-password');
  assert.equal(document.getElementById('user-form-panel').hidden, true);
});

test('submitting the form while resetting a password PATCHes that user\'s id with only the new password, then reloads the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseUser()] }));
  await flush();
  document.querySelector('#users-table button.text-button').dispatchEvent(new window.Event('click'));
  document.getElementById('user-password').value = 'a-new-password';

  let gotURL, gotOpts;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'PATCH') {
      gotURL = url;
      gotOpts = opts;
      return { ok: true, json: async () => baseUser() };
    }
    return { ok: true, json: async () => [baseUser()] };
  };
  document.getElementById('user-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();

  assert.equal(gotURL, '/admin/api/users/user_alice');
  const body = JSON.parse(gotOpts.body);
  assert.equal(body.password, 'a-new-password');
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'username'), false);
  assert.equal(document.getElementById('user-form-panel').hidden, true);
});

test('a failed save shows an error message and re-enables the button, without closing the form', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  document.getElementById('add-user-btn').dispatchEvent(new window.Event('click'));
  document.getElementById('user-username').value = 'x';
  document.getElementById('user-password').value = 'short';

  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') return { ok: false, status: 400, text: async () => 'password too short' };
    return { ok: true, json: async () => [] };
  };
  document.getElementById('user-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();

  assert.equal(document.getElementById('user-form-status').textContent.includes('password too short'), true);
  assert.equal(document.getElementById('user-form-panel').hidden, false);
  assert.equal(document.getElementById('user-save-btn').disabled, false);
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
  document.querySelectorAll('#users-table button.text-button')[1].dispatchEvent(new window.Event('click'));
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
  document.querySelectorAll('#users-table button.text-button')[1].dispatchEvent(new window.Event('click'));
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
  document.querySelectorAll('#users-table button.text-button')[1].dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(alertMessage.includes('db down'), true);
});
