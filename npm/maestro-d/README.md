# maestro-d

Drive a phone, simulator, emulator or browser from JavaScript one Maestro
command at a time — no YAML flow files, no re-launching the driver between
steps.

```js
import { MaestroD } from 'maestro-d'

const m = await MaestroD.attach({ device: '29271FDH200ABP', appId: 'co.edgesecure.app' })
await m.launchApp({ clearState: true })
await m.tapOn('Get Started')
await m.inputText('alice@example.com')
await m.assertVisible('Welcome', { timeout: 10_000 })
const png = await m.screenshot()
```

Every command from the Maestro YAML vocabulary (`tapOn`, `assertVisible`,
`swipe`, `runFlow`, …) is a method with the same name, taking the same value
the YAML would hold. Behind the scenes the first call starts a background
**daemon** (`maestro-d serve`) that keeps the device driver open; every
later call — from this process, a later `node` run, or the `maestro-d`
CLI — is one HTTP request to it. The daemon is
[maestro-runner](https://github.com/devicelab-dev/maestro-runner) with a
server bolted on, so each step runs through the exact same code as a flow
file would.

- **Same daemon, three doors.** The JS library, the `maestro-d` CLI and
  the REST API share a session, so a script can drop to the shell and back.
- **Errors are `MaestroError`s.** A failed step rejects with a code
  (`COMMAND_FAILED`, `DEVICE_ERROR`, …), the CLI exit code, the HTTP status,
  and the full step result. Programming mistakes (wrong argument shapes) are
  `TypeError`s.
- **No runtime dependencies.** Node ≥ 18, CommonJS and ESM, TypeScript
  types for every command's fields.

## Install

```sh
npm install maestro-d
```

The Go binary comes from a per-platform optional dependency
(`maestro-d-darwin-arm64`, `-darwin-x64`, `-linux-arm64`, `-linux-x64`).
If you install with `--no-optional`, or run on another platform, put
`maestro-d` on `PATH` or point `MAESTRO_D_BIN` at it.

The platform package carries the device drivers and is the binary's home
(caches land inside it, as with maestro-runner's own npm package). To share
an existing `~/.maestro-runner/` instead, set
`MAESTRO_RUNNER_HOME=$HOME/.maestro-runner` — or point `MAESTRO_D_BIN`
at `~/.maestro-runner/bin/maestro-d` after `make build`.

Device prerequisites are maestro-runner's: `adb` for Android, Xcode for iOS
(`teamId` for a physical iPhone), a browser for web.

## Concepts

**Daemon.** One background process per *name* (`--name`, `$MAESTRO_D`,
default `default`). A daemon owns one or more attached devices and exits
after 30 minutes idle (configurable) or on `shutdown()`. Different processes
should use different daemon names rather than sharing one; a device can only
be attached to one daemon at a time (`DEVICE_IN_USE` otherwise).

**Device.** A `MaestroDevice` is a handle on a device the daemon has a driver
open on. `attach()` opens the driver (installing the app if `appFile` is
given); `detach()` closes it and leaves the device alone; `stop()` shuts the
simulator/emulator down.

**Step.** Each method call is one step in the device's session, with the same
variables (`${NAME}`), `output` object and JavaScript engine as a flow. Steps
on one device run one at a time; steps on different devices run in parallel.

## API

### `MaestroD`

```ts
static connect(opts?: ConnectOptions): Promise<MaestroD>
static attach(opts?: ConnectOptions & DeviceAttachOptions): Promise<MaestroDevice>
```

`connect()` finds the named daemon, spawning it when it is not running.

| option | meaning |
| --- | --- |
| `name` | daemon name (default `$MAESTRO_D`, else `default`) |
| `spawn: false` | reject with `DAEMON_UNAVAILABLE` instead of starting one |
| `idleTimeout` | seconds idle before the spawned daemon exits (0 = never; default 1800) |
| `http`, `token` | also listen on TCP (`127.0.0.1:7788`) with a bearer token |
| `url`, `token` | connect to an existing TCP listener instead of the unix socket (nothing is spawned) |
| `bin` | path to the binary (else `$MAESTRO_D_BIN`, the platform package, `PATH`) |
| `startTimeout` | ms to wait for the daemon to come up (default 20000) |
| `log` | `(line) => void` for "Starting daemon …" progress |
| `signal` | `AbortSignal` that aborts every request through this handle |

Instance:

| member | |
| --- | --- |
| `name`, `info`, `started` | daemon name, its `daemon.json`, whether this call spawned it |
| `status()` | daemon info plus every device it knows |
| `devices({platform?})` | connected devices, booted simulators/emulators, attached sessions |
| `device(id)` | one device record (`DEVICE_NOT_FOUND`) |
| `startDevice({platform, name, bootTimeout?})` | boot a simulator/AVD by name; the daemon shuts it down on exit |
| `stopDevice(id, {force?})` | shut a device down; devices the daemon did not boot need `force` (`DEVICE_EXTERNAL`) |
| `attach(opts)` | open a driver and return a `MaestroDevice` (below) |
| `detach(id)` | close the driver on a device |
| `events({device?, after?, signal})` | `AsyncIterable<DaemonEvent>` of step/device events (Server-Sent Events) |
| `shutdown({wait?})` | detach everything, stop what the daemon booted, exit |
| `close()` | drop idle connections; the daemon keeps running |

### `daemon.attach(opts)` → `MaestroDevice`

`device` defaults to `$MAESTRO_DEVICE`, then to the only attached device;
otherwise `USAGE`. Attaching to an already-attached device returns a handle
without reconnecting, so a second process can pick up a session.

The rest are maestro-runner's run configuration, camel-cased: `platform`
(inferred from the id when omitted), `driver`, `appFile`, `appId`, `url`,
`teamId`, `wdaBundleId`, `appiumUrl`, `env`, `artifacts`
(`none | on-failure | all`), `noAppInstall`, `noDriverInstall`,
`driverStartTimeout` (s), `waitForIdleTimeout`, `conditionTimeout`,
`stepDelay`, `commandTimeout` (ms), `typingFrequency`, and `extra` for
driver-specific keys (`headed`, `browser`, `windowSize`, `userDataDir`, …).

### `MaestroDevice`

#### Command methods

One per YAML command, typed from the daemon's command table
(`COMMANDS`). The signature is

```ts
tapOn(value?: string | TapOnParams, opts?: TapOnParams & CallOptions): Promise<StepResult>
```

- A **bare value** is the YAML shorthand: `tapOn('Login')` is `- tapOn: Login`,
  `swipe('UP')`, `setDarkMode(true)`. Commands with no shorthand
  (`setLocation`, `assertCondition`, …) take an object.
- An **object** is the YAML map: `tapOn({ id: 'submit', index: 1 })`.
- **`opts`** adds fields to either form — `tapOn('Login', { optional: true })`
  is `{ text: 'Login', optional: true }` — plus the per-call
  `wait`, `cwd` and `signal` (see below). Every command accepts `optional`,
  `label`, `timeout` and `platform` (skip the step on other platforms).
- **Value-less** commands take nothing: `back()`, `hideKeyboard()`,
  `waitForAnimationToEnd()`.
- Compound commands take inline steps: `repeat({ times: 3, commands: [{ tapOn: 'Next' }] })`,
  `runFlow({ file: 'login.yaml', env: { USER: 'alice' } })`,
  `retry({ maxRetries: '2', commands: [...] })`.

Wrong shapes throw `TypeError` before anything is sent. `command(name, value?, opts?)`
is the untyped form for commands the library does not know yet.

#### Batches

| | |
| --- | --- |
| `run(steps, {continueOnError?})` | run a list of step objects (`[{ tapOn: 'A' }, 'back']`) in one request; stops at the first failure unless `continueOnError`; rejects with the partial `StepsResult` in `error.result` |
| `runYaml(yaml, opts)` | the same for a YAML list of steps |
| `flow({file \| yaml, env?})` | run a whole flow file, header included, as one compound step |

#### Inspection (not recorded as steps)

`screenshot()` → PNG `Buffer`; `hierarchy()` → the normalized tree
(`HierarchyNode`: `type`, `id`, `text`, `bounds`, `children`, the same on
every platform) / `hierarchyRaw()` → the driver's own XML/JSON (`Buffer`); `state()`; `platformInfo()`; `vars()` / `setVars({NAME: value})`;
`eval(script)` runs JavaScript in the session's engine; `refresh()` re-reads
the device record; `events({after?, signal})` streams this device's events.

#### Lifecycle

`detach()` closes the driver; `stop({force?})` also shuts the device down.

#### `CallOptions`

| option | |
| --- | --- |
| `wait: false` | return `BUSY` instead of queueing behind a step already running on this device |
| `cwd` | directory relative paths in the step resolve against (default `process.cwd()`) |
| `signal` | `AbortSignal`; the step is cancelled on the device and the call rejects with `INTERRUPTED` |

### Results

Every successful call resolves with the daemon's JSON envelope; for a step:

```ts
interface StepResult {
  ok: true
  device: string
  step: string        // "tapOn: text=\"Login\""
  type: string        // "tapOn"
  durationMs: number
  message?: string
  skipped?: boolean   // platform did not match
  optional?: boolean
  error?: ErrorBody   // a failed *optional* step resolves with ok:true and this set
  element?: ElementInfo
  data?: unknown      // copyTextFrom text, extractTextWithAI, …
  artifacts: { screenshotBefore?, screenshotAfter?, viewHierarchy? }
  subSteps?: SubStep[]
  reportDir: string   // where artifacts for this session live
}
```

### `MaestroError`

Every failure the daemon reports rejects with a `MaestroError`:

```ts
class MaestroError extends Error {
  code: ErrorCode        // 'COMMAND_FAILED' | 'USAGE' | …
  exitCode: number       // what the CLI exits with
  httpStatus: number     // what the REST API answers with
  reason: string         // the daemon's message, without the step prefix
  step?: string          // the step description, for step failures
  details?: Record<string, unknown>  // reportDir, screenshot, hierarchy, errorType, …
  result?: StepResult | StepsResult  // the full response for a failed step or batch
  toJSON(): { ok: false, exitCode, error: { code, message, step?, details? } }
}
```

| code | exit | HTTP | meaning |
| --- | --- | --- | --- |
| `COMMAND_FAILED` | 1 | 422 | the step ran and failed (element not found, assertion false, …) |
| `USAGE` | 2 | 400 | bad command, field or argument |
| `DEVICE_NOT_ATTACHED` | 2 | 404 | attach first |
| `DAEMON_UNAVAILABLE` | 3 | 503 | no daemon, not answering, shutting down |
| `DEVICE_ERROR` | 4 | 502 | the driver failed (device disconnected, driver crashed) |
| `DEVICE_NOT_FOUND` | 4 | 404 | no such device id |
| `INTERNAL` | 4 | 500 | daemon bug |
| `BUSY` | 5 | 409 | `wait: false` and the device is running a step |
| `DEVICE_IN_USE` | 5 | 409 | another daemon has the device |
| `DEVICE_EXTERNAL` | 5 | 409 | `stopDevice` on a device this daemon did not boot |
| `INTERRUPTED` | 130 | 499 | the `signal` fired |

`isMaestroError(err, code?)` narrows in a `catch`. `EXIT_CODES` and
`HTTP_STATUS` export the table.

### Environment

| variable | |
| --- | --- |
| `MAESTRO_D` | default daemon name |
| `MAESTRO_DEVICE` | default device id for `attach()` |
| `MAESTRO_D_BIN` | path to the Go binary |
| `MAESTRO_D_HOME` | run-file root (default `~/.maestro-d`); sockets, logs and reports live under `run/<name>/` |

## Recipes

**Two devices in one script.** Steps on different devices run in parallel.

```js
const d = await MaestroD.connect({ name: 'pair-test' })
const [android, ios] = await Promise.all([
  d.attach({ device: '29271FDH200ABP', appId: 'co.edgesecure.app' }),
  d.attach({ device: '00008110-0006098A02FA801E', appId: 'co.edgesecure.app', teamId: 'G5LQ7MERPK' }),
])
await Promise.all([android.launchApp(), ios.launchApp()])
```

**Handle a failing step.**

```js
try {
  await m.assertVisible('Balance')
} catch (err) {
  if (isMaestroError(err, 'COMMAND_FAILED')) {
    console.error(err.message, err.details.screenshot)
    process.exit(err.exitCode)
  }
  throw err
}
```

**Time out a step.**

```js
await m.extendedWaitUntil({ visible: 'Done', timeout: 60_000 }, { signal: AbortSignal.timeout(90_000) })
```

**Watch events while a batch runs.**

```js
const ac = new AbortController()
;(async () => {
  for await (const ev of m.events({ signal: ac.signal })) console.log(ev.type, ev.step, ev.passed)
})()
await m.run(steps, { continueOnError: true })
ac.abort()
```

**Simulator lifecycle.**

```js
const sim = await d.startDevice({ platform: 'ios', name: 'iPhone 16' })
const m = await d.attach({ device: sim.id, appFile: 'build/Edge.app' })
// …
await d.shutdown() // also shuts the simulator down, since the daemon booted it
```

## CLI

The package also installs the `maestro-d` command:

```sh
npx maestro-d launchApp co.edgesecure.app --device 29271FDH200ABP
npx maestro-d tapOn Login
npx maestro-d commands           # every one-shot command
npx maestro-d tapOn --help       # a command's fields
npx maestro-d status
npx maestro-d stop
```

See the [maestro-d repository](https://github.com/EdgeApp/maestro-d)
for the CLI and REST documentation.

## Development

```sh
npm install
npm test        # builds, then runs test/ against a daemon driving the mock driver
```

The tests build the Go binary from the repository root; set
`MAESTRO_D_TEST_BIN` to use one you already built.

## License

Apache-2.0, like maestro-runner.
