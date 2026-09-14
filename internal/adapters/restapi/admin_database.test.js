'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { setupDOM, teardownDOM, requireFresh } = require('./dom_helper.test_util');

const DATABASE_HTML = fs.readFileSync(path.join(__dirname, 'admin_database.html'), 'utf8');

// fetchImpl is installed *before* requireFresh, because admin_database.js
// calls load() itself at module load time -- setting global.fetch after
// requiring the module would race the module's own auto-triggered call
// instead of controlling it.
function loadFixture(fetchImpl) {
  setupDOM(DATABASE_HTML);
  const adminHelpers = requireFresh('./admin.js');
  Object.assign(global, adminHelpers);
  global.fetch = fetchImpl || (async () => ({ ok: true, json: async () => ({ driver: '', pool: {}, table_rows: {} }) }));
  return requireFresh('./admin_database.js');
}

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

test.afterEach(() => {
  teardownDOM();
  delete global.fetch;
});

test('renderDatabase renders driver, pool stats, and a sorted table row list', () => {
  const { renderDatabase } = loadFixture();
  renderDatabase({
    driver: 'postgres',
    pool: {
      max_open_connections: 25, open_connections: 3, in_use: 1, idle: 2,
      wait_count: 4, wait_duration_ms: 12, max_idle_closed: 5,
      max_idle_time_closed: 6, max_lifetime_closed: 7,
    },
    table_rows: { zeta: 9, alpha: 3 },
  });
  const connection = document.getElementById('db-connection');
  assert.equal(connection.textContent.includes('postgres'), true);

  const pool = document.getElementById('db-pool');
  assert.equal(pool.textContent.includes('25'), true);
  assert.equal(pool.textContent.includes('12 ms'), true);

  const rows = document.getElementById('db-tables').querySelectorAll('tbody tr');
  assert.equal(rows.length, 2);
  assert.equal(rows[0].children[0].textContent, 'alpha');
  assert.equal(rows[1].children[0].textContent, 'zeta');
});

test('load renders the fetched diagnostics on success', async () => {
  loadFixture(async () => ({
    ok: true,
    json: async () => ({ driver: 'sqlite', pool: { max_open_connections: 1, open_connections: 1, in_use: 0, idle: 1, wait_count: 0, wait_duration_ms: 0, max_idle_closed: 0, max_idle_time_closed: 0, max_lifetime_closed: 0 }, table_rows: {} }),
  }));
  await flush();
  assert.equal(document.getElementById('db-connection').textContent.includes('sqlite'), true);
});

test('load reports the error message on a failed fetch', async () => {
  loadFixture(async () => ({ ok: false, status: 500, text: async () => 'db unreachable' }));
  await flush();
  assert.equal(document.getElementById('db-connection').textContent.includes('db unreachable'), true);
});
