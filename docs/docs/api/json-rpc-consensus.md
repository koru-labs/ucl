To enable the consensus JSON-RPC namespace, start the node with:

```
--enable-consensus-endpoints
```

The flag is disabled by default. The JSON-RPC listener is unauthenticated by default; enabling this exposes validator identities and in-flight vote sets. Bind it to a trusted interface.

## consensus_state

Returns this node's view of IBFT consensus, including:

1. **`current`** — live state for the height being decided right now
2. **`current.phaseSnapshots`** — frozen end-state of every completed phase (plus a lightweight `in_progress` marker for the active phase)
3. **`lastFinalized`** — retained archive of the previous completed/cancelled height, including its full phase history

You do **not** need to poll mid-phase to reconstruct history: each phase transition persists its final vote/quorum/proposal snapshot in memory.

The live read path is non-blocking. When a section cannot be acquired without waiting, it is marked unavailable and listed in `unavailableSections` (`complete=false`).

### Parameters

None.

### Encoding conventions

| Kind | Format |
|------|--------|
| Timestamps | RFC3339Nano UTC strings |
| Durations | Integer milliseconds |
| Addresses / IDs / hashes / signatures | `0x`-prefixed lowercase hex |
| Voting powers | Decimal integer strings |

### Example request

````bash
curl http://127.0.0.1:10002 -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"consensus_state","params":[],"id":1}'
````

### Example response shape

```json
{
  "capturedAt": "2026-08-04T11:43:36.492465Z",
  "complete": true,
  "nodeId": "0x8b35…",
  "current": {
    "status": "running",
    "height": 23,
    "round": 0,
    "phase": "prepare",
    "proposal": { "available": true, "hash": "0x…", "blockNumber": 23 },
    "proposer": "0xb37a…",
    "isProposer": false,
    "validators": [ { "id": "0x…", "votingPower": "1" } ],
    "quorumSize": "3",
    "quorum": { "prepare": { "available": true, "count": 2, "proposerImplied": true, "receivedPower": "3", "requiredPower": "3", "hasQuorum": true } },
    "messages": { "prepare": { "available": true, "viewHeight": 23, "viewRound": 0, "count": 2, "messages": [] } },
    "phaseSnapshots": [
      {
        "phase": "new_round",
        "status": "completed",
        "height": 23,
        "round": 0,
        "startedAt": "…",
        "endedAt": "…",
        "durationMs": 42,
        "quorum": { "preprepare": { "available": true, "count": 1, "hasQuorum": true, "receivedPower": "1", "requiredPower": "1" } },
        "messages": { "preprepare": { "available": true, "viewHeight": 23, "viewRound": 0, "count": 1 } }
      },
      {
        "phase": "prepare",
        "status": "in_progress",
        "height": 23,
        "round": 0,
        "startedAt": "…",
        "durationMs": 15
      }
    ],
    "roundHistory": []
  },
  "lastFinalized": {
    "status": "completed",
    "height": 22,
    "round": 0,
    "phase": "fin",
    "lastRoundEndReason": "committed",
    "sequenceStartedAt": "…",
    "sequenceEndedAt": "…",
    "proposal": { "available": true, "blockNumber": 22, "blockHash": "0x…", "txCount": 3 },
    "phaseSnapshots": [
      { "phase": "new_round", "status": "completed", "round": 0, "durationMs": 10 },
      { "phase": "prepare", "status": "completed", "round": 0, "durationMs": 20 },
      { "phase": "commit", "status": "completed", "round": 0, "durationMs": 18 },
      { "phase": "fin", "status": "completed", "round": 0, "durationMs": 1 }
    ],
    "committedSeals": [ { "signer": "0x…", "signature": "0x…" } ]
  }
}
```

### Top-level fields

| Field | Type | Description |
|-------|------|-------------|
| `capturedAt` | string | When this RPC response was assembled |
| `complete` | bool | All live sections acquired without contention/truncation |
| `unavailableSections` | string[] | Busy/truncated sections (e.g. `state`, `messages.prepare`, `archive`) |
| `nodeId` | string | This node's validator ID |
| `current` | object | Live height being decided (see Height state) |
| `lastFinalized` | object | Retained archive of the previous finished height (omitted until one exists) |

### Height state (`current` / `lastFinalized`)

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | `inactive` \| `running` \| `completed` \| `cancelled` |
| `height` / `round` / `phase` | | IBFT view and phase (`new_round`, `prepare`, `commit`, `fin`) |
| `roundStarted` | bool | Whether the round worker has started |
| `lastRoundEndReason` | string | `timeout`, `future_proposal`, `round_change_certificate`, `committed`, `cancelled`, `unknown` |
| `sequenceStartedAt` / `sequenceEndedAt` | string | Sequence bounds (`endedAt` mainly on finalized) |
| `roundStartedAt` / `phaseStartedAt` | string | Current round/phase start |
| `roundTimeoutMs` / `roundDeadline` / `roundRemainingMs` | | Live round timer fields (`current` only) |
| `phaseElapsedMs` / `sequenceElapsedMs` / `roundElapsedMs` | | Live elapsed times (`current` only) |
| `completedPhaseDurationsMs` | object | Finished phase durations in the **current round** |
| `roundHistory` | array | Completed rounds for this height |
| `phaseSnapshots` | array | **Frozen per-phase end states** (see below) |
| `proposal` | object | Accepted proposal + decoded block metadata when available |
| `proposer` / `isProposer` | | Expected proposer for the height/round |
| `validators` / `totalVotingPower` / `quorumSize` | | Validator set and quorum threshold |
| `quorum` / `messages` | object | Live vote inventory for the current view |
| `latestPreparedCertificate` | object | Latest PC once prepare quorum was reached |
| `committedSeals` | array | Seals used/about to be used for insertion |

