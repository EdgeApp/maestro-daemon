// Starting a daemon that isn't running (mirrors pkg/daemon/spawn.go and the
// Edge CLI's ensureEngine): spawn `maestro-d serve --name <name>`
// detached with its output in startup.log, then poll daemon.json until the
// new process answers.

import { spawn } from 'node:child_process'
import * as fs from 'node:fs'

import { binaryPath } from './binary.js'
import { MaestroError } from './errors.js'
import { liveInfo, pidAlive, readInfo, removeRunFiles, runDir, startupLogPath, tailFile, validateName } from './runfiles.js'
import { Transport } from './transport.js'
import type { DaemonInfo } from './types.js'

export interface SpawnOptions {
  /** Fail with DAEMON_UNAVAILABLE instead of starting one. */
  spawn?: boolean
  /** Seconds of inactivity before the daemon exits; 0 = never. Default 1800. */
  idleTimeout?: number
  /** Also listen on TCP (`127.0.0.1:7788`). */
  http?: string
  /** Bearer token required on TCP. */
  token?: string
  /** Milliseconds to wait for the daemon to come up (default 20000). */
  startTimeout?: number
  /** Extra `serve` arguments. */
  extraArgs?: string[]
  /** Path of the maestro-d binary (else MAESTRO_D_BIN, bundled, PATH). */
  bin?: string
  /** Progress lines ("Starting daemon …"). */
  log?: (message: string) => void
  signal?: AbortSignal
}

export interface Ensured {
  info: DaemonInfo
  transport: Transport
  /** True when this call started the daemon. */
  started: boolean
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    function onAbort(): void {
      clearTimeout(t)
      reject(MaestroError.of('INTERRUPTED', 'aborted while waiting for the daemon'))
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

async function ping(t: Transport, timeout: number): Promise<boolean> {
  try {
    await t.json('/v1/status', { timeout })
    return true
  } catch {
    return false
  }
}

/** Connect to the named daemon, spawning it when needed. */
export async function ensureDaemon(name: string, opts: SpawnOptions = {}): Promise<Ensured> {
  validateName(name)
  const existing = liveInfo(name)
  if (existing) {
    const t = new Transport({ socketPath: existing.socketPath })
    if (await ping(t, 3000)) {
      return { info: existing, transport: t, started: false }
    }
    t.close()
    opts.log?.(`daemon "${name}" (pid ${existing.pid}) is not answering`)
    if (!pidAlive(existing.pid)) {
      removeRunFiles(name)
    } else {
      throw MaestroError.of(
        'DAEMON_UNAVAILABLE',
        `daemon "${name}" (pid ${existing.pid}) exists but does not answer on ${existing.socketPath}; ` +
          `stop it with \`maestro-d stop --name ${name}\``,
      )
    }
  }
  if (opts.spawn === false) {
    throw MaestroError.of('DAEMON_UNAVAILABLE', `daemon "${name}" is not running`)
  }
  return spawnDaemon(name, opts)
}

async function spawnDaemon(name: string, opts: SpawnOptions): Promise<Ensured> {
  const bin = binaryPath(opts.bin)
  fs.mkdirSync(runDir(name), { recursive: true, mode: 0o700 })
  const logPath = startupLogPath(name)
  const logFd = fs.openSync(logPath, 'w', 0o600)

  const args = ['serve', '--name', name]
  if (opts.idleTimeout !== undefined) args.push('--idle-timeout', String(opts.idleTimeout))
  if (opts.http) args.push('--http', opts.http)
  if (opts.token) args.push('--token', opts.token)
  if (opts.extraArgs) args.push(...opts.extraArgs)

  opts.log?.(`Starting daemon "${name}": ${bin} ${args.join(' ')}`)
  let child
  try {
    // Own process group so a Ctrl-C in the caller's terminal does not kill it.
    child = spawn(bin, args, { detached: true, stdio: ['ignore', logFd, logFd], env: process.env })
  } catch (err) {
    fs.closeSync(logFd)
    throw MaestroError.of('DAEMON_UNAVAILABLE', `spawn ${bin}: ${(err as Error).message}`, { cause: err })
  }
  fs.closeSync(logFd)

  let exited: { code: number | null; signal: NodeJS.Signals | null } | undefined
  let spawnError: Error | undefined
  child.on('exit', (code, signal) => {
    exited = { code, signal }
  })
  child.on('error', (err) => {
    spawnError = err
  })
  child.unref()
  const pid = child.pid ?? 0

  const timeout = opts.startTimeout ?? 20_000
  const deadline = Date.now() + timeout
  for (;;) {
    await sleep(100, opts.signal)
    if (spawnError) {
      throw MaestroError.of('DAEMON_UNAVAILABLE', `spawn ${bin}: ${spawnError.message}`, { cause: spawnError })
    }
    if (exited) {
      const how = exited.signal ? `signal ${exited.signal}` : `exit ${exited.code}`
      throw MaestroError.of('DAEMON_UNAVAILABLE', `daemon "${name}" (pid ${pid}) exited during startup: ${how}\n${tailFile(logPath, 20)}`)
    }
    const info = readInfo(name)
    if (info && info.pid === pid) {
      const t = new Transport({ socketPath: info.socketPath })
      if (await ping(t, 2000)) {
        opts.log?.(`Daemon "${name}" ready (pid ${pid}, socket ${info.socketPath})`)
        return { info, transport: t, started: true }
      }
      t.close()
    }
    if (Date.now() > deadline) {
      try {
        child.kill()
      } catch {
        // already gone
      }
      throw MaestroError.of('DAEMON_UNAVAILABLE', `daemon "${name}" (pid ${pid}) did not become ready within ${timeout}ms\n${tailFile(logPath, 20)}`)
    }
  }
}

/** Wait until the daemon's pid is gone; false on timeout. */
export async function waitForExit(info: DaemonInfo, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs
  while (pidAlive(info.pid)) {
    if (Date.now() > deadline) return false
    await sleep(100)
  }
  return true
}
