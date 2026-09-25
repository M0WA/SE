function clear(el) {
  while (el.firstChild) el.firstChild.remove();
}

// setButtonLoading swaps a button's label for a spinner+verb while busy, disabling it against
// double-clicks, and restores the label on setButtonLoading(btn, false). Shared by every admin
// form's submit button.
function setButtonLoading(btn, loading, loadingLabel) {
  if (loading) {
    if (btn.dataset.originalLabel === undefined) btn.dataset.originalLabel = btn.textContent;
    btn.disabled = true;
    clear(btn);
    const spinner = document.createElement('span');
    spinner.className = 'spinner';
    btn.appendChild(spinner);
    btn.appendChild(document.createTextNode(loadingLabel || btn.dataset.originalLabel));
  } else {
    btn.disabled = false;
    if (btn.dataset.originalLabel !== undefined) {
      btn.textContent = btn.dataset.originalLabel;
      delete btn.dataset.originalLabel;
    }
  }
}

// normalizeURL prepends https:// to a scheme-less URL (so "example.com" works); a URL with an
// explicit scheme is left untouched.
function normalizeURL(value) {
  const trimmed = value.trim();
  if (trimmed === '' || /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(trimmed)) {
    return trimmed;
  }
  return 'https://' + trimmed;
}

// ICON_SVGS/ACTION_GLYPHS/setIconLabel: shared icon-only action-button convention for admin lists
// (see CLAUDE.md's "Icon conventions"). Plain Unicode glyphs where genuinely monochrome ("▶" run,
// "✎" edit); "view"/"delete" use inline SVG instead, since their closest Unicode codepoints render
// as full-color emoji. stroke="currentColor" keeps an SVG matching color/hover/focus like a glyph would.
const ICON_SVGS = {
  view: '<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>',
  delete: '<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><line x1="10" y1="11" x2="10" y2="17"/><line x1="14" y1="11" x2="14" y2="17"/></svg>',
};
const ACTION_GLYPHS = { run: '▶', edit: '✎' };

// setIconLabel gives el an icon (SVG or glyph) as its only content, plus a title/aria-label carrying
// the real action name -- e.g. Cancel can reuse the "delete" trash icon while still reading as
// "Cancel" to a screen reader.
function setIconLabel(el, iconKey, label) {
  el.classList.add('icon-button');
  if (ICON_SVGS[iconKey]) {
    el.innerHTML = ICON_SVGS[iconKey];
  } else {
    el.textContent = ACTION_GLYPHS[iconKey];
  }
  el.title = label;
  el.setAttribute('aria-label', label);
}

// actionsCell builds a standard Edit/Delete actions <td>, reused by every list page's Edit/Delete
// column (job/schedule rows build their own, having more actions).
function actionsCell(editHref, deleteLabel, onDelete) {
  const td = document.createElement('td');
  td.className = 'actions';
  const editLink = document.createElement('a');
  editLink.className = 'text-button';
  editLink.href = editHref;
  setIconLabel(editLink, 'edit', 'Edit');
  td.appendChild(editLink);
  const delBtn = document.createElement('button');
  delBtn.type = 'button';
  delBtn.className = 'text-button';
  setIconLabel(delBtn, 'delete', deleteLabel);
  delBtn.addEventListener('click', onDelete);
  td.appendChild(delBtn);
  return td;
}

function kvRow(container, key, value) {
  const row = document.createElement('div');
  row.className = 'kv-row';
  const k = document.createElement('span');
  k.className = 'k';
  k.textContent = key;
  const v = document.createElement('span');
  v.className = 'v';
  v.textContent = value;
  row.appendChild(k);
  row.appendChild(v);
  container.appendChild(row);
}

// listItem appends one plain row with no key column -- for a flat list of same-kind values, where
// kvRow would just repeat the same label.
function listItem(container, value) {
  const row = document.createElement('div');
  row.className = 'list-item';
  row.textContent = value;
  container.appendChild(row);
}

