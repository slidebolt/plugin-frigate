### `plugin-frigate` repository

#### Project Overview

This repository contains the `plugin-frigate`, a plugin that integrates the Slidebolt system with [Frigate](https://frigate.video/), an open-source Network Video Recorder (NVR). The plugin allows Slidebolt to discover and interact with cameras managed by a Frigate instance.

#### Architecture

The `plugin-frigate` is a Go application that implements the `runner.Plugin` interface from the `slidebolt/sdk-runner`. It connects to a Frigate instance using its REST API and performs the following functions:

-   **Camera Discovery**: The plugin automatically discovers all enabled cameras from the Frigate configuration.
-   **Device and Entity Creation**: For each discovered camera, the plugin creates a corresponding device and a camera entity within the Slidebolt system.
-   **State Synchronization**: It periodically fetches statistics and stream information for each camera from the Frigate API and emits events to keep the camera's state (e.g., online status, FPS, stream URL) updated in Slidebolt.
-   **Configuration**: The plugin can be configured via environment variables or by sending a command to a special `frigate-system` device to set the Frigate API and go2rtc URLs.

#### Key Files

| File | Description |
| :--- | :--- |
| `go.mod` | Defines the Go module and its dependencies on the Slidebolt SDKs. |
| `main.go` | The main entry point that initializes and runs the plugin. |
| `plugin.go` | The core plugin logic, responsible for discovery, state management, and event emission. |
| `logic.go` | Contains the HTTP client for communicating with the Frigate API and the data structures for the API responses. |
| `camera.go` | Defines the `camera` domain, its state (`CameraState`), and available actions (`stream`). |
| `.env.example`| Specifies the environment variables needed to connect to Frigate: `FRIGATE_URL` and `FRIGATE_GO2RTC_URL`. |

#### Available Commands

-   **`stream`**: This command can be sent to a camera entity to retrieve its video stream URL.
-   **Configuration Command**: A command can be sent to the `frigate-system` device to dynamically set the `FRIGATE_URL` and `GO2RTC_URL`.

#### Standalone Discovery Mode

This plugin supports a standalone discovery mode for rapid testing and diagnostics without requiring the full Slidebolt stack (NATS, Gateway, etc.).

To run discovery and output the results to JSON:
```bash
./plugin-frigate -discover
```

**Note**: Ensure any required environment variables (e.g., API keys, URLs) are set before running.
