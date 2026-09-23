'use strict';
// admin_search.js wires its two forms' submit handlers directly at load time, with no named
// functions worth exporting -- these tests require the fixture, dispatch real 'submit' events,
// and assert on the DOM.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const SEARCH_HTML = fs.readFileSync(path.join(__dirname, 'admin_search.html'), 'utf8');

function loadFixture(fetchImpl) {
  setupDOM(SEARCH_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => [] }));
  requireFresh('./admin_search.js');
}

function submit(formId) {
  document.getElementById(formId).dispatchEvent(new window.Event('submit', { cancelable: true }));
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('debug form: empty query shows a prompt instead of searching', async () => {
  let called = false;
  loadFixture(async () => { called = true; return { ok: true, json: async () => [] }; });
  document.getElementById('debug-q').value = '   ';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('debug-status').textContent, 'Type a query to debug.');
  assert.equal(called, false);
});

test('debug form: no matches reports a "no matches" status with the query', async () => {
  loadFixture(async () => ({ ok: true, json: async () => [] }));
  document.getElementById('debug-q').value = 'zzz';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('debug-status').textContent, 'No matches for “zzz”.');
});

test('debug form: renders a result row with scores, and a fuzzy-correction suffix', async () => {
  loadFixture(async (url) => {
    assert.equal(url.includes('q=cats'), true);
    assert.equal(url.includes('sort=relevance'), true);
    return {
      ok: true,
      json: async () => [{
        doc_id: 'd1', title: 'Cats', url: 'http://a/cats', snippet: '<mark>cats</mark>',
        bm25_score: 1.2345, semantic_sim: 0.5, pagerank: 0.001, final_score: 0.987,
        corrected_terms: [{ original: 'catz', corrected: 'cats' }],
      }],
    };
  });
  document.getElementById('debug-q').value = 'cats';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  const status = document.getElementById('debug-status').textContent;
  assert.equal(status.includes('1 match'), true);
  assert.equal(status.includes('"catz"→"cats"'), true);
  const rows = document.getElementById('debug-result').querySelectorAll('tbody tr');
  assert.equal(rows.length, 1);
  assert.equal(rows[0].querySelector('a').textContent, 'Cats');
  assert.equal(
    rows[0].querySelector('a').getAttribute('href'),
    '/admin/search/result?doc_id=d1&q=cats&sort=relevance',
  );
});

test('debug form: reports plural match count for more than one result', async () => {
  loadFixture(async () => ({
    ok: true,
    json: async () => [
      { doc_id: 'd1', url: 'http://a/1', snippet: '', bm25_score: 1, semantic_sim: 1, pagerank: 1, final_score: 1 },
      { doc_id: 'd2', url: 'http://a/2', snippet: '', bm25_score: 1, semantic_sim: 1, pagerank: 1, final_score: 1 },
    ],
  }));
  document.getElementById('debug-q').value = 'cats';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('debug-status').textContent, '2 matches');
});

test('debug form: reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'search down' }));
  document.getElementById('debug-q').value = 'cats';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('debug-status').textContent.includes('search down'), true);
});

test('debug form: requests top_k=5000 by default, not the public search default', async () => {
  loadFixture(async (url) => {
    assert.equal(url.includes('top_k=5000'), true);
    return { ok: true, json: async () => [] };
  });
  document.getElementById('debug-q').value = 'cats';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
});

function manyResults(n) {
  const out = [];
  for (let i = 0; i < n; i++) {
    out.push({
      doc_id: 'd' + i, url: 'http://a/' + i, title: 'Doc ' + i, snippet: '',
      bm25_score: 1, semantic_sim: 1, pagerank: 1, final_score: n - i,
    });
  }
  return out;
}

test('debug form: paginates results at 50 per page and hides the pager for one page', async () => {
  loadFixture(async () => ({ ok: true, json: async () => manyResults(10) }));
  document.getElementById('debug-q').value = 'cats';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('debug-result').querySelectorAll('tbody tr').length, 10);
  assert.equal(document.getElementById('debug-pager').hidden, true);
});

