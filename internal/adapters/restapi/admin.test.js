'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

function load() {
  return requireFresh('./admin.js');
}

test.beforeEach(() => setupDOM());
test.afterEach(() => teardownDOM());

test('clear removes every child of an element', () => {
  const { clear } = load();
  const el = document.createElement('div');
  el.appendChild(document.createElement('span'));
  el.appendChild(document.createTextNode('text'));
  assert.equal(el.childNodes.length, 2);
  clear(el);
  assert.equal(el.childNodes.length, 0);
});

test('setButtonLoading(true) disables the button, shows a spinner, and swaps the label', () => {
  const { setButtonLoading } = load();
  const btn = document.createElement('button');
  btn.textContent = 'Save';
  setButtonLoading(btn, true, 'Saving…');
  assert.equal(btn.disabled, true);
  assert.equal(btn.dataset.originalLabel, 'Save');
  assert.equal(btn.querySelector('.spinner') !== null, true);
  assert.equal(btn.textContent.includes('Saving…'), true);
});

test('setButtonLoading(false) restores the original label and re-enables the button', () => {
  const { setButtonLoading } = load();
  const btn = document.createElement('button');
  btn.textContent = 'Save';
  setButtonLoading(btn, true, 'Saving…');
  setButtonLoading(btn, false);
  assert.equal(btn.disabled, false);
  assert.equal(btn.textContent, 'Save');
  assert.equal(btn.dataset.originalLabel, undefined);
});

test('setButtonLoading falls back to the original label when no loadingLabel is given', () => {
  const { setButtonLoading } = load();
  const btn = document.createElement('button');
  btn.textContent = 'Delete';
  setButtonLoading(btn, true);
  assert.equal(btn.textContent.includes('Delete'), true);
});

test('normalizeURL prepends https:// to a bare host', () => {
  const { normalizeURL } = load();
  assert.equal(normalizeURL('example.com'), 'https://example.com');
});

test('normalizeURL trims surrounding whitespace', () => {
  const { normalizeURL } = load();
  assert.equal(normalizeURL('  example.com  '), 'https://example.com');
});

test('normalizeURL leaves an explicit scheme untouched', () => {
  const { normalizeURL } = load();
  assert.equal(normalizeURL('http://example.com'), 'http://example.com');
  assert.equal(normalizeURL('ftp://example.com'), 'ftp://example.com');
});

test('normalizeURL leaves an empty/blank value untouched', () => {
  const { normalizeURL } = load();
  assert.equal(normalizeURL(''), '');
  assert.equal(normalizeURL('   '), '');
});

test('linesToText joins an array into one line per entry', () => {
  const { linesToText } = load();
  assert.equal(linesToText(['a', 'b', 'c']), 'a\nb\nc');
});

test('linesToText treats a missing/empty list as an empty string', () => {
  const { linesToText } = load();
  assert.equal(linesToText(undefined), '');
  assert.equal(linesToText([]), '');
});

test('parseLines trims each line and drops blank ones', () => {
  const { parseLines } = load();
  assert.deepEqual(parseLines('  a.example  \n\nb.example\n   \nc.example'), ['a.example', 'b.example', 'c.example']);
});

test('parseLines returns an empty array for blank input', () => {
  const { parseLines } = load();
  assert.deepEqual(parseLines(''), []);
  assert.deepEqual(parseLines('   \n   '), []);
});

test('kvRow appends a key/value row with the expected structure', () => {
  const { kvRow } = load();
  const container = document.createElement('div');
  kvRow(container, 'Documents', '42');
  const row = container.querySelector('.kv-row');
  assert.notEqual(row, null);
  assert.equal(row.querySelector('.k').textContent, 'Documents');
  assert.equal(row.querySelector('.v').textContent, '42');
});

test('checkResponse returns the response as-is when ok', async () => {
  const { checkResponse } = load();
  const resp = { ok: true };
  assert.equal(await checkResponse(resp), resp);
});

test('checkResponse throws the response body text when not ok', async () => {
  const { checkResponse } = load();
  const resp = { ok: false, status: 500, text: async () => 'boom\n' };
  await assert.rejects(() => checkResponse(resp), /boom/);
});

test('checkResponse falls back to a generic message when the body is empty', async () => {
  const { checkResponse } = load();
  const resp = { ok: false, status: 503, text: async () => '' };
  await assert.rejects(() => checkResponse(resp), /request failed: 503/);
});

test('getJSON issues a plain GET and returns the parsed body', async () => {
  const { getJSON } = load();
  let gotURL;
  global.fetch = async (url) => {
    gotURL = url;
    return { ok: true, json: async () => ({ hello: 'world' }) };
  };
  const result = await getJSON('/admin/api/stats');
  assert.equal(gotURL, '/admin/api/stats');
  assert.deepEqual(result, { hello: 'world' });
});

