# Replace liercd's IRC client with irc-go

## Why

`pkg/lierc` is a hand-written IRC client from early in the project: its own
line parser, socket loops, CAP/SASL negotiation and keepalive. It has parsing
bugs (no tag unescaping, splitting on single spaces, `line[0]` on an empty
remainder) and handlers that index `m.Params[n]` without checking the length,
so a short or odd line from any server can panic liercd and drop every user's
connection. The goal is reliability and idiomatic Go, by replacing as much as
possible with [ergochat/irc-go](https://github.com/ergochat/irc-go) (v0.7.0),
a maintained library used by the Ergo IRC server.

## Goals

- `ircevent.Connection` owns the connection: dialing, TLS, line reading
  (`ircreader`), parsing (`ircmsg`), CAP negotiation, SASL PLAIN, PASS,
  registration, nick-in-use retries, PING/PONG and timeouts.
- Our code keeps only what irc-go doesn't do: channel/nick/mode/topic state,
  the reconnect backoff, the send rate limit, the private-address guard, and
  turning messages into liercd's events.
- No handler can panic on a short message: every parameter access is
  length-checked. (irc-go also recovers panics in callbacks; that is a
  backstop, not the plan.)
- `ParseIRCMessage` is replaced by `ircmsg.ParseLine` everywhere, including the
  synthetic lines `pkg/liercd/manager.go` builds.

## Non-goals

- No change to what liercd sends to other services. These are contracts with
  the logger, notifier, lierc-api (Perl and Go) and the UI, and stay the same
  shape:
  - `IRCMessage` JSON: `Prefix{Name,User,Server,Self}`, `Tags`, `Command`,
    `Raw`, `Time` (local receive time, float seconds), `Params`, `Direct`.
  - `IRCClientData` JSON from `/<id>/status`: `Id`, `Config`, `Nick`,
    `Channels[{Name,Nicks (with prefix),Topic,Mode}]`, `Registered`, `Status`,
    `Isupport` (005 tokens: lierc-api replays them as a 005),
    `CapsAcked`. The one exception: `Caps` (advertised caps) is dropped,
    because nothing reads it (see Incoming messages).
  - The `Events`, `Multi`, `Status`, `ConfigChanges` channels, the synthetic
    `CONNECT`/`DISCONNECT`/`CREATE`/`DELETE` events, and the status messages
    ("Connecting", "Connected", "Reconnecting in 45s", "Closing connection").
  - The command API (`create`, `destroy`, `raw`, `status`, `portmap`).
- No change to the logger, notifier or identd beyond what compiles against a
  changed type, if anything.
- No case-mapping change: channel and nick keys stay `strings.ToLower`.
- `lierc-ngui` is not touched.

## Anti-goals

- Changing the message format "while we're here". Anything that makes a
  consumer in another repo change is out of scope.
- A wrapper that re-implements what irc-go already does (CAP state machine,
  SASL, ping timers). If irc-go does it, use irc-go's.

## Approaches considered

- **A. irc-go for the whole connection layer (chosen).** `ircevent` replaces
  `connection.go` and the protocol parts of `client.go`/`events.go`; we keep
  channel state as callbacks and convert at the edge. Removes the most code
  and the most panics. Costs: our own reconnect driver and send limiter.
- **B. irc-go for parsing only.** `ircmsg`/`ircreader`/`ircutils`, with our
  own rewritten connection, CAP and SASL code. Keeps reconnect behaviour
  untouched but keeps the protocol state machines we want to be rid of.
- **C. A, plus switching the wire format to irc-go's.** Ripples through four
  repos for no reliability gain. Ruled out.

The user picked A and chose to keep the growing reconnect backoff.

## Design

### Package layout

Stay in `pkg/lierc` so imports don't move. After the change:

- `message.go`: `IRCMessage`/`IRCPrefix` types unchanged, plus
  `newIRCMessage(raw string, m ircmsg.Message) *IRCMessage`, which fills
  `Prefix` from `m.Nick()`/`ircmsg.ParseNUH(m.Source)`, copies tags (now
  unescaped values), `Command`, `Params`, `Raw` and `Time`. A
  `ParseIRCMessage(line string) (*IRCMessage, error)` helper on top of
  `ircmsg.ParseLine` stays for `manager.go`'s synthetic lines (note the
  return order is fixed to the idiomatic `(value, error)`; update callers).
