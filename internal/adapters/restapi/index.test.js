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
  // Ignores index.js's own on-load /session call (unrelated to this test)
  // and only tracks whether an actual search request went out.
  global.fetch = async (url) => { if (String(url).startsWith('/search')) fetched = true; return { ok: true, json: async () => ({ results: [] }) }; };
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

test('defaults to chat mode on load', () => {
  loadFixture();
  assert.equal(document.getElementById('chat-panel').hidden, false);
  assert.equal(document.getElementById('search-form').hidden, true);
  assert.equal(document.getElementById('mode-switch').getAttribute('aria-checked'), 'true');
});

test('setMode toggles the switch and swaps panel visibility in both directions', () => {
  const { setMode } = loadFixture();
  const modeSwitch = document.getElementById('mode-switch');
  const searchForm = document.getElementById('search-form');
  const chatPanel = document.getElementById('chat-panel');
  const chatOptions = document.getElementById('chat-options');
  const syntaxNote = document.getElementById('syntax-note');
  const status = document.getElementById('status');
  const resultsEl = document.getElementById('results');

  // The page now defaults to chat mode on load (see the new
  // 'defaults to chat mode on load' test below), so before any explicit
  // setMode call here the switch is already checked and the chat panel
  // already visible.
  assert.equal(modeSwitch.getAttribute('aria-checked'), 'true');
  assert.equal(chatPanel.hidden, false);
  assert.equal(chatOptions.hidden, false);

  setMode('chat');
  assert.equal(modeSwitch.getAttribute('aria-checked'), 'true');
  assert.equal(searchForm.hidden, true);
  assert.equal(syntaxNote.hidden, true);
  assert.equal(status.hidden, true);
  assert.equal(resultsEl.hidden, true);
  assert.equal(chatPanel.hidden, false);
  assert.equal(chatOptions.hidden, false);

  setMode('search');
  assert.equal(modeSwitch.getAttribute('aria-checked'), 'false');
  assert.equal(searchForm.hidden, false);
  assert.equal(syntaxNote.hidden, false);
  assert.equal(status.hidden, false);
  assert.equal(resultsEl.hidden, false);
  assert.equal(chatPanel.hidden, true);
  assert.equal(chatOptions.hidden, true);
});

test('the mode switch button toggles mode on click', () => {
  loadFixture();
  const modeSwitch = document.getElementById('mode-switch');
  const chatPanel = document.getElementById('chat-panel');

  // Chat is already the default on load, so the first click switches to
  // search, and the second click switches back to chat.
  modeSwitch.dispatchEvent(new window.Event('click', { bubbles: true }));
  assert.equal(modeSwitch.getAttribute('aria-checked'), 'false');
  assert.equal(chatPanel.hidden, true);

  modeSwitch.dispatchEvent(new window.Event('click', { bubbles: true }));
  assert.equal(modeSwitch.getAttribute('aria-checked'), 'true');
  assert.equal(chatPanel.hidden, false);
});

test('setMode restores a hidden correction-note rather than forcing it open', () => {
  const { setMode } = loadFixture();
  const correctionNote = document.getElementById('correction-note');
  correctionNote.hidden = false;
  correctionNote.textContent = 'Showing results for “the” instead of “teh”.';

  setMode('chat');
  assert.equal(correctionNote.hidden, true);

  setMode('search');
  assert.equal(correctionNote.hidden, false);
});

test('switching back to search does not clear chat history or messages', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'hi' }) });
  const { setMode, sendChatMessage } = loadFixture();
  await sendChatMessage('hello');
  assert.equal(document.getElementById('chat-messages').children.length, 2);

  setMode('search');
  setMode('chat');
  assert.equal(document.getElementById('chat-messages').children.length, 2);
});

test('setMode toggles main.chat-mode for the wide-screen chat layout', () => {
  const { setMode } = loadFixture();
  const main = document.querySelector('main');
  assert.equal(main.classList.contains('chat-mode'), true, 'chat is the default mode on load');

  setMode('search');
  assert.equal(main.classList.contains('chat-mode'), false);

  setMode('chat');
  assert.equal(main.classList.contains('chat-mode'), true);
});

test('chat-input placeholder no longer references "the index" (the index is only searched in Search mode)', () => {
  loadFixture();
  assert.equal(document.getElementById('chat-input').placeholder, 'Enter to send, Shift+Enter for a new line');
});

test('a fresh page starts with exactly one tab and no close button', () => {
  const { tabs } = loadFixture();
  assert.equal(tabs.length, 1);
  assert.equal(document.querySelectorAll('.chat-tab').length, 1);
  assert.equal(document.querySelectorAll('.chat-tab-close').length, 0);
});

test('sendChatMessage renames a fresh tab\'s title from its first message, but not its second', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'hi' }) });
  const { sendChatMessage, activeTab } = loadFixture();
  await sendChatMessage('short question');
  assert.equal(activeTab().title, 'short question');

  await sendChatMessage('and the second question?');
  assert.equal(activeTab().title, 'short question');
});

