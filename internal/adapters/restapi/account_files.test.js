'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const ACCOUNT_FILES_HTML = fs.readFileSync(path.join(__dirname, 'account_files.html'), 'utf8');

function baseFile(overrides) {
  return Object.assign({
    id: 'f1',
    filename: 'notes.txt',
    content_type: 'text/plain',
    size: 5,
    created_at: '2026-01-01T00:00:00Z',
  }, overrides);
}

// This page's own script intentionally does NOT load admin.js -- same
// reasoning as account_mcp_servers.js's own comment.
function loadFixture(fetchImpl) {
  setupDOM(ACCOUNT_FILES_HTML);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => [] }));
  return requireFresh('./account_files.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('loadFiles shows a message and no table when there are none', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  await flush();
  assert.equal(document.getElementById('files-status').textContent.includes('No files yet'), true);
  assert.equal(document.getElementById('files-table').querySelector('table'), null);
});

test('renderFiles builds a row per file with its fields', () => {
  const { renderFiles } = loadFixture();
  renderFiles([baseFile(), baseFile({ id: 'f2', filename: 'other.csv', size: 2048 })]);
  const text = document.getElementById('files-table').textContent;
  assert.equal(text.includes('notes.txt'), true);
  assert.equal(text.includes('other.csv'), true);
  assert.equal(document.getElementById('files-status').textContent, '');
});

test('renderFiles renders a Download link pointing at the file\'s own URL', () => {
  const { renderFiles } = loadFixture();
  renderFiles([baseFile()]);
  const downloadLink = document.querySelector('#files-table a.text-button');
  assert.equal(downloadLink.textContent, 'Download');
  assert.equal(downloadLink.getAttribute('href'), '/account/api/files/f1');
});

test('formatSize renders bytes, KB, and MB scaled appropriately', () => {
  const { formatSize } = loadFixture();
  assert.equal(formatSize(500), '500 B');
  assert.equal(formatSize(2048), '2.0 KB');
  assert.equal(formatSize(5 * 1024 * 1024), '5.0 MB');
});

test('loadFiles reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db down' }));
  await flush();
  assert.equal(document.getElementById('files-status').textContent.includes('db down'), true);
});

test('clicking Delete does nothing when the confirm dialog is declined', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseFile()] }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => [] };
  };
  document.querySelector('#files-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('clicking Delete calls the DELETE endpoint and reloads the list when confirmed', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseFile()] }));
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
  document.querySelector('#files-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(gotURL, '/account/api/files/f1');
  assert.equal(gotMethod, 'DELETE');
});

test('a failed delete shows an alert and does not reload the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseFile()] }));
  await flush();
  window.confirm = () => true;
  let alertMessage = '';
  window.alert = (msg) => { alertMessage = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'db down' };
    return { ok: true, json: async () => [baseFile()] };
  };
  document.querySelector('#files-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(alertMessage.includes('db down'), true);
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
