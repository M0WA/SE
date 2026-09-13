#!/usr/bin/env node
'use strict';
// Runs the JS unit test suite (internal/adapters/restapi/*.test.js) with
// coverage. Node's --test-coverage-include/--test-coverage-exclude flags
// (added in Node 20.1.0) scope V8 coverage to just this project's own
// files -- without them, coverage also picks up whatever third-party
// modules the test runner's own reporter happens to load (e.g.
// has-flag/supports-color for terminal color detection), which is noise,
// not a real gap in this project's JS coverage. On Node <20.1 (no such
// flags), this just falls back to the unscoped report -- correctness of
// the tests themselves is unaffected either way, only how tidy the
// coverage table looks locally.
const fs = require('fs');
const path = require('path');
const { spawnSync } = require('child_process');

const restapiDir = path.join(__dirname, '..', 'internal', 'adapters', 'restapi');
const testFiles = fs
  .readdirSync(restapiDir)
  .filter((f) => f.endsWith('.test.js'))
  .map((f) => path.relative(process.cwd(), path.join(restapiDir, f)));

const [major, minor] = process.versions.node.split('.').map(Number);
const supportsCoverageFilters = major > 20 || (major === 20 && minor >= 1);

const args = ['--test', '--experimental-test-coverage'];
if (supportsCoverageFilters) {
  args.push(
    '--test-coverage-include=internal/adapters/restapi/*.js',
    '--test-coverage-exclude=internal/adapters/restapi/*.test.js',
    '--test-coverage-exclude=internal/adapters/restapi/dom_helper.test_util.js',
  );
}

const result = spawnSync(process.execPath, [...args, ...testFiles], { stdio: 'inherit' });
process.exit(result.status ?? 1);