test('sendChatMessage truncates a long first message into the tab title', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'hi' }) });
  const { sendChatMessage, activeTab } = loadFixture();
  await sendChatMessage('this is a very long first question that should be truncated for the tab title');
  assert.equal(activeTab().title, 'this is a very long firs…');
});

test('newChatTab adds and switches to a fresh, empty tab; the tab strip gains close buttons once there are two', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'hi' }) });
  const { sendChatMessage, newChatTab, tabs, activeTab } = loadFixture();
  await sendChatMessage('first tab question');
  const firstId = activeTab().id;

  const created = newChatTab();
  assert.equal(tabs.length, 2);
  assert.equal(activeTab().id, created.id);
  assert.notEqual(activeTab().id, firstId);
  assert.equal(activeTab().history.length, 0);
  assert.equal(document.getElementById('chat-messages').children.length, 0);
  assert.equal(document.querySelectorAll('.chat-tab-close').length, 2);
});

test('switchTab re-renders #chat-messages from the target tab\'s own stored history', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'first answer' }) });
  const { sendChatMessage, newChatTab, switchTab, tabs } = loadFixture();
  await sendChatMessage('first tab question');
  const firstId = tabs[0].id;

  const second = newChatTab();
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'second answer' }) });
  await sendChatMessage('second tab question');
  assert.equal(document.getElementById('chat-messages').children.length, 2);

  switchTab(firstId);
  const messages = document.getElementById('chat-messages').children;
  assert.equal(messages.length, 2);
  assert.equal(messages[0].textContent, 'first tab question');
  assert.equal(messages[1].textContent.includes('first answer'), true);

  switchTab(second.id);
  const messages2 = document.getElementById('chat-messages').children;
  assert.equal(messages2[1].textContent.includes('second answer'), true);
});

test('switchTab to the already-active tab is a no-op (no re-render churn)', () => {
  const { switchTab, activeTab, renderActiveTab } = loadFixture();
  const id = activeTab().id;
  // Sanity: calling switchTab with the current tab's own id must not throw
  // and must leave the active tab unchanged.
  switchTab(id);
  assert.equal(activeTab().id, id);
});

test('forkActiveTab deep-copies history into a new independent tab, titled "<original> (fork)"', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'first answer', tool_results: [{ tool_name: 'web_search', output: 'x' }] }) });
  const { sendChatMessage, forkActiveTab, tabs, activeTab } = loadFixture();
  await sendChatMessage('original question');
  const source = activeTab();

  const forked = forkActiveTab();
  assert.equal(tabs.length, 2);
  assert.equal(activeTab().id, forked.id);
  assert.equal(forked.title, source.title + ' (fork)');
  assert.deepEqual(forked.history, source.history);
  assert.notEqual(forked.history, source.history, 'must be a copy, not the same array reference');
  assert.notEqual(forked.history[1], source.history[1], 'each entry must be its own copy too');

  // Continuing the fork must never mutate the source tab.
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'fork-only answer' }) });
  await sendChatMessage('fork-only question');
  assert.equal(forked.history.length, 4);
  assert.equal(source.history.length, 2);
});

test('closeTab removes a tab and falls back to its previous sibling when it was active', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'hi' }) });
  const { sendChatMessage, newChatTab, closeTab, tabs, activeTab } = loadFixture();
  await sendChatMessage('tab one');
  const second = newChatTab();
  const third = newChatTab();
  assert.equal(tabs.length, 3);
  assert.equal(activeTab().id, third.id);

  closeTab(third.id);
  assert.equal(tabs.length, 2);
  assert.equal(tabs.some((t) => t.id === third.id), false, 'the closed tab must be gone');
  assert.equal(activeTab().id, second.id, 'falls back to the immediately preceding tab');
});

test('closeTab never removes the last remaining tab', () => {
  const { closeTab, tabs, activeTab } = loadFixture();
  const onlyId = activeTab().id;
  closeTab(onlyId);
  assert.equal(tabs.length, 1);
  assert.equal(activeTab().id, onlyId);
});

test('closeTab on a background (non-active) tab does not change which tab is active', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'hi' }) });
  const { sendChatMessage, newChatTab, closeTab, tabs, activeTab } = loadFixture();
  await sendChatMessage('tab one');
  const first = activeTab();
  const second = newChatTab();
  assert.equal(activeTab().id, second.id);

  closeTab(first.id);
  assert.equal(tabs.length, 1);
  assert.equal(activeTab().id, second.id);
});

