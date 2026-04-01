use std::io::{Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::thread;
use std::time::Duration;

use anyhow::{Context, Result, anyhow};
use tauri::AppHandle;

const SINGLE_INSTANCE_PORT: u16 = 47631;
const SINGLE_INSTANCE_HELLO: &[u8] = b"minipunch-desktop-single-instance-v1\n";
const SINGLE_INSTANCE_ACK: &[u8] = b"ok\n";

pub struct SingleInstanceGuard {
    listener: TcpListener,
}

pub fn acquire_or_activate_existing() -> Result<Option<SingleInstanceGuard>> {
    let bind_addr = single_instance_addr();
    match TcpListener::bind(bind_addr) {
        Ok(listener) => {
            listener
                .set_nonblocking(true)
                .context("failed to mark single-instance listener as non-blocking")?;
            Ok(Some(SingleInstanceGuard { listener }))
        }
        Err(err) if err.kind() == std::io::ErrorKind::AddrInUse => {
            if notify_existing_instance(bind_addr)? {
                Ok(None)
            } else {
                Err(anyhow!(
                    "single-instance port {} is occupied by another process",
                    bind_addr
                ))
            }
        }
        Err(err) => {
            Err(err).with_context(|| format!("failed to bind single-instance port {bind_addr}"))
        }
    }
}

pub fn spawn_listener(guard: SingleInstanceGuard, app: AppHandle) {
    thread::spawn(move || {
        loop {
            match guard.listener.accept() {
                Ok((mut stream, _)) => {
                    if handle_incoming_activation(&mut stream) {
                        let app_handle = app.clone();
                        tauri::async_runtime::spawn(async move {
                            let _ = crate::desktop::reveal_main_window(&app_handle);
                        });
                    }
                }
                Err(err) if err.kind() == std::io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(200));
                }
                Err(_) => break,
            }
        }
    });
}

fn single_instance_addr() -> SocketAddr {
    SocketAddr::from(([127, 0, 0, 1], SINGLE_INSTANCE_PORT))
}

fn notify_existing_instance(addr: SocketAddr) -> Result<bool> {
    let timeout = Duration::from_secs(3);
    let mut stream = TcpStream::connect_timeout(&addr, timeout)
        .with_context(|| format!("failed to connect to existing desktop instance at {addr}"))?;
    stream
        .set_read_timeout(Some(timeout))
        .context("failed to set single-instance read timeout")?;
    stream
        .set_write_timeout(Some(timeout))
        .context("failed to set single-instance write timeout")?;
    stream
        .write_all(SINGLE_INSTANCE_HELLO)
        .context("failed to send single-instance activation request")?;
    let mut response = [0_u8; 16];
    let size = stream
        .read(&mut response)
        .context("failed to read single-instance activation response")?;
    Ok(&response[..size] == SINGLE_INSTANCE_ACK)
}

fn handle_incoming_activation(stream: &mut TcpStream) -> bool {
    let mut request = [0_u8; 64];
    let Ok(size) = stream.read(&mut request) else {
        return false;
    };
    if &request[..size] != SINGLE_INSTANCE_HELLO {
        return false;
    }
    let _ = stream.write_all(SINGLE_INSTANCE_ACK);
    true
}
