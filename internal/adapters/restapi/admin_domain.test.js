'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('jsdom');
const { teardownDOM, requireFresh } = require('./dom_helper.test_util');

const DOMAIN_HTML = fs.readFileSync(path.join(__dirname, 'admin_domain.html'), 'utf8');

// admin_domain.js reads the domain host out of window.location.pathname at
// load time, so (unlike most other pages) its fixture needs a real
// pathname, not just a body -- dom_helper.test_util's setupDOM() always
// uses http://localhost/, so this constructs its own JSDOM directly instead.
function setupDOMAt(html, pathname) {
  const dom = new JSDOM(html, { url: 'http://localhost' + pathname });
  global.window = dom.window;
  global.document = dom.window.document;
  global.navigator = dom.window.navigator;
  return dom;
}

// fetchImpl is installed *before* requireFresh, because admin_domain.js
// calls load() itself at module load time -- setting global.fetch after
// requiring the module would race the module's own auto-triggered call
// instead of controlling it.
function loadFixture(host, fetchImpl) {
  setupDOMAt(DOMAIN_HTML, '/admin/documents/' + encodeURIComponent(host || 'example.com'));
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => ([]) }));
  return requireFresh('./admin_domain.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
  delete global.window?.confirm;
});

test('the host is parsed from the URL path and used as the page title', () => {
  loadFixture('example.com');
  assert.equal(document.getElementById('domain-title').textContent, 'example.com');
  assert.equal(document.title, 'se. — example.com');
});

test('filterDocs returns every doc unchanged for a blank pattern', () => {
  const { filterDocs } = loadFixture();
  const docs = [{ title: 'a', url: 'http://a' }];
  assert.equal(filterDocs(docs, ''), docs);
});

test('filterDocs matches title or url, case-insensitively', () => {
  const { filterDocs } = loadFixture();
  const docs = [{ title: 'Hello world', url: 'http://a' }, { title: 'other', url: 'http://b/hello' }];
  assert.deepEqual(filterDocs(docs, 'HELLO'), docs);
});

test('filterDocs reports an invalid regex and returns the unfiltered list', () => {
  const { filterDocs } = loadFixture();
  const docs = [{ title: 'a', url: 'http://a' }];
  assert.deepEqual(filterDocs(docs, '('), docs);
  assert.equal(document.getElementById('domain-filter-error').textContent.includes('Invalid pattern'), true);
});

test('buildChart renders one rect per document with a tooltip', () => {
  const { buildChart } = loadFixture();
  const svg = buildChart([{ title: 'A', url: 'http://a', doc_length: 100 }, { url: 'http://b', doc_length: 50 }]);
  const rects = svg.querySelectorAll('rect');
  assert.equal(rects.length, 2);
  assert.equal(rects[0].querySelector('title').textContent.includes('100 tokens indexed'), true);
  assert.equal(rects[1].querySelector('title').textContent.includes('http://b'), true);
});

test('buildChart falls back to a minimum width/scale for an empty document list', () => {
  const { buildChart } = loadFixture();
  const svg = buildChart([]);
  assert.equal(svg.querySelectorAll('rect').length, 0);
  assert.equal(svg.getAttribute('viewBox'), '0 0 1 56');
});

test('renderDocs shows a status message and hides filter/delete-all for an empty domain', () => {
  const { renderDocs } = loadFixture();
  renderDocs([]);
  assert.equal(document.getElementById('domain-status').textContent, 'No pages indexed for this domain.');
  assert.equal(document.getElementById('domain-filter').hidden, true);
  assert.equal(document.getElementById('delete-all').hidden, true);
});