test('a reply arriving after the user switched away updates the sending tab\'s own history but leaves the visible tab/DOM untouched', async () => {
  let resolveFetch;
  global.fetch = () => new Promise((resolve) => { resolveFetch = resolve; });
  const { sendChatMessage, newChatTab, tabs, activeTab } = loadFixture();
  const first = activeTab();
  const sendPromise = sendChatMessage('slow question');

  const second = newChatTab();
  assert.equal(activeTab().id, second.id);

  resolveFetch({ ok: true, json: async () => ({ answer: 'late answer' }) });
  await sendPromise;

  // The background tab's own data is still updated...
  assert.equal(first.history.length, 2);
  assert.equal(first.history[1].content, 'late answer');
  // ...but the currently-visible tab (and its chat-messages DOM) is
  // untouched by the late arrival.
  assert.equal(activeTab().id, second.id);
  assert.equal(document.getElementById('chat-messages').children.length, 0);
  assert.equal(tabs.length, 2);
});

test('serializeTab/deserializeTab round trip a tab\'s title and history', () => {
  const { serializeTab, deserializeTab } = loadFixture();
  const tab = {
    id: 1, title: 'My chat',
    history: [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: 'hello', context_trimmed: true, tool_results: [{ tool_name: 'web_search', output: 'x' }] },
    ],
    tokenUsage: null,
  };
  const json = serializeTab(tab);
  const parsed = deserializeTab(json);
  assert.equal(parsed.title, 'My chat');
  // deserializeTab normalizes every entry with context_trimmed/tool_results
  // defaults, even one (like the plain user turn here) that never had them
  // in the first place.
  assert.deepEqual(parsed.history, [
    { role: 'user', content: 'hi', context_trimmed: false, tool_results: [] },
    { role: 'assistant', content: 'hello', context_trimmed: true, tool_results: [{ tool_name: 'web_search', output: 'x' }] },
  ]);
});

test('deserializeTab throws on something that is not a chat export', () => {
  const { deserializeTab } = loadFixture();
  assert.throws(() => deserializeTab('{"not":"a chat export"}'));
  assert.throws(() => deserializeTab('not even json'));
});

test('deserializeTab drops malformed history entries but keeps the valid ones, and defaults a missing title', () => {
  const { deserializeTab } = loadFixture();
  const parsed = deserializeTab(JSON.stringify({
    history: [
      { role: 'user', content: 'kept' },
      { role: 'bogus', content: 'dropped: bad role' },
      { role: 'user', content: 42 },
      null,
      { role: 'assistant', content: 'kept too' },
    ],
  }));
  assert.equal(parsed.title, 'Imported chat');
  assert.equal(parsed.history.length, 2);
  assert.equal(parsed.history[0].content, 'kept');
  assert.equal(parsed.history[1].content, 'kept too');
});

test('importTabFromJSON creates and switches to a new tab built from the parsed export', () => {
  const { importTabFromJSON, tabs, activeTab } = loadFixture();
  const json = JSON.stringify({ title: 'Imported', history: [{ role: 'user', content: 'hi' }] });
  const created = importTabFromJSON(json);
  assert.equal(tabs.length, 2);
  assert.equal(activeTab().id, created.id);
  assert.equal(created.title, 'Imported');
  assert.equal(document.getElementById('chat-messages').children.length, 1);
});

test('the import file input wires a chosen file through importTabFromJSON', async () => {
  const { tabs } = loadFixture();
  const input = document.getElementById('chat-tab-import-input');
  const file = new window.File([JSON.stringify({ title: 'From file', history: [] })], 'chat.json', { type: 'application/json' });
  Object.defineProperty(input, 'files', { value: [file], configurable: true });
  input.dispatchEvent(new window.Event('change'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(tabs.length, 2);
  assert.equal(tabs[1].title, 'From file');
});

test('the import file input reports an error status without adding a tab when the file is not valid JSON', async () => {
  const { tabs } = loadFixture();
  const input = document.getElementById('chat-tab-import-input');
  const file = new window.File(['not json'], 'chat.json', { type: 'application/json' });
  Object.defineProperty(input, 'files', { value: [file], configurable: true });
  input.dispatchEvent(new window.Event('change'));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(tabs.length, 1);
  assert.equal(document.getElementById('chat-status').textContent.includes('Could not import chat'), true);
});

test('exportActiveTab builds a Blob and triggers/cleans up a download without throwing', () => {
  const { sendChatMessage, exportActiveTab } = loadFixture();
  let created = 0;
  let revoked = 0;
  // index.js runs under plain Node (via require(), not inside jsdom's own
  // window), so the Blob it constructs is Node's own global Blob, not
  // window.Blob -- check its shape/type instead of an instanceof that
  // would only hold true for a real browser <script> tag.
  global.URL.createObjectURL = (blob) => { created++; assert.equal(blob.type, 'application/json'); return 'blob:mock-url'; };
  global.URL.revokeObjectURL = () => { revoked++; };
  try {
    exportActiveTab();
  } finally {
    delete global.URL.createObjectURL;
    delete global.URL.revokeObjectURL;
  }
  assert.equal(created, 1);
  assert.equal(revoked, 1);
});

test('clicking the tab-strip buttons wires new/fork/export/import to their own functions', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'hi' }) });
  global.URL.createObjectURL = () => 'blob:mock-url';
  global.URL.revokeObjectURL = () => {};
  try {
    const { sendChatMessage, tabs } = loadFixture();
    await sendChatMessage('seed question');

    document.getElementById('chat-tab-new').dispatchEvent(new window.Event('click'));
    assert.equal(tabs.length, 2);

    document.getElementById('chat-tab-fork').dispatchEvent(new window.Event('click'));
    assert.equal(tabs.length, 3);

    // Export must not throw when wired through the real button click.
    document.getElementById('chat-tab-export').dispatchEvent(new window.Event('click'));

    // Import opens the native file picker via the hidden input's own
    // click() -- just prove the button is wired to trigger it, not the
    // browser's file dialog itself (untestable in jsdom).
    let importInputClicked = false;
    document.getElementById('chat-tab-import-input').addEventListener('click', () => { importInputClicked = true; });
    document.getElementById('chat-tab-import').dispatchEvent(new window.Event('click'));
    assert.equal(importInputClicked, true);
  } finally {
    delete global.URL.createObjectURL;
    delete global.URL.revokeObjectURL;
  }
});

