# Arrow IPC Schema: TelemetryFrame

Defines the fixed-schema contract between Rust writer and Python reader.

## Columns

| Column       | Type          | Width | Notes                         |
|-------------|---------------|-------|-------------------------------|
| ipv4        | uint32        | 4 B   | Network byte order            |
| timestamp_ns| uint64        | 8 B   | Unix nanosecond clock         |
| route_id    | uint32        | 4 B   | Hashed route identifier       |
| latency_ns  | uint64        | 8 B   | Go-measured latency           |

Total row width: 24 bytes.

## Batch Layout

- Schema: `TelemetryFrame` (4 fields above)
- IPC record batches: 4096 rows per batch (~96 KB per batch)
- Memory mapping: Rust writes to `/dev/shm/raftlite_telemetry.arrow`
- Python opens via `pyarrow.ipc.open_stream` on the shared file descriptor
- Frame count header appended as first 8 bytes of the IPC stream

## Alignment

All columns are naturally aligned (no padding needed). Arrow IPC handles
endianness; both Rust and Python run little-endian on x86_64.
