#!/usr/bin/env node
'use strict';
// Runs the JS unit test suite (internal/adapters/restapi/*.test.js) with
// coverage.
//
// Node's --test-coverage-include/--test-coverage-exclude flags would let
// this scope the report to just this project's own files -- verified
// against real Node 18.19 and 20.20 that neither actually has them
// ("bad option"), despite being documented for "Node.js 20.1.0+"; rather
// than guess at which version truly ships them, this always runs the
// plain, unscoped --experimental-test-coverage. The one side effect: the
// report can include a couple of unrelated node_modules-of-node_modules
// files the test runner's own reporter happens to load (e.g.
// has-flag/supports-color, for terminal color detection) -- that's noise
// in the printed table, not a real gap in this project's own JS coverage
// (internal/adapters/restapi/*.js), so read the report with that in mind.
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const restapiDir = path.join(__dirname, '..', 'internal', 'adapters', 'restapi');
const testFiles = fs
  .readdirSync(restapiDir)
  .filter((f) => f.endsWith('.test.js'))
  .map((f) => path.relative(process.cwd(), path.join(restapiDir, f)));

const result = spawnSync(
  process.execPath,
  ['--test', '--experimental-test-coverage', ...testFiles],
  { stdio: 'inherit' },
);
process.exit(result.status ?? 1);