test('renderChatMessage scrolls #chat-messages so the new turn\'s own beginning is visible', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'hi there' }) });
  const { sendChatMessage } = loadFixture();
  const chatMessages = document.getElementById('chat-messages');
  // jsdom never computes real layout, so offsetTop is always 0 by default --
  // stub it to grow with each message's position (as a real stacked chat
  // history would), so scrollTop actually moving to match the newest
  // message's own offsetTop -- not chatMessages.scrollHeight, which would
  // land on that message's *end* rather than its *beginning* -- proves the
  // scroll call ran, and that a long answer is read starting from its first
  // line rather than its last.
  Object.defineProperty(window.HTMLElement.prototype, 'offsetTop', {
    get() { return Array.from(chatMessages.children).indexOf(this) * 100; },
    configurable: true,
  });

  await sendChatMessage('hello');
  // Two turns appended (user, then assistant) land at indices 0 and 1; the
  // assistant turn, appended last, is what the scroll should land on.
  assert.equal(chatMessages.scrollTop, 100, 'expected scroll to the assistant turn\'s own top');

  await sendChatMessage('another question');
  assert.equal(chatMessages.scrollTop, 300, 'expected scroll again to the newest assistant turn\'s own top');
});

test('sendChatMessage on success appends both turns to history and renders the answer', async () => {
  let gotURL, gotOpts;
  global.fetch = async (url, opts) => {
    gotURL = url;
    gotOpts = opts;
    return {
      ok: true,
      json: async () => ({ answer: 'The answer is 42.' }),
    };
  };
  const { sendChatMessage, activeTab } = loadFixture();
  await sendChatMessage('what is the answer?');

  assert.equal(gotURL, '/chat');
  assert.equal(gotOpts.method, 'POST');
  assert.equal(gotOpts.headers['Content-Type'], 'application/json');
  // At the moment the request was sent, the active tab's history held only
  // the user's just-appended turn -- the assistant's reply is pushed only
  // afterward, once the response comes back.
  assert.deepEqual(JSON.parse(gotOpts.body), { messages: [{ role: 'user', content: 'what is the answer?' }], web_search: true });

  const history = activeTab().history;
  assert.equal(history.length, 2);
  assert.deepEqual(history[0], { role: 'user', content: 'what is the answer?' });
  assert.deepEqual(history[1], { role: 'assistant', content: 'The answer is 42.', context_trimmed: undefined, tool_results: [] });

  const messages = document.getElementById('chat-messages').children;
  assert.equal(messages.length, 2);
  assert.equal(messages[0].className, 'chat-msg chat-msg-user');
  assert.equal(messages[0].querySelector('.chat-msg-bubble').textContent, 'what is the answer?');
  assert.equal(messages[1].className, 'chat-msg chat-msg-assistant');
  assert.equal(messages[1].querySelector('.chat-msg-bubble').textContent, 'The answer is 42.');

  assert.equal(document.getElementById('chat-status').textContent, '');
});

test('sendChatMessage renders a context-trimmed note when the backend flags context_trimmed', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'answer', context_trimmed: true }) });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  const note = assistantMsg.querySelector('.chat-context-note');
  assert.notEqual(note, null);
  assert.equal(note.textContent, 'Older messages were dropped from context to fit the model’s limit.');
});

test('sendChatMessage renders no context-trimmed note when context_trimmed is absent', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'answer' }) });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  assert.equal(assistantMsg.querySelector('.chat-context-note'), null);
});

