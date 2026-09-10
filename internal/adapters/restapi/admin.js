function clear(el) {
  while (el.firstChild) el.removeChild(el.firstChild);
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

// textCell builds a plain <td>; opts.num right-aligns and monospaces it
// (for numeric/technical columns).
function textCell(text, opts) {
  const td = document.createElement('td');
  if (opts && opts.num) td.className = 'num';
  td.textContent = text;
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