test('postJSON sends a JSON POST body with the right headers', async () => {
  const { postJSON } = load();
  let gotOpts;
  global.fetch = async (url, opts) => {
    gotOpts = opts;
    return { ok: true, json: async () => ({ ok: true }) };
  };
  await postJSON('/admin/api/schedules', { seed_urls: ['http://a'] });
  assert.equal(gotOpts.method, 'POST');
  assert.equal(gotOpts.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(gotOpts.body), { seed_urls: ['http://a'] });
});

test('patchJSON sends a JSON PATCH body with the right headers', async () => {
  const { patchJSON } = load();
  let gotOpts;
  global.fetch = async (url, opts) => {
    gotOpts = opts;
    return { ok: true, json: async () => ({ ok: true }) };
  };
  await patchJSON('/admin/api/schedules/sched-1', { max_pages: 5 });
  assert.equal(gotOpts.method, 'PATCH');
  assert.equal(gotOpts.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(gotOpts.body), { max_pages: 5 });
});

test('deleteRequest issues a plain DELETE', async () => {
  const { deleteRequest } = load();
  let gotOpts;
  global.fetch = async (url, opts) => {
    gotOpts = opts;
    return { ok: true, json: async () => ({ ok: true }) };
  };
  await deleteRequest('/admin/api/schedules/sched-1');
  assert.equal(gotOpts.method, 'DELETE');
});

test('textCell builds a plain <td>, right-aligned/monospaced for opts.num', () => {
  const { textCell } = load();
  const plain = textCell('hello');
  assert.equal(plain.tagName, 'TD');
  assert.equal(plain.textContent, 'hello');
  assert.equal(plain.className, '');

  const numeric = textCell('42', { num: true });
  assert.equal(numeric.className, 'num');
});

test('snippetCell renders its argument as HTML, defaulting to empty', () => {
  const { snippetCell } = load();
  const td = snippetCell('<mark>hit</mark> text');
  assert.equal(td.className, 'excerpt');
  assert.equal(td.innerHTML, '<mark>hit</mark> text');

  const empty = snippetCell(undefined);
  assert.equal(empty.innerHTML, '');
});

test('urlCell builds a <td class="url">', () => {
  const { urlCell } = load();
  const td = urlCell('http://example.com');
  assert.equal(td.className, 'url');
  assert.equal(td.textContent, 'http://example.com');
});

test('seedSummary reports "(no seed)" for an empty/missing list', () => {
  const { seedSummary } = load();
  assert.equal(seedSummary([]), '(no seed)');
  assert.equal(seedSummary(undefined), '(no seed)');
});

test('seedSummary shows the single seed when there is exactly one', () => {
  const { seedSummary } = load();
  assert.equal(seedSummary(['http://a']), 'http://a');
});

test('seedSummary shows the first seed plus a remainder count for more than one', () => {
  const { seedSummary } = load();
  assert.equal(seedSummary(['http://a', 'http://b', 'http://c']), 'http://a +2 more');
});

test('formatTimestamp renders an em-dash for an empty/missing value', () => {
  const { formatTimestamp } = load();
  assert.equal(formatTimestamp(''), '—');
  assert.equal(formatTimestamp(null), '—');
});

test('formatTimestamp renders the full local date+time by default', () => {
  const { formatTimestamp } = load();
  const iso = '2026-01-02T03:04:05Z';
  assert.equal(formatTimestamp(iso), new Date(iso).toLocaleString());
});

test('formatTimestamp renders only the time when opts.timeOnly is set', () => {
  const { formatTimestamp } = load();
  const iso = '2026-01-02T03:04:05Z';
  assert.equal(formatTimestamp(iso, { timeOnly: true }), new Date(iso).toLocaleTimeString());
});

test('buildTable renders a header row and one row per data item via cellsForRow', () => {
  const { buildTable, textCell } = load();
  const table = buildTable(
    [{ label: 'name' }, { label: 'count', num: true }],
    [{ name: 'a', count: 1 }, { name: 'b', count: 2 }],
    (row) => [textCell(row.name), textCell(String(row.count), { num: true })],
  );
  const headers = Array.from(table.querySelectorAll('thead th')).map((th) => th.textContent);
  assert.deepEqual(headers, ['name', 'count']);
  assert.equal(table.querySelector('thead th.num').textContent, 'count');
  const rows = table.querySelectorAll('tbody tr');
  assert.equal(rows.length, 2);
  assert.equal(rows[0].children[0].textContent, 'a');
  assert.equal(rows[1].children[1].textContent, '2');
});

test('buildTable renders zero body rows for an empty row list', () => {
  const { buildTable } = load();
  const table = buildTable([{ label: 'x' }], [], () => []);
  assert.equal(table.querySelectorAll('tbody tr').length, 0);
});