test('sendChatMessage renders a folded, closed <details> per tool result', async () => {
  global.fetch = async () => ({
    ok: true,
    json: async () => ({
      answer: 'answer',
      tool_results: [
        { tool_name: 'web_search', output: '{"results":[]}' },
        { tool_name: 'broken_tool', err: 'server timed out' },
      ],
    }),
  });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  const results = assistantMsg.querySelectorAll('.chat-hook-result');
  assert.equal(results.length, 2);

  assert.equal(results[0].open, false);
  assert.equal(results[0].querySelector('summary').textContent, 'web_search');
  assert.equal(results[0].querySelector('pre').textContent, '{"results":[]}');
  assert.equal(results[0].classList.contains('chat-hook-result-error'), false);

  assert.equal(results[1].open, false);
  assert.equal(results[1].querySelector('summary').textContent, 'broken_tool');
  assert.equal(results[1].querySelector('pre').textContent, 'server timed out');
  assert.equal(results[1].classList.contains('chat-hook-result-error'), true);
});

test('sendChatMessage renders no tool-result elements when tool_results is absent', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'answer' }) });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  assert.equal(assistantMsg.querySelectorAll('.chat-hook-result').length, 0);
});

test('sendChatMessage renders a fetch-named tool result as an open outer fold with a link and a closed nested response fold', async () => {
  global.fetch = async () => ({
    ok: true,
    json: async () => ({
      answer: 'answer',
      tool_results: [
        { tool_name: 'web_fetch', arguments: JSON.stringify({ url: 'https://example.com/page' }), output: 'page contents here' },
      ],
    }),
  });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  const outer = assistantMsg.querySelector('.chat-hook-result');
  assert.notEqual(outer, null);
  assert.equal(outer.hasAttribute('open'), true);
  assert.equal(outer.querySelector('summary').textContent, 'web_fetch');

  const link = outer.querySelector('.chat-hook-target a');
  assert.notEqual(link, null);
  assert.equal(link.getAttribute('href'), 'https://example.com/page');
  assert.equal(link.textContent, 'https://example.com/page');

  const nested = outer.querySelector('.chat-hook-response');
  assert.notEqual(nested, null);
  assert.equal(nested.hasAttribute('open'), false);
  assert.equal(nested.querySelector('summary').textContent, 'Response');
  assert.equal(nested.querySelector('pre').textContent, 'page contents here');
});

test('sendChatMessage renders a search-named tool result\'s parsed results list, with the raw JSON still in the nested fold', async () => {
  const rawOutput = JSON.stringify({
    results: [
      { title: 'First', url: 'https://a.example' },
      { title: 'Second', url: 'https://b.example' },
      { title: 'Third', url: 'https://c.example' },
      { title: 'Fourth', url: 'https://d.example' },
      { title: 'Fifth', url: 'https://e.example' },
      { title: 'Sixth', url: 'https://f.example' },
    ],
  });
  global.fetch = async () => ({
    ok: true,
    json: async () => ({
      answer: 'answer',
      tool_results: [
        { tool_name: 'web_search', arguments: JSON.stringify({ query: 'golang release notes' }), output: rawOutput },
      ],
    }),
  });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  const outer = assistantMsg.querySelector('.chat-hook-result');
  assert.equal(outer.hasAttribute('open'), true);
  assert.equal(outer.querySelector('.chat-hook-target code').textContent, 'golang release notes');

  const items = outer.querySelectorAll('.chat-hook-results-list li');
  assert.equal(items.length, 5, 'expected the results list capped at 5 entries');
  assert.equal(items[0].querySelector('a').textContent, 'First');
  assert.equal(items[0].querySelector('a').getAttribute('href'), 'https://a.example');

  const nested = outer.querySelector('.chat-hook-response');
  assert.notEqual(nested, null);
  assert.equal(nested.hasAttribute('open'), false);
  assert.equal(nested.querySelector('summary').textContent, 'Raw output');
  assert.equal(nested.querySelector('pre').textContent, rawOutput);
});

test('sendChatMessage renders no results list for a search-named tool whose output is not valid JSON, but still shows the raw output fold', async () => {
  global.fetch = async () => ({
    ok: true,
    json: async () => ({
      answer: 'answer',
      tool_results: [
        { tool_name: 'web_search', arguments: JSON.stringify({ query: 'golang release notes' }), output: 'not json at all' },
      ],
    }),
  });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  const outer = assistantMsg.querySelector('.chat-hook-result');
  assert.equal(outer.querySelectorAll('.chat-hook-results-list').length, 0);

  const nested = outer.querySelector('.chat-hook-response');
  assert.notEqual(nested, null);
  assert.equal(nested.hasAttribute('open'), false);
  assert.equal(nested.querySelector('pre').textContent, 'not json at all');
});

test('sendChatMessage keeps the old single-level, closed-by-default rendering for a generic/other-named tool result', async () => {
  global.fetch = async () => ({
    ok: true,
    json: async () => ({
      answer: 'answer',
      tool_results: [
        { tool_name: 'lookup_docs', arguments: JSON.stringify({ q: 'ignored for generic tools' }), output: 'doc contents' },
      ],
    }),
  });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  const results = assistantMsg.querySelectorAll('.chat-hook-result');
  assert.equal(results.length, 1);
  assert.equal(results[0].hasAttribute('open'), false);
  assert.equal(results[0].classList.contains('chat-hook-response'), false);
  assert.equal(results[0].querySelector('summary').textContent, 'lookup_docs');
  assert.equal(results[0].querySelector('pre').textContent, 'doc contents');
  assert.equal(results[0].querySelectorAll('.chat-hook-target').length, 0);
});

