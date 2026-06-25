use std::sync::atomic::{AtomicUsize, Ordering};

const RING_BUF_SIZE: usize = 4096;

#[repr(C, packed)]
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct TelemetrySlot {
    pub ipv4: u32,
    pub timestamp_ns: u64,
    pub route_id: u32,
    pub latency_ns: u64,
    _pad: [u8; 8],
}

pub struct RingBuffer {
    slots: Vec<TelemetrySlot>,
    head: AtomicUsize,
    tail: AtomicUsize,
}

impl RingBuffer {
    pub fn new() -> Self {
        let mut slots = Vec::with_capacity(RING_BUF_SIZE);
        for _ in 0..RING_BUF_SIZE {
            slots.push(TelemetrySlot {
                ipv4: 0,
                timestamp_ns: 0,
                route_id: 0,
                latency_ns: 0,
                _pad: [0; 8],
            });
        }
        Self {
            slots,
            head: AtomicUsize::new(0),
            tail: AtomicUsize::new(0),
        }
    }

    pub fn write(&self, slot: &TelemetrySlot) -> bool {
        let head = self.head.load(Ordering::Acquire);
        let tail = self.tail.load(Ordering::Acquire);
        let next = (head + 1) % RING_BUF_SIZE;
        if next == tail {
            return false;
        }
        unsafe {
            let ptr = self.slots.as_ptr().add(head) as *mut TelemetrySlot;
            std::ptr::copy_nonoverlapping(slot, ptr, 1);
        }
        self.head.store(next, Ordering::Release);
        true
    }

    pub fn read(&self) -> Option<TelemetrySlot> {
        let tail = self.tail.load(Ordering::Acquire);
        let head = self.head.load(Ordering::Acquire);
        if tail == head {
            return None;
        }
        let slot = unsafe { std::ptr::read_unaligned(self.slots.as_ptr().add(tail)) };
        self.tail.store((tail + 1) % RING_BUF_SIZE, Ordering::Release);
        Some(slot)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn write_read_roundtrip() {
        let ring = RingBuffer::new();
        let slot = TelemetrySlot {
            ipv4: 0xC0A80101,
            timestamp_ns: 1000,
            route_id: 42,
            latency_ns: 500,
            _pad: [0; 8],
        };
        assert!(ring.write(&slot));
        let read = ring.read().expect("should read slot");
        // copy fields to avoid unaligned reference UB
        assert_eq!({ read.ipv4 }, 0xC0A80101);
        assert_eq!({ read.timestamp_ns }, 1000);
        assert_eq!({ read.route_id }, 42);
        assert_eq!({ read.latency_ns }, 500);
    }

    #[test]
    fn read_empty() {
        let ring = RingBuffer::new();
        assert!(ring.read().is_none());
    }

    #[test]
    fn full_buffer() {
        let ring = RingBuffer::new();
        let slot = TelemetrySlot {
            ipv4: 1,
            timestamp_ns: 0,
            route_id: 0,
            latency_ns: 0,
            _pad: [0; 8],
        };
        for _ in 0..RING_BUF_SIZE - 1 {
            assert!(ring.write(&slot));
        }
        assert!(!ring.write(&slot)); // full
    }
}
