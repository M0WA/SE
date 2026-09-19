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

test('setMode toggles the switch and swaps panel visibility in both directions', () => {
  const { setMode } = loadFixture();
  const modeSwitch = document.getElementById('mode-switch');
  const searchForm = document.getElementById('search-form');
  const chatPanel = document.getElementById('chat-panel');
  const syntaxNote = document.getElementById('syntax-note');
  const status = document.getElementById('status');
  const resultsEl = document.getElementById('results');

  assert.equal(modeSwitch.getAttribute('aria-checked'), 'false');
  assert.equal(chatPanel.hidden, true);

  setMode('chat');
  assert.equal(modeSwitch.getAttribute('aria-checked'), 'true');
  assert.equal(searchForm.hidden, true);
  assert.equal(syntaxNote.hidden, true);
  assert.equal(status.hidden, true);
  assert.equal(resultsEl.hidden, true);
  assert.equal(chatPanel.hidden, false);

  setMode('search');
  assert.equal(modeSwitch.getAttribute('aria-checked'), 'false');
  assert.equal(searchForm.hidden, false);
  assert.equal(syntaxNote.hidden, false);
  assert.equal(status.hidden, false);
  assert.equal(resultsEl.hidden, false);
  assert.equal(chatPanel.hidden, true);
});

test('the mode switch button toggles mode on click', () => {
  loadFixture();
  const modeSwitch = document.getElementById('mode-switch');
  const chatPanel = document.getElementById('chat-panel');

  modeSwitch.dispatchEvent(new window.Event('click', { bubbles: true }));
  assert.equal(modeSwitch.getAttribute('aria-checked'), 'true');
  assert.equal(chatPanel.hidden, false);

  modeSwitch.dispatchEvent(new window.Event('click', { bubbles: true }));
  assert.equal(modeSwitch.getAttribute('aria-checked'), 'false');
  assert.equal(chatPanel.hidden, true);
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

test('sendChatMessage on success appends both turns to history and renders sources', async () => {
  let gotURL, gotOpts;
  global.fetch = async (url, opts) => {
    gotURL = url;
    gotOpts = opts;
    return {
      ok: true,
      json: async () => ({
        answer: 'The answer is 42.',
        sources: [{ url: 'http://a', title: 'A' }, { url: 'http://b', title: '' }],
      }),
    };
  };
  const { sendChatMessage, chatHistory } = loadFixture();
  await sendChatMessage('what is the answer?');

  assert.equal(gotURL, '/chat');
  assert.equal(gotOpts.method, 'POST');
  assert.equal(gotOpts.headers['Content-Type'], 'application/json');
  // At the moment the request was sent, chatHistory held only the user's
  // just-appended turn -- the assistant's reply is pushed only afterward,
  // once the response comes back.
  assert.deepEqual(JSON.parse(gotOpts.body), { messages: [{ role: 'user', content: 'what is the answer?' }], rag: true });

  assert.equal(chatHistory.length, 2);
  assert.deepEqual(chatHistory[0], { role: 'user', content: 'what is the answer?' });
  assert.deepEqual(chatHistory[1], { role: 'assistant', content: 'The answer is 42.' });

  const messages = document.getElementById('chat-messages').children;
  assert.equal(messages.length, 2);
  assert.equal(messages[0].className, 'chat-msg chat-msg-user');
  assert.equal(messages[0].querySelector('.chat-msg-bubble').textContent, 'what is the answer?');
  assert.equal(messages[1].className, 'chat-msg chat-msg-assistant');
  assert.equal(messages[1].querySelector('.chat-msg-bubble').textContent, 'The answer is 42.');

  const links = messages[1].querySelectorAll('.chat-source-link');
  assert.equal(links.length, 2);
  assert.equal(links[0].getAttribute('href'), 'http://a');
  assert.equal(links[0].textContent, 'A');
  assert.equal(links[1].getAttribute('href'), 'http://b');
  assert.equal(links[1].textContent, 'http://b');

  assert.equal(document.getElementById('chat-status').textContent, '');
});

test('sendChatMessage renders an assistant message with no source list when none are given', async () => {
  global.fetch = async () => ({ ok: true, json: async () => ({ answer: 'no sources here' }) });
  const { sendChatMessage } = loadFixture();
  await sendChatMessage('q');
  const assistantMsg = document.getElementById('chat-messages').children[1];
  assert.equal(assistantMsg.querySelector('.chat-sources'), null);
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
  global.fetch = async () => { fetched = true; return { ok: true, json: async () => ({ answer: '' }) }; };
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

test('the "use search results" checkbox is checked by default, and sends rag accordingly per question', async () => {
  const { sendChatMessage } = loadFixture();
  let sent;
  global.fetch = async (url, opts) => { sent = JSON.parse(opts.body); return { ok: true, json: async () => ({ answer: 'ok' }) }; };
  const chatRag = document.getElementById('chat-rag');
  assert.equal(chatRag.checked, true);

  await sendChatMessage('first question');
  assert.equal(sent.rag, true);

  chatRag.checked = false;
  await sendChatMessage('second question');
  assert.equal(sent.rag, false);
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
