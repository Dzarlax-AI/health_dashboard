# Night sleep coverage contract v1

This is the controlled Health Sync → Health Processing contract required before
the server can treat a night as complete enough for a personal sleep claim.
It does not require Apple Health or a wearable vendor to provide an irreversible
"night closed" flag.

## Payload

The normal `POST /health` payload may include `data.night_sleep_coverage`.
Every item must point at one exact `night_sleep_total` item in the same upload.

```json
{
  "data": {
    "metrics": [{
      "name": "night_sleep_total",
      "units": "hr",
      "data": [{
        "date": "2026-09-10T07:00:00+02:00",
        "source": "Apple Watch",
        "qty": 7.2
      }]
    }],
    "night_sleep_coverage": [{
      "wake_date": "2026-09-10",
      "metric_date": "2026-09-10T07:00:00+02:00",
      "source": "Apple Watch",
      "source_epoch": "initial",
      "capture_completeness": "complete",
      "sync_generation": "sync-42",
      "covered_interval_start": "2026-09-09T20:00:00+02:00",
      "covered_interval_end": "2026-09-10T08:00:00+02:00"
    }]
  }
}
```

All timestamp fields are RFC 3339 with an offset. `wake_date` is the tenant
local date on which the night ended; it is not derived by the server from a
UTC prefix. `metric_date` and `source` must match the raw
`night_sleep_total` point byte-for-byte after JSON decoding.

## Semantics

- `complete` means this adapter read the stated covered interval in one named
  `sync_generation`. It means complete *as of that generation*, not that a
  wearable can never revise historical sleep.
- `partial` is allowed for an explicitly incomplete sync. It is stored but is
  never claim-eligible.
- A payload cannot mark an arbitrary `sleep_total`, daily score, stage, or nap
  as a complete night. The server queries only the named
  `night_sleep_total` point.
- The accepted nightly point, coverage commitment and canonical night are
  updated together. A partial or unattested replay cannot replace a complete
  night. Its original payload is still accepted and retained.
- For a covered night, identical observations are no-ops. The server orders
  queued observations by immutable raw receipt time and ID. It also remembers
  fingerprints of known full versions in the canonical date's diagnostics,
  including a complete replay received again under another ID. A previously
  superseded version from before the guard is not necessarily in that history.
  The adapter's generation identifies a snapshot and does not establish its
  chronology.
- A new, valid complete correction can change the selected duration or wake
  time. When the point key changes, only a demonstrably superseded point is
  removed from derived storage. The immutable raw payload is preserved.
- The adapter must send one unambiguous commitment and point per source and
  wake date. When valid complete candidates exist, partial candidates do not
  participate in selection or conflict detection. The existing source order
  applies within each completeness class. Equal-priority full observations
  within the same epoch can still conflict.

## Serving boundary

At any time before 18:00 on `wake_date`, a complete plausible night can power
only a provisional observation. At or after 18:00, the server finalizer may
make it claim-eligible if the captured value remains plausible. The client must
not use this payload to create an action, a diagnosis, a sleep-debt estimate,
or a recommendation; those remain server-owned.

Legacy payloads without this object remain valid for all existing dashboard
surfaces. They simply cannot create a definitive B0 sleep claim.
