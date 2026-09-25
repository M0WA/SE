'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const DETAIL_HTML = fs.readFileSync(path.join(__dirname, 'admin_document_detail.html'), 'utf8');

function baseJob(overrides) {
  return Object.assign({
    id: 'docjob-1',
    filename: 'notes.txt',
    content_type: 'text/plain',
    size: 42,
    source: 'upload',
    index_vocabulary: true,
    status: 'done',
    doc_id: 'doc-docjob-1',
    created_at: '2026-01-02T03:04:05Z',
  }, overrides);
}

function loadFixture(url, fetchImpl) {
  setupDOM(DETAIL_HTML, url || 'http://x/admin/document-upload/docjob-1');
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => ({}) }));
  return Object.assign({}, adminHelpers, requireFresh('./admin_document_detail.js'));
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(async () => {
  await flush();
  teardownDOM();
  delete global.fetch;
});

test('documentJobIDFromPath reads the last path segment', () => {
  const { documentJobIDFromPath } = loadFixture('http://x/admin/document-upload/docjob-42');
  assert.equal(documentJobIDFromPath(), 'docjob-42');
});

test('renderJobMeta shows every field, skipping error/doc_id/started/finished when absent', () => {
  const { renderJobMeta } = loadFixture();
  renderJobMeta(baseJob({ error: '', doc_id: '', started_at: null, finished_at: null }));
  const container = document.getElementById('job-meta');
  const keys = Array.from(container.querySelectorAll('.k')).map((k) => k.textContent);
  assert.deepEqual(keys, ['Filename', 'Content type', 'Size', 'Source', 'Vocabulary indexed', 'Status', 'Created']);
});

test('renderJobMeta includes Error/Document ID/Started/Finished when present', () => {
  const { renderJobMeta } = loadFixture();
  renderJobMeta(baseJob({ error: 'boom', started_at: '2026-01-02T03:05:00Z', finished_at: '2026-01-02T03:06:00Z' }));
  const container = document.getElementById('job-meta');
  const keys = Array.from(container.querySelectorAll('.k')).map((k) => k.textContent);
  assert.deepEqual(keys, ['Filename', 'Content type', 'Size', 'Source', 'Vocabulary indexed', 'Status', 'Error', 'Document ID', 'Created', 'Started', 'Finished']);
});

test('loadJobDetail shows the extracted text for a done text job', async () => {
  loadFixture('http://x/admin/document-upload/docjob-1', async (url) => {
    if (url.includes('/admin/api/document-jobs/docjob-1')) return { ok: true, json: async () => baseJob() };
    if (url.includes('/admin/api/documents/doc-docjob-1')) return { ok: true, json: async () => ({ id: 'doc-docjob-1', text: 'hello world' }) };
    return { ok: false, status: 404, text: async () => 'not found' };
  });
  const { loadJobDetail, documentJobIDFromPath } = require('./admin_document_detail.js');
  await loadJobDetail(documentJobIDFromPath());
  assert.equal(document.getElementById('job-title').textContent, 'notes.txt');
  assert.equal(document.getElementById('text-preview-wrap').hidden, false);
  assert.equal(document.getElementById('text-preview').textContent, 'hello world');
  assert.equal(document.getElementById('image-preview-wrap').hidden, true);
});

test('loadJobDetail shows an image preview for an image job, without fetching document text', async () => {
  let fetchedDocumentText = false;
  loadFixture('http://x/admin/document-upload/docjob-2', async (url) => {
    if (url.includes('/admin/api/document-jobs/docjob-2')) {
      return { ok: true, json: async () => baseJob({ id: 'docjob-2', filename: 'photo.png', content_type: 'image/png', doc_id: 'doc-docjob-2' }) };
    }
    if (url.includes('/admin/api/documents/')) fetchedDocumentText = true;
    return { ok: true, json: async () => ({}) };
  });
  const { loadJobDetail, documentJobIDFromPath } = require('./admin_document_detail.js');
  await loadJobDetail(documentJobIDFromPath());
  assert.equal(document.getElementById('image-preview-wrap').hidden, false);
  assert.equal(document.getElementById('image-preview').getAttribute('src'), '/admin/api/document-jobs/docjob-2/data');
  assert.equal(fetchedDocumentText, false);
});

