# Git Workflow for plugin-frigate

This repository contains the Slidebolt Frigate Plugin, providing integration with Frigate NVR for object detection and camera management. It produces a standalone binary.

## Dependencies
- **Internal:**
  - `sb-contract`: Core interfaces.
  - `sb-domain`: Shared domain models.
  - `sb-messenger-sdk`: Shared messaging interfaces.
  - `sb-runtime`: Core execution environment.
  - `sb-storage-sdk`: Shared storage interfaces.
  - `sb-testkit`: Testing utilities.
- **External:** 
  - `github.com/cucumber/godog`: BDD testing framework.

## Build Process
- **Type:** Go Application (Plugin).
- **Consumption:** Run as a background plugin service.
- **Artifacts:** Produces a binary named `plugin-frigate`.
- **Command:** `go build -o plugin-frigate ./cmd/plugin-frigate`
- **Validation:** 
  - Validated through unit tests: `go test -v ./...`
  - Validated through BDD tests: `go test -v ./cmd/plugin-frigate`
  - Validated by successful compilation of the binary.

## Pre-requisites & Publishing
As a Frigate integration plugin, `plugin-frigate` must be updated whenever the core domain, messaging, storage, or testkit SDKs are changed.

**Before publishing:**
1. Determine current tag: `git tag | sort -V | tail -n 1`
2. Ensure all local tests pass: `go test -v ./...`
3. Ensure the binary builds: `go build -o plugin-frigate ./cmd/plugin-frigate`

**Publishing Order:**
1. Ensure all internal dependencies are tagged and pushed.
2. Update `plugin-frigate/go.mod` to reference the latest tags.
3. Determine next semantic version for `plugin-frigate` (e.g., `v1.0.5`).
4. Commit and push the changes to `main`.
5. Tag the repository: `git tag v1.0.5`.
6. Push the tag: `git push origin main v1.0.5`.

## Update Workflow & Verification
1. **Modify:** Update Frigate integration logic in `internal/` or `app/`.
2. **Verify Local:**
   - Run `go mod tidy`.
   - Run `go test ./...`.
   - Run `go test ./cmd/plugin-frigate` (BDD features).
   - Run `go build -o plugin-frigate ./cmd/plugin-frigate`.
3. **Commit:** Ensure the commit message clearly describes the changes.
4. **Tag & Push:** (Follow the Publishing Order above).
