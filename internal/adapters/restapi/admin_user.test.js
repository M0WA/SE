'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('jsdom');
const { teardownDOM, requireFresh } = require('./dom_helper.test_util');

const USER_HTML = fs.readFileSync(path.join(__dirname, 'admin_user.html'), 'utf8');

// Reads user id (or "new") from window.location.pathname at load time, so needs a jsdom instance
// with a specific URL -- same as admin_embedding_endpoint.test.js's setupEndpointDOM.
function setupUserDOM(id) {
  const dom = new JSDOM(USER_HTML, { url: 'http://localhost/admin/users/' + encodeURIComponent(id) });
  global.window = dom.window;
  global.document = dom.window.document;
  Object.defineProperty(global, 'navigator', {
    value: dom.window.navigator, configurable: true, writable: true,
  });
  return dom;
}

function baseUser(overrides) {
  return Object.assign({
    id: 'user_alice',
    username: 'alice',
    is_admin: false,
    custom_prompt: '',
    created_at: '2026-01-02T03:04:05Z',
    updated_at: '2026-01-02T03:04:05Z',
  }, overrides);
}

function loadFixture(id, fetchImpl) {
  setupUserDOM(id || 'user_alice');
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => baseUser() }));
  return requireFresh('./admin_user.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('load() applies the fetched user to the form and reveals it', async () => {
  loadFixture('user_alice', async (url) => {
    assert.equal(url, '/admin/api/users/user_alice');
    return { ok: true, json: async () => baseUser({ custom_prompt: 'Be terse.', is_admin: true }) };
  });
  await flush();
  assert.equal(document.getElementById('user-title').textContent, 'alice');
  assert.equal(document.getElementById('user-form').hidden, false);
  assert.equal(document.getElementById('user-username').value, 'alice');
  assert.equal(document.getElementById('user-username').disabled, true);
  assert.equal(document.getElementById('user-password').value, '');
  assert.equal(document.getElementById('user-is-admin').checked, true);
  assert.equal(document.getElementById('user-custom-prompt').value, 'Be terse.');
  assert.equal(document.getElementById('user-delete-btn').hidden, false);
  const meta = document.getElementById('user-meta').textContent;
  assert.equal(meta.includes('user_alice'), true);
});

// The user-edit subpage must show the same admin nav rail as every other admin page (unlike a
// plain back-link drill-down page) -- originally omitted here (mirroring
// admin_embedding_endpoint.html), a reported UX gap.
test('renders the admin nav rail on load', async () => {
  loadFixture('user_alice', async () => ({ ok: true, json: async () => baseUser() }));
  await flush();
  const rail = document.getElementById('admin-rail');
  assert.notEqual(rail.querySelector('a'), null);
});

test('load() shows "Not found" and the error message on failure', async () => {
  loadFixture('user_alice', async () => ({ ok: false, status: 404, text: async () => 'no such user' }));
  await flush();
  assert.equal(document.getElementById('user-title').textContent, 'Not found');
  assert.equal(document.getElementById('user-status').textContent.includes('no such user'), true);
  assert.equal(document.getElementById('user-form').hidden, true);
});

test('"new" mode shows an empty form without loading or fetching, and hides delete', async () => {
  let fetched = false;
  loadFixture('new', async () => { fetched = true; return { ok: true, json: async () => baseUser() }; });
  await flush();
  assert.equal(fetched, false);
  assert.equal(document.getElementById('user-title').textContent, 'Add user');
  assert.equal(document.getElementById('user-form').hidden, false);
  assert.equal(document.getElementById('user-username').disabled, false);
  assert.equal(document.getElementById('user-password').required, true);
  assert.equal(document.getElementById('user-delete-btn').hidden, true);
});

test('requestBody in "new" mode includes username, password, and is_admin', async () => {
  loadFixture('new');
  await flush();
  document.getElementById('user-username').value = 'newuser';
  document.getElementById('user-password').value = 'a-strong-password';
  document.getElementById('user-is-admin').checked = true;
  document.getElementById('user-custom-prompt').value = 'Answer briefly.';
  const { requestBody } = require('./admin_user.js');
  const body = requestBody();
  assert.equal(body.username, 'newuser');
  assert.equal(body.password, 'a-strong-password');
  assert.equal(body.is_admin, true);
  assert.equal(body.custom_prompt, 'Answer briefly.');
});

test('requestBody in edit mode omits password when left blank, but always includes custom_prompt', async () => {
  loadFixture('user_alice', async () => ({ ok: true, json: async () => baseUser({ custom_prompt: 'Old prompt.' }) }));
  await flush();
  document.getElementById('user-custom-prompt').value = '';
  const { requestBody } = require('./admin_user.js');
  const body = requestBody();
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'password'), false);
  assert.equal(Object.prototype.hasOwnProperty.call(body, 'username'), false);
  assert.equal(body.custom_prompt, '');
});

test('requestBody in edit mode includes password when typed', async () => {
  loadFixture('user_alice', async () => ({ ok: true, json: async () => baseUser() }));
  await flush();
  document.getElementById('user-password').value = 'a-new-password';
  const { requestBody } = require('./admin_user.js');
  assert.equal(requestBody().password, 'a-new-password');
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
      return { ok: true, json: async () => baseUser({ id: 'user_newuser', username: 'newuser' }) };
    }
    return { ok: true, json: async () => baseUser() };
  };
  document.getElementById('user-username').value = 'newuser';
  document.getElementById('user-password').value = 'a-strong-password';
  document.getElementById('user-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(postedURL, '/admin/api/users');
  assert.equal(postedBody.username, 'newuser');
});

test('submitting in edit mode patches the specific user', async () => {
  let patchedURL = null;
  loadFixture('user_alice', async () => ({ ok: true, json: async () => baseUser() }));
  await flush();
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'PATCH') {
      patchedURL = url;
      return { ok: true, json: async () => baseUser() };
    }
    return { ok: true, json: async () => baseUser() };
  };
  document.getElementById('user-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(patchedURL, '/admin/api/users/user_alice');
  assert.equal(document.getElementById('user-form-status').textContent, 'Saved.');
});

test('submitting reports the error message on failure', async () => {
  loadFixture('user_alice', async () => ({ ok: true, json: async () => baseUser() }));
  await flush();
  global.fetch = async () => ({ ok: false, status: 400, text: async () => 'password too short' });
  document.getElementById('user-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(document.getElementById('user-form-status').textContent, 'Could not save: password too short');
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture('user_alice', async () => ({ ok: true, json: async () => baseUser() }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => ({}) };
  };
  document.getElementById('user-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('clicking Delete removes the user when confirmed and redirects to the list', async () => {
  let deletedURL = null;
  loadFixture('user_alice', async () => ({ ok: true, json: async () => baseUser() }));
  await flush();
  window.confirm = () => true;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') {
      deletedURL = url;
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => baseUser() };
  };
  document.getElementById('user-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deletedURL, '/admin/api/users/user_alice');
});

test('clicking Delete re-enables the button and alerts on failure', async () => {
  loadFixture('user_alice', async () => ({ ok: true, json: async () => baseUser() }));
  await flush();
  window.confirm = () => true;
  let alertMsg = null;
  window.alert = (msg) => { alertMsg = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'in use' };
    return { ok: true, json: async () => baseUser() };
  };
  document.getElementById('user-delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(document.getElementById('user-delete-btn').disabled, false);
  assert.equal(alertMsg, 'Could not delete: in use');
});
