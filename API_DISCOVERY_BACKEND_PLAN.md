# API Discovery — Backend Guide (collector sync)

The collector (`elchi-collector`) is **write-only**; all read paths, cleanup and
analytics live here. This guide lists ONLY the work that is still **missing** —
items already implemented in this repo are listed at the bottom as "no action".
Every task below is feasible with data already in `api_events_raw` (ClickHouse)
and `api_inventory` (Mongo); no collector change is required.

## What changed in the collector this cycle (why these tasks exist)

1. **`confirmed` is now ROUTE-MATCH based, status-independent.** A request that
   matched a configured Envoy route (`route_name`/`upstream_cluster` set and NOT
   the `NR`/`no_route_found` flag) is confirmed regardless of status — a 4xx/5xx
   from a real backend is a real protected/broken endpoint, not attack surface.
   `no_route_found` → never confirmed (genuine probe). Scanner flags +
   static-asset/SPA content types still force unconfirmed. Sticky via `$max`
   (false→true) → self-heals on next traffic.
2. **`error_status` / `client_error_status` moved THREAT → POSTURE axis.** The
   collector now adds their severity to `posture_score`, not `risk_score`.
   (Informational: the read-side `riskFlagTaxonomy` is a display-only class
   lookup — no change needed there; `SecurityScore`/`RiskSummary` already use the
   pre-split `max_risk_score`/`max_posture_score`.)
3. **`risk_score` is now MAX-ANCHORED** (`max(sev)+Σ(others)/4`, clamp 255), not a
   naive sum — a pile of mediums no longer outranks one critical. A lone flag
   still scores its own severity.
4. **New PII categories** `secret_in_path` / `jwt_in_path` (credential/JWT leaked
   in the URL path) — flow into `pii_categories` automatically; `PIIInventory`
   already shows them. No action (optional: a "leaked credentials" highlight).
5. **Raw sampling** (`api_collector_config.raw_sample_rate >= 2`) biases the
   ClickHouse rollups (`events_count`/`unique_consumers`/`error_rate`/percentiles).
   Mongo inventory is exact. Default off.
6. **`posture_score` is now a ClickHouse RAW column** (migration `007`), the
   EXPOSURE-axis counterpart to `risk_score`. So `current-posture` can now cover
   BOTH axes: `current_max_risk` from the rollup (`maxMerge(max_risk_score)`,
   sampling-safe) and `current_posture` from raw (`max(posture_score)` over the
   window — posture is ambient so the windowed max is accurate even under
   sampling; raw-only, not rolled up). Available once collectors ship the
   migration + redeploy. Phase 1's `posture_current_available:false` flips true
   with a one-line add to the query.

---

## 1. (P0) Bulk re-baseline of sticky scores — `max_risk_score` + `max_posture_score`

**Why.** Changes (2) and (3) make the collector emit different (mostly lower for
multi-flag) per-event scores, but inventory `max_*_score` are sticky `$max` — they
stay **stale-HIGH** and never self-lower. Existing endpoints keep showing
inflated/old danger (the classic "score was 26, should be 10" symptom). The
collector cannot fix this (`$max`).

**What.**
- Add a bulk admin operation (project-scoped, Admin/Owner) that resets
  `max_risk_score` AND `max_posture_score` to 0 across a project; the collector
  re-accumulates correct values on the next event. Mirror `CleanupStaleInventory`
  (`controller/handlers/inventory_cleanup.go:192`) but `UpdateMany` + `$set`
  instead of `DeleteMany`.
- **Bug to fix while here:** `inventoryResetFields`
  (`controller/handlers/inventory_cleanup.go:115`) zeros `max_risk_score` but
  **omits `max_posture_score`** — add it, so per-item `ResetInventoryItem` also
  clears the posture axis.
- Better than reset-to-0 (avoids "shows 0 until next traffic" for low-traffic
  endpoints): recompute per endpoint from ClickHouse last-N-days and `$set` the
  computed scores. Optional; reset-to-0 is acceptable for v1.

**Note.** Task 3 (current-state) largely subsumes this long-term — if the UI shows
a ClickHouse-derived *current* score, the stale inventory max matters less. Do
this as the quick consistency fix now; Task 3 is the structural fix.

---

## 2. (P0) Read-side maturity threshold for the confirmed catalog

