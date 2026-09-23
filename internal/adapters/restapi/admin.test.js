'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

function load() {
  return requireFresh('./admin.js');
}

// Compares SVGs via round-tripped innerHTML, since jsdom expands self-closing tags
// (e.g. <circle/> -> <circle></circle>) -- raw string comparison would spuriously fail.
function normalizedSVG(raw) {
  const el = document.createElement('div');
  el.innerHTML = raw;
  return el.innerHTML;
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

test('buildTile builds a .tile with a .k label and a .v value', () => {
  const { buildTile } = load();
  const tile = buildTile('Running crawl jobs', '2');
  assert.equal(tile.className, 'tile');
  assert.equal(tile.querySelector('.k').textContent, 'Running crawl jobs');
  assert.equal(tile.querySelector('.v').textContent, '2');
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

test('regexFilter returns items unchanged and clears the error for a blank pattern', () => {
  const { regexFilter } = load();
  const errorEl = document.createElement('div');
  errorEl.textContent = 'stale error';
  const items = ['a', 'b'];
  assert.equal(regexFilter(items, '', errorEl, () => false), items);
  assert.equal(errorEl.textContent, '');
});

test('regexFilter filters via testFn and clears any previous error', () => {
  const { regexFilter } = load();
  const errorEl = document.createElement('div');
  errorEl.textContent = 'stale error';
  const result = regexFilter(['foo', 'bar', 'baz'], 'ba', errorEl, (re, item) => re.test(item));
  assert.deepEqual(result, ['bar', 'baz']);
  assert.equal(errorEl.textContent, '');
});

test('regexFilter reports an invalid pattern with the default prefix and returns items unchanged', () => {
  const { regexFilter } = load();
  const errorEl = document.createElement('div');
  const items = ['a'];
  const result = regexFilter(items, '(', errorEl, () => true);
  assert.equal(result, items);
  assert.equal(errorEl.textContent.startsWith('Invalid regex: '), true);
});

test('regexFilter uses a custom error prefix when given one', () => {
  const { regexFilter } = load();
  const errorEl = document.createElement('div');
  regexFilter(['a'], '(', errorEl, () => true, 'Invalid pattern');
  assert.equal(errorEl.textContent.startsWith('Invalid pattern: '), true);
});

test('pollWhileInProgress schedules a new timer when in progress and none is running', () => {
  const { pollWhileInProgress } = load();
  const originalSetTimeout = global.setTimeout;
  let scheduledFn, scheduledMs;
  global.setTimeout = (fn, ms) => { scheduledFn = fn; scheduledMs = ms; return 'fake-timer'; };
  try {
    const reload = () => {};
    const result = pollWhileInProgress(true, null, reload);
    assert.equal(result, 'fake-timer');
    assert.equal(scheduledFn, reload);
    assert.equal(scheduledMs, 2000);
  } finally {
    global.setTimeout = originalSetTimeout;
  }
});

test('pollWhileInProgress leaves an already-scheduled timer alone', () => {
  const { pollWhileInProgress } = load();
  const originalSetTimeout = global.setTimeout;
  let calls = 0;
  global.setTimeout = () => { calls++; return 'new-timer'; };
  try {
    const result = pollWhileInProgress(true, 'existing-timer', () => {});
    assert.equal(result, 'existing-timer');
    assert.equal(calls, 0);
  } finally {
    global.setTimeout = originalSetTimeout;
  }
});

test('pollWhileInProgress clears a running timer once no longer in progress', () => {
  const { pollWhileInProgress } = load();
  const originalClearTimeout = global.clearTimeout;
  let cleared;
  global.clearTimeout = (handle) => { cleared = handle; };
  try {
    const result = pollWhileInProgress(false, 'existing-timer', () => {});
    assert.equal(result, null);
    assert.equal(cleared, 'existing-timer');
  } finally {
    global.clearTimeout = originalClearTimeout;
  }
});

test('pollWhileInProgress is a no-op when not in progress and nothing is scheduled', () => {
  const { pollWhileInProgress } = load();
  const result = pollWhileInProgress(false, null, () => {});
  assert.equal(result, null);
});

test('renderAdminNav is a no-op when the page has no #admin-rail element', () => {
  const { renderAdminNav } = load();
  assert.doesNotThrow(() => renderAdminNav());
});

test('renderAdminNav renders Overview plus every group from ADMIN_NAV_GROUPS', () => {
  setupDOM('<!doctype html><html><body><nav id="admin-rail"></nav></body></html>', 'http://x/admin/settings');
  const { renderAdminNav, ADMIN_NAV_GROUPS } = load();
  renderAdminNav();
  const rail = document.getElementById('admin-rail');

  const top = rail.querySelector('.rail-top');
  assert.equal(top.textContent, 'Overview');
  assert.equal(top.getAttribute('href'), '/admin');

  const groups = rail.querySelectorAll('.rail-group');
  assert.equal(groups.length, ADMIN_NAV_GROUPS.length - 1);
  assert.equal(groups[0].querySelector('.rail-tab').textContent, 'Content');
  const contentLinks = Array.from(groups[0].querySelectorAll('a')).map((a) => a.textContent);
  assert.deepEqual(contentLinks, ['Documents', 'Content dedup']);
});

test('renderAdminNav includes a Chat group linking to the settings page\'s chat section', () => {
  setupDOM('<!doctype html><html><body><nav id="admin-rail"></nav></body></html>', 'http://x/admin/settings');
  const { renderAdminNav } = load();
  renderAdminNav();
  const rail = document.getElementById('admin-rail');
  const groups = Array.from(rail.querySelectorAll('.rail-group'));
  const chatGroup = groups.find((g) => g.querySelector('.rail-tab').textContent === 'Chat');
  assert.ok(chatGroup, 'expected a Chat nav group');
  const links = Array.from(chatGroup.querySelectorAll('a'));
  assert.equal(links.length, 3);
  assert.equal(links[0].textContent, 'Settings');
  assert.equal(links[0].getAttribute('href'), '/admin/chat/settings');
  assert.equal(links[1].textContent, 'MCP servers');
  assert.equal(links[1].getAttribute('href'), '/admin/mcp-servers');
  assert.equal(links[2].textContent, 'Agents');
  assert.equal(links[2].getAttribute('href'), '/admin/agents');
});

test('renderAdminNav marks the entry matching the current path as current, and nothing else', () => {
  setupDOM('<!doctype html><html><body><nav id="admin-rail"></nav></body></html>', 'http://x/admin/settings');
  const { renderAdminNav } = load();
  renderAdminNav();
  const rail = document.getElementById('admin-rail');
  const current = rail.querySelectorAll('[aria-current="page"]');
  assert.equal(current.length, 1);
  assert.equal(current[0].textContent, 'Settings');
  assert.equal(rail.querySelector('.rail-top').hasAttribute('aria-current'), false);
});

test('renderAdminNav marks Overview itself as current on /admin', () => {
  setupDOM('<!doctype html><html><body><nav id="admin-rail"></nav></body></html>', 'http://x/admin');
  const { renderAdminNav } = load();
  renderAdminNav();
  assert.equal(document.querySelector('.rail-top').getAttribute('aria-current'), 'page');
});

test('renderAdminNav clears any previous content before re-rendering', () => {
  setupDOM('<!doctype html><html><body><nav id="admin-rail"><span>stale</span></nav></body></html>', 'http://x/admin');
  const { renderAdminNav } = load();
  renderAdminNav();
  assert.equal(document.getElementById('admin-rail').querySelector('span'), null);
});

test('wireSignOut is a no-op when there is no #sign-out button', () => {
  const { wireSignOut } = load();
  assert.doesNotThrow(() => wireSignOut());
});

// Post-logout navigation isn't asserted -- jsdom's window.location is a real, non-configurable
// Location object that can't be spied on, and real navigation is unsupported/noisy. The fetch
// call is the meaningful logic here.
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

// vocabFixture builds the vocabulary panel's full markup (search form, page-size field,
// summary/table, pager) -- every test below shares this shape, since these pieces are usually
// exercised together.
function vocabFixture() {
  setupDOM(
    '<!doctype html><html><body>' +
      '<form id="vocab-search-form"><input id="vocab-q">' +
      '<input id="vocab-page-size" value="20"></form>' +
      '<div id="vocab-summary"></div><div id="vocab-table"></div>' +
      '<div id="vocab-pager" hidden><button id="vocab-prev"></button>' +
      '<span id="vocab-page-info"></span><button id="vocab-next"></button></div>' +
      '</body></html>',
  );
}

test('loadVocabulary is a no-op when the page has neither vocab element', async () => {
  const { loadVocabulary } = load();
  await assert.doesNotReject(() => loadVocabulary());
});

test('loadVocabulary requests limit/offset/sort/dir and renders vocabulary_size plus the term list', async () => {
  vocabFixture();
  const { loadVocabulary } = load();
  let gotURL;
  global.fetch = async (url) => {
    gotURL = url;
    return {
      ok: true,
      json: async () => ({
        vocabulary_size: 100,
        matched_count: 100,
        terms: [
          { term: 'search', doc_freq: 10, total_freq: 20 },
          { term: 'engine', doc_freq: 5, total_freq: 8 },
        ],
      }),
    };
  };
  await loadVocabulary();
  assert.equal(gotURL, '/admin/api/vocabulary?limit=20&offset=0&sort=doc_freq&dir=desc');
  assert.equal(document.getElementById('vocab-summary').textContent.includes('100'), true);
  const rows = document.getElementById('vocab-table').querySelectorAll('tbody tr');
  assert.equal(rows.length, 2);
  const link = document.getElementById('vocab-table').querySelector('a');
  assert.equal(link.textContent, 'search');
  assert.equal(link.getAttribute('href'), '/admin/vocabulary/term?term=search');
});

test('loadVocabulary shows a no-terms message when the corpus has none', async () => {
  vocabFixture();
  const { loadVocabulary } = load();
  global.fetch = async () => ({ ok: true, json: async () => ({ vocabulary_size: 0, matched_count: 0, terms: [] }) });
  await loadVocabulary();
  assert.equal(document.getElementById('vocab-table').textContent, 'No terms indexed yet.');
});

test('loadVocabulary reports the error message on a failed fetch', async () => {
  vocabFixture();
  const { loadVocabulary } = load();
  global.fetch = async () => ({ ok: false, status: 500, text: async () => 'vocab unavailable' });
  await loadVocabulary();
  assert.equal(document.getElementById('vocab-summary').textContent.includes('vocab unavailable'), true);
});

test('wireVocabularySearch does nothing when there is no search form', () => {
  setupDOM('<!doctype html><html><body><div id="vocab-summary"></div><div id="vocab-table"></div></body></html>');
  const { wireVocabularySearch } = load();
  let called = false;
  global.fetch = async () => {
    called = true;
    return { ok: true, json: async () => ({ vocabulary_size: 0, matched_count: 0, terms: [] }) };
  };
  assert.doesNotThrow(() => wireVocabularySearch());
  assert.equal(called, false);
});

test('wireVocabularySearch loads page 1 immediately (a real list, not search-only)', async () => {
  vocabFixture();
  const { wireVocabularySearch } = load();
  let fetchCount = 0;
  global.fetch = async () => {
    fetchCount++;
    return { ok: true, json: async () => ({ vocabulary_size: 1, matched_count: 1, terms: [{ term: 'foo', doc_freq: 1, total_freq: 1 }] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(fetchCount, 1);
  assert.equal(document.getElementById('vocab-table').querySelector('a').textContent, 'foo');
});

test('wireVocabularySearch debounces typing into the search filter, resets to page 1, and sends the trimmed term', async () => {
  vocabFixture();
  const { wireVocabularySearch } = load();
  let fetchCount = 0;
  let lastURL;
  global.fetch = async (url) => {
    fetchCount++;
    lastURL = url;
    return { ok: true, json: async () => ({ vocabulary_size: 0, matched_count: 0, terms: [{ term: 'foo', doc_freq: 1, total_freq: 1 }] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0)); // the immediate page-1 load
  fetchCount = 0;
  const input = document.getElementById('vocab-q');
  input.value = 'f';
  input.dispatchEvent(new window.Event('input'));
  input.value = 'fo';
  input.dispatchEvent(new window.Event('input'));
  input.value = '  foo  ';
  input.dispatchEvent(new window.Event('input'));
  // Only the last keystroke's debounced call should ever fire.
  await new Promise((resolve) => setTimeout(resolve, 250));
  assert.equal(fetchCount, 1);
  assert.equal(lastURL.includes('search=foo'), true);
  assert.equal(lastURL.includes('offset=0'), true);
});

test('wireVocabularySearch submitting the form loads immediately with the trimmed query', async () => {
  vocabFixture();
  const { wireVocabularySearch } = load();
  let lastURL;
  global.fetch = async (url) => {
    lastURL = url;
    return { ok: true, json: async () => ({ vocabulary_size: 0, matched_count: 0, terms: [{ term: 'Search', doc_freq: 1, total_freq: 1 }] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('vocab-q').value = '  Search  ';
  document.getElementById('vocab-search-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL.includes('search=Search'), true);
  assert.equal(document.getElementById('vocab-table').querySelector('a').textContent, 'Search');
});

test('wireVocabularySearch reads the page-size field\'s initial value and sends it as limit', async () => {
  vocabFixture();
  document.getElementById('vocab-page-size').value = '5';
  const { wireVocabularySearch } = load();
  let lastURL;
  global.fetch = async (url) => {
    lastURL = url;
    return { ok: true, json: async () => ({ vocabulary_size: 0, matched_count: 0, terms: [] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL.includes('limit=5'), true);
});

test('changing the page-size field reloads page 1 with the new limit', async () => {
  vocabFixture();
  const { wireVocabularySearch } = load();
  let lastURL;
  global.fetch = async (url) => {
    lastURL = url;
    return { ok: true, json: async () => ({ vocabulary_size: 0, matched_count: 0, terms: [] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('vocab-page-size').value = '50';
  document.getElementById('vocab-page-size').dispatchEvent(new window.Event('change'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL.includes('limit=50'), true);
  assert.equal(lastURL.includes('offset=0'), true);
});

test('an invalid page-size value falls back to 20', async () => {
  vocabFixture();
  const { wireVocabularySearch } = load();
  let lastURL;
  global.fetch = async (url) => {
    lastURL = url;
    return { ok: true, json: async () => ({ vocabulary_size: 0, matched_count: 0, terms: [] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  document.getElementById('vocab-page-size').value = 'abc';
  document.getElementById('vocab-page-size').dispatchEvent(new window.Event('change'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL.includes('limit=20'), true);
});

test('clicking a column header sorts by it, defaulting frequency columns to descending', async () => {
  vocabFixture();
  const { wireVocabularySearch } = load();
  let lastURL;
  global.fetch = async (url) => {
    lastURL = url;
    return { ok: true, json: async () => ({ vocabulary_size: 1, matched_count: 1, terms: [{ term: 'foo', doc_freq: 1, total_freq: 3 }] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  const totalFreqHeader = document.getElementById('vocab-table').querySelectorAll('th')[2];
  totalFreqHeader.dispatchEvent(new window.Event('click', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL.includes('sort=total_freq'), true);
  assert.equal(lastURL.includes('dir=desc'), true);
});

test('clicking the term column header defaults to ascending', async () => {
  vocabFixture();
  const { wireVocabularySearch } = load();
  let lastURL;
  global.fetch = async (url) => {
    lastURL = url;
    return { ok: true, json: async () => ({ vocabulary_size: 1, matched_count: 1, terms: [{ term: 'foo', doc_freq: 1, total_freq: 3 }] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  const termHeader = document.getElementById('vocab-table').querySelectorAll('th')[0];
  termHeader.dispatchEvent(new window.Event('click', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL.includes('sort=term'), true);
  assert.equal(lastURL.includes('dir=asc'), true);
});

test('clicking the already-active column header flips direction instead of resetting it', async () => {
  vocabFixture();
  const { wireVocabularySearch } = load();
  let lastURL;
  global.fetch = async (url) => {
    lastURL = url;
    return { ok: true, json: async () => ({ vocabulary_size: 1, matched_count: 1, terms: [{ term: 'foo', doc_freq: 1, total_freq: 3 }] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  // Default sort is doc_freq/desc -- clicking that same column should flip to asc.
  const docFreqHeader = document.getElementById('vocab-table').querySelectorAll('th')[1];
  docFreqHeader.dispatchEvent(new window.Event('click', { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL.includes('sort=doc_freq'), true);
  assert.equal(lastURL.includes('dir=asc'), true);
});

test('the pager is hidden for a single page and shown, with Previous/Next wired, across multiple pages', async () => {
  vocabFixture();
  document.getElementById('vocab-page-size').value = '1';
  const { wireVocabularySearch } = load();
  let lastURL;
  global.fetch = async (url) => {
    lastURL = url;
    return { ok: true, json: async () => ({ vocabulary_size: 2, matched_count: 2, terms: [{ term: 'foo', doc_freq: 1, total_freq: 1 }] }) };
  };
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  const pagerEl = document.getElementById('vocab-pager');
  assert.equal(pagerEl.hidden, false);
  assert.equal(document.getElementById('vocab-page-info').textContent, 'Page 1 of 2');
  assert.equal(document.getElementById('vocab-prev').disabled, true);
  assert.equal(document.getElementById('vocab-next').disabled, false);

  document.getElementById('vocab-next').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL.includes('offset=1'), true);
  assert.equal(document.getElementById('vocab-page-info').textContent, 'Page 2 of 2');
  assert.equal(document.getElementById('vocab-next').disabled, true);
  assert.equal(document.getElementById('vocab-prev').disabled, false);

  document.getElementById('vocab-prev').dispatchEvent(new window.Event('click'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(lastURL.includes('offset=0'), true);
});

test('the pager is hidden when everything fits on one page', async () => {
  vocabFixture();
  const { wireVocabularySearch } = load();
  global.fetch = async () => ({ ok: true, json: async () => ({ vocabulary_size: 1, matched_count: 1, terms: [{ term: 'foo', doc_freq: 1, total_freq: 1 }] }) });
  wireVocabularySearch();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(document.getElementById('vocab-pager').hidden, true);
});

test('setIconLabel sets an SVG icon, title, and aria-label for a view/delete key', () => {
  const { setIconLabel, ICON_SVGS } = load();
  const btn = document.createElement('button');
  setIconLabel(btn, 'delete', 'Delete');
  assert.equal(btn.classList.contains('icon-button'), true);
  assert.equal(btn.innerHTML, normalizedSVG(ICON_SVGS.delete));
  assert.equal(btn.title, 'Delete');
  assert.equal(btn.getAttribute('aria-label'), 'Delete');
});

test('setIconLabel reuses the delete icon under a different label (job list\'s Cancel button)', () => {
  const { setIconLabel, ICON_SVGS } = load();
  const btn = document.createElement('button');
  setIconLabel(btn, 'delete', 'Cancel');
  assert.equal(btn.innerHTML, normalizedSVG(ICON_SVGS.delete));
  assert.equal(btn.title, 'Cancel');
  assert.equal(btn.getAttribute('aria-label'), 'Cancel');
});

test('setIconLabel sets a plain glyph as text content for a run/edit key', () => {
  const { setIconLabel, ACTION_GLYPHS } = load();
  const link = document.createElement('a');
  setIconLabel(link, 'edit', 'Edit');
  assert.equal(link.textContent, ACTION_GLYPHS.edit);
  assert.equal(link.getAttribute('aria-label'), 'Edit');
});

test('actionsCell builds an Edit link and a Delete button wired to the given handler', () => {
  const { actionsCell } = load();
  let deleted = false;
  const td = actionsCell('/admin/users/bob', 'Delete', () => { deleted = true; });
  assert.equal(td.className, 'actions');
  const editLink = td.querySelector('a.text-button');
  assert.equal(editLink.getAttribute('href'), '/admin/users/bob');
  assert.equal(editLink.getAttribute('aria-label'), 'Edit');
  const delBtn = td.querySelector('button.text-button');
  assert.equal(delBtn.getAttribute('aria-label'), 'Delete');
  delBtn.dispatchEvent(new window.Event('click'));
  assert.equal(deleted, true);
});