test('sendChatMessage falls back to the generic rendering when a fetch-named tool\'s arguments have no string value', async () => {
  global.fetch = async () => ({
    ok: true,
    json: async () => ({
      answer: 'answer',
      tool_results: [
        { tool_name: 'web_fetch', arguments: JSON.stringify({ count: 3 }), output: 'no url to show' },
      ],
    }),
  });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  const results = assistantMsg.querySelectorAll('.chat-hook-result');
  assert.equal(results.length, 1);
  assert.equal(results[0].hasAttribute('open'), false);
  assert.equal(results[0].querySelectorAll('.chat-hook-target').length, 0);
  assert.equal(results[0].querySelector('pre').textContent, 'no url to show');
});

test('sendChatMessage shows the persistent token-usage badge next to the Web checkbox, not in the chat', async () => {
  global.fetch = async () => ({
    ok: true,
    json: async () => ({
      answer: 'answer',
      token_usage: { global_prompt_tokens: 10, tool_prompt_tokens: 5, user_prompt_tokens: 2, history_tokens: 3, max_context_tokens: 1000 },
    }),
  });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  assert.equal(assistantMsg.querySelectorAll('.chat-token-usage').length, 0);

  const usage = document.getElementById('chat-token-usage');
  assert.equal(usage.hidden, false);
  assert.equal(document.getElementById('chat-token-usage-summary').textContent, '20 / 1,000');
  const donut = document.getElementById('chat-token-usage-donut');
  assert.equal(donut.querySelectorAll('svg.donut-chart').length, 1);
  assert.equal(donut.querySelectorAll('.donut-legend-row').length, 4);
  const mini = document.getElementById('chat-token-usage-mini');
  assert.equal(mini.querySelectorAll('svg.donut-chart').length, 1);
});

test('sendChatMessage omits the max-context suffix and renders 0 for token_usage fields the response leaves out', async () => {
  global.fetch = async () => ({
    ok: true,
    json: async () => ({ answer: 'answer', token_usage: { history_tokens: 7 } }),
  });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  assert.equal(document.getElementById('chat-token-usage-summary').textContent, '7');
});

test('sendChatMessage renders a zero-value badge, still visible, when token_usage is absent', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'answer' }) });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  assert.equal(document.getElementById('chat-token-usage').hidden, false);
  assert.equal(document.getElementById('chat-token-usage-summary').textContent, '0');
});

test('buildDonutSVG renders one circle per nonzero segment, skipping zero-value ones', () => {
  const { buildDonutSVG } = loadFixture();
  const svg = buildDonutSVG([{ label: 'a', value: 3, color: 'red' }, { label: 'b', value: 0, color: 'blue' }, { label: 'c', value: 1, color: 'green' }]);
  assert.equal(svg.querySelectorAll('circle').length, 2);
});

test('buildDonutSVG renders a single muted ring when every segment is zero', () => {
  const { buildDonutSVG } = loadFixture();
  const svg = buildDonutSVG([{ label: 'a', value: 0, color: 'red' }]);
  const circles = svg.querySelectorAll('circle');
  assert.equal(circles.length, 1);
  assert.equal(circles[0].getAttribute('stroke'), 'var(--rule)');
});

test('buildDonutLegend renders a swatch and label:value text per segment', () => {
  const { buildDonutLegend } = loadFixture();
  const legend = buildDonutLegend([{ label: 'History', value: 1234, color: 'var(--ink-muted)' }]);
  const row = legend.querySelector('.donut-legend-row');
  assert.equal(row.querySelector('.donut-legend-swatch').style.background, 'var(--ink-muted)');
  assert.equal(row.textContent, 'History: 1,234');
});

test('tokenUsageSegments maps the flat response shape to the four fixed chart segments', () => {
  const { tokenUsageSegments } = loadFixture();
  const segments = tokenUsageSegments({ global_prompt_tokens: 1, tool_prompt_tokens: 2, user_prompt_tokens: 8, history_tokens: 4 });
  assert.deepEqual(segments.map((s) => s.value), [1, 2, 8, 4]);
  assert.deepEqual(segments.map((s) => s.label), ['Global prompt', 'Tool prompts', 'Your prompt', 'Conversation history']);
});

test('renderTokenUsage renders an all-zero donut, without a max-context suffix, for a falsy tokenUsage', () => {
  const { renderTokenUsage } = loadFixture();
  renderTokenUsage(null);
  assert.equal(document.getElementById('chat-token-usage').hidden, false);
  assert.equal(document.getElementById('chat-token-usage-summary').textContent, '0');
  const donut = document.getElementById('chat-token-usage-donut');
  assert.equal(donut.querySelectorAll('svg.donut-chart').length, 1);
  assert.equal(donut.querySelector('.donut-center-label'), null);
});

