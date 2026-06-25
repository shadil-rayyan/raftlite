import struct
import mmap
import os
import time
import logging

import grpc

from models.config import (
    SHM_PATH, WINDOW_SIZE, ANOMALY_THRESHOLD,
    LOOPBACK_INTERVAL_S, RAFT_LEADER_ADDR
)
from models.anomaly import AnomalyDetector

# ponytail: raw binary read from shared memory — Arrow IPC in production
SLOT_FMT = "<IQII"  # ipv4(u32), ts(u64), route(u32), latency(u64) + 8 pad
SLOT_SIZE = 32
BATCH_SIZE = 4096

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger("ai-engine")


def read_shm_slots(path: str) -> list[dict]:
    """Read raw TelemetrySlot frames from shared memory file."""
    if not os.path.exists(path):
        return []

    with open(path, "rb") as f:
        data = f.read()

    frames = []
    offset = 0
    while offset + SLOT_SIZE <= len(data):
        raw = data[offset : offset + SLOT_SIZE]
        ipv4, ts, route, latency = struct.unpack_from(SLOT_FMT, raw, 0)
        ip = ".".join(str((ipv4 >> (8 * i)) & 0xFF) for i in range(4))
        frames.append({
            "ip": ip,
            "timestamp_ns": ts,
            "route_id": route,
            "latency_ns": latency,
        })
        offset += SLOT_SIZE
    return frames


def loopback_block(ip: str) -> bool:
    """gRPC call to Go Raft leader to add block rule."""
    try:
        channel = grpc.insecure_channel(RAFT_LEADER_ADDR)
        # ponytail: generated Python gRPC stubs needed — manual call for now
        channel.close()
        logger.info("would block %s via gRPC loopback", ip)
        return True
    except Exception as e:
        logger.error("loopback failed: %s", e)
        return False


def main():
    detector = AnomalyDetector(WINDOW_SIZE, ANOMALY_THRESHOLD)
    logger.info("ai-engine starting, watching %s", SHM_PATH)

    while True:
        frames = read_shm_slots(SHM_PATH)
        if frames:
            ips = detector.feed(frames)
            for ip in ips:
                logger.warning("anomaly detected: %s", ip)
                loopback_block(ip)

        time.sleep(LOOPBACK_INTERVAL_S)


if __name__ == "__main__":
    main()
