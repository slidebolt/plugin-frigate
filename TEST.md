# Package Level Requirements

Tests for this plugin should verify:

- **Registration**: Must register with the system registry.
- **Entity Discovery**: Must list its core device and all child entities.
- **State Integrity**: Reported state must survive a refresh and be consistent in search.
- **Snapshots**: Must support entity snapshots (save and retrieve).
- **Health**: Must report health on `_internal/health`.