test('renderDocs renders aggregate metrics, a crawl link, and the filtered table', () => {
  const { renderDocs } = loadFixture();
  renderDocs([
    { url: 'http://example.com/a', title: 'A', doc_length: 10, internal_links: 1, external_links: 2, backlinks: 3, pagerank: 0.1, version: 1 },
    { url: 'http://example.com/b', title: 'B', doc_length: 20, internal_links: 0, external_links: 0, backlinks: 0, pagerank: 0.2, version: 1 },
  ]);
  const tail = document.getElementById('domain-tail').textContent;
  assert.equal(tail.includes('2 pages'), true);
  assert.equal(tail.includes('total 30'), true);
  const crawlLink = document.querySelector('.domain-crawl-link');
  assert.equal(crawlLink.getAttribute('href'), '/admin/crawl?url=' + encodeURIComponent('http://example.com'));
  assert.equal(document.getElementById('domain-filter').hidden, false);
  assert.equal(document.getElementById('delete-all').hidden, false);
  assert.equal(document.getElementById('domain-table').querySelectorAll('tbody tr').length, 2);
});

test('renderTable shows a no-match message for an empty filtered list', () => {
  const { renderTable } = loadFixture();
  renderTable([]);
  assert.equal(document.getElementById('domain-table').textContent, 'No pages match that filter.');
});

test('renderTable renders a version button (not plain "v1") once a document has more than one version', () => {
  const { renderTable } = loadFixture();
  renderTable([{ url: 'http://a', title: 'A', doc_length: 5, internal_links: 0, external_links: 0, backlinks: 0, pagerank: 0, version: 3, id: 'doc-1' }]);
  const row = document.querySelector('#domain-table tbody tr');
  const versionCell = row.children[6];
  const btn = versionCell.querySelector('button');
  assert.equal(btn.textContent, 'v3');
});

test('typing into the domain filter re-renders the table against the current doc list', async () => {
  // Goes through load() (not a direct renderDocs() call) because the input
  // handler filters module-scope `allDocs`, which only load() populates.
  loadFixture('example.com', async () => ({
    ok: true,
    json: async () => ([
      { url: 'http://example.com/a', title: 'Alpha', doc_length: 1, internal_links: 0, external_links: 0, backlinks: 0, pagerank: 0, version: 1 },
      { url: 'http://example.com/b', title: 'Beta', doc_length: 1, internal_links: 0, external_links: 0, backlinks: 0, pagerank: 0, version: 1 },
    ]),
  }));
  await flush();
  const filterEl = document.getElementById('domain-filter');
  filterEl.value = 'Alpha';
  filterEl.dispatchEvent(new window.Event('input'));
  assert.equal(document.getElementById('domain-table').querySelectorAll('tbody tr').length, 1);
});

test('deleteDocument does nothing when the confirm dialog is declined', async () => {
  const { deleteDocument } = loadFixture();
  window.confirm = () => false;
  let called = false;
  global.fetch = async () => { called = true; return { ok: true, json: async () => ({}) }; };
  const tr = document.createElement('tr');
  const btn = document.createElement('button');
  tr.appendChild(btn);
  document.body.appendChild(tr);
  await deleteDocument('doc-1', btn);
  assert.equal(called, false);
});

test('deleteDocument removes the row on confirmed success', async () => {
  const { deleteDocument } = loadFixture();
  window.confirm = () => true;
  let gotURL, gotOpts;
  global.fetch = async (url, opts) => { gotURL = url; gotOpts = opts; return { ok: true, json: async () => ({}) }; };
  const tr = document.createElement('tr');
  const btn = document.createElement('button');
  tr.appendChild(btn);
  document.body.appendChild(tr);
  await deleteDocument('doc-1', btn);
  assert.equal(gotURL, '/admin/api/documents/doc-1');
  assert.equal(gotOpts.method, 'DELETE');
  assert.equal(document.body.contains(tr), false);
});

test('deleteDocument re-enables the button and alerts on a failed delete', async () => {
  const { deleteDocument } = loadFixture();
  window.confirm = () => true;
  window.alert = () => {};
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'boom' });
  const tr = document.createElement('tr');
  const btn = document.createElement('button');
  tr.appendChild(btn);
  document.body.appendChild(tr);
  await deleteDocument('doc-1', btn);
  assert.equal(btn.disabled, false);
  assert.equal(document.body.contains(tr), true);
});

