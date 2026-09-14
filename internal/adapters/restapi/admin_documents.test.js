'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const DOCUMENTS_HTML = fs.readFileSync(path.join(__dirname, 'admin_documents.html'), 'utf8');

function loadFixture() {
  setupDOM(DOCUMENTS_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async (url) => {
    if (url.includes('/admin/api/vocabulary')) {
      return { ok: true, json: async () => ({ vocabulary_size: 0, matched_count: 0, terms: [] }) };
    }
    return { ok: true, json: async () => ([]) };
  };
  return requireFresh('./admin_documents.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('hostOf extracts the host from a valid URL', () => {
  const { hostOf } = loadFixture();
  assert.equal(hostOf('https://example.com/page'), 'example.com');
});

test('hostOf returns an empty string for an invalid URL', () => {
  const { hostOf } = loadFixture();
  assert.equal(hostOf('not a url'), '');
});

test('renderDomainResults shows a no-match message for an empty list', () => {
  const { renderDomainResults } = loadFixture();
  renderDomainResults([], 'zz');
  assert.equal(document.getElementById('domain-results').textContent, 'No domain matches “zz”.');
});

test('renderDomainResults renders one row per domain with a pluralized page count', () => {
  const { renderDomainResults } = loadFixture();
  renderDomainResults([{ host: 'a.example', doc_count: 1 }, { host: 'b.example', doc_count: 3 }], 'example');
  const rows = document.querySelectorAll('#domain-results .domain-result');
  assert.equal(rows.length, 2);
  assert.equal(rows[0].getAttribute('href'), '/admin/documents/a.example');
  assert.equal(rows[0].querySelector('.domain-metrics').textContent, '1 page');
  assert.equal(rows[1].querySelector('.domain-metrics').textContent, '3 pages');
});

test('renderDocumentResults shows a no-match message for an empty list', () => {
  const { renderDocumentResults } = loadFixture();
  renderDocumentResults([], 'zz');
  assert.equal(document.getElementById('document-results').textContent, 'No document matches “zz”.');
});

test('renderDocumentResults renders a table row per document, deriving the host column', () => {
  const { renderDocumentResults } = loadFixture();
  renderDocumentResults([{ url: 'https://example.com/x', title: 'Hello' }], 'hello');
  const rows = document.querySelectorAll('#document-results tbody tr');
  assert.equal(rows.length, 1);
  assert.equal(rows[0].children[2].textContent, 'example.com');
});

test('searchDomains clears results for a blank query', async () => {
  const { searchDomains } = loadFixture();
  document.getElementById('domain-results').textContent = 'stale';
  document.getElementById('domain-q').value = '   ';
  await searchDomains();
  assert.equal(document.getElementById('domain-results').textContent, '');
});

test('searchDomains reports an invalid regex pattern without touching the network', async () => {
  const { searchDomains } = loadFixture();
  document.getElementById('domain-q').value = '(';
  await searchDomains();
  assert.equal(document.getElementById('domain-search-error').textContent.includes('Invalid pattern'), true);
});

test('searchDomains filters the fetched-once domain/document candidates by the query', async () => {
  const { searchDomains } = loadFixture();
  global.fetch = async (url) => {
    if (url.includes('/admin/api/domains')) {
      return { ok: true, json: async () => ([{ host: 'match.example', doc_count: 2 }, { host: 'other.test', doc_count: 1 }]) };
    }
    return { ok: true, json: async () => ([{ url: 'https://match.example/a', title: 'hit' }]) };
  };
  document.getElementById('domain-q').value = 'match';
  await searchDomains();
  assert.equal(document.getElementById('domain-results').querySelectorAll('.domain-result').length, 1);
  assert.equal(document.getElementById('document-results').querySelectorAll('tbody tr').length, 1);
});

test('searchDomains reports a fetch error', async () => {
  const { searchDomains } = loadFixture();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'boom' });
  document.getElementById('domain-q').value = 'x';
  await searchDomains();
  assert.equal(document.getElementById('domain-search-error').textContent.includes('boom'), true);
});

test('typing into the domain search box debounces and triggers a search', async () => {
  loadFixture();
  let called = false;
  global.fetch = async (url) => {
    called = true;
    if (url.includes('/admin/api/domains')) return { ok: true, json: async () => ([]) };
    return { ok: true, json: async () => ([]) };
  };
  document.getElementById('domain-q').value = 'x';
  document.getElementById('domain-q').dispatchEvent(new window.Event('input'));
  await new Promise((resolve) => setTimeout(resolve, 250));
  assert.equal(called, true);
});

test('submitting the domain search form triggers a search', async () => {
  loadFixture();
  let called = false;
  global.fetch = async (url) => {
    called = true;
    if (url.includes('/admin/api/domains')) return { ok: true, json: async () => ([]) };
    return { ok: true, json: async () => ([]) };
  };
  document.getElementById('domain-q').value = 'x';
  document.getElementById('domain-search-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(called, true);
});