test('debug form: shows a pager and only 50 rows when results exceed one page', async () => {
  loadFixture(async () => ({ ok: true, json: async () => manyResults(120) }));
  document.getElementById('debug-q').value = 'cats';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('debug-result').querySelectorAll('tbody tr').length, 50);
  assert.equal(document.getElementById('debug-pager').hidden, false);
  assert.equal(document.getElementById('debug-page-info').textContent, 'Page 1 of 3 (120 results)');
  assert.equal(document.getElementById('debug-prev').disabled, true);
  assert.equal(document.getElementById('debug-next').disabled, false);
});

test('debug form: next/previous buttons page through results', async () => {
  loadFixture(async () => ({ ok: true, json: async () => manyResults(120) }));
  document.getElementById('debug-q').value = 'cats';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));

  document.getElementById('debug-next').click();
  assert.equal(document.getElementById('debug-page-info').textContent, 'Page 2 of 3 (120 results)');
  assert.equal(document.getElementById('debug-result').querySelectorAll('tbody tr').length, 50);
  assert.equal(document.getElementById('debug-prev').disabled, false);
  assert.equal(document.getElementById('debug-next').disabled, false);

  document.getElementById('debug-next').click();
  assert.equal(document.getElementById('debug-page-info').textContent, 'Page 3 of 3 (120 results)');
  assert.equal(document.getElementById('debug-result').querySelectorAll('tbody tr').length, 20);
  assert.equal(document.getElementById('debug-next').disabled, true);

  document.getElementById('debug-prev').click();
  assert.equal(document.getElementById('debug-page-info').textContent, 'Page 2 of 3 (120 results)');
});

test('debug form: a new search resets back to page 1', async () => {
  let n = 120;
  loadFixture(async () => ({ ok: true, json: async () => manyResults(n) }));
  document.getElementById('debug-q').value = 'cats';
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('debug-next').click();
  assert.equal(document.getElementById('debug-page-info').textContent, 'Page 2 of 3 (120 results)');

  n = 10;
  submit('debug-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('debug-pager').hidden, true);
  assert.equal(document.getElementById('debug-result').querySelectorAll('tbody tr').length, 10);
});

test('postings form: empty term shows a prompt instead of looking up', async () => {
  let called = false;
  loadFixture(async () => { called = true; return { ok: true, json: async () => ({ postings: [] }) }; });
  document.getElementById('postings-term').value = '  ';
  submit('postings-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('postings-status').textContent, 'Enter a term to look up.');
  assert.equal(called, false);
});

test('postings form: reports when the term does not appear in the index', async () => {
  loadFixture(async () => ({ ok: true, json: async () => ({ postings: [] }) }));
  document.getElementById('postings-term').value = 'zzz';
  submit('postings-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('postings-status').textContent, '"zzz" does not appear in the index.');
});

test('postings form: renders postings rows and a singular/plural document count', async () => {
  loadFixture(async (url) => {
    assert.equal(url.includes('term=cats'), true);
    return {
      ok: true,
      json: async () => ({ doc_freq: 1, postings: [{ doc_id: 'd1', term_freq: 3, doc_length: 100 }] }),
    };
  });
  document.getElementById('postings-term').value = 'Cats';
  submit('postings-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('postings-status').textContent, 'Appears in 1 document.');
  const rows = document.getElementById('postings-result').querySelectorAll('tbody tr');
  assert.equal(rows.length, 1);
  assert.equal(rows[0].children[0].textContent, 'd1');
});

test('postings form: reports plural "documents" for doc_freq > 1', async () => {
  loadFixture(async () => ({
    ok: true,
    json: async () => ({ doc_freq: 2, postings: [{ doc_id: 'd1', term_freq: 1, doc_length: 10 }] }),
  }));
  document.getElementById('postings-term').value = 'cats';
  submit('postings-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('postings-status').textContent, 'Appears in 2 documents.');
});

test('postings form: reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'index down' }));
  document.getElementById('postings-term').value = 'cats';
  submit('postings-form');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('postings-status').textContent.includes('index down'), true);
});