test('wireSignOut is a no-op when there is no #sign-out button', () => {
  const { wireSignOut } = load();
  assert.doesNotThrow(() => wireSignOut());
});

// wireSignOut's post-logout navigation (`window.location = '/'`) isn't
// asserted here -- jsdom's window.location is a real (non-configurable)
// Location object that can't be swapped for a spy, and actually navigating
// jsdom itself is unsupported/noisy. The fetch call is this function's own
// meaningful logic; the navigation is a one-line browser-API call.
test('wireSignOut posts to /logout on click', async () => {
  setupDOM('<!doctype html><html><body><button id="sign-out"></button></body></html>');
  const { wireSignOut } = load();
  let fetchedURL, fetchedOpts;
  global.fetch = async (url, opts) => {
    fetchedURL = url;
    fetchedOpts = opts;
    return { ok: true };
  };
  wireSignOut();
  document.getElementById('sign-out').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(fetchedURL, '/logout');
  assert.equal(fetchedOpts.method, 'POST');
});

test('loadStats is a no-op when the page has no #stats element', async () => {
  const { loadStats } = load();
  await assert.doesNotReject(() => loadStats());
});

test('loadStats renders the corpus stats into #stats on success', async () => {
  setupDOM('<!doctype html><html><body><div id="stats"></div></body></html>');
  const { loadStats } = load();
  global.fetch = async () => ({
    ok: true,
    json: async () => ({ total_docs: 5, avg_doc_len: 12.34, driver: 'sqlite' }),
  });
  await loadStats();
  const text = document.getElementById('stats').textContent;
  assert.equal(text.includes('5'), true);
  assert.equal(text.includes('12.3'), true);
  assert.equal(text.includes('sqlite'), true);
});

test('loadStats reports the error message on a failed fetch', async () => {
  setupDOM('<!doctype html><html><body><div id="stats"></div></body></html>');
  const { loadStats } = load();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'db down' });
  await loadStats();
  assert.equal(document.getElementById('stats').textContent.includes('db down'), true);
});

test('loadVocabulary is a no-op when the page has neither vocab element', async () => {
  const { loadVocabulary } = load();
  await assert.doesNotReject(() => loadVocabulary('any'));
});

test('loadVocabulary renders nothing for a blank pattern (no preloaded table)', async () => {
  setupDOM('<!doctype html><html><body><div id="vocab-summary"></div><div id="vocab-table"></div></body></html>');
  const { loadVocabulary } = load();
  let called = false;
  global.fetch = async () => {
    called = true;
    return { ok: true, json: async () => ({ vocabulary_size: 100, top_terms: [] }) };
  };
  await loadVocabulary('');
  assert.equal(called, false);
  assert.equal(document.getElementById('vocab-summary').textContent, '');
  assert.equal(document.getElementById('vocab-table').textContent, '');
});

test('loadVocabulary fetches once, regex-filters the cached terms, and always shows the true vocabulary_size', async () => {
  setupDOM('<!doctype html><html><body><div id="vocab-summary"></div><div id="vocab-table"></div></body></html>');
  const { loadVocabulary } = load();
  let fetchCount = 0;
  global.fetch = async () => {
    fetchCount++;
    return {
      ok: true,
      json: async () => ({
        vocabulary_size: 100,
        top_terms: [
          { term: 'search', doc_freq: 10, total_freq: 20 },
          { term: 'engine', doc_freq: 5, total_freq: 8 },
        ],
      }),
    };
  };
  await loadVocabulary('^sea');
  assert.equal(document.getElementById('vocab-summary').textContent.includes('100'), true);
  const rows = document.getElementById('vocab-table').querySelectorAll('tbody tr');
  assert.equal(rows.length, 1);
  const link = document.getElementById('vocab-table').querySelector('a');
  assert.equal(link.textContent, 'search');
  assert.equal(link.getAttribute('href'), '/admin/vocabulary/term?term=search');

  // A second filter against a different pattern must not re-fetch.
  await loadVocabulary('engine');
  assert.equal(fetchCount, 1);
  assert.equal(document.getElementById('vocab-summary').textContent.includes('100'), true);
  assert.equal(document.getElementById('vocab-table').querySelector('a').textContent, 'engine');
});

test('loadVocabulary matches case-insensitively', async () => {
  setupDOM('<!doctype html><html><body><div id="vocab-summary"></div><div id="vocab-table"></div></body></html>');
  const { loadVocabulary } = load();
  global.fetch = async () => ({
    ok: true,
    json: async () => ({ vocabulary_size: 1, top_terms: [{ term: 'Search', doc_freq: 1, total_freq: 1 }] }),
  });
  await loadVocabulary('search');
  assert.equal(document.getElementById('vocab-table').querySelector('a').textContent, 'Search');
});

