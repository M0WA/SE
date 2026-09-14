'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const INDEX_HTML = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');

function loadFixture() {
  setupDOM(INDEX_HTML);
  return requireFresh('./index.js');
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('renderCorrectionNote hides itself when nothing was corrected', () => {
  const { renderCorrectionNote } = loadFixture();
  document.getElementById('correction-note').hidden = false;
  renderCorrectionNote([{ corrected_terms: [] }]);
  const note = document.getElementById('correction-note');
  assert.equal(note.hidden, true);
  assert.equal(note.textContent, '');
});

test('renderCorrectionNote hides itself for an empty result list', () => {
  const { renderCorrectionNote } = loadFixture();
  renderCorrectionNote([]);
  assert.equal(document.getElementById('correction-note').hidden, true);
});

test('renderCorrectionNote shows the substituted terms', () => {
  const { renderCorrectionNote } = loadFixture();
  renderCorrectionNote([{ corrected_terms: [{ original: 'teh', corrected: 'the' }] }]);
  const note = document.getElementById('correction-note');
  assert.equal(note.hidden, false);
  assert.equal(note.textContent, 'Showing results for “the” instead of “teh”.');
});

test('clear removes every child of an element', () => {
  const { clear } = loadFixture();
  const el = document.createElement('div');
  el.appendChild(document.createElement('span'));
  clear(el);
  assert.equal(el.childNodes.length, 0);
});

test('scoreRow renders a label/value pair with the value fixed to 3 decimals', () => {
  const { scoreRow } = loadFixture();
  const row = scoreRow('bm25', 1.23456);
  assert.equal(row.querySelector('.score-label').textContent, 'bm25');
  assert.equal(row.querySelector('.score-value').textContent, '1.235');
});

test('renderResults shows a "no matches" message for an empty list', () => {
  const { renderResults } = loadFixture();
  renderResults('nothing', []);
  assert.equal(document.getElementById('status').textContent, 'No matches for “nothing”.');
  assert.equal(document.getElementById('results').children.length, 0);
});

test('renderResults reports a singular match count for exactly one result', () => {
  const { renderResults } = loadFixture();
  renderResults('q', [{ url: 'http://a', title: 'A', score: 1 }]);
  assert.equal(document.getElementById('status').textContent, '1 match');
});

test('renderResults reports a plural match count and renders each result', () => {
  const { renderResults } = loadFixture();
  renderResults('q', [
    { url: 'http://a', title: 'A', score: 1.5 },
    { url: 'http://b', title: 'B', score: 0.5 },
  ]);
  assert.equal(document.getElementById('status').textContent, '2 matches');
  const rows = document.getElementById('results').querySelectorAll('.result');
  assert.equal(rows.length, 2);
  assert.equal(rows[0].querySelector('.result-title').textContent, 'A');
  assert.equal(rows[0].querySelector('.result-title').getAttribute('href'), 'http://a');
  assert.equal(rows[0].querySelector('.result-score').textContent, '1.500');
});

test('renderResults falls back to the url as title when a result has none', () => {
  const { renderResults } = loadFixture();
  renderResults('q', [{ url: 'http://only-url', score: 1 }]);
  assert.equal(document.getElementById('results').querySelector('.result-title').textContent, 'http://only-url');
});

test('renderResults renders a snippet only when the result has one', () => {
  const { renderResults } = loadFixture();
  renderResults('q', [{ url: 'http://a', title: 'A', score: 1, snippet: '<mark>hit</mark>' }]);
  const snippet = document.getElementById('results').querySelector('.result-snippet');
  assert.notEqual(snippet, null);
  assert.equal(snippet.innerHTML, '<mark>hit</mark>');

  renderResults('q', [{ url: 'http://b', title: 'B', score: 1 }]);
  assert.equal(document.getElementById('results').querySelector('.result-snippet'), null);
});

test('renderResults renders a score breakdown only when bm25/semantic scores are present', () => {
  const { renderResults } = loadFixture();
  renderResults('q', [{ url: 'http://a', title: 'A', score: 1, bm25_score: 0.7, semantic_sim: 0.3 }]);
  const details = document.getElementById('results').querySelector('.result-details');
  assert.notEqual(details, null);
  const rows = details.querySelectorAll('.score-row');
  assert.equal(rows.length, 3);

  renderResults('q', [{ url: 'http://b', title: 'B', score: 1 }]);
  assert.equal(document.getElementById('results').querySelector('.result-details'), null);
});

test('renderResults clears any previous correction note when there is none this time', () => {
  const { renderResults } = loadFixture();
  renderResults('q', [{ url: 'http://a', title: 'A', score: 1, corrected_terms: [{ original: 'x', corrected: 'y' }] }]);
  assert.equal(document.getElementById('correction-note').hidden, false);
  renderResults('q2', [{ url: 'http://b', title: 'B', score: 1 }]);
  assert.equal(document.getElementById('correction-note').hidden, true);
});

test('submitting an empty query shows a prompt and does not search', () => {
  let fetched = false;
  global.fetch = async () => { fetched = true; return { ok: true, json: async () => ({ results: [] }) }; };
  loadFixture();
  document.getElementById('results').appendChild(document.createElement('div'));
  document.getElementById('q').value = '   ';
  document.getElementById('search-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  assert.equal(document.getElementById('status').textContent, 'Type something to search for.');
  assert.equal(document.getElementById('results').children.length, 0);
  assert.equal(fetched, false);
});

test('submitting a query fetches with the query and sort order, then renders results', async () => {
  let gotURL;
  global.fetch = async (url) => {
    gotURL = url;
    return { ok: true, json: async () => ({ results: [{ url: 'http://a', title: 'A', score: 1 }] }) };
  };
  loadFixture();
  document.getElementById('q').value = 'hello world';
  document.getElementById('sort').value = 'recency';
  document.getElementById('search-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  assert.equal(document.getElementById('status').textContent, 'Searching…');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(gotURL, '/search?q=hello%20world&sort=recency');
  assert.equal(document.getElementById('status').textContent, '1 match');
});

test('runSearch reports the server error message on a non-ok response', async () => {
  global.fetch = async () => ({ ok: false, text: async () => ' bad query ' });
  const { runSearch } = loadFixture();
  await runSearch('q', 'relevance');
  assert.equal(document.getElementById('status').textContent, 'Search failed: bad query');
});

test('runSearch reports a network-error message when fetch throws', async () => {
  global.fetch = async () => { throw new Error('boom'); };
  const { runSearch } = loadFixture();
  await runSearch('q', 'relevance');
  assert.equal(document.getElementById('status').textContent, 'Search failed: could not reach the server.');
});

test('sign-out posts to /logout on click', async () => {
  loadFixture();
  let fetchedURL, fetchedOpts;
  global.fetch = async (url, opts) => {
    fetchedURL = url;
    fetchedOpts = opts;
    return { ok: true };
  };
  document.getElementById('sign-out').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(fetchedURL, '/logout');
  assert.equal(fetchedOpts.method, 'POST');
});
