'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const ACCOUNT_HTML = fs.readFileSync(path.join(__dirname, 'account.html'), 'utf8');

function loadFixture(fetchImpl) {
  setupDOM(ACCOUNT_HTML);
  global.fetch = fetchImpl || (async () => ({
    ok: true, json: async () => ({ username: 'alice', custom_prompt: '' }),
  }));
  return requireFresh('./account.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('loadAccount populates username and custom prompt, and reveals the panel', async () => {
  loadFixture(async () => ({
    ok: true, json: async () => ({ username: 'alice', custom_prompt: 'Be terse.' }),
  }));
  await flush();
  assert.equal(document.getElementById('account-username').value, 'alice');
  assert.equal(document.getElementById('account-custom-prompt').value, 'Be terse.');
  assert.equal(document.getElementById('account-panel').hidden, false);
  assert.equal(document.getElementById('account-status').textContent, '');
});

test('loadAccount leaves the custom prompt field empty when custom_prompt is empty', async () => {
  loadFixture(async () => ({
    ok: true, json: async () => ({ username: 'alice', custom_prompt: '' }),
  }));
  await flush();
  assert.equal(document.getElementById('account-custom-prompt').value, '');
});

test('loadAccount reports the error message on a failed fetch and keeps the panel hidden', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db down' }));
  await flush();
  assert.equal(document.getElementById('account-status').textContent.includes('db down'), true);
  assert.equal(document.getElementById('account-panel').hidden, true);
});

test('submitting with a blank password sends only custom_prompt', async () => {
  loadFixture();
  await flush();
  let sentURL, sentOpts;
  global.fetch = async (url, opts) => {
    sentURL = url;
    sentOpts = opts;
    return { ok: true, json: async () => ({ username: 'alice', custom_prompt: 'new prompt' }) };
  };
  document.getElementById('account-custom-prompt').value = 'new prompt';
  document.getElementById('account-form').dispatchEvent(new window.Event('submit'));
  await flush();

  assert.equal(sentURL, '/account/api');
  assert.equal(sentOpts.method, 'PATCH');
  const body = JSON.parse(sentOpts.body);
  assert.deepEqual(body, { custom_prompt: 'new prompt' });
  assert.equal(document.getElementById('account-form-status').textContent, 'Saved.');
});

test('submitting with a non-blank password includes it, and clears the field on success', async () => {
  loadFixture();
  await flush();
  let sentOpts;
  global.fetch = async (url, opts) => {
    sentOpts = opts;
    return { ok: true, json: async () => ({ username: 'alice', custom_prompt: '' }) };
  };
  document.getElementById('account-password').value = 'a-new-password';
  document.getElementById('account-form').dispatchEvent(new window.Event('submit'));
  await flush();

  const body = JSON.parse(sentOpts.body);
  assert.equal(body.password, 'a-new-password');
  assert.equal(document.getElementById('account-password').value, '');
});

test('a failed save reports the error message and leaves the password field untouched', async () => {
  loadFixture();
  await flush();
  global.fetch = async () => ({ ok: false, status: 400, text: async () => 'password too short' });
  document.getElementById('account-password').value = 'short';
  document.getElementById('account-form').dispatchEvent(new window.Event('submit'));
  await flush();

  assert.equal(document.getElementById('account-form-status').textContent.includes('password too short'), true);
  assert.equal(document.getElementById('account-password').value, 'short');
});

test('sign-out posts to /logout on click', async () => {
  loadFixture();
  await flush();
  let fetchedURL, fetchedOpts;
  global.fetch = async (url, opts) => {
    fetchedURL = url;
    fetchedOpts = opts;
    return { ok: true };
  };
  document.getElementById('sign-out').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(fetchedURL, '/logout');
  assert.equal(fetchedOpts.method, 'POST');
});
