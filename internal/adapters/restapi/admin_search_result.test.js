'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { JSDOM } = require('jsdom');
const { teardownDOM, requireFresh } = require('./dom_helper.test_util');

const RESULT_HTML = fs.readFileSync(path.join(__dirname, 'admin_search_result.html'), 'utf8');

// setupWithQuery mirrors dom_helper.test_util's setupDOM, but this page reads
// window.location.search at load time (doc_id/q/sort come from the URL, not
// the DOM) -- setupDOM's fixed 'http://localhost/' has no query string, so
// this builds its own jsdom instance with one instead.
function setupWithQuery(qs) {
  const dom = new JSDOM(RESULT_HTML, { url: 'http://localhost/admin/search/result' + (qs ? '?' + qs : '') });
  global.window = dom.window;
  global.document = dom.window.document;
  global.navigator = dom.window.navigator;
  return dom;
}

const FULL_RESULT = {
  doc_id: 'doc-1',
  title: 'Example Page',
  url: 'http://example.com/page',
  snippet: '<mark>hit</mark> text',
  final_score: 0.789,
  alpha: 0.5,
  pagerank_weight: 0.1,
  norm_bm25: 0.6,
  semantic_sim: 0.4,
  normalized_pagerank: 0.2,
  pagerank: 0.000123,
  k1: 1.2,
  b: 0.75,
  bm25_terms: [
    { term: 'cats', score: 1.5, term_freq: 3, doc_freq: 10 },
    { term: 'dogs', score: 0.5, term_freq: 1, doc_freq: 20 },
  ],
};

function loadFixture(qs) {
  setupWithQuery(qs);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async () => ({ ok: true, json: async () => [FULL_RESULT] });
  return requireFresh('./admin_search_result.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('load reports a message when doc_id or q is missing from the URL', async () => {
  const { load } = loadFixture('');
  await load();
  assert.equal(
    document.getElementById('result-status').textContent.includes('needs both a doc_id and q'),
    true,
  );
  assert.equal(document.getElementById('result-body').hidden, true);
});

test('load reports a message when only one of doc_id/q is present', async () => {
  const { load } = loadFixture('doc_id=doc-1');
  await load();
  assert.equal(
    document.getElementById('result-status').textContent.includes('needs both a doc_id and q'),
    true,
  );
});

test('load renders the result when doc_id and q are present and match a fetched result', async () => {
  const { load } = loadFixture('doc_id=doc-1&q=cats&sort=relevance');
  await load();
  assert.equal(document.getElementById('result-status').textContent, '');
  assert.equal(document.getElementById('result-body').hidden, false);
  assert.equal(document.getElementById('result-title').textContent, 'Example Page');
  assert.equal(document.getElementById('result-url').textContent, 'http://example.com/page');
  assert.equal(document.getElementById('result-final').textContent, '0.789');
  assert.equal(document.getElementById('result-snippet').innerHTML, '<mark>hit</mark> text');
});

test('load reports a message when no fetched result matches doc_id', async () => {
  const dom = setupWithQuery('doc_id=doc-missing&q=cats');
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async () => ({ ok: true, json: async () => [FULL_RESULT] });
  const { load } = requireFresh('./admin_search_result.js');
  await load();
  assert.equal(
    document.getElementById('result-status').textContent.includes('No result for'),
    true,
  );
  void dom;
});

test('load reports the error message on a failed fetch', async () => {
  setupWithQuery('doc_id=doc-1&q=cats');
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'search unavailable' });
  const { load } = requireFresh('./admin_search_result.js');
  await load();
  assert.equal(
    document.getElementById('result-status').textContent.includes('search unavailable'),
    true,
  );
});

test('render falls back to the URL as a title when the document has none', async () => {
  const { render } = loadFixture('doc_id=doc-1&q=cats');
  const r = { ...FULL_RESULT, title: '' };
  render(r);
  assert.equal(document.getElementById('result-title').textContent, r.url);
});

test('renderComposition shows the override note when the composed score does not match final_score', () => {
  const { renderComposition } = loadFixture('doc_id=doc-1&q=cats');
  renderComposition(FULL_RESULT); // norm_bm25/semantic_sim/pagerank combo != final_score 0.789
  assert.equal(document.getElementById('composition-note').hidden, false);
});

test('renderComposition hides the note when the composed score matches final_score', () => {
  const { renderComposition } = loadFixture('doc_id=doc-1&q=cats');
  const alpha = 0.5, w = 0.1, normBM25 = 0.6, semantic = 0.4, normPR = 0.2;
  const composed = alpha * (1 - w) * normBM25 + (1 - alpha) * (1 - w) * semantic + w * normPR;
  renderComposition({ ...FULL_RESULT, final_score: composed });
  assert.equal(document.getElementById('composition-note').hidden, true);
});

test('renderComposition builds one legend entry per component with its contribution', () => {
  const { renderComposition } = loadFixture('doc_id=doc-1&q=cats');
  renderComposition(FULL_RESULT);
  const legendItems = document.getElementById('composition-legend').children;
  assert.equal(legendItems.length, 3);
  const track = document.getElementById('composition-track').children;
  assert.equal(track.length, 3);
});

test('renderBM25Terms shows a bar per matched term, scaled against the max score', () => {
  const { renderBM25Terms } = loadFixture('doc_id=doc-1&q=cats');
  renderBM25Terms(FULL_RESULT);
  const rows = document.getElementById('bm25-terms').querySelectorAll('.bar-row');
  assert.equal(rows.length, 2);
  assert.equal(document.getElementById('bm25-caption').textContent, 'computed with k1=1.20, b=0.75');
});

test('renderBM25Terms shows a semantic-only message when no term matched directly', () => {
  const { renderBM25Terms } = loadFixture('doc_id=doc-1&q=cats');
  renderBM25Terms({ ...FULL_RESULT, bm25_terms: [] });
  assert.equal(
    document.getElementById('bm25-terms').textContent.includes('semantic similarity alone'),
    true,
  );
});

test('barRow includes an optional meta span only when meta is given', () => {
  const { barRow } = loadFixture('doc_id=doc-1&q=cats');
  const withMeta = barRow('term', 0.5, '0.500', 'tf 1 · df 2');
  assert.equal(withMeta.querySelector('.bar-meta').textContent, 'tf 1 · df 2');

  const withoutMeta = barRow('term', 0.5, '0.500');
  assert.equal(withoutMeta.querySelector('.bar-meta'), null);
});

test('barRow clamps the fill fraction to [0, 1]', () => {
  const { barRow } = loadFixture('doc_id=doc-1&q=cats');
  const over = barRow('term', 5, '5.000');
  assert.equal(over.querySelector('.bar-fill').style.width, '100%');
  const under = barRow('term', -1, '-1.000');
  assert.equal(under.querySelector('.bar-fill').style.width, '0%');
});