// buildTile builds one ".tile" box (see style.css's ".tiles" grid) -- a compact key/value pair,
// distinct from kvRow's list-row treatment.
function buildTile(k, v) {
  const tile = document.createElement('div');
  tile.className = 'tile';
  const kEl = document.createElement('div');
  kEl.className = 'k';
  kEl.textContent = k;
  const vEl = document.createElement('div');
  vEl.className = 'v';
  vEl.textContent = v;
  tile.appendChild(kEl);
  tile.appendChild(vEl);
  return tile;
}

async function checkResponse(resp) {
  if (!resp.ok) {
    const msg = await resp.text();
    throw new Error(msg.trim() || ('request failed: ' + resp.status));
  }
  return resp;
}

async function getJSON(url) {
  return (await checkResponse(await fetch(url))).json();
}

async function postJSON(url, body) {
  return (await checkResponse(await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }))).json();
}

async function deleteRequest(url) {
  return (await checkResponse(await fetch(url, { method: 'DELETE' }))).json();
}

async function patchJSON(url, body) {
  return (await checkResponse(await fetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }))).json();
}

// textCell builds a plain <td>; opts.num right-aligns and monospaces it
// (for numeric/technical columns).
function textCell(text, opts) {
  const td = document.createElement('td');
  if (opts?.num) td.className = 'num';
  td.textContent = text;
  return td;
}

// snippetCell builds a <td> for a server-rendered excerpt carrying <mark> tags (domain.Snippet) --
// rendered via innerHTML so the highlighting actually shows.
function snippetCell(html) {
  const td = document.createElement('td');
  td.className = 'excerpt';
  td.innerHTML = html || '';
  return td;
}

// urlCell builds a <td class="url"> for a raw URL/text value -- shared by
// the crawl and schedules admin pages' job/schedule tables.
function urlCell(text) {
  const td = document.createElement('td');
  td.className = 'url';
  td.textContent = text;
  return td;
}

// seedSummary renders a crawl's seed URLs as "first +N more" (or "(no seed)") -- shared by the
// job and schedules tables.
function seedSummary(seedURLs) {
  const urls = seedURLs || [];
  if (urls.length === 0) return '(no seed)';
  return urls.length === 1 ? urls[0] : urls[0] + ' +' + (urls.length - 1) + ' more';
}

// linesToText/parseLines round-trip a one-value-per-line <textarea> against the string array a
// JSON request/response carries -- shared by every page with such a field.
function linesToText(lines) {
  return (lines || []).join('\n');
}

function parseLines(text) {
  return text.split('\n').map((s) => s.trim()).filter(Boolean);
}

// formatTimestamp renders an ISO timestamp, or an em-dash if empty. opts.timeOnly renders just the
// time (for an already-job-scoped list); otherwise full date+time.
function formatTimestamp(iso, opts) {
  if (!iso) return '—';
  const d = new Date(iso);
  return opts?.timeOnly ? d.toLocaleTimeString() : d.toLocaleString();
}

