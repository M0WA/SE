  const form = document.getElementById('search-form');
  const input = document.getElementById('q');
  const status = document.getElementById('status');
  const correctionNote = document.getElementById('correction-note');
  const results = document.getElementById('results');
  const syntaxNote = document.getElementById('syntax-note');

  const mainEl = document.querySelector('main');
  const modeSwitch = document.getElementById('mode-switch');
  const chatOptions = document.getElementById('chat-options');
  const chatPanel = document.getElementById('chat-panel');
  const chatTabList = document.getElementById('chat-tab-list');
  const chatTabNewBtn = document.getElementById('chat-tab-new');
  const chatTabForkBtn = document.getElementById('chat-tab-fork');
  const chatTabExportBtn = document.getElementById('chat-tab-export');
  const chatTabImportBtn = document.getElementById('chat-tab-import');
  const chatTabImportInput = document.getElementById('chat-tab-import-input');
  const chatMessages = document.getElementById('chat-messages');
  const chatStatus = document.getElementById('chat-status');
  const chatForm = document.getElementById('chat-form');
  const chatInput = document.getElementById('chat-input');
  const chatAttachBtn = document.getElementById('chat-attach');
  const chatAttachInput = document.getElementById('chat-attach-input');
  const chatWebSearch = document.getElementById('chat-web-search');
  const chatAgentSelect = document.getElementById('chat-agent-select');
  const chatTokenUsage = document.getElementById('chat-token-usage');
  const chatTokenUsageMini = document.getElementById('chat-token-usage-mini');
  const chatTokenUsageSummary = document.getElementById('chat-token-usage-summary');
  const chatTokenUsageDonut = document.getElementById('chat-token-usage-donut');
  const adminLink = document.getElementById('admin-link');
  const accountLink = document.getElementById('account-link');

  // tabs holds every open conversation this session -- forking a tab deep-
  // copies its history into a new independent one, so answering in one
  // never affects another. Session-only (in-memory): nothing here survives
  // a reload, by design (Export/Import below is the deliberate escape
  // hatch for anything worth keeping). activeTabId names which one is
  // currently rendered into #chat-messages; nextTabId is a plain
  // incrementing counter (not a timestamp), so tab ids stay small,
  // readable, and deterministic in tests.
  let nextTabId = 1;
  function makeTab(overrides) {
    const id = nextTabId++;
    return Object.assign({ id: id, title: 'Chat ' + id, history: [], tokenUsage: null, agentId: '' }, overrides);
  }
  const tabs = [makeTab()];
  let activeTabId = tabs[0].id;

  function activeTab() {
    return tabs.find((t) => t.id === activeTabId);
  }

  // deriveTabTitle shortens a tab's first user message into a readable tab
  // label -- only applied the moment a brand-new tab gets its first turn
  // (see sendChatMessage), so a forked or imported tab's own inherited
  // title is never overwritten.
  function deriveTabTitle(content) {
    const trimmed = content.trim().replace(/\s+/g, ' ');
    return trimmed.length > 24 ? trimmed.slice(0, 24) + '…' : trimmed;
  }

  // renderAgentSelectOptions populates the agent picker from every enabled
  // agent (GET /agents already filters to just those), keeping the
  // built-in "Default agent" option (empty value, falls back to
  // ChatEndpoint.DefaultAgentID server-side) first.
  function renderAgentSelectOptions(agents) {
    while (chatAgentSelect.options.length > 1) chatAgentSelect.remove(1);
    for (const a of agents) {
      const opt = document.createElement('option');
      opt.value = a.id;
      opt.textContent = a.name;
      chatAgentSelect.appendChild(opt);
    }
  }

  // loadAgentOptions fetches the picker's own options once on page load --
  // best-effort, same convention as every other auxiliary fetch on this
  // page: a failure just leaves the select at its built-in "Default agent"
  // option rather than blocking the rest of the page.
  async function loadAgentOptions() {
    try {
      const resp = await fetch('/agents');
      if (!resp.ok) return;
      renderAgentSelectOptions(await resp.json());
    } catch (err) {
      // Leave just "Default agent" in place.
    }
  }

  chatAgentSelect.addEventListener('change', () => {
    activeTab().agentId = chatAgentSelect.value;
  });

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
    } else {
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
    }

    // opts.centerText (e.g. a "42%" context-usage figure) sits in the
    // ring's own hole -- only passed by renderTokenUsage's larger hover
    // donut, never the 14px always-visible mini one, which is too small to
    // hold legible text.
    if (opts.centerText) {
      const text = document.createElementNS('http://www.w3.org/2000/svg', 'text');
      text.setAttribute('x', String(c));
      text.setAttribute('y', String(c));
      text.setAttribute('text-anchor', 'middle');
      text.setAttribute('dominant-baseline', 'central');
      text.classList.add('donut-center-label');
      text.textContent = opts.centerText;
      svg.appendChild(text);
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
  // -- a fixed split (global prompt, active agent's own prompt, active MCP
  // server prompts, your own prompt, conversation history) shared by every
  // turn, regardless of which pieces were actually nonzero this time.
  // Accepts a falsy u (e.g. before any turn has completed) and returns the
  // same shape, all zeros.
  function tokenUsageSegments(u) {
    u = u || {};
    return [
      { label: 'Global prompt', value: u.global_prompt_tokens || 0, color: 'var(--chart-1)' },
      { label: 'Tool prompts', value: u.tool_prompt_tokens || 0, color: 'var(--chart-2)' },
      { label: 'Your prompt', value: u.user_prompt_tokens || 0, color: 'var(--chart-3)' },
      { label: 'Agent prompt', value: u.agent_prompt_tokens || 0, color: 'var(--chart-4)' },
      { label: 'Conversation history', value: u.history_tokens || 0, color: 'var(--ink-muted)' },
    ];
  }

  // renderTokenUsage updates the persistent token-usage summary shown next
  // to the Web checkbox (see #chat-token-usage in index.html) -- unlike the
  // old per-turn folded donut this replaces, it stays visible and up to
  // date for the whole chat session, not just once a first answer sets it:
  // a falsy tokenUsage (e.g. before any turn has completed) renders an
  // empty/zero-value donut instead of hiding the badge, so the control's
  // position on the toolbar row is stable from page load. The larger hover
  // donut additionally gets a centered usage-percentage label once
  // max_context_tokens is known; the always-visible mini donut does not
  // (too small to hold legible text).
  function renderTokenUsage(tokenUsage) {
    const u = tokenUsage || {};
    const total = (u.global_prompt_tokens || 0) + (u.tool_prompt_tokens || 0) +
      (u.user_prompt_tokens || 0) + (u.agent_prompt_tokens || 0) + (u.history_tokens || 0);
    const segments = tokenUsageSegments(u);
    clear(chatTokenUsageMini);
    chatTokenUsageMini.appendChild(buildDonutSVG(segments, { size: 14, strokeWidth: 4 }));
    chatTokenUsageSummary.textContent = total.toLocaleString() +
      (u.max_context_tokens ? ' / ' + u.max_context_tokens.toLocaleString() : '');
    clear(chatTokenUsageDonut);
    const donutOpts = u.max_context_tokens
      ? { centerText: Math.round((total / u.max_context_tokens) * 100) + '%' }
      : undefined;
    chatTokenUsageDonut.appendChild(buildDonutSVG(segments, donutOpts));
    chatTokenUsageDonut.appendChild(buildDonutLegend(segments));
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

  // firstArgumentValue extracts the first string value from a tool call's
  // raw JSON Arguments object -- MCP tools can take multiple structured
  // arguments (unlike the old single-parameter chat-hook convention), but
  // web_search/web_fetch and most other simple tools still take exactly
  // one, so this best-effort heuristic is what decides whether the fetch/
  // search two-level rendering below applies at all: a tool whose
  // Arguments has no string value (multi-argument, or genuinely empty)
  // falls through to the generic single-level rendering instead of
  // guessing wrong.
  function firstArgumentValue(argumentsJSON) {
    try {
      const parsed = JSON.parse(argumentsJSON || '');
      if (parsed && typeof parsed === 'object') {
        for (const v of Object.values(parsed)) {
          if (typeof v === 'string') return v;
        }
      }
    } catch (e) {
      // Not valid JSON (or empty) -- fall through to the generic rendering.
    }
    return '';
  }

  // buildToolResponseFold builds the nested, closed-by-default <details>
  // that holds a tool result's raw output/error -- shared by the fetch and
  // search two-level renderings in renderChatMessage below. It reuses
  // .chat-hook-result's own box/summary/pre styling by adding that class
  // alongside .chat-hook-response, rather than duplicating those rules.
  function buildToolResponseFold(tr, summaryText) {
    const nested = document.createElement('details');
    nested.className = 'chat-hook-result chat-hook-response' + (tr.err ? ' chat-hook-result-error' : '');
    const summary = document.createElement('summary');
    summary.textContent = summaryText;
    nested.appendChild(summary);
    const pre = document.createElement('pre');
    pre.textContent = tr.err || tr.output;
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
  // a tab's whole history on every call and the backend silently drops the
  // oldest messages to fit the endpoint's token budget, without this note a
  // user would have no way to know this answer was generated without seeing
  // the full conversation. toolResults (also assistant-only) is one entry
  // per MCP tool call the model made this turn -- each rendered as its own
  // folded <details>, closed by
  // default, so a tool's raw output/error is available on demand without
  // cluttering the answer itself. Token usage is NOT rendered here -- see
  // renderTokenUsage, which keeps one persistent summary next to the Web
  // checkbox instead of repeating it per turn.
  function renderChatMessage(role, content, contextTrimmed, toolResults) {
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

    if (role === 'assistant' && toolResults && toolResults.length > 0) {
      for (const tr of toolResults) {
        const name = (tr.tool_name || '').toLowerCase();
        const input = firstArgumentValue(tr.arguments);

        if (name === 'write_file' && !tr.err) {
          // cmd/mcp-files' write_file tool returns {id, filename, size} as
          // its JSON output -- render a real download link (pointing at
          // /account/api/files/{id}, the same endpoint the Your files page
          // itself links to) rather than only the raw JSON, so a produced
          // artifact is immediately clickable in the transcript.
          let parsed = null;
          try {
            parsed = JSON.parse(tr.output);
          } catch (e) {
            parsed = null;
          }
          const details = document.createElement('details');
          details.className = 'chat-hook-result';
          const summary = document.createElement('summary');
          summary.textContent = tr.tool_name;
          details.appendChild(summary);
          if (parsed && parsed.id && parsed.filename) {
            const target = document.createElement('div');
            target.className = 'chat-hook-target';
            const link = document.createElement('a');
            link.href = '/account/api/files/' + encodeURIComponent(parsed.id);
            link.textContent = 'Download ' + parsed.filename;
            target.appendChild(link);
            details.appendChild(target);
          }
          details.appendChild(buildToolResponseFold(tr, 'Raw output'));
          msg.appendChild(details);
        } else if (name.includes('fetch') && input) {
          // Two-level fold: the outer <details> is closed by default, same
          // as every other tool result -- expanding it shows what was
          // fetched via a real link, with the raw response/error tucked
          // away in a further-nested, closed-by-default fold.
          const details = document.createElement('details');
          details.className = 'chat-hook-result' + (tr.err ? ' chat-hook-result-error' : '');
          const summary = document.createElement('summary');
          summary.textContent = tr.tool_name;
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

          details.appendChild(buildToolResponseFold(tr, tr.err ? 'Error' : 'Response'));
          msg.appendChild(details);
        } else if (name.includes('search') && input) {
          // Same closed-by-default outer fold, but the target is a query
          // string (not a link), and -- best effort -- a parsed result
          // list is shown directly once expanded, with the raw JSON still
          // available in the nested fold for anyone who wants it.
          const details = document.createElement('details');
          details.className = 'chat-hook-result' + (tr.err ? ' chat-hook-result-error' : '');
          const summary = document.createElement('summary');
          summary.textContent = tr.tool_name;
          details.appendChild(summary);

          const target = document.createElement('div');
          target.className = 'chat-hook-target';
          const code = document.createElement('code');
          code.textContent = input;
          target.appendChild(code);
          details.appendChild(target);

          let parsed = null;
          try {
            parsed = JSON.parse(tr.output);
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

          details.appendChild(buildToolResponseFold(tr, tr.err ? 'Error' : 'Raw output'));
          msg.appendChild(details);
        } else {
          // Generic fallback: today's original single-level, closed-by-
          // default rendering, unchanged -- used for any tool name that
          // isn't fetch/search-shaped, and also when a fetch/search tool
          // fired without a single string argument to show.
          const details = document.createElement('details');
          details.className = 'chat-hook-result' + (tr.err ? ' chat-hook-result-error' : '');
          const summary = document.createElement('summary');
          summary.textContent = tr.tool_name;
          details.appendChild(summary);
          const pre = document.createElement('pre');
          pre.textContent = tr.err ? tr.err : tr.output;
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

  // renderActiveTab fully re-renders #chat-messages from the active tab's
  // own stored history -- unlike renderChatMessage (which only ever
  // appends the newest turn during a live send), this replays every past
  // turn, needed whenever the visible tab changes (switch/fork/new/import)
  // since #chat-messages itself holds no state of its own between
  // switches. Each stored assistant entry keeps its own context_trimmed/
  // tool_results (see sendChatMessage), so switching back to a tab shows
  // exactly what it showed before, tool-result folds included.
  function renderActiveTab() {
    clear(chatMessages);
    const tab = activeTab();
    for (const m of tab.history) {
      if (m.role === 'user') {
        renderChatMessage('user', m.content);
      } else if (m.role === 'assistant') {
        renderChatMessage('assistant', m.content, m.context_trimmed, m.tool_results);
      }
    }
    chatStatus.textContent = '';
    renderTokenUsage(tab.tokenUsage);
    // Reflect this tab's own chosen agent in the picker -- falls back to
    // "Default agent" (empty value) if the tab never had one selected, or
    // if it named an agent this select has no matching option for (e.g.
    // imported from another deployment, or since deleted).
    chatAgentSelect.value = tab.agentId || '';
  }

  // renderTabs rebuilds the tab strip from `tabs` -- called after any
  // change to the list itself or to which one is active. The close button
  // is omitted entirely while only one tab remains, so there's always at
  // least one conversation open; closing never needs a confirmation
  // dialog since a closed tab's history was already exportable beforehand
  // if it mattered.
  function renderTabs() {
    clear(chatTabList);
    for (const tab of tabs) {
      const item = document.createElement('div');
      item.className = 'chat-tab' + (tab.id === activeTabId ? ' chat-tab-active' : '');
      item.setAttribute('role', 'tab');
      item.setAttribute('aria-selected', String(tab.id === activeTabId));

      const switchBtn = document.createElement('button');
      switchBtn.type = 'button';
      switchBtn.className = 'chat-tab-label';
      switchBtn.textContent = tab.title;
      switchBtn.title = tab.title;
      switchBtn.addEventListener('click', () => switchTab(tab.id));
      item.appendChild(switchBtn);

      if (tabs.length > 1) {
        const closeBtn = document.createElement('button');
        closeBtn.type = 'button';
        closeBtn.className = 'chat-tab-close';
        closeBtn.textContent = '×';
        closeBtn.title = 'Close ' + tab.title;
        closeBtn.setAttribute('aria-label', 'Close ' + tab.title);
        closeBtn.addEventListener('click', () => closeTab(tab.id));
        item.appendChild(closeBtn);
      }
      chatTabList.appendChild(item);
    }
  }

  function switchTab(id) {
    if (id === activeTabId) return;
    activeTabId = id;
    renderTabs();
    renderActiveTab();
  }

  function newChatTab() {
    const tab = makeTab();
    tabs.push(tab);
    activeTabId = tab.id;
    renderTabs();
    renderActiveTab();
    return tab;
  }

  // forkActiveTab deep-copies the active tab's history (each message
  // object shallow-copied, so editing the fork's own tool_results array
  // later can't ever mutate the source tab's) into a new, independent tab
  // and switches to it -- the source conversation keeps going exactly as
  // it was.
  function forkActiveTab() {
    const source = activeTab();
    const tab = makeTab({
      title: source.title + ' (fork)',
      history: source.history.map((m) => Object.assign({}, m)),
      tokenUsage: source.tokenUsage,
      agentId: source.agentId,
    });
    tabs.push(tab);
    activeTabId = tab.id;
    renderTabs();
    renderActiveTab();
    return tab;
  }

  function closeTab(id) {
    if (tabs.length <= 1) return;
    const idx = tabs.findIndex((t) => t.id === id);
    if (idx === -1) return;
    tabs.splice(idx, 1);
    if (activeTabId === id) {
      activeTabId = tabs[Math.max(0, idx - 1)].id;
      renderActiveTab();
    }
    renderTabs();
  }

  // serializeTab/deserializeTab are the pure JSON shape Export/Import
  // trade in -- kept separate from the DOM-triggering
  // exportActiveTab/importTabFromJSON below so the format itself is
  // testable without a real file download/upload round trip.
  function serializeTab(tab) {
    return JSON.stringify({ title: tab.title, history: tab.history, agent_id: tab.agentId || '' }, null, 2);
  }

  // deserializeTab validates and normalizes an imported chat export --
  // tolerant of a hand-edited or partial file (drops any history entry
  // that isn't a recognizable {role, content} turn, rather than rejecting
  // the whole import over one bad entry) but throws on something that
  // isn't a chat export at all (no history array).
  function deserializeTab(jsonText) {
    const parsed = JSON.parse(jsonText);
    if (!parsed || typeof parsed !== 'object' || !Array.isArray(parsed.history)) {
      throw new Error('not a valid chat export');
    }
    const history = parsed.history
      .filter((m) => m && (m.role === 'user' || m.role === 'assistant') && typeof m.content === 'string')
      .map((m) => ({
        role: m.role,
        content: m.content,
        context_trimmed: !!m.context_trimmed,
        tool_results: Array.isArray(m.tool_results) ? m.tool_results : [],
      }));
    const title = typeof parsed.title === 'string' && parsed.title ? parsed.title : 'Imported chat';
    const agentId = typeof parsed.agent_id === 'string' ? parsed.agent_id : '';
    return { title: title, history: history, agentId: agentId };
  }

  // exportActiveTab downloads the active tab as a JSON file via a
  // throwaway <a download> link -- the standard no-server-round-trip way
  // to save browser-side data to disk.
  function exportActiveTab() {
    const tab = activeTab();
    const blob = new Blob([serializeTab(tab)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = (tab.title || 'chat').replace(/[^a-z0-9-_]+/gi, '_') + '.json';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }

  function importTabFromJSON(jsonText) {
    const parsedTab = deserializeTab(jsonText);
    const tab = makeTab({ title: parsedTab.title, history: parsedTab.history, tokenUsage: null, agentId: parsedTab.agentId });
    tabs.push(tab);
    activeTabId = tab.id;
    renderTabs();
    renderActiveTab();
    return tab;
  }

  chatTabNewBtn.addEventListener('click', newChatTab);
  chatTabForkBtn.addEventListener('click', forkActiveTab);
  chatTabExportBtn.addEventListener('click', exportActiveTab);
  chatTabImportBtn.addEventListener('click', () => chatTabImportInput.click());
  chatTabImportInput.addEventListener('change', async () => {
    const file = chatTabImportInput.files[0];
    chatTabImportInput.value = '';
    if (!file) return;
    try {
      importTabFromJSON(await file.text());
    } catch (err) {
      chatStatus.textContent = 'Could not import chat: ' + err.message;
    }
  });

  // uploadAttachedFile posts the chosen file to /account/api/files (the
  // same self-service endpoint the Your files page uses) -- the file
  // becomes available for the model to discover and read via the
  // file-operations MCP server's own tools (list_files/read_file), the
  // next time the model chooses to look, not injected into the outgoing
  // message text itself. 404/503 here most likely means no signed-in
  // regular-user account (an admin session has no files of its own -- see
  // domain.UploadedFile's own doc comment) or the feature isn't
  // configured on this deployment.
  async function uploadAttachedFile(file) {
    const body = new FormData();
    body.append('file', file);
    const resp = await fetch('/account/api/files', { method: 'POST', body });
    if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
    return resp.json();
  }

  chatAttachBtn.addEventListener('click', () => chatAttachInput.click());
  chatAttachInput.addEventListener('change', async () => {
    const file = chatAttachInput.files[0];
    chatAttachInput.value = '';
    if (!file) return;
    try {
      const uploaded = await uploadAttachedFile(file);
      chatStatus.textContent = 'Attached "' + uploaded.filename + '" -- ask about it and the model will read it.';
    } catch (err) {
      chatStatus.textContent = 'Could not attach file: ' + err.message;
    }
  });

  // toWireHistory strips a tab's own client-side rendering metadata
  // (context_trimmed/tool_results, kept in tab.history purely so
  // renderActiveTab can faithfully replay a tab's folds after switching
  // away and back -- see sendChatMessage/deserializeTab) down to the bare
  // {role, content} pairs the backend actually reads (see
  // domain.ChatMessage's own json tags -- anything else is silently
  // ignored server-side anyway). Sending the untrimmed entries directly
  // was a real bug: tool_results carries each web_fetch call's full,
  // UNtruncated page text (application.maxHookOutputCharsForModel only
  // caps what's fed back to the model internally, not what the HTTP
  // response returns), so a conversation with even a few tool calls would
  // resend that same large payload, growing every turn, until nginx's
  // client_max_body_size rejected the request outright (413).
  function toWireHistory(history) {
    return history.map((m) => ({ role: m.role, content: m.content }));
  }

  // sendChatMessage appends the user's turn to the active tab's own
  // history, renders it immediately, then POSTs the full history to /chat
  // -- see runSearch above for the same ok/non-ok/network-failure pattern
  // this mirrors. web_search is read fresh from its checkbox on every
  // call, so switching it mid-conversation only ever affects the question
  // being asked right now, not history already answered under other
  // settings. tab is captured once at the start (not re-read as
  // activeTab() after the await) so a reply that arrives after the user
  // has switched to a different tab still updates the RIGHT tab's stored
  // history -- but only touches the visible DOM (chatMessages/chatStatus/
  // the donut) when that tab is still the one on screen, so a slow
  // background answer can never clobber whatever tab the user is looking
  // at by then.
  async function sendChatMessage(content) {
    const tab = activeTab();
    tab.history.push({ role: 'user', content });
    if (tab.history.length === 1) tab.title = deriveTabTitle(content);
    renderChatMessage('user', content);
    renderTabs();
    chatStatus.textContent = 'Thinking…';
    try {
      const resp = await fetch('/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ messages: toWireHistory(tab.history), web_search: chatWebSearch.checked, agent_id: tab.agentId || '' }),
      });
      if (!resp.ok) {
        const msg = await resp.text();
        if (tab.id === activeTabId) chatStatus.textContent = 'Chat failed: ' + msg.trim();
        return;
      }
      const data = await resp.json();
      tab.history.push({
        role: 'assistant', content: data.answer,
        context_trimmed: data.context_trimmed, tool_results: data.tool_results || [],
      });
      tab.tokenUsage = data.token_usage;
      if (tab.id === activeTabId) {
        renderChatMessage('assistant', data.answer, data.context_trimmed, data.tool_results || []);
        renderTokenUsage(tab.tokenUsage);
        chatStatus.textContent = '';
      }
    } catch (err) {
      if (tab.id === activeTabId) chatStatus.textContent = 'Chat failed: could not reach the server.';
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
    if (mainEl) mainEl.classList.toggle('chat-mode', isChat);
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

  renderTabs();
  renderActiveTab();
  setMode('chat');
  loadAgentOptions();

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

  // loadSession asks the backend which role the current session has (an
  // admin-only session vs. a regular self-service user) and shows exactly
  // one of the two header icon links accordingly -- #admin-link and
  // #account-link both start hidden in index.html, so any failure path
  // here (network error, non-ok status) just leaves both hidden rather
  // than risking showing the admin backend link to a non-admin session.
  // This is a non-critical UI enhancement fetch (worst case: no icon link
  // at all), so failures are swallowed silently, matching this file's
  // existing tone for that kind of call (cf. runSearch/sendChatMessage,
  // which surface errors because they're the user's actual action, vs. this
  // one which isn't triggered by anything the user did).
  async function loadSession() {
    try {
      const resp = await fetch('/session');
      if (!resp.ok) return;
      const data = await resp.json();
      if (data.role === 'admin') {
        adminLink.hidden = false;
      } else if (data.role === 'user') {
        accountLink.hidden = false;
      }
    } catch (err) {
      // Non-critical: both links simply stay hidden.
    }
  }

  loadSession();

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
      tabs, activeTab, renderChatMessage, renderActiveTab, renderTabs,
      switchTab, newChatTab, forkActiveTab, closeTab,
      serializeTab, deserializeTab, exportActiveTab, importTabFromJSON,
      toWireHistory, sendChatMessage, setMode,
      escapeHTML, renderInline, renderMarkdown,
      buildDonutSVG, buildDonutLegend, tokenUsageSegments, renderTokenUsage,
      loadSession, renderAgentSelectOptions, loadAgentOptions,
      uploadAttachedFile,
    };
  }
