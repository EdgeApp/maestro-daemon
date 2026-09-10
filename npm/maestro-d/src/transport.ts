// HTTP transport over the daemon's unix socket or TCP address. Every
// failure surfaces as a MaestroError: decoded from the body when the daemon
// answered, DAEMON_UNAVAILABLE when it did not, INTERRUPTED when the
// caller's AbortSignal fired.

import * as http from 'node:http'

import { MaestroError } from './errors.js'
import type { Envelope } from './types.js'

export const API_VERSION = '1'

export interface TransportOptions {
  socketPath?: string
  /** `http://127.0.0.1:7788` or `127.0.0.1:7788`. */
  url?: string
  token?: string
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  body?: unknown
  accept?: string
  signal?: AbortSignal
  /** Milliseconds; 0 = none. */
  timeout?: number
}

export interface RawResponse {
  status: number
  headers: http.IncomingHttpHeaders
  body: Buffer
}

export class Transport {
  private readonly agent: http.Agent
  private readonly socketPath?: string
  private readonly host: string
  private readonly port: number
  private readonly token?: string
  readonly describe: string

  constructor(opts: TransportOptions) {
    // The daemon serialises steps per device; keep a few idle sockets so a
    // long step and an event stream do not fight over one connection.
    this.agent = new http.Agent({ keepAlive: true, maxSockets: 8 })
    if (opts.socketPath) {
      this.socketPath = opts.socketPath
      this.host = 'maestro-d'
      this.port = 0
      this.describe = opts.socketPath
    } else if (opts.url) {
      const u = new URL(opts.url.includes('://') ? opts.url : `http://${opts.url}`)
      this.host = u.hostname
      this.port = Number(u.port || 80)
      this.describe = u.origin
    } else {
      throw new TypeError('Transport needs a socketPath or url')
    }
    if (opts.token) this.token = opts.token
  }

  /** Close idle keep-alive sockets. */
  close(): void {
    this.agent.destroy()
  }

  /** Perform a request and return the raw response. */
  raw(path: string, opts: RequestOptions = {}): Promise<RawResponse> {
    const method = opts.method ?? 'GET'
    const headers: http.OutgoingHttpHeaders = {
      'X-Maestro-Api-Version': API_VERSION,
      Accept: opts.accept ?? 'application/json',
    }
    let payload: Buffer | undefined
    if (opts.body !== undefined) {
      payload = Buffer.from(JSON.stringify(opts.body), 'utf8')
      headers['Content-Type'] = 'application/json'
      headers['Content-Length'] = payload.length
    }
    if (this.token) headers.Authorization = `Bearer ${this.token}`

    const reqOpts: http.RequestOptions = {
      method,
      path,
      headers,
      agent: this.agent,
      host: this.host,
      port: this.port,
    }
    if (this.socketPath) {
      reqOpts.socketPath = this.socketPath
      delete reqOpts.port
    }
    if (opts.signal) reqOpts.signal = opts.signal
    if (opts.timeout) reqOpts.timeout = opts.timeout

    return new Promise<RawResponse>((resolve, reject) => {
      const req = http.request(reqOpts, (res) => {
        const chunks: Buffer[] = []
        res.on('data', (c: Buffer) => chunks.push(c))
        res.on('end', () => resolve({ status: res.statusCode ?? 0, headers: res.headers, body: Buffer.concat(chunks) }))
        res.on('error', (err) => reject(this.transportError(err, opts.signal)))
      })
      req.on('error', (err) => reject(this.transportError(err, opts.signal)))
      if (opts.timeout) {
        req.on('timeout', () => req.destroy(new Error(`no response within ${opts.timeout}ms`)))
      }
      if (payload) req.write(payload)
      req.end()
    })
  }

  /**
   * JSON request. A non-2xx response is decoded into a MaestroError; when
   * the body is itself a result envelope (a failed step) it is attached as
   * `result`.
   */
  async json<T extends Envelope>(path: string, opts: RequestOptions = {}): Promise<T> {
    const res = await this.raw(path, opts)
    const text = res.body.toString('utf8')
    let parsed: T | undefined
    if (text.trim() !== '') {
      try {
        parsed = JSON.parse(text) as T
      } catch (err) {
        if (res.status < 300) {
          throw MaestroError.of('INTERNAL', `decode ${opts.method ?? 'GET'} ${path}: ${(err as Error).message}`)
        }
      }
    }
    if (res.status >= 300) {
      const result = parsed && ('step' in parsed || 'results' in parsed) ? parsed : undefined
      throw MaestroError.fromResponse(res.status, text, result as never)
    }
    if (parsed === undefined) {
      throw MaestroError.of('INTERNAL', `empty response from ${opts.method ?? 'GET'} ${path}`)
    }
    return parsed
  }

  /** Binary request (screenshots, raw hierarchies). */
  async bytes(path: string, opts: RequestOptions = {}): Promise<Buffer> {
    const res = await this.raw(path, { accept: '*/*', ...opts })
    if (res.status >= 300) {
      throw MaestroError.fromResponse(res.status, res.body.toString('utf8'))
    }
    return res.body
  }

  /**
   * Open a Server-Sent Events stream and yield each event's parsed `data`.
   * Ends when the signal fires or the daemon closes the stream.
   */
  async *sse<T>(path: string, signal?: AbortSignal): AsyncGenerator<T, void, undefined> {
    const headers: http.OutgoingHttpHeaders = {
      'X-Maestro-Api-Version': API_VERSION,
      Accept: 'text/event-stream',
    }
    if (this.token) headers.Authorization = `Bearer ${this.token}`
    const reqOpts: http.RequestOptions = { method: 'GET', path, headers, host: this.host, port: this.port }
    if (this.socketPath) {
      reqOpts.socketPath = this.socketPath
      delete reqOpts.port
    }
    if (signal) reqOpts.signal = signal

    const res = await new Promise<http.IncomingMessage>((resolve, reject) => {
      const req = http.request(reqOpts, resolve)
      req.on('error', (err) => reject(this.transportError(err, signal)))
      req.end()
    })
    if ((res.statusCode ?? 0) >= 300) {
      const chunks: Buffer[] = []
      for await (const c of res) chunks.push(c as Buffer)
      throw MaestroError.fromResponse(res.statusCode ?? 0, Buffer.concat(chunks).toString('utf8'))
    }

    let buffered = ''
    let data: string[] = []
    try {
      for await (const chunk of res) {
        buffered += (chunk as Buffer).toString('utf8')
        let nl: number
        while ((nl = buffered.indexOf('\n')) >= 0) {
          const line = buffered.slice(0, nl).replace(/\r$/, '')
          buffered = buffered.slice(nl + 1)
          if (line === '') {
            if (data.length > 0) {
              const text = data.join('\n')
              data = []
              try {
                yield JSON.parse(text) as T
              } catch {
                // malformed event; skip
              }
            }
          } else if (line.startsWith('data:')) {
            data.push(line.slice(5).trim())
          }
        }
      }
    } catch (err) {
      if (signal?.aborted) return
      throw this.transportError(err, signal)
    } finally {
      res.destroy()
    }
  }

  private transportError(err: unknown, signal?: AbortSignal): MaestroError {
    if (err instanceof MaestroError) return err
    const e = err as NodeJS.ErrnoException
    if (signal?.aborted || e.name === 'AbortError' || e.code === 'ABORT_ERR') {
      return MaestroError.of('INTERRUPTED', 'request aborted', { cause: err })
    }
    return MaestroError.of('DAEMON_UNAVAILABLE', `daemon ${this.describe}: ${e.message ?? String(err)}`, { cause: err })
  }
}