// buildTable assembles a <table> from a header spec and data rows, delegating each row's cells to
// cellsForRow so callers can mix textCell with richer custom cells.
function buildTable(headers, rows, cellsForRow) {
  const table = document.createElement('table');
  const thead = document.createElement('thead');
  const headRow = document.createElement('tr');
  for (const h of headers) {
    const th = document.createElement('th');
    if (h.num) th.className = 'num';
    th.textContent = h.label;
    headRow.appendChild(th);
  }
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  for (const row of rows) {
    const tr = document.createElement('tr');
    for (const cell of cellsForRow(row)) {
      tr.appendChild(cell);
    }
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  return table;
}

// regexFilter is the shared case-insensitive-regex-against-an-in-memory-array admin list filter
// (see filterJobs/filterPages/filterCrawls, filterDocs). Blank pattern returns items unchanged;
// an invalid one reports errorPrefix+message into errorEl rather than throwing.
function regexFilter(items, pattern, errorEl, testFn, errorPrefix) {
  if (!pattern) {
    errorEl.textContent = '';
    return items;
  }
  try {
    const re = new RegExp(pattern, 'i');
    errorEl.textContent = '';
    return items.filter((item) => testFn(re, item));
  } catch (err) {
    errorEl.textContent = (errorPrefix || 'Invalid regex') + ': ' + err.message;
    return items;
  }
}

// ADMIN_NAV_GROUPS is the sidebar's single source of truth for grouping/order -- previously every
// page hand-copied the same flat nav, so reordering meant editing ten HTML files. renderAdminNav
// now builds it once per page from this list.
const ADMIN_NAV_GROUPS = [
  { label: 'Overview', href: '/admin' },
  {
    label: 'Content',
    items: [
      { label: 'Documents', href: '/admin/documents' },
      { label: 'Upload', href: '/admin/document-upload' },
      { label: 'Content dedup', href: '/admin/content_dedup' },
    ],
  },
  {
    label: 'Index',
    items: [
      { label: 'Crawler', href: '/admin/crawl' },
      { label: 'Jobs', href: '/admin/jobs' },
    ],
  },
  {
    label: 'Relevance',
    items: [
      { label: 'Search', href: '/admin/search' },
      { label: 'PageRank', href: '/admin/pagerank' },
      { label: 'Embeddings', href: '/admin/embeddings' },
    ],
  },
  {
    label: 'Chat',
    items: [
      { label: 'Settings', href: '/admin/chat/settings' },
      { label: 'MCP servers', href: '/admin/mcp-servers' },
      { label: 'Agents', href: '/admin/agents' },
    ],
  },
  {
    label: 'System',
    items: [
      { label: 'Settings', href: '/admin/settings' },
      { label: 'Database', href: '/admin/database' },
      { label: 'Users', href: '/admin/users' },
    ],
  },
];

// renderAdminNav fills #admin-rail from ADMIN_NAV_GROUPS, marking the current path's entry. No-op
// without that element -- drill-down pages keep a plain "back" link instead.
function renderAdminNav() {
  const rail = document.getElementById('admin-rail');
  if (!rail) return;
  clear(rail);
  const path = window.location.pathname;

  const [overview, ...groups] = ADMIN_NAV_GROUPS;
  const top = document.createElement('a');
  top.className = 'rail-top';
  top.href = overview.href;
  top.textContent = overview.label;
  if (path === overview.href) top.setAttribute('aria-current', 'page');
  rail.appendChild(top);

  for (const group of groups) {
    const groupEl = document.createElement('div');
    groupEl.className = 'rail-group';
    const tab = document.createElement('div');
    tab.className = 'rail-tab';
    tab.textContent = group.label;
    groupEl.appendChild(tab);
    for (const item of group.items) {
      const a = document.createElement('a');
      a.href = item.href;
      a.textContent = item.label;
      if (path === item.href) a.setAttribute('aria-current', 'page');
      groupEl.appendChild(a);
    }
    rail.appendChild(groupEl);
  }
}

function wireSignOut() {
  const btn = document.getElementById('sign-out');
  if (!btn) return;
  btn.addEventListener('click', async () => {
    try {
      await fetch('/logout', { method: 'POST' });
    } finally {
      window.location = '/';
    }
  });
}

async function loadStats() {
  const el = document.getElementById('stats');
  if (!el) return;
  try {
    const s = await getJSON('/admin/api/stats');
    clear(el);
    kvRow(el, 'Documents', String(s.total_docs));
    kvRow(el, 'Avg length', s.avg_doc_len.toFixed(1) + ' tokens');
    kvRow(el, 'Driver', s.driver);
  } catch (err) {
    el.textContent = 'Could not load stats: ' + err.message;
  }
}

// Vocabulary state is a real server-paged/sorted list (see handleAdminVocabulary), unlike the old
// fetch-2000-then-filter-client-side approach -- correct regardless of corpus size, since the DB
// does the ordering/paging. Tradeoff: search is now a plain substring filter, not client-side regex.
let vocabPage = 0;
let vocabPageSize = 20;
let vocabSearch = '';
let vocabSortBy = 'doc_freq';
let vocabSortDir = 'desc';

const VOCAB_COLUMNS = [
  { key: 'term', label: 'term' },
  { key: 'doc_freq', label: 'doc freq', num: true },
  { key: 'total_freq', label: 'total freq', num: true },
];

// vocabDefaultDir picks a first-click direction: alphabetical starts ascending, frequency columns
// start descending (most-frequent first).
function vocabDefaultDir(key) {
  return key === 'term' ? 'asc' : 'desc';
}

// buildVocabTable renders one page of terms with clickable sort headers -- clicking the active
// column flips direction; a different one switches to its default (vocabDefaultDir) and reloads page 1.
function buildVocabTable(terms) {
  const table = document.createElement('table');
  const thead = document.createElement('thead');
  const headRow = document.createElement('tr');
  for (const col of VOCAB_COLUMNS) {
    const th = document.createElement('th');
    if (col.num) th.className = 'num';
    th.classList.add('sortable-th');
    th.tabIndex = 0;
    const active = vocabSortBy === col.key;
    let sortIndicator = '';
    if (active) sortIndicator = vocabSortDir === 'asc' ? ' ▲' : ' ▼';
    th.textContent = col.label + sortIndicator;
    const activate = () => {
      if (vocabSortBy === col.key) {
        vocabSortDir = vocabSortDir === 'asc' ? 'desc' : 'asc';
      } else {
        vocabSortBy = col.key;
        vocabSortDir = vocabDefaultDir(col.key);
      }
      vocabPage = 0;
      loadVocabulary();
    };
    th.addEventListener('click', activate);
    th.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); activate(); }
    });
    headRow.appendChild(th);
  }
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  for (const t of terms) {
    const tr = document.createElement('tr');
    const link = document.createElement('a');
    link.href = '/admin/vocabulary/term?term=' + encodeURIComponent(t.term);
    link.style.color = 'var(--ink)';
    link.textContent = t.term;
    link.title = 'See which pages contain this term';
    const termTd = document.createElement('td');
    termTd.appendChild(link);
    tr.appendChild(termTd);
    tr.appendChild(textCell(String(t.doc_freq), { num: true }));
    tr.appendChild(textCell(String(t.total_freq), { num: true }));
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);
  return table;
}