- `connection.go`: deleted.
- `client.go`: `IRCClient` wraps an `*ircevent.Connection`; reconnect driver,
  send limiter, `ClientData`, config-change helpers.
- `events.go`: our handlers, rewritten as functions of `ircmsg.Message`.
- `dialguard.go`: unchanged; `guardedDialer(exempt).DialContext` becomes the
  connection's `DialContext`.
- `channel.go`, `config.go`: unchanged apart from anything that falls out.

### Building a connection

For each attempt, a fresh `ircevent.Connection` from the config:

| Field | Value |
|---|---|
| `Server` | `config.Server()` |
| `UseTLS` / `TLSConfig` | `config.Ssl` / `&tls.Config{InsecureSkipVerify: true}` (kept from today: users connect to servers with self-signed certs) |
| `Nick` | the client's current preferred nick (`config.Nick` at first) |
| `User`, `RealName` | `config.User`, falling back to `config.Nick` (today's USER sends the user as realname too) |
| `Password` | `config.Pass` when not SASL |
| `UseSASL`, `SASLLogin`, `SASLPassword`, `SASLMech` | when `config.SASL`: `config.User`, `config.Pass`, `PLAIN`. SASL failure stays fatal for the attempt (irc-go's default), as today |
| `RequestCaps` | `echo-message`, `message-tags` |
| `KeepAlive` / `Timeout` | 4m / 2m. In irc-go `Timeout` bounds dial + TLS + all of registration and the wait for each PONG, so 2m (not today's 1m write deadline) tolerates a lagging server; a dead connection is still noticed within about 6m |
| `DialContext` | the guarded dialer |
| `MaxLineLen` | left at irc-go's default (512). The read buffer is then capped at 8704 bytes (512 body + 8192 tags), which fits spec-compliant servers; a longer line is fatal and is logged as `readQ exceeded`. Raise it only if a real server needs it (decided by the user) |
| `Log` | a logger prefixed with the connection id; `Debug` when `LIERC_DEBUG` > 1 |

irc-go's own reconnect loop must never run (we drive reconnects, below).

### Incoming messages

One `AddRawCallback` sees every line, after irc-go's own handlers have run
(so `CurrentNick()` is already updated). For a line that parsed:

1. Lock the client, run our handler for the command if there is one, unlock.
2. Build the `IRCMessage` from the raw line and parsed message, set `Direct`
   and `Prefix.Self` as today, and send it on `Events` (or `Multi` for
   QUIT/NICK, as today).

Lines that fail to parse are logged and dropped, as today.

Handlers keep today's behaviour for JOIN, PART, KICK, QUIT, NICK, MODE,
TOPIC, 324, 332, 333, 353, 366, and the config-change publishing (channels
on join/part/kick; nick only when it's the nick we asked for). Each checks
`len(m.Params)` before indexing. Handlers for things irc-go now owns go away:
001/376/422 registration, 433/437, PING, CAP, AUTHENTICATE, 90x, 421.

State that irc-go tracks is read from it rather than duplicated where the
output shape allows:

- Current nick: `CurrentNick()`.
- PREFIX: read from irc-go's `ISupport()["PREFIX"]` when registration
  completes (376/422; our raw callback runs after irc-go has filled the
  map), and parsed with a small bounds-checked function (irc-go doesn't
  parse the value). We join only after registration, so every NAMES reply
  sees it. irc-go ignores 005 after registration, so a server changing
  PREFIX mid-session isn't picked up; accepted. Default `(ov)@+` plus `%h`
  as today until then and when the token is missing or malformed.
- `CapsAcked`: from `AcknowledgedCaps()`.
- `Isupport`: built from irc-go's `ISupport()` map when the status JSON is
  produced, not tracked by us (decided by the user). Each entry becomes
  `KEY` (empty value) or `KEY=value`, re-escaping backslash, space and `=`
  as `\x5C`, `\x20`, `\x3D` to undo irc-go's decoding. Commas are left
  alone: irc-go also decodes `\x2C`, which can't be told apart from a
  literal list comma, so a server-sent `\x2C` comes back as `,`. The UI
  reads only PREFIX and CHANTYPES from lierc-api's replayed 005 and `lctl`
  only NETWORK, so token order doesn't matter. The map is irc-go's own (not
  a copy) and is never changed after registration: read it, never write it.
  It's nil before registration, when lierc-api doesn't replay anyway.
- `Caps` (advertised caps): dropped from the status JSON, with all of our
  CAP tracking (decided by the user). irc-go keeps advertised caps privately
  with no getter, and nothing reads the field: lierc-api's Go status struct
  has no `Caps` field and no Perl or UI code reads it.
- `Registered`: set when 001, 376 or 422 arrives (our own handler; irc-go's
  connect callback is 376/422 only), which also resets the retry count and
  joins `config.Channels`, as `Welcome` does today.
- `Nick` in the status JSON: `CurrentNick()`, falling back to the preferred
  nick before 001, when `CurrentNick()` is empty.

### Sending

`IRCClient.Send(line)` goes through a `rate.Limiter(1, 4)` (today's limit)
and then `SendRaw`, without blocking the caller: the API's HTTP handler and
the raw callback must not stall on the limiter. A small per-client send
goroutine fed by a bounded queue is the expected shape; the builder picks the
details. irc-go's internal sends (PONG, CAP, NICK retries) bypass the limiter,
which is fine.

A raw `NICK <nick>` from the API must call `conn.SetNick(nick)` instead of
sending the line, because irc-go periodically tries to regain its preferred
nick and would otherwise change back. `RequestNick` stays, so the config is
updated only when the server confirms.

Nick in use at registration is irc-go's behaviour, unmodified (decided by
the user, to keep less of our own code): it tries `nick_0`, `nick_1`, …, and
after registering under a fallback it asks for the configured nick again on
each keepalive tick (every 4m) until it gets it.

Nicks asked for later are not retried (decided by the user), so irc-go
doesn't repeat a refused request forever or fight services:

- On 432/433/437 after registration, or a NICK the server applies to us that
  we didn't ask for (services renames), set irc-go's preferred nick back to
  `CurrentNick()` and clear `requestedNick`, so irc-go doesn't keep asking.
- Each new connection starts with `config.Nick`, which only changes when the
  server confirms a nick we asked for.

The manager's "echo the PRIVMSG/NOTICE locally when echo-message isn't
acked" path stays, reading acked caps from irc-go.

### Reconnects and status

Keep today's behaviour exactly: on any failure or disconnect, status
"Reconnecting in Ns" with N = min(15 × retries, 300), retries reset on
registration.

- Each attempt uses a fresh `ircevent.Connection`. `Connect()` returning an
  error (dial refused, private address, TLS, SASL failure) is a failed
  attempt: publish a disconnected status with the error and schedule the next.
- An established connection reports its end through `AddDisconnectCallback`.
  The client then `Quit()`s that connection object so irc-go's own
  reconnect loop never starts, and schedules the next attempt itself.
  (`Reconnect()` on a long `ReconnectFreq` was considered and rejected:
  irc-go doesn't report a failed in-loop reconnect, so we couldn't back off.)
- `Status` messages and the `CONNECT`/`DISCONNECT` events are produced at
  the same points as today: "Connected" when the socket is up, the error text
  on failure, "Reconnecting in …" when scheduling. `Connect()` blocks until
  376/422, so "Connected" is published from the dial wrapper once the socket
  (and TLS) is up; `TestDestroyWhenServerIgnoresQuit` depends on this.
- The client closes the `net.Conn` it kept when an attempt fails or ends;
  without `Loop()` nothing else is guaranteed to.
- `Destroy()`: status "Closing connection", stop any pending reconnect timer,
  `Quit()` with "bye", and wait for the connection to stop, bounded by the
  same 2-second timeout as today (the existing
  `TestDestroyWhenServerIgnoresQuit` must still pass).

### Port map (identd)

`PortMap()` needs the socket's local and remote ports, which irc-go doesn't
expose. Wrap the guarded dialer so the client keeps the `net.Conn` it last
dialed (under the client's lock), and read the addresses from that.

### Concurrency

irc-go runs callbacks serially on its read goroutine. The client's existing
`RWMutex` guards our state; the `status` handler reads under `RLock` as
today. Callbacks must not hold the lock while sending on the global channels
(today's code does; fixing that is in scope since a slow consumer would
block the HTTP status handler too). Connection replacement on reconnect
happens under the lock.

## Testing

- Keep all existing tests passing, adjusted only for signature changes
  (`ParseIRCMessage`'s return order).
- Tests drive the client against a local fake server on a loopback listener
  (allowed via `Exempt`), as `client_test.go` does today. Cover at least:
  registration and channel join; SASL success and failure; nick in use at
  registration falling back to `nick_0` and regaining `nick` on a later
  keepalive tick once it's free; a raw NICK from the API updating the config only on
  confirmation and not being reverted; a refused NICK and a server rename
  not being retried after the next keepalive tick; disconnect → "Reconnecting in 15s" →
  reconnect → retries reset; `Destroy` with a server that ignores QUIT.
- A table test that feeds every handled command with zero, one and too few
  params and asserts no panic and no state change.
- `Isupport` round trip: raw 005 tokens (including `NETWORK=Foo\x20Bar`,
  a value-less token and `CHANLIMIT=#:120,&:50`) → irc-go's map → our
  tokens, equal as a set.
- `IRCMessage` JSON for a set of representative lines (tags, no prefix,
  trailing with colons, server prefix, NUH prefix) compared against
  golden output from today's parser, with deliberate differences (tag
  unescaping, runs of spaces) listed in the test.
- `go vet` and `go test -race ./...` clean.

## Behaviour changes to expect

- `Isupport` in the status JSON is in map order, ignores a 005 sent after
  registration, and turns a server's `\x2C` into a plain comma.
- Tag values in `IRCMessage.Tags` are unescaped (`\s` → space, etc.). Correct
  per the spec; today they're stored escaped.
- Lines with runs of spaces parse correctly instead of producing empty params.
- Nick in use during registration becomes `nick_0`, `nick_1`, … instead of
  `nick_`, `nick__`, and liercd now regains the configured nick when it
  frees up (a NICK attempt every 4 minutes until then; today it stays on
  `nick_` until the user changes it).
- A server that lags more than 2 minutes, or takes longer than 2 minutes
  to register, now disconnects (with backoff) where today it wouldn't.
- An incoming line over 8704 bytes with no newline drops the connection
  (today there is no limit). Outgoing lines from the API are unaffected:
  `SendRaw` doesn't check length, so they're sent as today.
- liercd pings every 4 minutes even when the connection is busy.
- USER's middle params become irc-go's (`s e`) instead of hostname and server.
- `ircmsg` uppercases commands and rejects tag values that aren't valid
  UTF-8; both go on the golden test's list of deliberate differences.

## Left to the builder

- Exact shape of the send queue and its size, and what happens when it's full
  (log and drop, matching the publisher's behaviour, is a reasonable call).
- Whether `newIRCMessage` and the handlers live in `message.go`/`events.go`
  or new files.
- How the fake server for tests is structured.

## Before starting

The working tree has uncommitted work (config-change publishing in
`client.go`, `events.go`, `manager.go`, logger store and tests). Commit it
first, then do this on a branch.
