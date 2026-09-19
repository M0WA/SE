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
  assert.deepEqual(JSON.parse(gotOpts.body), { messages: [{ role: 'user', content: 'what is the answer?' }], rag: true, web_search: true });

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

test('pressing Enter in chat-input submits the form', () => {
  let fetched = false;
  global.fetch = async () => { fetched = true; return { ok: true, json: async () => ({ answer: 'ok' }) }; };
  loadFixture();
  const chatInput = document.getElementById('chat-input');
  chatInput.value = 'hello';
  chatInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
  assert.equal(fetched, true);
  assert.equal(chatInput.value, '');
});

test('pressing Shift+Enter in chat-input does not submit the form', () => {
  let fetched = false;
  global.fetch = async () => { fetched = true; return { ok: true, json: async () => ({ answer: 'ok' }) }; };
  loadFixture();
  const chatInput = document.getElementById('chat-input');
  chatInput.value = 'hello';
  chatInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, bubbles: true, cancelable: true }));
  assert.equal(fetched, false);
  assert.equal(chatInput.value, 'hello');
});

test('pressing a non-Enter key in chat-input does not submit the form', () => {
  let fetched = false;
  global.fetch = async () => { fetched = true; return { ok: true, json: async () => ({ answer: 'ok' }) }; };
  loadFixture();
  const chatInput = document.getElementById('chat-input');
  chatInput.value = 'hello';
  chatInput.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'a', bubbles: true, cancelable: true }));
  assert.equal(fetched, false);
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
