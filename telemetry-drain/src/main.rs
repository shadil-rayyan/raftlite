mod ring_buffer;
mod writer;

use ring_buffer::RingBuffer;
use std::os::unix::net::UnixListener;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::Duration;
use writer::ArrowBatcher;

const SOCK_PATH: &str = "/tmp/raftlite_telemetry.sock";

fn main() {
    let _ = std::fs::remove_file(SOCK_PATH);
    let listener = UnixListener::bind(SOCK_PATH).expect("bind socket");
    let ring = Arc::new(RingBuffer::new());
    let running = Arc::new(AtomicBool::new(true));

    let ring_ingest = ring.clone();
    let running_ingest = running.clone();
    let ingestor = thread::spawn(move || {
        let mut batcher = ArrowBatcher::new();
        while running_ingest.load(Ordering::Relaxed) {
            batcher.ingest(&ring_ingest);
            thread::sleep(Duration::from_millis(10));
        }
        batcher.flush();
    });

    for stream in listener.incoming() {
        match stream {
            Ok(mut s) => {
                let mut buf = [0u8; 32];
                while let Ok(n) = std::io::Read::read(&mut s, &mut buf) {
                    if n == 0 {
                        break;
                    }
                    if n == 32 {
                        let slot = unsafe {
                            std::ptr::read_unaligned(buf.as_ptr() as *const ring_buffer::TelemetrySlot)
                        };
                        ring.write(&slot);
                    }
                }
                let _ = s.shutdown(std::net::Shutdown::Both);
            }
            Err(_) => break,
        }
    }

    running.store(false, Ordering::Relaxed);
    let _ = ingestor.join();
    let _ = std::fs::remove_file(SOCK_PATH);
}
