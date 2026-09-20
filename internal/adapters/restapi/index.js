  const form = document.getElementById('search-form');
  const input = document.getElementById('q');
  const status = document.getElementById('status');
  const correctionNote = document.getElementById('correction-note');
  const results = document.getElementById('results');
  const syntaxNote = document.getElementById('syntax-note');

  const modeSwitch = document.getElementById('mode-switch');
  const chatOptions = document.getElementById('chat-options');
  const chatPanel = document.getElementById('chat-panel');
  const chatMessages = document.getElementById('chat-messages');
  const chatStatus = document.getElementById('chat-status');
  const chatForm = document.getElementById('chat-form');
  const chatInput = document.getElementById('chat-input');
  const chatWebSearch = document.getElementById('chat-web-search');
  const chatTokenUsage = document.getElementById('chat-token-usage');
  const chatTokenUsageMini = document.getElementById('chat-token-usage-mini');
  const chatTokenUsageSummary = document.getElementById('chat-token-usage-summary');
  const chatTokenUsageDonut = document.getElementById('chat-token-usage-donut');

  // chatHistory is the full running conversation, sent in full on every
  // /chat call -- the backend is stateless and has no server-side session,
  // so the client is the only place this state lives.
  const chatHistory = [];

  // buildDonutSVG/buildDonutLegend render a per-turn token-usage chart from
  // {label, value, color} segments -- a small, local duplicate of
  // admin.js's own copy (see admin_chat_settings.js's context-budget
  // preview) rather than a shared import: this is the public search page,
  // served by search-server, and admin.js is only routed to admin-server
  // paths (see packaging/nginx/searchengine.conf) -- pulling it in here
  // would mean either a cross-server fetch or restructuring routing for a
  // ~30-line helper, not worth it.
  function buildDonutSVG(segments, opts) {
    opts = opts || {};
    const size = opts.size || 72;
    const strokeWidth = opts.strokeWidth || 12;
    const r = (size - strokeWidth) / 2;
    const c = size / 2;
    const circumference = 2 * Math.PI * r;
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 ' + size + ' ' + size);
    svg.setAttribute('width', String(size));
    svg.setAttribute('height', String(size));
    svg.classList.add('donut-chart');

    const total = segments.reduce((sum, s) => sum + Math.max(0, s.value), 0);
    if (total <= 0) {
      const bg = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
      bg.setAttribute('cx', String(c));
      bg.setAttribute('cy', String(c));
      bg.setAttribute('r', String(r));
      bg.setAttribute('fill', 'none');
      bg.setAttribute('stroke', 'var(--rule)');
      bg.setAttribute('stroke-width', String(strokeWidth));
      svg.appendChild(bg);
      return svg;
    }

    let offset = 0;
    for (const seg of segments) {
      const value = Math.max(0, seg.value);
      if (value === 0) continue;
      const dash = (value / total) * circumference;
      const circle = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
      circle.setAttribute('cx', String(c));
      circle.setAttribute('cy', String(c));
      circle.setAttribute('r', String(r));
      circle.setAttribute('fill', 'none');
      circle.setAttribute('stroke', seg.color);
      circle.setAttribute('stroke-width', String(strokeWidth));
      circle.setAttribute('stroke-dasharray', dash + ' ' + (circumference - dash));
      circle.setAttribute('stroke-dashoffset', String(-offset));
      circle.setAttribute('transform', 'rotate(-90 ' + c + ' ' + c + ')');
      const title = document.createElementNS('http://www.w3.org/2000/svg', 'title');
      title.textContent = seg.label + ': ' + seg.value;
      circle.appendChild(title);
      svg.appendChild(circle);
      offset += dash;
    }
    return svg;
  }

  function buildDonutLegend(segments) {
    const list = document.createElement('div');
    list.className = 'donut-legend';
    for (const seg of segments) {
      const row = document.createElement('div');
      row.className = 'donut-legend-row';
      const swatch = document.createElement('span');
      swatch.className = 'donut-legend-swatch';
      swatch.style.background = seg.color;
      row.appendChild(swatch);
      const label = document.createElement('span');
      label.textContent = seg.label + ': ' + seg.value.toLocaleString();
      row.appendChild(label);
      list.appendChild(row);
    }
    return list;
  }

  // tokenUsageSegments turns the backend's flat token_usage breakdown into
  // the {label, value, color} shape buildDonutSVG/buildDonutLegend expect
  // -- a fixed 3-way split (global prompt, active hook prompts, conversation
  // history) shared by every turn, regardless of which pieces were actually
  // nonzero this time.
  function tokenUsageSegments(u) {
    return [
      { label: 'Global prompt', value: u.global_prompt_tokens || 0, color: 'var(--chart-1)' },
      { label: 'Hook prompts', value: u.hook_prompt_tokens || 0, color: 'var(--chart-2)' },
      { label: 'Conversation history', value: u.history_tokens || 0, color: 'var(--ink-muted)' },
    ];
  }

  // renderTokenUsage updates the persistent token-usage summary shown next
  // to the Web checkbox (see #chat-token-usage in index.html) -- unlike the
  // old per-turn folded donut this replaces, it stays visible and up to
  // date for the whole chat session once a first answer sets it, rather
  // than being buried inside each individual chat bubble. Passing a falsy
  // tokenUsage (e.g. before any turn has completed) hides it.
  function renderTokenUsage(tokenUsage) {
    if (!tokenUsage) {
      chatTokenUsage.hidden = true;
      return;
    }
    const total = (tokenUsage.global_prompt_tokens || 0) + (tokenUsage.hook_prompt_tokens || 0) + (tokenUsage.history_tokens || 0);
    const segments = tokenUsageSegments(tokenUsage);
    clear(chatTokenUsageMini);
    chatTokenUsageMini.appendChild(buildDonutSVG(segments, { size: 14, strokeWidth: 4 }));
    chatTokenUsageSummary.textContent = total.toLocaleString() +
      (tokenUsage.max_context_tokens ? ' / ' + tokenUsage.max_context_tokens.toLocaleString() : '');
    clear(chatTokenUsageDonut);
    chatTokenUsageDonut.appendChild(buildDonutSVG(segments));
    chatTokenUsageDonut.appendChild(buildDonutLegend(segments));
    chatTokenUsage.hidden = false;
  }

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

  // escapeHTML neutralizes raw HTML in model output before any markdown
  // transform runs, so renderMarkdown's innerHTML use below can never
  // inject a tag/script the model happened to emit -- every markdown
  // pattern is matched and replaced strictly *after* this, working only
  // with already-inert text and the specific tags this function itself
  // introduces.
  function escapeHTML(text) {
    return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  // renderInline applies span-level markdown -- code spans first (each
  // pulled out to a plain SPANn placeholder so nothing inside it is ever
  // touched by the bold/italic/link patterns that follow, then restored
  // verbatim at the end), to already-HTML-escaped text. Bold is matched
  // before italic so **x** is never left as <em>*x</em>.
  function renderInline(text) {
    const codeSpans = [];
    text = text.replace(/`([^`\n]+)`/g, (_, code) => {
      codeSpans.push(code);
      return 'SPAN' + (codeSpans.length - 1) + 'END';
    });
    text = text.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
    text = text.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
    text = text.replace(/\*([^*\n]+)\*/g, '<em>$1</em>');
    text = text.replace(/SPAN(\d+)END/g, (_, i) => '<code>' + codeSpans[Number(i)] + '</code>');
    return text;
  }

  // renderMarkdown turns a chat model's plain-text-with-markdown answer
  // into safe HTML: fenced code blocks are pulled out first (so nothing
  // inside them is ever touched by inline/list/heading rules), then each
  // remaining line is classified into a heading, a list item, or plain
  // paragraph text, matching CommonMark closely enough for typical answers
  // without pulling in a full parser for a chat bubble. Simplification
  // accepted: a fence opened and closed on the same line (rare in
  // practice) isn't specially recognized and renders as literal text
  // instead of a code block.
  function renderMarkdown(raw) {
    const codeBlocks = [];
    const text = escapeHTML(raw).replace(/```[a-zA-Z0-9]*\n?([\s\S]*?)```/g, (_, code) => {
      codeBlocks.push(code.replace(/\n$/, ''));
      return '\nBLOCKFENCE' + (codeBlocks.length - 1) + '\n';
    });

    const html = [];
    let list = null; // { tag: 'ul'|'ol', items: string[] }
    let para = [];

    function flushPara() {
      if (para.length === 0) return;
      html.push('<p>' + renderInline(para.join('\n')).replace(/\n/g, '<br>') + '</p>');
      para = [];
    }
    function flushList() {
      if (!list) return;
      const items = list.items.map((item) => '<li>' + renderInline(item) + '</li>').join('');
      html.push('<' + list.tag + '>' + items + '</' + list.tag + '>');
      list = null;
    }

    for (const rawLine of text.split('\n')) {
      const line = rawLine.trim();
      const blockMatch = line.match(/^BLOCKFENCE(\d+)$/);
      const headingMatch = line.match(/^(#{1,6})\s+(.*)$/);
      const ulMatch = line.match(/^[-*]\s+(.*)$/);
      const olMatch = line.match(/^\d+\.\s+(.*)$/);
      if (blockMatch) {
        flushPara();
        flushList();
        html.push('<pre><code>' + codeBlocks[Number(blockMatch[1])] + '</code></pre>');
      } else if (line === '') {
        flushPara();
        flushList();
      } else if (headingMatch) {
        flushPara();
        flushList();
        const level = Math.min(headingMatch[1].length + 2, 6);
        html.push('<h' + level + '>' + renderInline(headingMatch[2]) + '</h' + level + '>');
      } else if (ulMatch) {
        flushPara();
        if (!list || list.tag !== 'ul') { flushList(); list = { tag: 'ul', items: [] }; }
        list.items.push(ulMatch[1]);
      } else if (olMatch) {
        flushPara();
        if (!list || list.tag !== 'ol') { flushList(); list = { tag: 'ol', items: [] }; }
        list.items.push(olMatch[1]);
      } else {
        flushList();
        para.push(line);
      }
    }
    flushPara();
    flushList();
    return html.join('');
  }

  // buildHookResponseFold builds the nested, closed-by-default <details>
  // that holds a hook result's raw output/error -- shared by the fetch and
  // search two-level renderings in renderChatMessage below. It reuses
  // .chat-hook-result's own box/summary/pre styling by adding that class
  // alongside .chat-hook-response, rather than duplicating those rules.
  function buildHookResponseFold(hr, summaryText) {
    const nested = document.createElement('details');
    nested.className = 'chat-hook-result chat-hook-response' + (hr.err ? ' chat-hook-result-error' : '');
    const summary = document.createElement('summary');
    summary.textContent = summaryText;
    nested.appendChild(summary);
    const pre = document.createElement('pre');
    pre.textContent = hr.err || hr.output;
    nested.appendChild(pre);
    return nested;
  }

  // renderChatMessage appends one message to #chat-messages for a
  // {role, content} turn. User and assistant turns are told apart by
  // alignment and a quiet tint (see .chat-msg-user/.chat-msg-assistant in
  // style.css) rather than a "You:"/"Assistant:" label. The assistant's
  // own text is rendered as markdown (renderMarkdown escapes it first, so
  // this is safe against anything the model emits); a user's own typed
  // text is shown as plain text -- markdown syntax they typed is not
  // something they'd expect reinterpreted. contextTrimmed (assistant-only)
  // surfaces the backend's context_trimmed flag: since the client resends
  // its whole chatHistory on every call and the backend silently drops the
  // oldest messages to fit the endpoint's token budget, without this note a
  // user would have no way to know this answer was generated without seeing
  // the full conversation. hookResults (also assistant-only, never echoed
  // back into chatHistory) is one entry per regex-triggered chat hook that
  // matched this turn's answer -- each rendered as its own folded
  // <details>, closed by default, so a hook's raw output/error is available
  // on demand without cluttering the answer itself. Token usage is NOT
  // rendered here -- see renderTokenUsage, which keeps one persistent
  // summary next to the Web checkbox instead of repeating it per turn.
  function renderChatMessage(role, content, contextTrimmed, hookResults) {
    const msg = document.createElement('div');
    msg.className = role === 'user' ? 'chat-msg chat-msg-user' : 'chat-msg chat-msg-assistant';

    const bubble = document.createElement('div');
    bubble.className = 'chat-msg-bubble';
    if (role === 'assistant') {
      bubble.innerHTML = renderMarkdown(content);
    } else {
      bubble.textContent = content;
    }
    msg.appendChild(bubble);

    if (role === 'assistant' && contextTrimmed) {
      const note = document.createElement('div');
      note.className = 'chat-context-note';
      note.textContent = 'Older messages were dropped from context to fit the model’s limit.';
      msg.appendChild(note);
    }

    if (role === 'assistant' && hookResults && hookResults.length > 0) {
      for (const hr of hookResults) {
        const name = (hr.hook_name || '').toLowerCase();
        const input = hr.input || '';

        if (name.includes('fetch') && input) {
          // Two-level fold: the outer <details> (open by default) shows
          // what was fetched via a real link; the raw response/error is
          // tucked away in a nested, closed-by-default fold so the target
          // URL is the first thing seen without the (often long) response
          // body pushing it out of view.
          const details = document.createElement('details');
          details.className = 'chat-hook-result' + (hr.err ? ' chat-hook-result-error' : '');
          details.open = true;
          const summary = document.createElement('summary');
          summary.textContent = hr.hook_name;
          details.appendChild(summary);

          const target = document.createElement('div');
          target.className = 'chat-hook-target';
          const link = document.createElement('a');
          link.href = input;
          link.textContent = input;
          link.target = '_blank';
          link.rel = 'noopener noreferrer';
          target.appendChild(link);
          details.appendChild(target);

          details.appendChild(buildHookResponseFold(hr, hr.err ? 'Error' : 'Response'));
          msg.appendChild(details);
        } else if (name.includes('search') && input) {
          // Same open-by-default outer fold, but the target is a query
          // string (not a link), and -- best effort -- a parsed result
          // list is shown directly, with the raw JSON still available in
          // the nested fold for anyone who wants it.
          const details = document.createElement('details');
          details.className = 'chat-hook-result' + (hr.err ? ' chat-hook-result-error' : '');
          details.open = true;
          const summary = document.createElement('summary');
          summary.textContent = hr.hook_name;
          details.appendChild(summary);

          const target = document.createElement('div');
          target.className = 'chat-hook-target';
          const code = document.createElement('code');
          code.textContent = input;
          target.appendChild(code);
          details.appendChild(target);

          let parsed = null;
          try {
            parsed = JSON.parse(hr.output);
          } catch (e) {
            parsed = null;
          }
          if (parsed && Array.isArray(parsed.results) &&
              parsed.results.every((r) => r && typeof r.title === 'string' && typeof r.url === 'string')) {
            const list = document.createElement('ul');
            list.className = 'chat-hook-results-list';
            for (const r of parsed.results.slice(0, 5)) {
              const li = document.createElement('li');
              const a = document.createElement('a');
              a.href = r.url;
              a.textContent = r.title || r.url;
              a.target = '_blank';
              a.rel = 'noopener noreferrer';
              li.appendChild(a);
              list.appendChild(li);
            }
            details.appendChild(list);
          }

          details.appendChild(buildHookResponseFold(hr, hr.err ? 'Error' : 'Raw output'));
          msg.appendChild(details);
        } else {
          // Generic fallback: today's original single-level, closed-by-
          // default rendering, unchanged -- used for any hook name that
          // isn't fetch/search-shaped, and also when a fetch/search hook
          // fired without a captured input to show.
          const details = document.createElement('details');
          details.className = 'chat-hook-result' + (hr.err ? ' chat-hook-result-error' : '');
          const summary = document.createElement('summary');
          summary.textContent = hr.hook_name;
          details.appendChild(summary);
          const pre = document.createElement('pre');
          pre.textContent = hr.err ? hr.err : hr.output;
          details.appendChild(pre);
          msg.appendChild(details);
        }
      }
    }

    chatMessages.appendChild(msg);
    // Scroll so the new turn's own beginning lands at the top of the
    // visible area -- #chat-messages is a fixed-height, scrollable box (see
    // style.css), so without this a long-running conversation would leave
    // the just-added turn below the visible area until scrolled manually.
    // Scrolling to msg's own top (rather than chatMessages.scrollHeight,
    // which would land on the turn's *end*) means a long answer is always
    // read starting from its first line, not its last.
    chatMessages.scrollTop = msg.offsetTop;
    return msg;
  }

  // sendChatMessage appends the user's turn to chatHistory, renders it
  // immediately, then POSTs the full history to /chat -- see runSearch
  // above for the same ok/non-ok/network-failure pattern this mirrors.
  // web_search is read fresh from its checkbox on every call, so switching
  // it mid-conversation only ever affects the question being asked right
  // now, not history already answered under other settings.
  async function sendChatMessage(content) {
    chatHistory.push({ role: 'user', content });
    renderChatMessage('user', content);
    chatStatus.textContent = 'Thinking…';
    try {
      const resp = await fetch('/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ messages: chatHistory, web_search: chatWebSearch.checked }),
      });
      if (!resp.ok) {
        const msg = await resp.text();
        chatStatus.textContent = 'Chat failed: ' + msg.trim();
        return;
      }
      const data = await resp.json();
      chatHistory.push({ role: 'assistant', content: data.answer });
      renderChatMessage('assistant', data.answer, data.context_trimmed, data.hook_results || []);
      renderTokenUsage(data.token_usage);
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
      chatOptions.hidden = false;
    } else {
      form.hidden = false;
      if (syntaxNote) syntaxNote.hidden = false;
      status.hidden = false;
      correctionNote.hidden = correctionNoteHiddenBeforeChat;
      results.hidden = false;
      chatPanel.hidden = true;
      chatOptions.hidden = true;
    }
  }

  modeSwitch.addEventListener('click', () => {
    const isChat = modeSwitch.getAttribute('aria-checked') === 'true';
    setMode(isChat ? 'search' : 'chat');
  });

  setMode('chat');

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

  // chat-input is a <textarea> (multi-line input, so the user can compose a
  // longer question) -- unlike a plain text <input>, a <textarea> never
  // submits its form on Enter by itself, so this wires up the standard chat
  // convention by hand: Enter alone sends, Shift+Enter inserts a newline.
  chatInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      chatForm.requestSubmit();
    }
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
      escapeHTML, renderInline, renderMarkdown,
      buildDonutSVG, buildDonutLegend, tokenUsageSegments, renderTokenUsage,
    };
  }