test('renderTokenUsage centers a rounded usage percentage in the hover donut once max_context_tokens is known', () => {
  const { renderTokenUsage } = loadFixture();
  renderTokenUsage({ global_prompt_tokens: 10, tool_prompt_tokens: 0, user_prompt_tokens: 0, history_tokens: 0, max_context_tokens: 1000 });
  const donut = document.getElementById('chat-token-usage-donut');
  const label = donut.querySelector('.donut-center-label');
  assert.notEqual(label, null);
  assert.equal(label.textContent, '1%');
  // The always-visible mini donut is too small to hold legible text.
  const mini = document.getElementById('chat-token-usage-mini');
  assert.equal(mini.querySelector('.donut-center-label'), null);
});

test('sendChatMessage renders the server error text on a non-ok response', async () => {
  global.fetch = async () => ({ ok: false, text: async () => ' endpoint not configured ' });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  assert.equal(document.getElementById('chat-status').textContent, 'Chat failed: endpoint not configured');
  // The failed turn's reply is never appended -- only the user's own message shows.
  assert.equal(document.getElementById('chat-messages').children.length, 1);
});

test('sendChatMessage renders a generic failure message when fetch throws', async () => {
  global.fetch = async () => { throw new Error('boom'); };
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  assert.equal(document.getElementById('chat-status').textContent, 'Chat failed: could not reach the server.');
});

test('submitting an empty chat message is a no-op', () => {
  let fetched = false;
  global.fetch = async (url) => { if (url === '/chat') fetched = true; return { ok: true, json: async () => ({ answer: '' }) }; };
  loadFixture();
  document.getElementById('chat-input').value = '   ';
  document.getElementById('chat-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  assert.equal(fetched, false);
  assert.equal(document.getElementById('chat-messages').children.length, 0);
  assert.equal(document.getElementById('chat-status').textContent, 'Type something to ask.');
});

test('submitting the chat form sends the trimmed input and clears the field', async () => {
  let sent;
  global.fetch = async (url, opts) => { sent = JSON.parse(opts.body); return { ok: true, json: async () => ({ answer: 'ok' }) }; };
  loadFixture();
  const chatInput = document.getElementById('chat-input');
  chatInput.value = '  hello there  ';
  document.getElementById('chat-form').dispatchEvent(new window.Event('submit', { cancelable: true }));
  assert.equal(chatInput.value, '');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(sent.messages[0], { role: 'user', content: 'hello there' });
});

test('pressing Enter in chat-input submits the form', () => {
  let fetched = false;
  global.fetch = async (url) => { if (url === '/chat') fetched = true; return { ok: true, json: async () => ({ answer: 'ok' }) }; };
  loadFixture();
  const chatInput = document.getElementById('chat-input');
  chatInput.value = 'hello';
  chatInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
  assert.equal(fetched, true);
  assert.equal(chatInput.value, '');
});

test('pressing Shift+Enter in chat-input does not submit the form', () => {
  let fetched = false;
  global.fetch = async (url) => { if (url === '/chat') fetched = true; return { ok: true, json: async () => ({ answer: 'ok' }) }; };
  loadFixture();
  const chatInput = document.getElementById('chat-input');
  chatInput.value = 'hello';
  chatInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, bubbles: true, cancelable: true }));
  assert.equal(fetched, false);
  assert.equal(chatInput.value, 'hello');
});

test('pressing a non-Enter key in chat-input does not submit the form', () => {
  let fetched = false;
  global.fetch = async (url) => { if (url === '/chat') fetched = true; return { ok: true, json: async () => ({ answer: 'ok' }) }; };
  loadFixture();
  const chatInput = document.getElementById('chat-input');
  chatInput.value = 'hello';
  chatInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'a', bubbles: true, cancelable: true }));
  assert.equal(fetched, false);
});

test('the "web" checkbox is checked by default, and sends web_search accordingly per question', async () => {
  const { sendChatMessage } = loadFixture();
  let sent;
  global.fetch = async (url, opts) => { sent = JSON.parse(opts.body); return { ok: true, json: async () => ({ answer: 'ok' }) }; };
  const chatWebSearch = document.getElementById('chat-web-search');
  assert.equal(chatWebSearch.checked, true);

  await sendChatMessage('first question');
  assert.equal(sent.web_search, true);

  chatWebSearch.checked = false;
  await sendChatMessage('second question');
  assert.equal(sent.web_search, false);
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

test('loadSession shows the admin link for an admin-role session', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ role: 'admin' }) });
  const { loadSession } = loadFixture();
  await loadSession();
  assert.equal(document.getElementById('admin-link').hidden, false);
  assert.equal(document.getElementById('account-link').hidden, true);
});

