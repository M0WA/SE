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

async function getJSON(url) {
  const resp = await fetch(url);
  if (!resp.ok) {
    const msg = await resp.text();
    throw new Error(msg.trim() || ('request failed: ' + resp.status));
  }
  return resp.json();
}

async function postJSON(url, body) {
  const resp = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    const msg = await resp.text();
    throw new Error(msg.trim() || ('request failed: ' + resp.status));
  }
  return resp.json();
}

async function deleteRequest(url) {
  const resp = await fetch(url, { method: 'DELETE' });
  if (!resp.ok) {
    const msg = await resp.text();
    throw new Error(msg.trim() || ('request failed: ' + resp.status));
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
