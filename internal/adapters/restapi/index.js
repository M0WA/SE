  const form = document.getElementById('search-form');
  const input = document.getElementById('q');
  const status = document.getElementById('status');
  const correctionNote = document.getElementById('correction-note');
  const results = document.getElementById('results');
  const syntaxNote = document.getElementById('syntax-note');

  const modeSwitch = document.getElementById('mode-switch');
  const chatPanel = document.getElementById('chat-panel');
  const chatMessages = document.getElementById('chat-messages');
  const chatStatus = document.getElementById('chat-status');
  const chatForm = document.getElementById('chat-form');
  const chatInput = document.getElementById('chat-input');

  // chatHistory is the full running conversation, sent in full on every
  // /chat call -- the backend is stateless and has no server-side session,
  // so the client is the only place this state lives.
  const chatHistory = [];

  // renderCorrectionNote shows a quiet, transparent note when the search
  // service fuzzy-corrected a misspelled query term (see corrected_terms on
  // each result) -- the displayed query itself is never silently rewritten,
  // this just says which term(s) were substituted for scoring.
  function renderCorrectionNote(list) {
    const corrected = (list[0] && list[0].corrected_terms) || [];
    if (corrected.length === 0) {
      correctionNote.hidden = true;
      correctionNote.textContent = '';
      return;
    }
    correctionNote.textContent = 'Showing results for ' +
      corrected.map((c) => '“' + c.corrected + '”').join(', ') +
      ' instead of ' + corrected.map((c) => '“' + c.original + '”').join(', ') + '.';
    correctionNote.hidden = false;
  }

  function clear(el) {
    while (el.firstChild) el.removeChild(el.firstChild);
  }

  function scoreRow(label, value) {
    const row = document.createElement('div');
    row.className = 'score-row';
    const k = document.createElement('span');
    k.className = 'score-label';
    k.textContent = label;
    const v = document.createElement('span');
    v.className = 'score-value';
    v.textContent = Number(value).toFixed(3);
    row.appendChild(k);
    row.appendChild(v);
    return row;
  }

  function renderResults(query, list) {
    clear(results);
    renderCorrectionNote(list);
    if (list.length === 0) {
      status.textContent = 'No matches for “' + query + '”.';
      return;
    }
    status.textContent = list.length === 1 ? '1 match' : list.length + ' matches';
    for (const r of list) {
      const row = document.createElement('div');
      row.className = 'result';

      const head = document.createElement('div');
      head.className = 'result-head';

      const title = document.createElement('a');
      title.className = 'result-title';
      title.href = r.url;
      title.target = '_blank';
      title.rel = 'noopener noreferrer';
      title.textContent = r.title || r.url;
      head.appendChild(title);

      const score = document.createElement('span');
      score.className = 'result-score';
      score.textContent = Number(r.score).toFixed(3);
      head.appendChild(score);

      row.appendChild(head);

      const url = document.createElement('div');
      url.className = 'result-url';
      url.textContent = r.url;
      row.appendChild(url);

      if (r.snippet) {
        const snippet = document.createElement('div');
        snippet.className = 'result-snippet';
        snippet.innerHTML = r.snippet;
        row.appendChild(snippet);
      }

      if (r.bm25_score !== undefined && r.semantic_sim !== undefined) {
        const details = document.createElement('details');
        details.className = 'result-details';
        const summary = document.createElement('summary');
        summary.textContent = 'Details';
        details.appendChild(summary);
        details.appendChild(scoreRow('bm25', r.bm25_score));
        details.appendChild(scoreRow('semantic', r.semantic_sim));
        details.appendChild(scoreRow('final', r.score));
        row.appendChild(details);
      }

      results.appendChild(row);
    }
  }

  async function runSearch(query, sort) {
    clear(results);
    correctionNote.hidden = true;
    status.textContent = 'Searching…';
    try {
      const resp = await fetch('/search?q=' + encodeURIComponent(query) + '&sort=' + encodeURIComponent(sort));
      if (!resp.ok) {
        const msg = await resp.text();
        status.textContent = 'Search failed: ' + msg.trim();
        return;
      }
      const data = await resp.json();
      renderResults(query, data.results || []);
    } catch (err) {
      status.textContent = 'Search failed: could not reach the server.';
    }
  }

  // renderChatMessage appends one message to #chat-messages for a
  // {role, content} turn. User and assistant turns are told apart by
  // alignment and a quiet tint (see .chat-msg-user/.chat-msg-assistant in
  // style.css) rather than a "You:"/"Assistant:" label. sources (only ever
  // present on the assistant's most recent turn -- chatHistory itself
  // never carries them, since the backend contract doesn't echo them back
  // on later turns) are rendered as a small link list underneath, same
  // title-or-url fallback renderResults already uses for r.title || r.url.
  function renderChatMessage(role, content, sources) {
    const msg = document.createElement('div');
    msg.className = role === 'user' ? 'chat-msg chat-msg-user' : 'chat-msg chat-msg-assistant';

    const bubble = document.createElement('div');
    bubble.className = 'chat-msg-bubble';
    bubble.textContent = content;
    msg.appendChild(bubble);

    if (role === 'assistant' && sources && sources.length > 0) {
      const list = document.createElement('div');
      list.className = 'chat-sources';
      for (const s of sources) {
        const link = document.createElement('a');
        link.className = 'chat-source-link';
        link.href = s.url;
        link.target = '_blank';
        link.rel = 'noopener noreferrer';
        link.textContent = s.title || s.url;
        list.appendChild(link);
      }
      msg.appendChild(list);
    }

    chatMessages.appendChild(msg);
    return msg;
  }

  // sendChatMessage appends the user's turn to chatHistory, renders it
  // immediately, then POSTs the full history to /chat -- see runSearch
  // above for the same ok/non-ok/network-failure pattern this mirrors.
  async function sendChatMessage(content) {
    chatHistory.push({ role: 'user', content });
    renderChatMessage('user', content);
    chatStatus.textContent = 'Thinking…';
    try {
      const resp = await fetch('/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ messages: chatHistory }),
      });
      if (!resp.ok) {
        const msg = await resp.text();
        chatStatus.textContent = 'Chat failed: ' + msg.trim();
        return;
      }
      const data = await resp.json();
      chatHistory.push({ role: 'assistant', content: data.answer });
      renderChatMessage('assistant', data.answer, data.sources || []);
      chatStatus.textContent = '';
    } catch (err) {
      chatStatus.textContent = 'Chat failed: could not reach the server.';
    }
  }

  // setMode swaps the page between its two independent views. Only
  // #correction-note has hidden-state of its own (renderCorrectionNote
  // shows/hides it depending on whether the last search had a correction),
  // so its prior state is saved and restored rather than forced open --
  // everything else here is unconditionally shown/hidden together.
  let correctionNoteHiddenBeforeChat = true;

  function setMode(mode) {
    const isChat = mode === 'chat';
    modeSwitch.setAttribute('aria-checked', String(isChat));
    if (isChat) {
      correctionNoteHiddenBeforeChat = correctionNote.hidden;
      form.hidden = true;
      if (syntaxNote) syntaxNote.hidden = true;
      status.hidden = true;
      correctionNote.hidden = true;
      results.hidden = true;
      chatPanel.hidden = false;
    } else {
      form.hidden = false;
      if (syntaxNote) syntaxNote.hidden = false;
      status.hidden = false;
      correctionNote.hidden = correctionNoteHiddenBeforeChat;
      results.hidden = false;
      chatPanel.hidden = true;
    }
  }

  modeSwitch.addEventListener('click', () => {
    const isChat = modeSwitch.getAttribute('aria-checked') === 'true';
    setMode(isChat ? 'search' : 'chat');
  });

  chatForm.addEventListener('submit', (e) => {
    e.preventDefault();
    const content = chatInput.value.trim();
    if (!content) {
      chatStatus.textContent = 'Type something to ask.';
      return;
    }
    chatInput.value = '';
    sendChatMessage(content);
  });

  const sortSelect = document.getElementById('sort');

  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const query = input.value.trim();
    if (!query) {
      status.textContent = 'Type something to search for.';
      clear(results);
      correctionNote.hidden = true;
      return;
    }
    runSearch(query, sortSelect.value);
  });

  // Mirrors admin.js's wireSignOut -- this page doesn't load admin.js (it's
  // the public site, not the admin backend), so the same few lines are
  // inlined here rather than pulling in the whole admin script for one
  // function.
  document.getElementById('sign-out').addEventListener('click', async () => {
    try {
      await fetch('/logout', { method: 'POST' });
    } finally {
      window.location = '/';
    }
  });

  // Exports for the Node test runner only -- `typeof module` is undefined in
  // a browser's <script> tag. See index.test.js.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
      renderCorrectionNote, clear, scoreRow, renderResults, runSearch,
      chatHistory, renderChatMessage, sendChatMessage, setMode,
    };
  }