test('deleteAllInDomain does nothing when the confirm dialog is declined', async () => {
  const { deleteAllInDomain } = loadFixture();
  window.confirm = () => false;
  let called = false;
  global.fetch = async () => { called = true; return { ok: true, json: async () => ({}) }; };
  await deleteAllInDomain(3);
  assert.equal(called, false);
});

test('deleteAllInDomain queues the removal and starts progress polling on confirmed success', async () => {
  const { deleteAllInDomain, stopDeleteProgressPolling } = loadFixture();
  window.confirm = () => true;
  let gotURL;
  global.fetch = async (url) => { gotURL = url; return { ok: true, json: async () => ({ queued: 3 }) }; };
  await deleteAllInDomain(3);
  assert.equal(gotURL, '/admin/api/documents?domain=example.com');
  assert.equal(document.getElementById('delete-all-status').textContent.includes('Removing 3 pages'), true);
  stopDeleteProgressPolling(); // avoid leaking the 1.5s progress-poll interval past this test
});

test('deleteAllInDomain reports an error and re-enables the button when the request fails', async () => {
  const { deleteAllInDomain } = loadFixture();
  window.confirm = () => true;
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'queue failed' });
  await deleteAllInDomain(2);
  assert.equal(document.getElementById('delete-all-status').textContent.includes('queue failed'), true);
  assert.equal(document.getElementById('delete-all').disabled, false);
});

test('pollDeleteProgress reports completion once every page is gone', async () => {
  const { pollDeleteProgress, stopDeleteProgressPolling } = loadFixture();
  global.fetch = async () => ({ ok: true, json: async () => ([]) });
  pollDeleteProgress(2);
  await new Promise((resolve) => setTimeout(resolve, 1600));
  assert.equal(document.getElementById('delete-all-status').textContent, 'Done — all 2 pages removed.');
  stopDeleteProgressPolling();
});

test('showHistory reports a no-prior-versions message', async () => {
  const { showHistory } = loadFixture();
  global.fetch = async () => ({ ok: true, json: async () => ([]) });
  await showHistory({ id: 'doc-1', title: 'A', url: 'http://a' });
  assert.equal(document.getElementById('history-panel').hidden, false);
  assert.equal(document.getElementById('history-body').textContent, 'No prior versions — this page has only been crawled once.');
});

test('showHistory renders a version table on success', async () => {
  const { showHistory } = loadFixture();
  global.fetch = async () => ({
    ok: true,
    json: async () => ([{ version: 1, title: 'Old', doc_length: 5, crawled_at: '2026-01-01T00:00:00Z' }]),
  });
  await showHistory({ id: 'doc-1', title: 'A', url: 'http://a' });
  assert.equal(document.getElementById('history-body').querySelectorAll('tbody tr').length, 1);
});

test('showHistory reports an error message on a failed fetch', async () => {
  const { showHistory } = loadFixture();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'history unavailable' });
  await showHistory({ id: 'doc-1', title: 'A', url: 'http://a' });
  assert.equal(document.getElementById('history-body').textContent.includes('history unavailable'), true);
});

test('load renders the fetched documents for this host', async () => {
  loadFixture('example.com', async () => ({
    ok: true,
    json: async () => ([{ url: 'http://example.com/a', title: 'A', doc_length: 1, internal_links: 0, external_links: 0, backlinks: 0, pagerank: 0, version: 1 }]),
  }));
  await flush();
  assert.equal(document.getElementById('domain-table').querySelectorAll('tbody tr').length, 1);
});

test('load reports the error message on a failed fetch', async () => {
  loadFixture('example.com', async () => ({ ok: false, status: 500, text: async () => 'load failed' }));
  await flush();
  assert.equal(document.getElementById('domain-status').textContent.includes('load failed'), true);
});
