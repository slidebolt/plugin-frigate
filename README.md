# Frigate NVR API Reference

This is a minimal reference implementation showing how to connect to Frigate NVR.

## Connection Overview

**Protocol**: HTTP REST API  
**Port**: 5000 (default Frigate port)  
**Authentication**: None (by default)  
**MQTT**: Optional, for real-time events

## Quick Start

```bash
# Set your Frigate URL
echo "FRIGATE_URL=http://192.168.88.10:5000" > .env.local
source .env.local

# Run discovery
go run .
```

## API Endpoints

### Get Configuration
```bash
GET /api/config
```
Returns camera configuration with enabled/disabled status.

### Get Camera Statistics
```bash
GET /api/stats
```
Returns FPS and processing stats per camera.

### Get WebRTC Streams
```bash
GET /api/streams
```
Returns WebRTC stream URLs for live viewing.

### Get Detection Events
```bash
GET /api/events?limit=100
```
Returns recent detection events with labels, timestamps, snapshots.

## Example Response: /api/config

```json
{
  "cameras": {
    "front_door": {
      "name": "front_door",
      "enabled": true,
      "detect": {"enabled": true},
      "motion": {"enabled": true},
      "record": {"enabled": true},
      "snapshots": {"enabled": true}
    }
  }
}
```

## Example Response: /api/events

```json
[
  {
    "id": "1234567890",
    "camera": "front_door",
    "label": "person",
    "start_time": 1700000000.0,
    "end_time": 1700000010.0,
    "has_snapshot": true,
    "has_clip": true
  }
]
```

## Environment Variables

```bash
FRIGATE_URL=http://127.0.0.1:5000          # Required - Frigate HTTP API
FRIGATE_GO2RTC_URL=http://127.0.0.1:1984   # Optional - WebRTC streaming
FRIGATE_MQTT_HOST=192.168.88.10            # Optional - MQTT broker
FRIGATE_MQTT_PORT=1883                     # Optional - MQTT port
FRIGATE_MQTT_USER=username                 # Optional - MQTT auth
FRIGATE_MQTT_PASSWORD=password             # Optional - MQTT auth
FRIGATE_MQTT_TOPIC_PREFIX=frigate          # Optional - MQTT topic prefix
```

## Key Differences from WiZ/Kasa

- **HTTP not UDP**: REST API over TCP
- **No encryption**: Plain HTTP (or HTTPS if configured)
- **Centralized**: Single Frigate instance manages multiple cameras
- **Optional MQTT**: For real-time event streaming
