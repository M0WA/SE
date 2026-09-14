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
    const rows = Object.keys(d.table_rows).sort().map((name) => ({ name, count: d.table_rows[name] }));
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

  wireSignOut();
  load();

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderDatabase, load };
  }
