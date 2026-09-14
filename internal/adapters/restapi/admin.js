function clear(el) {
  while (el.firstChild) el.removeChild(el.firstChild);
}

// setButtonLoading swaps a button's label for a small spinner + verb while
// an action is in flight, and disables it so the same click can't fire
// twice -- restoring the exact original label when the action settles
// (success or failure) via the matching setButtonLoading(btn, false) call.
// Shared by every admin form's submit button (see crawl.html, admin_pagerank.html)
// rather than each page hand-rolling its own busy state.
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

// normalizeURL prepends https:// when a URL has no scheme at all, so an
// admin can type "example.com" instead of always needing the full
// "https://example.com" -- a URL that already names an explicit scheme
// (http://, ftp://, etc.) is left untouched.
function normalizeURL(value) {
  const trimmed = value.trim();
  if (trimmed === '' || /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(trimmed)) {
    return trimmed;
  }
  return 'https://' + trimmed;
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
  if (opts && opts.num) td.className = 'num';
  td.textContent = text;
  return td;
}

// snippetCell builds a <td> for a server-rendered search excerpt, which
// carries <mark> tags around the matched terms (see domain.Snippet) --
// rendered via innerHTML, same as the public search page's result-snippet,
// so the highlighting actually shows rather than the raw markup as text.
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

// seedSummary renders a crawl's seed URL list as "first +N more" (or
// "(no seed)" for an empty list) -- shared by the crawl job table and the
// schedules table.
function seedSummary(seedURLs) {
  const urls = seedURLs || [];
  if (urls.length === 0) return '(no seed)';
  return urls.length === 1 ? urls[0] : urls[0] + ' +' + (urls.length - 1) + ' more';
}

// linesToText/parseLines round-trip a one-value-per-line <textarea> (seed
// URLs, allow/block domain lists, ...) against the string array a JSON
// request/response actually carries -- shared by every page with one of
// these fields (see crawl.html, admin_schedule.html, admin_settings.html).
function linesToText(lines) {
  return (lines || []).join('\n');
}

function parseLines(text) {
  return text.split('\n').map((s) => s.trim()).filter(Boolean);
}

// formatTimestamp renders an ISO timestamp for display, or an em-dash for
// an empty/missing one. opts.timeOnly renders just the time (for a
// same-page list of events that's already scoped to one job); otherwise
// renders the full local date and time.
function formatTimestamp(iso, opts) {
  if (!iso) return '—';
  const d = new Date(iso);
  return (opts && opts.timeOnly) ? d.toLocaleTimeString() : d.toLocaleString();
}

// buildTable assembles a <table> from a header spec ({label, num?}[]) and
// one or more data rows, delegating each row's <td> cells to cellsForRow
// so callers can mix textCell with richer custom cells (links, buttons).
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

// Vocabulary state: a real server-driven page (limit/offset), sorted by a
// real server-driven column/direction (sort/dir) -- see
// handleAdminVocabulary. Unlike the old approach (fetch up to 2000 terms
// once, then regex-filter/display that fixed slice client-side), every
// page and sort order rendered here is correct regardless of how large the
// corpus's vocabulary actually is, since the database itself does the
// ordering and paging rather than a client-side slice of a bounded sample.
// The tradeoff: the search box is now a plain substring filter (what the
// server itself supports), not a client-side regex.
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

// vocabDefaultDir picks a sensible starting direction the first time a
// column is clicked: alphabetical starts ascending, frequency columns
// start descending (most-frequent first, the old fixed behavior).
function vocabDefaultDir(key) {
  return key === 'term' ? 'asc' : 'desc';
}

// buildVocabTable renders one page of terms with clickable, sort-indicating
// column headers -- clicking the already-active column flips its
// direction; clicking a different one switches to it at its default
// direction (see vocabDefaultDir) and reloads page 1.
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
    th.textContent = col.label + (active ? (vocabSortDir === 'asc' ? ' ▲' : ' ▼') : '');
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

// renderVocabPager shows/hides the Previous/Next controls and page count --
// hidden entirely when everything fits on one page, same convention as
// admin_jobs.js's per-job page detail pager.
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

// loadVocabulary fetches the current page (vocabPage/vocabPageSize/
// vocabSearch/vocabSortBy/vocabSortDir) from the server and renders it.
// `vocabulary_size` in the summary is always the server's real
// corpus-wide distinct-term count; `matched_count` (shown alongside it
// only while a search filter is active) is what that filter matches, and
// is what the pager's page count is computed from.
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

// wireVocabularySearch wires the vocabulary panel's filter (debounced on
// input, immediate on submit), items-per-page field, and Previous/Next
// pager, then loads page 1 immediately -- unlike the old regex-search
// version, this is a real pageable list, so it has something to show
// before the admin types anything.
function wireVocabularySearch() {
  const form = document.getElementById('vocab-search-form');
  const input = document.getElementById('vocab-q');
  if (!form || !input) return;

  const pageSizeEl = document.getElementById('vocab-page-size');
  if (pageSizeEl) {
    const initial = parseInt(pageSizeEl.value, 10);
    if (Number.isFinite(initial) && initial > 0) vocabPageSize = initial;
    pageSizeEl.addEventListener('change', () => {
      const n = parseInt(pageSizeEl.value, 10);
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

// Exports for the Node test runner only -- `typeof module` is undefined in
// a browser's <script> tag, so this is a no-op there. See
// internal/adapters/restapi/admin.test.js.
if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    clear, setButtonLoading, normalizeURL, kvRow, checkResponse,
    getJSON, postJSON, deleteRequest, patchJSON,
    textCell, snippetCell, urlCell, seedSummary, formatTimestamp, buildTable,
    linesToText, parseLines,
    wireSignOut, loadStats, loadVocabulary, wireVocabularySearch,
  };
}
