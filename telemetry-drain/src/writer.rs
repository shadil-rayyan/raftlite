use crate::ring_buffer::RingBuffer;
use std::fs::OpenOptions;
use std::io::{Seek, SeekFrom, Write};
use std::os::unix::fs::OpenOptionsExt;

const SHM_PATH: &str = "/dev/shm/raftlite_telemetry.arrow";
const BATCH_SIZE: usize = 4096;

pub struct ArrowBatcher {
    buffer: Vec<crate::ring_buffer::TelemetrySlot>,
    fd: Option<std::fs::File>,
}

impl ArrowBatcher {
    pub fn new() -> Self {
        let fd = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .custom_flags(libc::O_SYNC)
            .open(SHM_PATH)
            .ok();
        Self {
            buffer: Vec::with_capacity(BATCH_SIZE),
            fd,
        }
    }

    pub fn ingest(&mut self, ring: &RingBuffer) {
        while let Some(slot) = ring.read() {
            self.buffer.push(slot);
            if self.buffer.len() >= BATCH_SIZE {
                self.flush();
            }
        }
    }

    pub fn flush(&mut self) {
        if self.buffer.is_empty() {
            return;
        }
        if let Some(ref mut fd) = self.fd {
            let sz = self.buffer.len() * std::mem::size_of::<crate::ring_buffer::TelemetrySlot>();
            let buf: &[u8] = unsafe {
                std::slice::from_raw_parts(self.buffer.as_ptr() as *const u8, sz)
            };
            let _ = fd.seek(SeekFrom::Start(0));
            let _ = fd.write_all(buf);
            let _ = fd.sync_all();
        }
        self.buffer.clear();
    }
}