// renderVocabPager shows/hides Previous/Next and the page count -- hidden entirely when everything
// fits on one page.
function renderVocabPager(pagerEl, matchedCount) {
  const totalPages = Math.max(1, Math.ceil(matchedCount / vocabPageSize));
  pagerEl.hidden = totalPages <= 1;
  const info = document.getElementById('vocab-page-info');
  if (info) info.textContent = 'Page ' + (vocabPage + 1) + ' of ' + totalPages;
  const prev = document.getElementById('vocab-prev');
  const next = document.getElementById('vocab-next');
  if (prev) prev.disabled = vocabPage <= 0;
  if (next) next.disabled = vocabPage + 1 >= totalPages;
}

// loadVocabulary fetches/renders the current page. `vocabulary_size` is the real corpus-wide
// distinct-term count; `matched_count` (shown only while filtering) is what the pager's page
// count is based on.
async function loadVocabulary() {
  const summaryEl = document.getElementById('vocab-summary');
  const tableEl = document.getElementById('vocab-table');
  const pagerEl = document.getElementById('vocab-pager');
  if (!summaryEl || !tableEl) return;
  try {
    const params = new URLSearchParams({
      limit: String(vocabPageSize),
      offset: String(vocabPage * vocabPageSize),
      sort: vocabSortBy,
      dir: vocabSortDir,
    });
    if (vocabSearch) params.set('search', vocabSearch);
    const v = await getJSON('/admin/api/vocabulary?' + params.toString());
    clear(summaryEl);
    kvRow(summaryEl, 'Vocabulary size', String(v.vocabulary_size) + ' distinct terms' +
      (vocabSearch ? ' (' + v.matched_count + ' match “' + vocabSearch + '”)' : ''));
    clear(tableEl);
    if (v.terms.length === 0) {
      tableEl.textContent = vocabSearch ? 'No terms match “' + vocabSearch + '”.' : 'No terms indexed yet.';
      if (pagerEl) pagerEl.hidden = true;
      return;
    }
    tableEl.appendChild(buildVocabTable(v.terms));
    if (pagerEl) renderVocabPager(pagerEl, v.matched_count);
  } catch (err) {
    clear(tableEl);
    summaryEl.textContent = 'Could not load vocabulary: ' + err.message;
    if (pagerEl) pagerEl.hidden = true;
  }
}