**Why.** Route-aware `confirmed` promotes an endpoint on a **single** route-matched
event — so one-off scanner hits against a real route (no scanner UA → no scanner
flag) can enter the clean catalog. The professional FP-reduction (Cloudflare's
"≥500 req / 10 days" analog) is a read-side **observation threshold**. The backend
currently filters only `confirmed:true` (`controller/handlers/inventory.go:149`,
`:385`, `:582`) with no `seen_count` gate.

**What.**
- Add an optional `min_seen` query param (default ~5, operator-tunable) →
  `seen_count >= min_seen` (use `total_seen` in the grouped views) in the
  clean-catalog filters: `ListInventory`, `ListInventoryOperations`,
  `ListInventoryListeners`.
- Attack-surface view stays `confirmed != true` (`inventory.go:510`), unchanged.
- Optional: a third "emerging" bucket (`confirmed:true` but below threshold) so
  nothing is hidden — just separated from the high-confidence catalog.
- Existing indexes (`project_last_seen`, `project_riskscore_lastseen`, …) cover it.

---

## 3. (P1 — biggest correctness fix) Current-state vs "ever"

**Why.** Inventory `max_*` and `$addToSet` arrays are MONOTONIC — they capture
"ever observed", never "current". A remediated endpoint (auth added, TLS fixed, a
single old `threat_intel_hit`) stays **red forever**. The collector cannot decay
`$max`; this is a read-side job. Today only `ResetInventoryItem` clears it, and
only one endpoint at a time.

**What.** Two-tier posture.
- Keep inventory as the historical/cumulative record (collector keeps `$max`/
  `$addToSet`).
- Compute CURRENT posture from ClickHouse, time-windowed (last N days): per
  endpoint, current `risk_score` (max-anchored over the window's flags), current
  flags / pii / auth from recent `api_events_raw`.
- Read API returns BOTH `*_ever` (inventory) and `*_current` (ClickHouse). UI
  defaults to **current**, shows "historically" as context.

**How.** A per-endpoint last-N-day risk aggregation in `pkg/clickhouse`, joined
with the inventory catalog. The ClickHouse cross-filter pattern already in
`ListInventory` (`controller/handlers/inventory.go:90`, `:393`) is the template.
Alternative: a periodic job that writes `current_*` fields onto inventory docs.

---

## 4. (P2 — low priority) Sampling-bias indicator

**Why.** When `api_collector_config.raw_sample_rate >= 2`, rollup-derived
analytics are approximate (collector change 5). Default off → usually a no-op.

**What.** Read `raw_sample_rate` from `api_collector_config`; add
`approximate: true` + the rate to rollup-backed responses (`ErrorAnalysis`,
`GeoSummary` time series, `GetInventoryStats`). Purely informational.

---

## Already in place — NO action

Verified present in this repo, listed so the scope is clear:

- **Consumer analytics** — `GET /inventory/consumers` (`ListConsumers`) +
  `/consumers/:hash` (`GetConsumerDetail`), ClickHouse `consumer_hash`
  aggregation. ✅
- **Drift / change-detection** — `GET /inventory/changes` (`ListChanges`),
  `GET/POST /inventory/snapshots` (`ListSnapshots`/`CreateSnapshot`),
  `controller/handlers/inventory_drift.go`. ✅
- **Two-axis scores** — `max_risk_score` (threat) + `max_posture_score` (posture)
  already read, sorted and surfaced (`inventory.go`, `risk-summary`,
  `security-score`, `transport`). ✅
- **Attack-surface / confirmed split, OpenAPI export, geo, errors, zombies,
  auth-coverage, bot-scanner, PII inventory, normalize-gaps, stale cleanup.** ✅

---

## Constraint / tuning note

The collector runs **multi-collector with no pinning and no shared state** (hard
constraint). Detector windows (BOLA, brute-force, …) are therefore **per-collector**;
a consumer's traffic fans across collectors, so the effective threshold ≈
`configured / (collectors the consumer hits)`. Set `detection.*` thresholds in
`api_collector_config` to `≈ desired_fleet_threshold / expected_replicas`, and
surface this relationship in the admin UI so operators tune correctly.

> Field contract: `elchi-collector/docs/schema.md` is authoritative — sync any
> new read against it.