test('loadVocabulary shows a no-match message when the pattern matches nothing', async () => {
  setupDOM('<!doctype html><html><body><div id="vocab-summary"></div><div id="vocab-table"></div></body></html>');
  const { loadVocabulary } = load();
  global.fetch = async () => ({ ok: true, json: async () => ({ vocabulary_size: 0, top_terms: [] }) });
  await loadVocabulary('zzz');
  assert.equal(document.getElementById('vocab-table').textContent.includes('zzz'), true);
});

test('loadVocabulary reports an invalid regex via the dedicated error element without crashing', async () => {
  setupDOM(
    '<!doctype html><html><body>' +
      '<div id="vocab-error"></div><div id="vocab-summary"></div><div id="vocab-table"></div>' +
      '</body></html>',
  );
  const { loadVocabulary } = load();
  let called = false;
  global.fetch = async () => {
    called = true;
    return { ok: true, json: async () => ({ vocabulary_size: 0, top_terms: [] }) };
  };
  await assert.doesNotReject(() => loadVocabulary('['));
  assert.equal(called, false);
  assert.equal(document.getElementById('vocab-error').textContent.includes('Invalid pattern'), true);
  assert.equal(document.getElementById('vocab-summary').textContent, '');
  assert.equal(document.getElementById('vocab-table').textContent, '');
});

test('loadVocabulary falls back to the table element for an invalid regex when there is no error element', async () => {
  setupDOM('<!doctype html><html><body><div id="vocab-summary"></div><div id="vocab-table"></div></body></html>');
  const { loadVocabulary } = load();
  await loadVocabulary('(');
  assert.equal(document.getElementById('vocab-table').textContent.includes('Invalid pattern'), true);
});

test('loadVocabulary reports the error message on a failed fetch', async () => {
  setupDOM('<!doctype html><html><body><div id="vocab-summary"></div><div id="vocab-table"></div></body></html>');
  const { loadVocabulary } = load();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'vocab unavailable' });
  await loadVocabulary('term');
  assert.equal(document.getElementById('vocab-summary').textContent.includes('vocab unavailable'), true);
});

test('wireVocabularySearch does nothing when there is no search form (no unconditional preload)', () => {
  setupDOM('<!doctype html><html><body><div id="vocab-summary"></div><div id="vocab-table"></div></body></html>');
  const { wireVocabularySearch } = load();
  let called = false;
  global.fetch = async () => {
    called = true;
    return { ok: true, json: async () => ({ vocabulary_size: 0, top_terms: [] }) };
  };
  assert.doesNotThrow(() => wireVocabularySearch());
  assert.equal(called, false);
});

test('wireVocabularySearch renders nothing until the admin types, then debounces typing before loading', async () => {
  setupDOM(
    '<!doctype html><html><body>' +
      '<form id="vocab-search-form"><input id="vocab-q"></form>' +
      '<div id="vocab-summary"></div><div id="vocab-table"></div>' +
      '</body></html>',
  );
  const { wireVocabularySearch } = load();
  let fetchCount = 0;
  global.fetch = async () => {
    fetchCount++;
    return { ok: true, json: async () => ({ vocabulary_size: 0, top_terms: [{ term: 'foo', doc_freq: 1, total_freq: 1 }] }) };
  };
  wireVocabularySearch();
  assert.equal(fetchCount, 0); // no preload
  const input = document.getElementById('vocab-q');
  input.value = 'f';
  input.dispatchEvent(new window.Event('input'));
  input.value = 'fo';
  input.dispatchEvent(new window.Event('input'));
  input.value = 'foo';
  input.dispatchEvent(new window.Event('input'));
  // Only the last keystroke's debounced call should ever fire.
  await new Promise((resolve) => setTimeout(resolve, 250));
  assert.equal(fetchCount, 1);
  assert.equal(document.getElementById('vocab-table').querySelector('a').textContent, 'foo');
});

test('wireVocabularySearch submitting the form loads with the trimmed query, preserving case for the regex', async () => {
  setupDOM(
    '<!doctype html><html><body>' +
      '<form id="vocab-search-form"><input id="vocab-q"></form>' +
      '<div id="vocab-summary"></div><div id="vocab-table"></div>' +
      '</body></html>',
  );
  const { wireVocabularySearch } = load();
  global.fetch = async () => ({
    ok: true,
    json: async () => ({ vocabulary_size: 0, top_terms: [{ term: 'Search', doc_freq: 1, total_freq: 1 }] }),
  });
  wireVocabularySearch();
  document.getElementById('vocab-q').value = '  Search  ';
  document.getElementById('vocab-search-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  // Case-insensitive match via the 'i' flag, not via lowercasing the input
  // (lowercasing would corrupt a pattern like "[A-Z]").
  assert.equal(document.getElementById('vocab-table').querySelector('a').textContent, 'Search');
});