test('loadJobDetail leaves both preview sections hidden for a job that has not finished indexing', async () => {
  loadFixture('http://x/admin/document-upload/docjob-3', async (url) => {
    if (url.includes('/admin/api/document-jobs/docjob-3')) {
      return { ok: true, json: async () => baseJob({ id: 'docjob-3', status: 'running', doc_id: '' }) };
    }
    return { ok: true, json: async () => ({}) };
  });
  const { loadJobDetail, documentJobIDFromPath } = require('./admin_document_detail.js');
  await loadJobDetail(documentJobIDFromPath());
  assert.equal(document.getElementById('text-preview-wrap').hidden, true);
  assert.equal(document.getElementById('image-preview-wrap').hidden, true);
});

test('loadJobDetail tolerates the indexed document having since been deleted', async () => {
  loadFixture('http://x/admin/document-upload/docjob-4', async (url) => {
    if (url.includes('/admin/api/document-jobs/docjob-4')) return { ok: true, json: async () => baseJob({ id: 'docjob-4' }) };
    if (url.includes('/admin/api/documents/')) return { ok: false, status: 404, text: async () => 'document not found' };
    return { ok: true, json: async () => ({}) };
  });
  const { loadJobDetail, documentJobIDFromPath } = require('./admin_document_detail.js');
  await loadJobDetail(documentJobIDFromPath());
  assert.equal(document.getElementById('text-preview-wrap').hidden, true);
  assert.equal(document.getElementById('job-status').textContent, '');
});

test('loadJobDetail reports the error message on a failed fetch', async () => {
  loadFixture('http://x/admin/document-upload/missing', async () => ({ ok: false, status: 404, text: async () => 'document job not found' }));
  const { loadJobDetail, documentJobIDFromPath } = require('./admin_document_detail.js');
  await loadJobDetail(documentJobIDFromPath());
  assert.equal(document.getElementById('job-status').textContent, 'Could not load document job: document job not found');
});

test('the Delete button does nothing when declined, deletes and redirects when confirmed', async () => {
  loadFixture('http://x/admin/document-upload/docjob-1', async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: true, json: async () => ({ ok: true }) };
    if (url.includes('/admin/api/document-jobs/docjob-1')) return { ok: true, json: async () => baseJob({ doc_id: '', status: 'queued' }) };
    return { ok: true, json: async () => ({}) };
  });
  const { loadJobDetail, documentJobIDFromPath } = require('./admin_document_detail.js');
  await loadJobDetail(documentJobIDFromPath());

  window.confirm = () => false;
  let deleteCalled = false;
  const originalFetch = global.fetch;
  global.fetch = async (url, opts) => {
    if (opts && opts.method === 'DELETE') deleteCalled = true;
    return originalFetch(url, opts);
  };
  document.getElementById('delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, false);

  window.confirm = () => true;
  document.getElementById('delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(deleteCalled, true);
});

test('the Delete button alerts on failure', async () => {
  loadFixture('http://x/admin/document-upload/docjob-1', async (url, opts) => {
    if (opts && opts.method === 'DELETE') return { ok: false, status: 500, text: async () => 'in use' };
    if (url.includes('/admin/api/document-jobs/docjob-1')) return { ok: true, json: async () => baseJob({ doc_id: '', status: 'queued' }) };
    return { ok: true, json: async () => ({}) };
  });
  const { loadJobDetail, documentJobIDFromPath } = require('./admin_document_detail.js');
  await loadJobDetail(documentJobIDFromPath());

  window.confirm = () => true;
  let alertMsg = null;
  window.alert = (msg) => { alertMsg = msg; };
  document.getElementById('delete-btn').dispatchEvent(new window.Event('click'));
  await flush();
  assert.equal(alertMsg, 'Could not delete: in use');
});
