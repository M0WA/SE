'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const UPLOAD_HTML = fs.readFileSync(path.join(__dirname, 'admin_document_upload.html'), 'utf8');

function baseJob(overrides) {
  return Object.assign({
    id: 'docjob-1',
    filename: 'notes.txt',
    content_type: 'text/plain',
    size: 42,
    source: 'upload',
    index_vocabulary: true,
    status: 'done',
    created_at: '2026-01-02T03:04:05Z',
  }, overrides);
}

function loadFixture(fetchImpl) {
  setupDOM(UPLOAD_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => [] }));
  return Object.assign({}, adminHelpers, requireFresh('./admin_document_upload.js'));
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('jobSourceLabel maps upload/s3 to their display labels', () => {
  const { jobSourceLabel } = loadFixture();
  assert.equal(jobSourceLabel('upload'), 'Upload');
  assert.equal(jobSourceLabel('s3'), 'S3 import');
});

test('renderJobs shows a status message and no table when there are none', () => {
  const { renderJobs } = loadFixture();
  renderJobs([]);
  assert.equal(document.getElementById('jobs-status').textContent.includes('No uploads yet'), true);
  assert.equal(document.getElementById('jobs-table').querySelector('table'), null);
});

test('renderJobs lists every column plus a View/Delete actions cell', () => {
  const { renderJobs } = loadFixture();
  renderJobs([baseJob(), baseJob({ id: 'docjob-2', filename: 'photo.png', source: 's3' })]);
  const headers = Array.from(document.querySelectorAll('#jobs-table th')).map((th) => th.textContent);
  assert.deepEqual(headers, ['filename', 'content type', 'size', 'source', 'status', 'created', '']);
  const rows = document.querySelectorAll('#jobs-table tbody tr');
  assert.equal(rows.length, 2);
  assert.equal(rows[1].children[3].textContent, 'S3 import');
  const viewLink = rows[0].querySelector('a.text-button');
  assert.equal(viewLink.getAttribute('href'), '/admin/document-upload/docjob-1');
  assert.equal(document.getElementById('jobs-status').textContent, '');
});

test('loadJobs reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db down' }));
  await flush();
  assert.equal(document.getElementById('jobs-status').textContent.includes('db down'), true);
});

test('deleteJob does nothing when the confirm dialog is declined', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseJob()] }));
  await flush();
  window.confirm = () => false;
  let deleteCalled = false;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return { ok: true, json: async () => [] };
  };
  document.querySelector('#jobs-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);
});

test('deleteJob removes the job when confirmed, then reloads the list', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseJob()] }));
  await flush();
  window.confirm = () => true;
  let deletedURL = null;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') {
      deletedURL = url;
      return { ok: true, json: async () => ({ ok: true }) };
    }
    return { ok: true, json: async () => [] };
  };
  document.querySelector('#jobs-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deletedURL, '/admin/api/document-jobs/docjob-1');
  assert.equal(document.getElementById('jobs-status').textContent.includes('No uploads yet'), true);
});

test('deleteJob alerts on failure', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [baseJob()] }));
  await flush();
  window.confirm = () => true;
  let alertMsg = null;
  window.alert = (msg) => { alertMsg = msg; };
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'in use' };
    return { ok: true, json: async () => [baseJob()] };
  };
  document.querySelector('#jobs-table button.text-button').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(alertMsg, 'Could not delete: in use');
});

test('submitting the upload form posts the file and vocabulary flag as multipart form data', async () => {
  loadFixture();
  await flush();
  const input = document.getElementById('upload-file');
  const file = new window.File(['hello world'], 'hello.txt', { type: 'text/plain' });
  Object.defineProperty(input, 'files', { value: [file], configurable: true });

  let gotURL, gotBody;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') {
      gotURL = url;
      gotBody = opts.body;
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => [] };
  };
  document.getElementById('upload-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(gotURL, '/admin/api/document-jobs');
  assert.equal(gotBody instanceof FormData, true);
  assert.equal(document.getElementById('upload-status').textContent.includes('Uploaded'), true);
});

test('a failed upload shows the error status instead of clearing the form', async () => {
  loadFixture();
  await flush();
  const input = document.getElementById('upload-file');
  const file = new window.File(['x'], 'x.txt', { type: 'text/plain' });
  Object.defineProperty(input, 'files', { value: [file], configurable: true });
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') return { ok: false, status: 400, text: async () => 'unsupported file type' };
    return { ok: true, json: async () => [] };
  };
  document.getElementById('upload-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(document.getElementById('upload-status').textContent, 'Could not upload: unsupported file type');
});

test('submitting the S3 import form posts the trimmed field values as JSON', async () => {
  loadFixture();
  await flush();
  document.getElementById('s3-region').value = ' eu-central-1 ';
  document.getElementById('s3-bucket').value = ' my-bucket ';
  document.getElementById('s3-key').value = ' docs/report.txt ';
  document.getElementById('s3-access-key-id').value = ' AKIAEXAMPLE ';
  document.getElementById('s3-secret-access-key').value = 'secret';
  let gotURL, gotBody;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') {
      gotURL = url;
      gotBody = JSON.parse(opts.body);
      return { ok: true, json: async () => ({}) };
    }
    return { ok: true, json: async () => [] };
  };
  document.getElementById('s3-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(gotURL, '/admin/api/document-jobs/import-s3');
  assert.equal(gotBody.region, 'eu-central-1');
  assert.equal(gotBody.bucket, 'my-bucket');
  assert.equal(gotBody.key, 'docs/report.txt');
  assert.equal(gotBody.access_key_id, 'AKIAEXAMPLE');
  assert.equal(gotBody.secret_access_key, 'secret');
  assert.equal(gotBody.index_vocabulary, true);
  assert.equal(document.getElementById('s3-status').textContent.includes('Imported'), true);
});

test('a failed S3 import shows the error status', async () => {
  loadFixture();
  await flush();
  document.getElementById('s3-region').value = 'eu-central-1';
  document.getElementById('s3-bucket').value = 'my-bucket';
  document.getElementById('s3-key').value = 'docs/report.txt';
  document.getElementById('s3-access-key-id').value = 'AKIAEXAMPLE';
  document.getElementById('s3-secret-access-key').value = 'secret';
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'POST') return { ok: false, status: 502, text: async () => 'fetching from S3: access denied' };
    return { ok: true, json: async () => [] };
  };
  document.getElementById('s3-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await flush();
  assert.equal(document.getElementById('s3-status').textContent, 'Could not import: fetching from S3: access denied');
});
