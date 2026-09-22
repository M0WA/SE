  const connectionEl = document.getElementById('db-connection');
  const poolEl = document.getElementById('db-pool');
  const tablesEl = document.getElementById('db-tables');

  function renderDatabase(d) {
    clear(connectionEl);
    kvRow(connectionEl, 'Driver', d.driver);

    clear(poolEl);
    kvRow(poolEl, 'Max open connections', String(d.pool.max_open_connections));
    kvRow(poolEl, 'Open connections', String(d.pool.open_connections));
    kvRow(poolEl, 'In use', String(d.pool.in_use));
    kvRow(poolEl, 'Idle', String(d.pool.idle));
    kvRow(poolEl, 'Wait count', String(d.pool.wait_count));
    kvRow(poolEl, 'Wait duration', d.pool.wait_duration_ms + ' ms');
    kvRow(poolEl, 'Closed (idle limit)', String(d.pool.max_idle_closed));
    kvRow(poolEl, 'Closed (idle timeout)', String(d.pool.max_idle_time_closed));
    kvRow(poolEl, 'Closed (max lifetime)', String(d.pool.max_lifetime_closed));

    clear(tablesEl);
    const rows = Object.keys(d.table_rows).sort((a, b) => a.localeCompare(b)).map((name) => ({ name, count: d.table_rows[name] }));
    const table = buildTable(
      [{ label: 'table' }, { label: 'rows', num: true }],
      rows,
      (row) => [textCell(row.name), textCell(String(row.count), { num: true })],
    );
    tablesEl.appendChild(table);
  }

  async function load() {
    try {
      renderDatabase(await getJSON('/admin/api/database'));
    } catch (err) {
      connectionEl.textContent = 'Could not load database diagnostics: ' + err.message;
    }
  }

  const clearContentBtn = document.getElementById('db-clear-content-btn');
  const clearContentStatusEl = document.getElementById('db-clear-content-status');
  const clearSettingsBtn = document.getElementById('db-clear-settings-btn');
  const clearSettingsStatusEl = document.getElementById('db-clear-settings-status');

  // clearContent/clearSettings share the same confirm-disable-post-reload
  // shape as admin_domain.js's deleteAllInDomain -- both actions here are
  // immediate and permanent, so a plain window.confirm() gate (not a
  // second click, not a typed confirmation phrase) matches every other
  // destructive action already in this admin UI.
  async function clearContent() {
    if (!window.confirm('Permanently delete every crawled document and crawl job? This cannot be undone.')) return;
    clearContentBtn.disabled = true;
    clearContentStatusEl.textContent = 'Clearing…';
    try {
      await postJSON('/admin/api/database/clear-content', {});
      clearContentStatusEl.textContent = 'Content cleared.';
      await load();
    } catch (err) {
      clearContentStatusEl.textContent = 'Could not clear content: ' + err.message;
    } finally {
      clearContentBtn.disabled = false;
    }
  }

  async function clearSettings() {
    if (!window.confirm('Permanently delete every settings row (tuning, operational, overrides, chat endpoint, embedding endpoints, crawl schedules)? This cannot be undone.')) return;
    clearSettingsBtn.disabled = true;
    clearSettingsStatusEl.textContent = 'Clearing…';
    try {
      await postJSON('/admin/api/database/clear-settings', {});
      clearSettingsStatusEl.textContent = 'Settings cleared on this admin-server. Restart search-server and crawl-server to fully apply.';
      await load();
    } catch (err) {
      clearSettingsStatusEl.textContent = 'Could not clear settings: ' + err.message;
    } finally {
      clearSettingsBtn.disabled = false;
    }
  }

  clearContentBtn.addEventListener('click', clearContent);
  clearSettingsBtn.addEventListener('click', clearSettings);

  renderAdminNav();
  wireSignOut();
  load();

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderDatabase, load, clearContent, clearSettings };
  }
