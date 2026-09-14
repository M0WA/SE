'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const LOGIN_HTML = fs.readFileSync(path.join(__dirname, 'login.html'), 'utf8');

function loadFixture(url) {
  const dom = setupDOM(LOGIN_HTML);
  if (url) dom.reconfigure({ url });
  return requireFresh('./login.js');
}

function submit() {
  document.getElementById('login-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('exports the "next" redirect target parsed from the URL, defaulting to /admin', () => {
  const { next } = loadFixture();
  assert.equal(next, '/admin');
  const { next: withNext } = loadFixture('http://localhost/login?next=/admin/settings');
  assert.equal(withNext, '/admin/settings');
});

test('submitting posts the username/password/next as JSON', async () => {
  let gotURL, gotOpts;
  global.fetch = async (url, opts) => {
    gotURL = url;
    gotOpts = opts;
    return { ok: true, json: async () => ({ redirect: '/admin' }) };
  };
  loadFixture('http://localhost/login?next=/admin/crawl');
  document.getElementById('username').value = 'alice';
  document.getElementById('password').value = 'hunter2';
  submit();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(gotURL, '/login');
  assert.equal(gotOpts.method, 'POST');
  assert.equal(gotOpts.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(gotOpts.body), { username: 'alice', password: 'hunter2', next: '/admin/crawl' });
});

test('a 401 response shows an incorrect-credentials message and clears/refocuses the password field', async () => {
  global.fetch = async () => ({ ok: false, status: 401 });
  loadFixture();
  document.getElementById('username').value = 'alice';
  document.getElementById('password').value = 'wrong';
  submit();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('login-error').textContent, 'Incorrect username or password.');
  assert.equal(document.getElementById('password').value, '');
  assert.equal(document.activeElement, document.getElementById('password'));
});

test('a non-401 error response shows a generic sign-in-failed message', async () => {
  global.fetch = async () => ({ ok: false, status: 500 });
  loadFixture();
  submit();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('login-error').textContent, 'Sign-in failed: could not reach the server.');
});

test('a network failure (fetch throws) shows the same generic message', async () => {
  global.fetch = async () => { throw new Error('network down'); };
  loadFixture();
  submit();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('login-error').textContent, 'Sign-in failed: could not reach the server.');
});

test('resubmitting clears any previous error message first', async () => {
  global.fetch = async () => ({ ok: false, status: 401 });
  loadFixture();
  submit();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.notEqual(document.getElementById('login-error').textContent, '');
  global.fetch = async () => ({ ok: true, json: async () => ({}) });
  submit();
  assert.equal(document.getElementById('login-error').textContent, '');
});