// wireVocabularySearch wires the filter (debounced on input, immediate on submit), page-size field,
// and pager, then loads page 1 immediately -- a real pageable list, unlike the old regex-search version.
function wireVocabularySearch() {
  const form = document.getElementById('vocab-search-form');
  const input = document.getElementById('vocab-q');
  if (!form || !input) return;

  const pageSizeEl = document.getElementById('vocab-page-size');
  if (pageSizeEl) {
    const initial = Number.parseInt(pageSizeEl.value, 10);
    if (Number.isFinite(initial) && initial > 0) vocabPageSize = initial;
    pageSizeEl.addEventListener('change', () => {
      const n = Number.parseInt(pageSizeEl.value, 10);
      vocabPageSize = (Number.isFinite(n) && n > 0) ? n : 20;
      vocabPage = 0;
      loadVocabulary();
    });
  }

  let searchTimer = null;
  input.addEventListener('input', () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      vocabSearch = input.value.trim();
      vocabPage = 0;
      loadVocabulary();
    }, 200);
  });
  form.addEventListener('submit', (e) => {
    e.preventDefault();
    clearTimeout(searchTimer);
    vocabSearch = input.value.trim();
    vocabPage = 0;
    loadVocabulary();
  });

  const prevBtn = document.getElementById('vocab-prev');
  const nextBtn = document.getElementById('vocab-next');
  if (prevBtn) {
    prevBtn.addEventListener('click', () => {
      if (vocabPage > 0) { vocabPage--; loadVocabulary(); }
    });
  }
  if (nextBtn) {
    nextBtn.addEventListener('click', () => {
      vocabPage++;
      loadVocabulary();
    });
  }

  loadVocabulary();
}

// pollWhileInProgress is the shared "re-fetch every 2s while a job runs, stop once it isn't" pattern
// (see admin_pagerank.js/admin_embeddings.js/admin_content_dedup.js). Call with the inProgress flag,
// current timer handle, and a reload callback that clears the caller's own timer var, then store the
// return value back into it.
function pollWhileInProgress(inProgress, pollTimer, reload) {
  if (inProgress) {
    return pollTimer || setTimeout(reload, 2000);
  }
  if (pollTimer) clearTimeout(pollTimer);
  return null;
}

// estimateTokensClient mirrors application.estimateTokens's approximate 3-chars/token estimate --
// lets admin_chat_settings.js preview a prompt's size against budget before any real turn exists
// to read a server estimate from.
const APPROX_CHARS_PER_TOKEN = 3;
function estimateTokensClient(text) {
  return Math.ceil((text || '').length / APPROX_CHARS_PER_TOKEN);
}

// buildDonutSVG renders {label,value,color} segments as an SVG ring (stroke-dasharray/dashoffset
// on concentric circles, no canvas/charting library) -- a plain dependency-free DOM node. A
// zero/negative total renders one muted full ring instead of vanishing.
function buildDonutSVG(segments, opts) {
  opts = opts || {};
  const size = opts.size || 120;
  const strokeWidth = opts.strokeWidth || 16;
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

// buildDonutLegend renders one line (swatch, label, value) per segment beside a buildDonutSVG chart
// -- the chart alone can't convey exact numbers or which color is which.
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

// Node test-runner export only; no-op in a browser <script> tag.
if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    clear, setButtonLoading, normalizeURL, kvRow, listItem, buildTile, checkResponse,
    getJSON, postJSON, deleteRequest, patchJSON,
    textCell, snippetCell, urlCell, seedSummary, formatTimestamp, buildTable,
    linesToText, parseLines, regexFilter,
    ADMIN_NAV_GROUPS, renderAdminNav,
    wireSignOut, loadStats, loadVocabulary, wireVocabularySearch, pollWhileInProgress,
    estimateTokensClient, buildDonutSVG, buildDonutLegend,
    ICON_SVGS, ACTION_GLYPHS, setIconLabel, actionsCell,
  };
}
