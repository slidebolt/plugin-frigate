# TASK: plugin-frigate

## Status: Functional Core — Refactored and Optimized

## Issues

### 1. Hardcoded "frigate-internal" Device Always Injected (Resolved)
Replaced with a formal `"frigate-system"` device and `"frigate-config"` entity with domain `"config"`.

- [x] Either document and formally define the internal config device as a supported pattern, or remove it
- [x] If kept, register it consistently and ensure its domain is valid within the system

### 2. OnCommand "internal-config" Path Has No Entity (Resolved)
Renamed to `"frigate-config"` and correctly exposed under the `"frigate-system"` device. Implemented state-based persistence for `FRIGATE_URL`.

- [x] Document the internal config command contract explicitly
- [x] Or replace runtime URL reconfiguration with a proper env/storage-based config pattern

### 3. emitStateEvent Called While Holding Write Lock (Resolved)
Refactored `discover()` to collect updates and emit events after releasing the lock.

- [x] Collect state changes to emit while holding the lock, then release the lock before calling `EmitEvent`

### 4. OnHealthCheck Returns "warning" String When Unconfigured (Resolved)
Standardized to return a Go error when `FRIGATE_URL` is missing.

- [x] Return a well-defined status string or return an error if the plugin cannot function without configuration