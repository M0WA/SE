function clear(el) {
  while (el.firstChild) el.removeChild(el.firstChild);
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
  await checkResponse(await fetch(url, { method: 'DELETE' }));
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

async function loadVocabulary() {
  const summaryEl = document.getElementById('vocab-summary');
  const tableEl = document.getElementById('vocab-table');
  if (!summaryEl || !tableEl) return;
  try {
    const v = await getJSON('/admin/api/vocabulary?limit=20');
    clear(summaryEl);
    kvRow(summaryEl, 'Vocabulary size', String(v.vocabulary_size) + ' distinct terms');
    clear(tableEl);
    if (v.top_terms.length === 0) {
      tableEl.textContent = 'No terms indexed yet.';
      return;
    }
    const table = buildTable(
      [{ label: 'term' }, { label: 'doc freq', num: true }, { label: 'total freq', num: true }],
      v.top_terms,
      (t) => [
        textCell(t.term),
        textCell(String(t.doc_freq), { num: true }),
        textCell(String(t.total_freq), { num: true }),
      ],
    );
    tableEl.appendChild(table);
  } catch (err) {
    summaryEl.textContent = 'Could not load vocabulary: ' + err.message;
  }
}