### `phaseSnapshots[]` (the important part)

Each entry is captured when a phase ends. On read, a lightweight `in_progress` marker is appended for the active phase (identity/timing/proposal only — live votes stay on `current.quorum` / `current.messages`).

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `new_round` \| `prepare` \| `commit` \| `fin` |
| `status` | string | `completed` (frozen at transition) or `in_progress` (live marker) |
| `height` / `round` | number | View when the phase ran |
| `startedAt` / `endedAt` | string | Phase wall-clock bounds (`endedAt` omitted while in progress) |
| `durationMs` | number | Phase duration |
| `proposer` | string | Proposer known at that moment |
| `proposal` | object | Proposal accepted by then (if any) |
| `quorum` | object | Quorum progress **as of phase end** (omitted on `in_progress`) |
| `messages` | object | Accepted messages **as of phase end** (voter/hash only in archives; no signature bytes) |
| `latestPreparedCertificate` | object | PC if present at that moment |

Retention:
- Current height keeps phase snapshots in memory (capped)
- When a height completes/cancels, the whole height archive is copied to `lastFinalized`
- Starting the next height begins a fresh `current` archive; `lastFinalized` remains until replaced by the next completion

### Proposal object

| Field | Type | Description |
|-------|------|-------------|
| `available` | bool | PREPREPARE accepted |
| `hash` / `round` / `rawSize` | | Proposal identity/size |
| `blockHash` / `blockNumber` / `txCount` / `gasLimit` / `gasUsed` / `timestamp` | | Decoded classic IBFT block fields |
| `decodeError` | string | Present if RLP decode failed |

### Quorum / messages objects

Same shape as before:

- `quorum.<type>`: `available`, `count`, `proposerImplied`, `receivedPower`, `requiredPower`, `hasQuorum`. `count` is the number of accepted messages; for `prepare` the proposer's PREPREPARE counts towards the quorum without a PREPARE message, so when `proposerImplied` is true `receivedPower` includes one validator more than `count`.
- `messages.<type>`: `available`, `truncated`, `viewHeight`, `viewRound`, `count`, `messages[]`

Keys: `preprepare`, `prepare`, `commit`, `round_change`.

Prepare quorum includes the proposer's voting power.

### How to use this without precise timing

| Goal | Where to look |
|------|----------------|
| Did the last block finish cleanly? | `lastFinalized.status`, `lastRoundEndReason`, `phaseSnapshots` |
| How long did prepare/commit take on the last block? | `lastFinalized.phaseSnapshots[].durationMs` |
| Who voted in each phase of the last block? | `lastFinalized.phaseSnapshots[].messages` |
| What is happening right now? | `current.phase`, `current.quorum`, `current.messages` |
| Did we timeout / round-change? | `current.roundHistory` / `lastRoundEndReason` |

### Errors

| Condition | Result |
|-----------|--------|
| Flag disabled | Method not found (`consensus_state` not registered) |
| Non-IBFT engine | JSON-RPC error: not supported |
| IBFT not initialized | JSON-RPC error: not initialized |

## Pushing snapshots to Consensus Health

UCL can POST the same snapshot JSON to an external collector (`con_health_backend`) whenever IBFT diagnostics change, without enabling the JSON-RPC method.

### Configuration

| Flag / YAML key | Default | Description |
|-----------------|---------|-------------|
| `--consensus-state-push-url` / `consensus_state_push_url` | empty | HTTP(S) base URL or full `/api/v1/snapshots` path. **Empty disables the pusher.** |
| `--consensus-state-push-token` / `consensus_state_push_token` / `CONSENSUS_STATE_PUSH_TOKEN` env | empty | Bearer token required by the ingest API (required when URL is set). The env var is used when the flag/key is empty and is the recommended way to supply it, since it keeps the secret out of `ps` and shell history. |
| `--consensus-state-push-interval` / `consensus_state_push_interval` | `30s` | Recovery heartbeat / maximum idle time between pushes (must be > 0 when URL is set) |

Example YAML:

```yaml
consensus_state_push_url: "http://127.0.0.1:8080"
consensus_state_push_token: "dev-ingest-token-change-me"
consensus_state_push_interval: 30s
```

Example CLI:

```bash
polygon-edge server \
  --consensus-state-push-url http://127.0.0.1:8080 \
  --consensus-state-push-token dev-ingest-token-change-me \
  --consensus-state-push-interval 30s
```

### Behavior

- Starts after consensus starts; stops on server shutdown.
- Pushes after accepted messages and phase, round, height, or finalization changes.
- Coalesces bursts into a capacity-one wakeup signal; notification never waits for the HTTP consumer.
- Uses the configured interval as a recovery heartbeat when no change event arrives.
- Event-triggered pushes are skipped when nothing but clock-derived fields (`capturedAt`, `*ElapsedMs`, `roundRemainingMs`, `roundDeadline`, the in-progress phase's `durationMs`) changed since the last successful push; heartbeat pushes are always sent.
- Captures via the non-blocking `ConsensusStateProvider` (IBFT only).
- POSTs to `{url}/api/v1/snapshots` with `Authorization: Bearer <token>` and `Content-Type: application/json`.
- Failed captures / HTTP errors are logged and skipped; consensus is never blocked.
- Independent of `--enable-consensus-endpoints`.
- Accepts and pushes `complete: false` partial snapshots.

### Security

- Treat the push token like a secret; prefer `CONSENSUS_STATE_PUSH_TOKEN` over the flag; rotate by updating node config + backend `INGEST_TOKEN` and restarting.
- Snapshot payloads include validator IDs, vote inventories, and (on live messages) signatures — keep the health API on a trusted network / VPN.
- Dashboard read APIs are unauthenticated by design for trusted deployments.
