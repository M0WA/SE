'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const TERM_HTML = fs.readFileSync(path.join(__dirname, 'admin_vocabulary_term.html'), 'utf8');

function loadFixture(url) {
  const dom = setupDOM(TERM_HTML);
  if (url) dom.reconfigure({ url });
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  return requireFresh('./admin_vocabulary_term.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('with no term in the URL, shows the "needs a term" message and does not fetch', async () => {
  let fetched = false;
  global.fetch = async () => { fetched = true; return { ok: true, json: async () => ({}) }; };
  loadFixture();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('term-title').textContent, '(no term given)');
  assert.equal(
    document.getElementById('term-status').textContent,
    'This page needs a term in its URL — open it from the Vocabulary list instead of directly.',
  );
  assert.equal(fetched, false);
});

test('sets the document title and heading from the term query param', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ postings: [], doc_freq: 0 }) });
  loadFixture('http://localhost/admin/vocabulary/term?term=search');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.title, 'se. — search');
  assert.equal(document.getElementById('term-title').textContent, 'search');
});

test('load requests postings for the term and renders "no pages" for an empty result', async () => {
  let gotURL;
  global.fetch = async (url) => {
    gotURL = url;
    return { ok: true, json: async () => ({ postings: [], doc_freq: 0 }) };
  };
  loadFixture('http://localhost/admin/vocabulary/term?term=xyzzy');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(gotURL, '/admin/api/postings?term=xyzzy');
  assert.equal(document.getElementById('term-status').textContent, 'No pages contain “xyzzy”.');
});

test('renderPostings sorts postings by term frequency descending and renders the table', () => {
  const { renderPostings } = loadFixture('http://localhost/admin/vocabulary/term?term=foo');
  const data = {
    doc_freq: 2,
    postings: [
      { url: 'http://a', title: 'A', term_freq: 1, doc_length: 100, snippet: '<mark>foo</mark>' },
      { url: 'http://b', title: 'B', term_freq: 5, doc_length: 200, snippet: '<mark>foo</mark> b' },
    ],
  };
  renderPostings(data);
  const rows = document.getElementById('term-table').querySelectorAll('tbody tr');
  assert.equal(rows.length, 2);
  assert.equal(rows[0].children[2].textContent, '5');
  assert.equal(rows[1].children[2].textContent, '1');
  assert.equal(document.getElementById('term-tail').textContent, '2 pages · appears in 2 documents total');
});

test('renderPostings shows a truncation note and singular units when appropriate', () => {
  const { renderPostings } = loadFixture('http://localhost/admin/vocabulary/term?term=foo');
  renderPostings({
    doc_freq: 5,
    postings: [{ url: 'http://a', title: 'A', term_freq: 1, doc_length: 10, snippet: '' }],
  });
  assert.equal(
    document.getElementById('term-status').textContent,
    'Showing the 1 strongest matches — refine the term to narrow further.',
  );
  assert.equal(document.getElementById('term-tail').textContent, '1 page shown of 5 documents total');
});

test('renderPostings uses the raw url as link text when a posting has no title', () => {
  const { renderPostings } = loadFixture('http://localhost/admin/vocabulary/term?term=foo');
  renderPostings({ doc_freq: 1, postings: [{ url: 'http://a/only-url', term_freq: 1, doc_length: 10, snippet: '' }] });
  const link = document.getElementById('term-table').querySelector('a');
  assert.equal(link.textContent, 'http://a/only-url');
});

test('load reports the error message when the fetch fails', async () => {
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'db down' });
  loadFixture('http://localhost/admin/vocabulary/term?term=foo');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('term-status').textContent, 'Could not load pages for “foo”: db down');
});
