# CRS Version Runbook — elchi-shield

How the OWASP CRS version works for **elchi-shield** (the edge ext_proc WAF sidecar),
and the exact steps to adopt a new one. This is **separate** from the WASM WAF path,
whose CRS is fixed at `4.14.0` (`pkg/waf/data/crs_rules_4.14.0.json`) and is **not**
touched by anything here.

## The two CRS axes (don't confuse them)

| | WASM WAF | **Shield WAF** |
|---|---|---|
| Delivery | Envoy `coraza.wasm` filter | edge ext_proc sidecar binary |
| CRS source | backend-managed, runtime-selectable | **compiled into the binary** (`coraza-coreruleset/v4` Go module) |
| Version | OWASP CRS semver, `4.14.0` (fixed) | coraza-coreruleset module version, e.g. `v4.25.0` |
| Backend library | `crs_rules_4.14.0.json` | `crs_rules_coreruleset_<ver>.json` |
| API | `/api/v3/waf/crs` | `/api/v3/shield/crs` |

The shield CRS version is the **`coraza-coreruleset/v4` pin in the shield repo's
`go.mod`** — it uniquely identifies the exact ruleset the binary enforces. Nothing at
runtime can change it; the UI only *reflects* it.

## How a version flows end-to-end (already built)

```
shield go.mod pin ──build ldflag──▶ shield /configz coreruleset_version
        │                                    │
        │                          elchi-client scrapes /configz, reports it in the
        │                          register Metadata["shield_coreruleset_version"]
        │                                    │
        ▼                                    ▼
scripts/gen-shield-crs.sh          controller stores it per node →
  → crs_rules_coreruleset_<ver>.json   GET /api/v3/shield/crs/fleet (versions + primary)
        │                                    │
        └──────────── backend serves ────────┴──▶ UI shield WAF Studio auto-pins its
             /api/v3/shield/crs                    CRS library to the fleet's primary
```

- The backend **discovers** `crs_rules_coreruleset_*.json` at startup
  (`pkg/waf/embedded_shield.go`, lazy `sync.Once`) — **adding a version needs no code
  change**, just the committed file. Zero hot-path / runtime cost.
- The UI **auto-pins** to the version the project's edges actually run and warns on a
  mixed-version fleet. It never lets a user pick a version that changes enforcement.

## Adopting a new OWASP CRS release

1. **Bump the shield binary** — in `elchi-shield/go.mod`, update the
   `github.com/corazawaf/coraza-coreruleset/v4` pin (and usually `coraza/v3`); `go mod
   tidy`; rebuild. The version auto-stamps into `/configz` + `build_info` via the
   `Makefile` ldflag (`-X main.crsVersion`). Run shield's tests + `make race`.

2. **Generate the matching backend library** — from this repo:
   ```
   scripts/gen-shield-crs.sh v4.26.0      # or no arg to derive from ../elchi-shield/go.mod
   ```
   This downloads that coraza-coreruleset version into the Go module cache and parses
   its `@owasp_crs` rules into `pkg/waf/data/crs_rules_coreruleset_v4.26.0.json`
   (+ `_metadata.json`). It validates the output (rule count, id uniqueness) and never
   touches the WASM `4.14.0` files.

3. **Commit** both generated files. On the next backend start they're served at
   `/api/v3/shield/crs?crs_version=v4.26.0` and listed by `/api/v3/shield/crs/versions`.

4. **Publish + roll out** the new shield binary via the normal elchi-archive / client
   bundle. Edges upgrade on their own cadence.

## What happens during a mixed rollout

- **Enforcement:** each edge enforces with *its own* embedded CRS. A policy is one
  merged config for all edges; `include_owasp` + custom SecLang + `exclude_rule_ids`
  apply on every edge. Rule IDs are stable across CRS minors, so tuning is portable;
  only genuinely new/removed rules differ per edge.
- **UI:** the shield WAF Studio shows a mixed-fleet warning (which versions, node
  counts) and pins its browsable library to the fleet `primary`. A rule id excluded but
  absent on some edge's version is a harmless no-op there.
- **No user disruption:** existing policies keep working; old edges keep their version
  until upgraded.

## Guarantees

- **Runtime unaffected.** Generation is a build-time script; serving is lazy file
  discovery off an embedded FS. No per-request cost, no hot-path change.
- **WASM path untouched.** The `4.14.0` library, its endpoints, and its generation
  default (`parse_coraza_rules.py` with no `--rules-dir`) are unchanged.