test('loadSession shows the account link for a user-role session', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ role: 'user' }) });
  const { loadSession } = loadFixture();
  await loadSession();
  assert.equal(document.getElementById('account-link').hidden, false);
  assert.equal(document.getElementById('admin-link').hidden, true);
});

test('loadSession keeps both links hidden on a non-ok /session response', async () => {
  global.fetch = async () => ({ ok: false, status: 401, text: async () => 'unauthorized' });
  const { loadSession } = loadFixture();
  await loadSession();
  assert.equal(document.getElementById('admin-link').hidden, true);
  assert.equal(document.getElementById('account-link').hidden, true);
});

test('loadSession keeps both links hidden when the fetch throws', async () => {
  global.fetch = async () => { throw new Error('network down'); };
  const { loadSession } = loadFixture();
  await loadSession();
  assert.equal(document.getElementById('admin-link').hidden, true);
  assert.equal(document.getElementById('account-link').hidden, true);
});

test('escapeHTML neutralizes tags and entities', () => {
  const { escapeHTML } = loadFixture();
  assert.equal(escapeHTML('<script>alert(1)</script>'), '&lt;script&gt;alert(1)&lt;/script&gt;');
  assert.equal(escapeHTML('a & b'), 'a &amp; b');
});

test('renderInline renders bold, italic, inline code, and links', () => {
  const { renderInline } = loadFixture();
  assert.equal(renderInline('a **bold** word'), 'a <strong>bold</strong> word');
  assert.equal(renderInline('an *italic* word'), 'an <em>italic</em> word');
  assert.equal(renderInline('some `code` here'), 'some <code>code</code> here');
  assert.equal(
    renderInline('a [link](https://example.com) here'),
    'a <a href="https://example.com" target="_blank" rel="noopener noreferrer">link</a> here',
  );
});

test('renderInline never interprets markdown syntax found inside a code span', () => {
  const { renderInline } = loadFixture();
  assert.equal(renderInline('`**not bold**`'), '<code>**not bold**</code>');
});

test('renderMarkdown escapes raw HTML in the model output before applying any markdown', () => {
  const { renderMarkdown } = loadFixture();
  assert.equal(renderMarkdown('<img src=x onerror=alert(1)>'), '<p>&lt;img src=x onerror=alert(1)&gt;</p>');
});

test('renderMarkdown wraps a single line of plain text in one paragraph', () => {
  const { renderMarkdown } = loadFixture();
  assert.equal(renderMarkdown('hello world'), '<p>hello world</p>');
});

test('renderMarkdown joins consecutive non-blank lines into one paragraph with <br>, and starts a new paragraph on a blank line', () => {
  const { renderMarkdown } = loadFixture();
  assert.equal(renderMarkdown('line one\nline two\n\nsecond paragraph'), '<p>line one<br>line two</p><p>second paragraph</p>');
});

test('renderMarkdown renders a fenced code block verbatim, untouched by inline formatting', () => {
  const { renderMarkdown } = loadFixture();
  assert.equal(renderMarkdown('```\nconst a = 1;\n```'), '<pre><code>const a = 1;</code></pre>');
});

test('renderMarkdown renders a language-tagged fence the same as an untagged one', () => {
  const { renderMarkdown } = loadFixture();
  assert.equal(renderMarkdown('```js\nconst a = 1;\n```'), '<pre><code>const a = 1;</code></pre>');
});

test('renderMarkdown renders a "-" unordered list as <ul><li>', () => {
  const { renderMarkdown } = loadFixture();
  assert.equal(renderMarkdown('- one\n- two'), '<ul><li>one</li><li>two</li></ul>');
});

test('renderMarkdown renders a "1." ordered list as <ol><li>', () => {
  const { renderMarkdown } = loadFixture();
  assert.equal(renderMarkdown('1. one\n2. two'), '<ol><li>one</li><li>two</li></ol>');
});

test('renderMarkdown renders an ATX heading as a heading tag two levels down, applying inline formatting', () => {
  const { renderMarkdown } = loadFixture();
  assert.equal(renderMarkdown('## **Bold** heading'), '<h4><strong>Bold</strong> heading</h4>');
});

test('renderMarkdown handles a heading, a paragraph, a list, and a code block together in one answer', () => {
  const { renderMarkdown } = loadFixture();
  const md = '# Summary\n\nHere is what I found:\n\n- first point\n- second point\n\n```\nfoo()\n```';
  assert.equal(
    renderMarkdown(md),
    '<h3>Summary</h3><p>Here is what I found:</p><ul><li>first point</li><li>second point</li></ul><pre><code>foo()</code></pre>',
  );
});

test('sendChatMessage renders the assistant answer as markdown', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'a **bold** claim' }) });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const bubble = document.getElementById('chat-messages').children[1].querySelector('.chat-msg-bubble');
  assert.equal(bubble.innerHTML, '<p>a <strong>bold</strong> claim</p>');
});
