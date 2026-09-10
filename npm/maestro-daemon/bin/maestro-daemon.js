#!/usr/bin/env node
// Entry point for `npx maestro-daemon`: hands over to the Go binary
// (MAESTRO_DAEMON_BIN, the bundled platform package, or PATH).

const { spawnSync } = require('node:child_process')
const { binaryPath } = require('../dist/cjs/binary.js')

let bin
try {
  bin = binaryPath()
} catch (err) {
  console.error(`maestro-daemon: ${err.message}`)
  process.exit(3)
}

const result = spawnSync(bin, process.argv.slice(2), { stdio: 'inherit' })

if (result.error) {
  console.error(`maestro-daemon: ${result.error.message}`)
  process.exit(3)
}
// A binary killed by a signal has no exit code; report it the way a shell would.
if (result.signal) {
  process.exit(result.signal === 'SIGINT' ? 130 : 1)
}
process.exit(result.status === null ? 1 : result.status)
