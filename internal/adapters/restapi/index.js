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
  const chatFiles = document.getElementById('chat-files');
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

  // tabs holds every open conversation this session -- forking deep-copies history into an
  // independent tab. Session-only in-memory (Export/Import is the escape hatch to keep one).
  // activeTabId is the rendered tab; nextTabId is a plain incrementing counter, not a timestamp,
  // so ids stay small and deterministic in tests.
  let nextTabId = 1;
  function makeTab(overrides) {
    const id = nextTabId++;
    return {
      id: id, title: 'Chat ' + id, history: [], tokenUsage: null, agentId: '',
      // persisted/chatId: whether this tab is pinned to a server-side PersistedChat row -- only
      // a persisted tab may attach files or survive a page reload.
      persisted: false, chatId: null,
      ...overrides,
    };
  }
  const tabs = [makeTab()];
  let activeTabId = tabs[0].id;

  function activeTab() {
    return tabs.find((t) => t.id === activeTabId);
  }

  // pinIconSVG returns the pin glyph for a tab's pin button -- filled once persisted, outline
  // otherwise, same inline-SVG-over-emoji reasoning as #chat-attach's paperclip.
  function pinIconSVG(filled) {
    return '<svg width="24" height="24" viewBox="0 0 24 24" fill="' + (filled ? 'currentColor' : 'none') +
      '" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      '<path d="M12 17v5"/><path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"/></svg>';
  }

  // deriveTabTitle shortens a tab's first message into a tab label -- only applied on a
  // brand-new tab's first turn, so a forked/imported tab's inherited title is never overwritten.
  function deriveTabTitle(content) {
    const trimmed = content.trim().replace(/\s+/g, ' ');
    return trimmed.length > 24 ? trimmed.slice(0, 24) + '…' : trimmed;
  }

  // renderAgentSelectOptions populates the agent picker from every enabled agent, keeping
  // "Default agent" (falls back to ChatEndpoint.DefaultAgentID server-side) first.
  function renderAgentSelectOptions(agents) {
    while (chatAgentSelect.options.length > 1) chatAgentSelect.remove(1);
    for (const a of agents) {
      const opt = document.createElement('option');
      opt.value = a.id;
      opt.textContent = a.name;
      chatAgentSelect.appendChild(opt);
    }
  }

  // loadAgentOptions fetches the picker's options once on load -- best-effort: a failure just
  // leaves "Default agent" selected rather than blocking the page.
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

  // buildDonutSVG/buildDonutLegend render a per-turn token-usage chart -- a small local
  // duplicate of admin.js's copy, not a shared import: this public page is served by
  // search-server, while admin.js only routes to admin-server (see searchengine.conf) -- not
  // worth restructuring routing for a ~30-line helper.
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

    // opts.centerText (e.g. "42%") sits in the ring's hole -- only passed by the larger hover
    // donut, never the 14px mini one, too small for legible text.
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

  // tokenUsageSegments turns the backend's flat token_usage breakdown into the shape
  // buildDonutSVG/buildDonutLegend expect -- a fixed split (global/agent/MCP prompts, your
  // prompt, history) shared by every turn. A falsy u (before any turn completes) returns the
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

  // renderTokenUsage updates the persistent token-usage summary next to the Web checkbox --
  // unlike the old per-turn folded donut it replaces, it stays visible for the whole session: a
  // falsy tokenUsage renders an empty/zero donut instead of hiding the badge, keeping the
  // toolbar's layout stable from page load. The hover donut gets a centered percentage label
  // once max_context_tokens is known; the mini one doesn't (too small for legible text).
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

  // renderCorrectionNote shows a quiet note when search fuzzy-corrected a misspelled term
  // (corrected_terms) -- the displayed query is never silently rewritten, this just names the
  // substituted term(s).
  function renderCorrectionNote(list) {
    const corrected = list[0]?.corrected_terms || [];
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
    while (el.firstChild) el.firstChild.remove();
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
      // Any failure (network error, refused connection, malformed JSON) is reported the same
      // way -- no more specific message is worth showing than "could not reach the server," but
      // log the real error for anyone debugging from the console.
      console.error('search request failed:', err);
      status.textContent = 'Search failed: could not reach the server.';
    }
  }

  // escapeHTML neutralizes raw HTML in model output before any markdown transform, so
  // renderMarkdown's innerHTML use can never inject a model-emitted tag/script -- every markdown
  // pattern runs strictly after this, on already-inert text.
  function escapeHTML(text) {
    return text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
  }

  // renderInline applies span-level markdown to already-escaped text -- code spans are pulled
  // to placeholders first (restored at the end) so bold/italic/link patterns never touch inside
  // them. Bold is matched before italic so **x** never ends up as <em>*x</em>.
  function renderInline(text) {
    const codeSpans = [];
    text = text.replace(/`([^`\n]+)`/g, (_, code) => {
      codeSpans.push(code);
      return 'SPAN' + (codeSpans.length - 1) + 'END';
    });
    text = text.replace(/\[([^\][]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
    text = text.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
    text = text.replace(/\*([^*\n]+)\*/g, '<em>$1</em>');
    text = text.replace(/SPAN(\d+)END/g, (_, i) => '<code>' + codeSpans[Number(i)] + '</code>');
    return text;
  }

  // renderMarkdown turns a model's plain-text-with-markdown answer into safe HTML: fenced code
  // blocks are pulled out first, then remaining lines are classified as heading/list-item/
  // paragraph, matching CommonMark closely enough without a full parser. Known gap: a fence
  // opened+closed on one line renders as literal text, not a code block.
  function renderMarkdown(raw) {
    const codeBlocks = [];
    // `(?=([a-zA-Z0-9]*))\1` matches what `[a-zA-Z0-9]*` would (JS lacks atomic/possessive
    // quantifiers) but pins its length, avoiding backtracking -- unterminated input otherwise
    // lets it overlap with the following `[\s\S]*?` and re-try every split, quadratic in answer
    // length. The language tag can't contain a backtick, so pinning changes no match.
    const text = escapeHTML(raw).replace(/```(?=([a-zA-Z0-9]*))\1\n?([\s\S]*?)```/g, (_, _tag, code) => {
      codeBlocks.push(code.replace(/\n$/, ''));
      return '\nBLOCKFENCE' + (codeBlocks.length - 1) + '\n';
    });

    const html = [];
    let list = null; // { tag: 'ul'|'ol', items: string[] }
    let para = [];

    function flushPara() {
      if (para.length === 0) return;
      html.push('<p>' + renderInline(para.join('\n')).replaceAll('\n', '<br>') + '</p>');
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
        if (list?.tag !== 'ul') { flushList(); list = { tag: 'ul', items: [] }; }
        list.items.push(ulMatch[1]);
      } else if (olMatch) {
        flushPara();
        if (list?.tag !== 'ol') { flushList(); list = { tag: 'ol', items: [] }; }
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

  // firstArgumentValue extracts the first string value from a tool call's Arguments object --
  // MCP tools can take multiple structured arguments, but simple tools like web_search/web_fetch
  // take one, so this heuristic decides whether the fetch/search two-level rendering applies.
  // No string value found falls through to generic single-level rendering instead of guessing
  // wrong.
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

  // buildToolResponseFold builds the nested, closed-by-default <details> holding a tool result's
  // raw output/error -- shared by the fetch/search renderings below. Reuses .chat-hook-result's
  // styling rather than duplicating it.
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

  // renderChatMessage appends one {role, content} turn to #chat-messages. User/assistant are
  // told apart by alignment/tint, not a label. Assistant text is rendered as markdown (escaped
  // first); user text is shown as plain text, since typed markdown isn't meant to be
  // reinterpreted. contextTrimmed (assistant-only) surfaces the backend's context_trimmed flag --
  // since the client resends full history but the backend silently drops the oldest messages to
  // fit budget, without this note a user couldn't tell the answer was generated on a partial
  // conversation. toolResults (assistant-only) is one closed-by-default <details> per MCP tool
  // call, keeping raw output/error available without cluttering the answer. Token usage is NOT
  // rendered here -- see renderTokenUsage's persistent summary instead.
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
          // write_file returns {id, filename, size} -- render a real download link
          // (/account/api/files/{id}, same as the Your files page) rather than just raw JSON, so
          // a produced artifact is immediately clickable.
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
          if (parsed?.id && parsed?.filename) {
            const target = document.createElement('div');
            target.className = 'chat-hook-target';
            const link = document.createElement('a');
            link.href = '/account/api/files/' + encodeURIComponent(parsed.id);
            link.textContent = 'Download ' + parsed.filename;
            target.appendChild(link);
            details.appendChild(target);
            // Also refresh #chat-files so a model-created file shows up as
            // its own box below the transcript, not just this inline link.
            loadChatFiles();
          }
          details.appendChild(buildToolResponseFold(tr, 'Raw output'));
          msg.appendChild(details);
        } else if (name.includes('fetch') && input) {
          // Two-level fold: outer <details> closed by default like every tool result --
          // expanding shows the fetched link, with raw response/error in a further-nested fold.
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
          // Same closed-by-default outer fold, but for a query string (not a link) -- a parsed
          // result list shows once expanded, raw JSON still in the nested fold.
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
          // Generic fallback: single-level, closed-by-default rendering, for any non-fetch/search
          // tool or one without a single string argument.
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
    // Scroll so the new turn's beginning lands at the top of #chat-messages (a fixed-height
    // scrollable box) -- scrolling to msg's top, not chatMessages.scrollHeight (which lands on
    // the end), means a long answer is always read from its first line.
    chatMessages.scrollTop = msg.offsetTop;
    return msg;
  }

  // renderActiveTab fully re-renders #chat-messages from the active tab's stored history,
  // replaying every past turn -- needed on any tab switch, since #chat-messages holds no state
  // between switches. Each entry keeps its own context_trimmed/tool_results, so switching back
  // shows exactly what it showed before.
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
    // Reflect this tab's chosen agent in the picker -- falls back to "Default agent" if none was
    // selected, or it names an agent with no matching option (e.g. imported, or since deleted).
    chatAgentSelect.value = tab.agentId || '';
  }

  // renderTabs rebuilds the tab strip from `tabs` -- called after any change to the list or
  // active tab. The close button is omitted while only one tab remains, so at least one stays
  // open; closing needs no confirmation, since history was already exportable beforehand.
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
      // Clicking the label switches to that tab -- unless already active, where switching is a
      // no-op, so the click renames it instead (renameTab).
      switchBtn.title = tab.id === activeTabId ? 'Rename "' + tab.title + '"' : tab.title;
      switchBtn.addEventListener('click', () => {
        if (tab.id === activeTabId) {
          renameTab(tab.id);
        } else {
          switchTab(tab.id);
        }
      });
      item.appendChild(switchBtn);

      const pinBtn = document.createElement('button');
      pinBtn.type = 'button';
      pinBtn.className = 'chat-tab-action chat-tab-pin' + (tab.persisted ? ' chat-tab-pin-active' : '');
      pinBtn.title = tab.persisted ? 'Unpin (this chat and its files will be deleted)' : 'Pin to save this chat and allow file attachments';
      pinBtn.setAttribute('aria-label', pinBtn.title);
      pinBtn.innerHTML = pinIconSVG(tab.persisted);
      pinBtn.addEventListener('click', () => togglePinTab(tab.id));
      item.appendChild(pinBtn);

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
    refreshTabFileState();
  }

  function newChatTab() {
    const tab = makeTab();
    tabs.push(tab);
    activeTabId = tab.id;
    renderTabs();
    renderActiveTab();
    refreshTabFileState();
    return tab;
  }

  // forkActiveTab deep-copies the active tab's history (each message shallow-copied, so editing
  // the fork's tool_results can't mutate the source) into a new tab and switches to it. A fork
  // always starts unpinned, even from a pinned chat -- pinning is a separate action, so a fork
  // never silently shares the source's saved files.
  function forkActiveTab() {
    const source = activeTab();
    const tab = makeTab({
      title: source.title + ' (fork)',
      history: source.history.map((m) => ({ ...m })),
      tokenUsage: source.tokenUsage,
      agentId: source.agentId,
    });
    tabs.push(tab);
    activeTabId = tab.id;
    renderTabs();
    renderActiveTab();
    refreshTabFileState();
    return tab;
  }

  // renameTab prompts for a new title and applies it locally -- for a persisted tab, also
  // resyncs it to the server so it survives a reload. An unpersisted tab's title is session-only;
  // nothing is sent.
  function renameTab(id) {
    const tab = tabs.find((t) => t.id === id);
    if (!tab) return;
    const next = window.prompt('Rename this chat', tab.title);
    if (next === null) return;
    const trimmed = next.trim();
    if (!trimmed || trimmed === tab.title) return;
    tab.title = trimmed;
    renderTabs();
    resyncPersistedChat(tab);
  }

  // resyncPersistedChat pushes tab's title/agent/history to its server-side row -- called after
  // every turn/rename of a persisted tab. Best-effort: a failed resync leaves the in-memory tab
  // correct; the next successful one catches the server up.
  async function resyncPersistedChat(tab) {
    if (!tab.persisted || !tab.chatId) return;
    try {
      await fetch('/account/api/chats/' + encodeURIComponent(tab.chatId), {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: tab.title, agent_id: tab.agentId || '', history: tab.history }),
      });
    } catch (err) {
      // Non-critical: see doc comment above.
    }
  }

  // pinTab creates this tab's server-side PersistedChat row -- from then on it survives a
  // reload and may attach files.
  async function pinTab(tab) {
    try {
      const resp = await fetch('/account/api/chats', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: tab.title, agent_id: tab.agentId || '', history: tab.history }),
      });
      if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
      const data = await resp.json();
      tab.persisted = true;
      tab.chatId = data.id;
      renderTabs();
      refreshTabFileState();
    } catch (err) {
      chatStatus.textContent = 'Could not pin chat: ' + err.message;
    }
  }

  // unpinTab deletes this tab's server-side row (and every attached file, see
  // handleAccountDeleteChat's cascade) but leaves the tab open, back to session-only.
  async function unpinTab(tab) {
    if (!tab.chatId) return;
    try {
      const resp = await fetch('/account/api/chats/' + encodeURIComponent(tab.chatId), { method: 'DELETE' });
      if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
      tab.persisted = false;
      tab.chatId = null;
      renderTabs();
      refreshTabFileState();
    } catch (err) {
      chatStatus.textContent = 'Could not unpin chat: ' + err.message;
    }
  }

  function togglePinTab(id) {
    const tab = tabs.find((t) => t.id === id);
    if (!tab) return;
    if (tab.persisted) {
      unpinTab(tab);
    } else {
      pinTab(tab);
    }
  }

  // closeTab removes a tab from the strip. A persisted tab first deletes its server-side row
  // (cascading to attached files) before removing it locally; the tab stays in place if that
  // delete fails, so a chat is never silently orphaned server-side. An unpersisted tab is simply
  // discarded.
  async function closeTab(id) {
    if (tabs.length <= 1) return;
    const tab = tabs.find((t) => t.id === id);
    if (!tab) return;
    if (tab.persisted && tab.chatId) {
      try {
        const resp = await fetch('/account/api/chats/' + encodeURIComponent(tab.chatId), { method: 'DELETE' });
        if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
      } catch (err) {
        chatStatus.textContent = 'Could not close "' + tab.title + '": ' + err.message;
        return;
      }
    }
    const idx = tabs.findIndex((t) => t.id === id);
    if (idx === -1) return;
    tabs.splice(idx, 1);
    if (activeTabId === id) {
      activeTabId = tabs[Math.max(0, idx - 1)].id;
      renderActiveTab();
      refreshTabFileState();
    }
    renderTabs();
  }

  // serializeTab/deserializeTab are the pure JSON shape Export/Import trade in -- kept separate
  // from the DOM-triggering functions below so the format is testable without a real file round
  // trip.
  function serializeTab(tab) {
    return JSON.stringify({ title: tab.title, history: tab.history, agent_id: tab.agentId || '' }, null, 2);
  }

  // deserializeTab validates/normalizes an imported chat export -- tolerant of a hand-edited/
  // partial file (drops unrecognizable entries rather than rejecting the whole import), but
  // throws if there's no history array at all.
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

  // exportActiveTab downloads the active tab as JSON via a throwaway <a download> link -- the
  // standard no-server-round-trip way to save browser data.
  function exportActiveTab() {
    const tab = activeTab();
    const blob = new Blob([serializeTab(tab)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = (tab.title || 'chat').replace(/[^a-z0-9-_]+/gi, '_') + '.json';
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  }

  function importTabFromJSON(jsonText) {
    const parsedTab = deserializeTab(jsonText);
    const tab = makeTab({ title: parsedTab.title, history: parsedTab.history, tokenUsage: null, agentId: parsedTab.agentId });
    tabs.push(tab);
    activeTabId = tab.id;
    renderTabs();
    renderActiveTab();
    refreshTabFileState();
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

  // uploadAttachedFile posts to /account/api/files (same endpoint as Your files) -- the model
  // discovers/reads it via the file-operations MCP server's tools, not injected into the message
  // text. 404/503 usually means no regular-user session or the feature isn't configured. Only a
  // pinned tab can reach this (updateAttachAvailability disables the button otherwise); the
  // backend also rejects an upload with no chat_id as defense in depth.
  async function uploadAttachedFile(file) {
    const tab = activeTab();
    const body = new FormData();
    body.append('file', file);
    if (tab?.chatId) body.append('chat_id', tab.chatId);
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
      loadChatFiles();
    } catch (err) {
      chatStatus.textContent = 'Could not attach file: ' + err.message;
    }
  });

  // renderChatFileBox builds one box for #chat-files -- a real download link (works like any
  // link: open in new tab, copy address) plus a "×" that deletes outright, no confirmation -- a
  // quick, low-friction remove, unlike the Your files page's more deliberate delete.
  function renderChatFileBox(f) {
    const box = document.createElement('div');
    box.className = 'chat-file-box';
    box.dataset.fileId = f.id;
    const link = document.createElement('a');
    link.href = '/account/api/files/' + encodeURIComponent(f.id);
    link.textContent = f.filename;
    link.title = 'Download ' + f.filename;
    box.appendChild(link);
    const closeBtn = document.createElement('button');
    closeBtn.type = 'button';
    closeBtn.className = 'chat-file-close';
    closeBtn.textContent = '×';
    closeBtn.title = 'Delete ' + f.filename;
    closeBtn.setAttribute('aria-label', 'Delete ' + f.filename);
    closeBtn.addEventListener('click', () => deleteChatFile(f.id));
    box.appendChild(closeBtn);
    return box;
  }

  // renderChatFiles fully replaces #chat-files' contents after every load/upload/create/delete,
  // same "small list, re-render it" convention as account_files.js. Hidden entirely (not just
  // empty) when there are no files.
  function renderChatFiles(files) {
    clear(chatFiles);
    files.forEach((f) => chatFiles.appendChild(renderChatFileBox(f)));
    chatFiles.hidden = files.length === 0;
  }

  // loadChatFiles is best-effort and silent on failure (like loadSession) -- an admin session or
  // unconfigured Files both 404/503, and #chat-files just stays hidden. Scoped to the active
  // tab's chat_id -- an unpersisted tab has none, so it short-circuits to empty rather than
  // fetching the account's entire file list (that's what Your files is for).
  async function loadChatFiles() {
    const tab = activeTab();
    if (!tab?.persisted || !tab?.chatId) {
      renderChatFiles([]);
      return;
    }
    try {
      const resp = await fetch('/account/api/files?chat_id=' + encodeURIComponent(tab.chatId));
      if (!resp.ok) return;
      renderChatFiles(await resp.json());
    } catch (err) {
      // Non-critical: the strip simply stays empty/hidden.
    }
  }

  // updateAttachAvailability enables the attach button only for a pinned tab -- account_files.go
  // requires a chat_id on upload, and an unpinned tab has none.
  function updateAttachAvailability() {
    const tab = activeTab();
    const persisted = !!tab?.persisted;
    chatAttachBtn.disabled = !persisted;
    chatAttachBtn.title = persisted ? 'Attach a file for the model to inspect' : 'Pin this chat first to attach files';
    chatAttachBtn.setAttribute('aria-label', chatAttachBtn.title);
  }

  // refreshTabFileState re-evaluates attach availability and reloads the file strip for the
  // active tab -- called after anything that changes the active tab or its persisted state.
  function refreshTabFileState() {
    updateAttachAvailability();
    loadChatFiles();
  }

  // loadPersistedChats reloads every pinned chat on page load, replacing the default empty tab
  // (most-recently-updated first, per ListChats) -- only for a confirmed role=user session.
  // Leaves the default tab alone if there are no pinned chats. History round-trips only
  // {role, content}; context_trimmed/tool_results are UI-only and never stored, so a reloaded
  // turn's tool-result folds simply don't reappear.
  async function loadPersistedChats() {
    try {
      const resp = await fetch('/account/api/chats');
      if (!resp.ok) return;
      const chats = await resp.json();
      if (!Array.isArray(chats) || chats.length === 0) return;
      tabs.length = 0;
      for (const c of chats) {
        tabs.push(makeTab({
          title: c.title,
          history: (c.history || []).map((m) => ({ role: m.role, content: m.content, context_trimmed: false, tool_results: [] })),
          agentId: c.agent_id || '',
          persisted: true,
          chatId: c.id,
        }));
      }
      activeTabId = tabs[0].id;
      renderTabs();
      renderActiveTab();
      refreshTabFileState();
    } catch (err) {
      // Non-critical: the default empty tab is left in place.
    }
  }

  async function deleteChatFile(id) {
    try {
      const resp = await fetch('/account/api/files/' + encodeURIComponent(id), { method: 'DELETE' });
      if (!resp.ok) throw new Error(await resp.text() || resp.statusText);
      const box = chatFiles.querySelector('[data-file-id="' + id + '"]');
      if (box) box.remove();
      if (chatFiles.children.length === 0) chatFiles.hidden = true;
    } catch (err) {
      chatStatus.textContent = 'Could not delete file: ' + err.message;
    }
  }

  // toWireHistory strips client-side rendering metadata (context_trimmed/tool_results, kept
  // only so renderActiveTab can replay folds) down to the {role, content} pairs the backend
  // reads. Sending the untrimmed entries was a real bug: tool_results carries each web_fetch's
  // full untruncated page text, so even a few tool calls made the resent payload grow every turn
  // until nginx's client_max_body_size rejected it (413).
  function toWireHistory(history) {
    return history.map((m) => ({ role: m.role, content: m.content }));
  }

  // sendChatMessage appends the user's turn, renders it, then POSTs full history to /chat --
  // mirrors runSearch's ok/non-ok/network-failure pattern. web_search is read fresh each call,
  // so toggling it mid-conversation only affects the current question. tab is captured once up
  // front (not re-read after the await), so a late reply updates the RIGHT tab's history, but
  // only touches the visible DOM if that tab is still on screen -- a slow background answer can
  // never clobber whatever's currently shown.
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
        body: JSON.stringify({
          messages: toWireHistory(tab.history), web_search: chatWebSearch.checked, agent_id: tab.agentId || '',
          // chat_id, only for a pinned tab -- scopes this turn's file-access token to this chat,
          // so the file-operations MCP server only sees this chat's files.
          chat_id: tab.persisted ? (tab.chatId || '') : '',
        }),
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
      resyncPersistedChat(tab);
      if (tab.id === activeTabId) {
        renderChatMessage('assistant', data.answer, data.context_trimmed, data.tool_results || []);
        renderTokenUsage(tab.tokenUsage);
        chatStatus.textContent = '';
      }
    } catch (err) {
      // A network-level failure has no server response text to include, unlike the !resp.ok
      // branch -- a generic message is all there is for the user, but log the real error for
      // anyone debugging from the console.
      console.error('chat request failed:', err);
      if (tab.id === activeTabId) chatStatus.textContent = 'Chat failed: could not reach the server.';
    }
  }

  // setMode swaps between the two independent views. Only #correction-note has its own
  // hidden-state (set by renderCorrectionNote), so it's saved/restored rather than forced open;
  // everything else is unconditionally shown/hidden.
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
  updateAttachAvailability();
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

  // chat-input is a <textarea> (for multi-line questions) -- unlike <input>, it never submits
  // on Enter by itself, so this wires the standard chat convention by hand: Enter sends,
  // Shift+Enter newlines.
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

  // loadSession asks which role the session has and shows #admin-link/#account-link
  // accordingly -- both start hidden, so any failure leaves both hidden rather than risk
  // showing the admin link to a non-admin. An admin account gets both: admin rights are
  // additive on top of full self-service, never a trade-off, so #account-link and persisted
  // chats work the same for every signed-in account. Non-critical: failures are swallowed
  // silently, unlike runSearch/sendChatMessage, which surface errors since those are direct
  // user actions.
  async function loadSession() {
    try {
      const resp = await fetch('/session');
      if (!resp.ok) return;
      const data = await resp.json();
      if (data.role === 'admin') {
        adminLink.hidden = false;
      }
      if (data.role === 'admin' || data.role === 'user') {
        accountLink.hidden = false;
        loadPersistedChats();
      }
    } catch (err) {
      // Non-critical: both links simply stay hidden.
    }
  }

  loadSession();

  // Mirrors admin.js's wireSignOut -- this page doesn't load admin.js (it's the public site), so
  // the same few lines are inlined rather than pulling in the whole script for one function.
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
      tabs, makeTab, activeTab, renderChatMessage, renderActiveTab, renderTabs,
      switchTab, newChatTab, forkActiveTab, closeTab, renameTab,
      pinTab, unpinTab, togglePinTab, resyncPersistedChat, loadPersistedChats,
      pinIconSVG, updateAttachAvailability, refreshTabFileState,
      serializeTab, deserializeTab, exportActiveTab, importTabFromJSON,
      toWireHistory, sendChatMessage, setMode,
      escapeHTML, renderInline, renderMarkdown,
      buildDonutSVG, buildDonutLegend, tokenUsageSegments, renderTokenUsage,
      loadSession, renderAgentSelectOptions, loadAgentOptions,
      uploadAttachedFile, renderChatFiles, loadChatFiles, deleteChatFile,
    };
  }
