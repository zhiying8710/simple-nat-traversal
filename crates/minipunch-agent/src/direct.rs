use std::collections::{BTreeMap, HashMap, VecDeque};
use std::net::SocketAddr;
use std::sync::Arc;
use std::sync::OnceLock;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use anyhow::{Context, Result, anyhow, bail};
use minipunch_core::{
    DeviceIdentity, DirectConnectionCandidate, DirectProbeEnvelope, DirectRendezvousSession,
    DirectSelectiveAckRange, DirectTunnelEnvelope, RelayKeypair, SecureChannelRole, SecureReceiver,
    SecureSender, device_id_from_public_key, direct_probe_ack_message, direct_probe_hello_message,
    generate_token, relay_channel_open_message, secure_channel_pair, verify_signature_base64,
};
use serde::Serialize;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream, UdpSocket};
use tokio::sync::{Mutex, Notify, mpsc};
use tokio::time::{self, Instant, MissedTickBehavior, timeout};
use tracing::{info, warn};

use crate::ResolvedService;
use crate::config::PublishedServiceConfig;
use crate::runtime_state::{
    DirectTransportMetrics, ForwardRuntimeHook, PublishedServiceRuntimeHook,
};

const DIRECT_PROBE_SEND_INTERVAL: Duration = Duration::from_millis(250);
const DIRECT_TUNNEL_MAX_PLAINTEXT: usize = 1024;
const DIRECT_TUNNEL_INITIAL_WINDOW_SIZE: usize = 4;
const DIRECT_TUNNEL_MIN_WINDOW_SIZE: usize = 2;
const DIRECT_TUNNEL_MAX_WINDOW_SIZE: usize = 32;
const DIRECT_TUNNEL_DUP_ACK_THRESHOLD: u32 = 3;
const DIRECT_TUNNEL_TIMER_GRANULARITY: Duration = Duration::from_millis(50);
const DIRECT_TUNNEL_INITIAL_RTO: Duration = Duration::from_millis(250);
const DIRECT_TUNNEL_MIN_RTO: Duration = Duration::from_millis(120);
const DIRECT_TUNNEL_MAX_RTO: Duration = Duration::from_secs(2);
const DIRECT_TUNNEL_MAX_RETRANSMIT_ATTEMPTS: u32 = 20;
const DIRECT_TUNNEL_MAX_INBOUND_REORDER_BUFFER: usize = 64;
const DIRECT_TUNNEL_MAX_SELECTIVE_ACKS: usize = 8;
const DIRECT_TUNNEL_HOUSEKEEPING_INTERVAL: Duration = Duration::from_secs(1);
const DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER: Duration = Duration::from_secs(10);
const DIRECT_TUNNEL_KEEPALIVE_SEND_INTERVAL: Duration = Duration::from_secs(10);
const DIRECT_TUNNEL_PEER_IDLE_TIMEOUT: Duration = Duration::from_secs(35);
const DIRECT_METRICS_REPORT_INTERVAL: Duration = Duration::from_millis(500);
const DIRECT_PREBRIDGE_FAIL_ONCE_ENV: &str = "MINIPUNCH_TEST_FAIL_DIRECT_PREBRIDGE_ONCE";

static DIRECT_PREBRIDGE_FAIL_ONCE_FLAG: OnceLock<AtomicBool> = OnceLock::new();

#[derive(Debug, Clone)]
struct PendingDirectData {
    sequence: u64,
    data_base64: String,
    attempts: u32,
    last_sent_at: Instant,
}

#[derive(Debug, Clone)]
struct PendingDirectClose {
    final_sequence: u64,
    reason: Option<String>,
    attempts: u32,
    last_sent_at: Instant,
}

#[derive(Debug, Default)]
struct DirectAckTracker {
    last_ack_sequence: Option<u64>,
    duplicate_ack_count: u32,
    last_fast_retransmit_sequence: Option<u64>,
}

#[derive(Debug, Default, Clone)]
struct DirectSelectiveAckScoreboard {
    ranges: Vec<DirectSelectiveAckRange>,
}

impl DirectSelectiveAckScoreboard {
    fn advance_cumulative_ack(&mut self, next_sequence: u64) {
        let mut trimmed = Vec::with_capacity(self.ranges.len());
        for mut range in self.ranges.drain(..) {
            if range.end_sequence < next_sequence {
                continue;
            }
            if range.start_sequence < next_sequence {
                range.start_sequence = next_sequence;
            }
            if range.start_sequence <= range.end_sequence {
                trimmed.push(range);
            }
        }
        self.ranges = trimmed;
    }

    fn observe_ranges(&mut self, incoming: &[DirectSelectiveAckRange]) -> usize {
        if incoming.is_empty() {
            return 0;
        }

        let before = covered_sequence_count(&self.ranges);
        self.ranges.extend(
            incoming
                .iter()
                .filter(|range| range.start_sequence <= range.end_sequence)
                .cloned(),
        );
        if self.ranges.is_empty() {
            return 0;
        }

        self.ranges
            .sort_by_key(|range| (range.start_sequence, range.end_sequence));

        let mut merged: Vec<DirectSelectiveAckRange> = Vec::with_capacity(self.ranges.len());
        for range in self.ranges.drain(..) {
            if let Some(last) = merged.last_mut() {
                if range.start_sequence <= last.end_sequence.saturating_add(1) {
                    last.end_sequence = last.end_sequence.max(range.end_sequence);
                    continue;
                }
            }
            merged.push(range);
        }

        let after = covered_sequence_count(&merged);
        self.ranges = merged;
        after.saturating_sub(before).min(usize::MAX as u128) as usize
    }

    fn contains(&self, sequence: u64) -> bool {
        self.ranges.iter().any(|range| {
            range.start_sequence <= sequence
                && sequence <= range.end_sequence
                && range.start_sequence <= range.end_sequence
        })
    }

    fn highest_sequence(&self) -> Option<u64> {
        self.ranges.last().map(|range| range.end_sequence)
    }

    #[cfg(test)]
    fn ranges(&self) -> &[DirectSelectiveAckRange] {
        &self.ranges
    }
}

#[derive(Debug, Clone, Copy)]
struct DirectFastRecovery {
    recover_until_sequence: u64,
}

#[derive(Debug, Clone)]
struct DirectCongestionController {
    cwnd_packets: f64,
    ssthresh_packets: f64,
    fast_recovery: Option<DirectFastRecovery>,
}

impl Default for DirectCongestionController {
    fn default() -> Self {
        Self {
            cwnd_packets: DIRECT_TUNNEL_INITIAL_WINDOW_SIZE as f64,
            ssthresh_packets: DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
            fast_recovery: None,
        }
    }
}

impl DirectCongestionController {
    fn send_window_size(&self) -> usize {
        self.cwnd_packets.floor().clamp(
            DIRECT_TUNNEL_MIN_WINDOW_SIZE as f64,
            DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
        ) as usize
    }

    fn on_ack_progress(
        &mut self,
        acked_packets: usize,
        next_sequence: u64,
        missing_front_sequence: Option<u64>,
    ) -> Option<u64> {
        if acked_packets == 0 {
            return None;
        }

        if let Some(recovery) = self.fast_recovery {
            if next_sequence >= recovery.recover_until_sequence {
                self.fast_recovery = None;
                self.cwnd_packets = self.ssthresh_packets.clamp(
                    DIRECT_TUNNEL_MIN_WINDOW_SIZE as f64,
                    DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
                );
                return None;
            }

            self.cwnd_packets = (self.ssthresh_packets + 1.0).clamp(
                DIRECT_TUNNEL_MIN_WINDOW_SIZE as f64,
                DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
            );
            return missing_front_sequence;
        }

        self.grow_window(acked_packets);
        None
    }

    fn on_duplicate_ack(&mut self) {
        if self.fast_recovery.is_some() {
            self.cwnd_packets = (self.cwnd_packets + 1.0).min(DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64);
        }
    }

    fn on_fast_retransmit(&mut self, inflight_packets: usize, recover_until_sequence: u64) {
        self.ssthresh_packets = self.compute_ssthresh(inflight_packets);
        self.cwnd_packets = (self.ssthresh_packets + DIRECT_TUNNEL_DUP_ACK_THRESHOLD as f64).clamp(
            DIRECT_TUNNEL_MIN_WINDOW_SIZE as f64,
            DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
        );
        self.fast_recovery = Some(DirectFastRecovery {
            recover_until_sequence,
        });
    }

    fn on_timeout(&mut self, inflight_packets: usize) {
        self.ssthresh_packets = self.compute_ssthresh(inflight_packets);
        self.cwnd_packets = DIRECT_TUNNEL_MIN_WINDOW_SIZE as f64;
        self.fast_recovery = None;
    }

    fn compute_ssthresh(&self, inflight_packets: usize) -> f64 {
        ((inflight_packets.max(DIRECT_TUNNEL_MIN_WINDOW_SIZE) as f64) / 2.0).clamp(
            DIRECT_TUNNEL_MIN_WINDOW_SIZE as f64,
            DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
        )
    }

    fn grow_window(&mut self, acked_packets: usize) {
        if self.cwnd_packets < self.ssthresh_packets {
            self.cwnd_packets += acked_packets as f64;
        } else {
            for _ in 0..acked_packets {
                self.cwnd_packets += 1.0 / self.cwnd_packets.max(1.0);
            }
        }
        self.cwnd_packets = self.cwnd_packets.clamp(
            DIRECT_TUNNEL_MIN_WINDOW_SIZE as f64,
            DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
        );
    }

    fn runtime_metrics(
        &self,
        current_rto: Duration,
        smoothed_rtt_ms: Option<f64>,
        pending_outbound_packets: usize,
        pending_inbound_packets: usize,
        keepalive_sent_count: u64,
        keepalive_ack_count: u64,
    ) -> DirectTransportMetrics {
        DirectTransportMetrics {
            window_packets: clamp_packet_count(self.cwnd_packets),
            ssthresh_packets: clamp_packet_count(self.ssthresh_packets),
            rto_ms: current_rto.as_millis().min(u128::from(u64::MAX)) as u64,
            smoothed_rtt_ms: clamp_optional_ms(smoothed_rtt_ms),
            pending_outbound_packets: pending_outbound_packets.min(u32::MAX as usize) as u32,
            pending_inbound_packets: pending_inbound_packets.min(u32::MAX as usize) as u32,
            fast_recovery: self.fast_recovery.is_some(),
            keepalive_sent_count,
            keepalive_ack_count,
        }
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct DirectProbeResult {
    pub rendezvous_id: String,
    pub local_device_id: String,
    pub peer_device_id: String,
    pub local_bind_addr: String,
    pub selected_peer_addr: String,
    pub selected_peer_candidate_type: Option<String>,
    pub role: String,
    pub completion_kind: String,
    pub ack_nonce: String,
    pub elapsed_ms: u128,
}

struct EstablishedDirectPath {
    socket: Arc<UdpSocket>,
    peer_addr: SocketAddr,
    peer_device_id: String,
    probe_result: DirectProbeResult,
}

struct PreparedSharedDirectPath {
    peer_addr: SocketAddr,
    peer_device_id: String,
    session_rx: mpsc::UnboundedReceiver<SharedDirectInboundPacket>,
    buffered_tunnel_packets: VecDeque<ReceivedDirectTunnelEnvelope>,
}

pub(crate) struct PreparedDirectForward {
    rendezvous_id: String,
    socket: Arc<UdpSocket>,
    peer_addr: SocketAddr,
    peer_device_id: String,
}

pub(crate) struct DirectForwardIngress {
    pub(crate) rendezvous_id: String,
    pub(crate) connection: DirectConnection,
    pub(crate) peer_device_id: String,
}

#[derive(Debug)]
enum SharedDirectInboundPacket {
    Probe {
        envelope: DirectProbeEnvelope,
        sender_addr: SocketAddr,
    },
    Tunnel {
        envelope: DirectTunnelEnvelope,
        sender_addr: SocketAddr,
    },
}

#[derive(Debug)]
struct ReceivedDirectTunnelEnvelope {
    envelope: DirectTunnelEnvelope,
    sender_addr: SocketAddr,
}

#[derive(Clone)]
pub(crate) struct SharedDirectSocketHub {
    socket: Arc<UdpSocket>,
    session_routes: Arc<Mutex<HashMap<String, mpsc::UnboundedSender<SharedDirectInboundPacket>>>>,
}

impl SharedDirectSocketHub {
    pub(crate) fn new(socket: Arc<UdpSocket>) -> Self {
        let session_routes = Arc::new(Mutex::new(HashMap::<
            String,
            mpsc::UnboundedSender<SharedDirectInboundPacket>,
        >::new()));
        let routes_for_reader = session_routes.clone();
        let socket_for_reader = socket.clone();
        tokio::spawn(async move {
            let mut buffer = [0u8; 16 * 1024];
            loop {
                let (size, sender_addr) = match socket_for_reader.recv_from(&mut buffer).await {
                    Ok(result) => result,
                    Err(err) => {
                        warn!("shared direct UDP read failed: {err}");
                        break;
                    }
                };

                let packet = if let Ok(envelope) =
                    serde_json::from_slice::<DirectProbeEnvelope>(&buffer[..size])
                {
                    Some((
                        direct_probe_rendezvous_id(&envelope).to_string(),
                        SharedDirectInboundPacket::Probe {
                            envelope,
                            sender_addr,
                        },
                    ))
                } else if let Ok(envelope) =
                    serde_json::from_slice::<DirectTunnelEnvelope>(&buffer[..size])
                {
                    Some((
                        direct_tunnel_rendezvous_id(&envelope).to_string(),
                        SharedDirectInboundPacket::Tunnel {
                            envelope,
                            sender_addr,
                        },
                    ))
                } else {
                    None
                };

                let Some((rendezvous_id, packet)) = packet else {
                    continue;
                };

                let route = {
                    let routes = routes_for_reader.lock().await;
                    routes.get(&rendezvous_id).cloned()
                };
                if let Some(route) = route {
                    if route.send(packet).is_err() {
                        let mut routes = routes_for_reader.lock().await;
                        if routes
                            .get(&rendezvous_id)
                            .map(|existing| existing.same_channel(&route))
                            .unwrap_or(false)
                        {
                            routes.remove(&rendezvous_id);
                        }
                    }
                }
            }

            let mut routes = routes_for_reader.lock().await;
            routes.clear();
        });

        Self {
            socket,
            session_routes,
        }
    }

    fn socket(&self) -> Arc<UdpSocket> {
        self.socket.clone()
    }

    async fn register_session(
        &self,
        rendezvous_id: &str,
    ) -> Result<mpsc::UnboundedReceiver<SharedDirectInboundPacket>> {
        let (tx, rx) = mpsc::unbounded_channel();
        let mut routes = self.session_routes.lock().await;
        if routes.insert(rendezvous_id.to_string(), tx).is_some() {
            bail!(
                "shared direct UDP hub already has an active route for rendezvous {}",
                rendezvous_id
            );
        }
        Ok(rx)
    }

    async fn unregister_session(&self, rendezvous_id: &str) {
        let mut routes = self.session_routes.lock().await;
        routes.remove(rendezvous_id);
    }
}

#[derive(Clone)]
pub(crate) struct DirectConnection {
    rendezvous_id: String,
    peer_addr: SocketAddr,
    outbound: mpsc::UnboundedSender<DirectTunnelEnvelope>,
    channel_routes: Arc<Mutex<HashMap<String, mpsc::UnboundedSender<DirectTunnelEnvelope>>>>,
    closed: Arc<AtomicBool>,
    closed_notify: Arc<Notify>,
}

#[derive(Debug, Clone)]
struct DirectIncomingChannel {
    rendezvous_id: String,
    channel_id: String,
    service_id: String,
    source_device_id: String,
    source_ephemeral_public_key: String,
    source_open_signature: String,
}

pub(crate) struct PreparedLocalDirectForward {
    peer_addr: String,
    connection: DirectConnection,
    channel_id: String,
    socket: TcpStream,
    channel_rx: mpsc::UnboundedReceiver<DirectTunnelEnvelope>,
    secure_sender: SecureSender,
    secure_receiver: SecureReceiver,
}

#[derive(Clone)]
enum DirectRuntimeMetricsHook {
    Forward(ForwardRuntimeHook),
    Service(PublishedServiceRuntimeHook),
}

impl DirectRuntimeMetricsHook {
    fn note_direct_metrics(&self, metrics: DirectTransportMetrics) {
        match self {
            Self::Forward(hook) => hook.note_direct_metrics(metrics),
            Self::Service(hook) => hook.note_direct_metrics(metrics),
        }
    }

    fn clear_direct_metrics(&self) {
        match self {
            Self::Forward(hook) => hook.clear_direct_metrics(),
            Self::Service(hook) => hook.clear_direct_metrics(),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct DirectMetricsSnapshot {
    metrics: DirectTransportMetrics,
}

struct DirectMetricsReporter {
    hook: Option<DirectRuntimeMetricsHook>,
    last_reported_at: Option<Instant>,
    last_reported_metrics: Option<DirectMetricsSnapshot>,
}

impl DirectMetricsReporter {
    fn new(hook: Option<DirectRuntimeMetricsHook>) -> Self {
        Self {
            hook,
            last_reported_at: None,
            last_reported_metrics: None,
        }
    }

    fn maybe_report(
        &mut self,
        congestion: &DirectCongestionController,
        current_rto: Duration,
        smoothed_rtt_ms: Option<f64>,
        pending_outbound_packets: usize,
        pending_inbound_packets: usize,
        keepalive_sent_count: u64,
        keepalive_ack_count: u64,
    ) {
        let Some(hook) = &self.hook else {
            return;
        };
        let snapshot = DirectMetricsSnapshot {
            metrics: congestion.runtime_metrics(
                current_rto,
                smoothed_rtt_ms,
                pending_outbound_packets,
                pending_inbound_packets,
                keepalive_sent_count,
                keepalive_ack_count,
            ),
        };
        if self.last_reported_metrics.as_ref() == Some(&snapshot) {
            return;
        }
        let now = Instant::now();
        if let Some(last_reported_at) = self.last_reported_at {
            if now.duration_since(last_reported_at) < DIRECT_METRICS_REPORT_INTERVAL {
                return;
            }
        }
        hook.note_direct_metrics(snapshot.metrics.clone());
        self.last_reported_at = Some(now);
        self.last_reported_metrics = Some(snapshot);
    }

    fn force_report(
        &mut self,
        congestion: &DirectCongestionController,
        current_rto: Duration,
        smoothed_rtt_ms: Option<f64>,
        pending_outbound_packets: usize,
        pending_inbound_packets: usize,
        keepalive_sent_count: u64,
        keepalive_ack_count: u64,
    ) {
        let Some(hook) = &self.hook else {
            return;
        };
        let snapshot = DirectMetricsSnapshot {
            metrics: congestion.runtime_metrics(
                current_rto,
                smoothed_rtt_ms,
                pending_outbound_packets,
                pending_inbound_packets,
                keepalive_sent_count,
                keepalive_ack_count,
            ),
        };
        hook.note_direct_metrics(snapshot.metrics.clone());
        self.last_reported_at = Some(Instant::now());
        self.last_reported_metrics = Some(snapshot);
    }

    fn clear(&mut self) {
        if let Some(hook) = &self.hook {
            hook.clear_direct_metrics();
        }
        self.last_reported_at = None;
        self.last_reported_metrics = None;
    }
}

#[derive(Debug)]
enum ChannelOpenState {
    Accepted,
    Rejected(String),
}

impl DirectConnection {
    fn from_socket(
        rendezvous_id: String,
        socket: Arc<UdpSocket>,
        peer_addr: SocketAddr,
    ) -> (Self, mpsc::UnboundedReceiver<DirectIncomingChannel>) {
        let (outbound, mut outbound_rx) = mpsc::unbounded_channel::<DirectTunnelEnvelope>();
        let (incoming_tx, incoming_rx) = mpsc::unbounded_channel::<DirectIncomingChannel>();
        let channel_routes = Arc::new(Mutex::new(HashMap::new()));
        let closed = Arc::new(AtomicBool::new(false));
        let closed_notify = Arc::new(Notify::new());

        let socket_for_writer = socket.clone();
        tokio::spawn(async move {
            while let Some(envelope) = outbound_rx.recv().await {
                let encoded = match serde_json::to_vec(&envelope) {
                    Ok(encoded) => encoded,
                    Err(err) => {
                        warn!("failed to encode direct tunnel envelope: {err}");
                        break;
                    }
                };
                if let Err(err) = socket_for_writer.send_to(&encoded, peer_addr).await {
                    warn!("failed to write direct UDP frame to {peer_addr}: {err}");
                    break;
                }
            }
        });

        let (packet_tx, packet_rx) = mpsc::unbounded_channel::<ReceivedDirectTunnelEnvelope>();
        let rendezvous_id_for_socket_reader = rendezvous_id.clone();
        tokio::spawn(async move {
            let mut buffer = [0u8; 16 * 1024];
            loop {
                let (size, sender_addr) = match socket.recv_from(&mut buffer).await {
                    Ok(result) => result,
                    Err(err) => {
                        warn!("direct UDP read failed: {err}");
                        break;
                    }
                };
                if sender_addr != peer_addr {
                    continue;
                }

                let Ok(envelope) = serde_json::from_slice::<DirectTunnelEnvelope>(&buffer[..size])
                else {
                    continue;
                };
                if direct_tunnel_rendezvous_id(&envelope) != rendezvous_id_for_socket_reader {
                    continue;
                }
                if packet_tx
                    .send(ReceivedDirectTunnelEnvelope {
                        envelope,
                        sender_addr,
                    })
                    .is_err()
                {
                    break;
                }
            }
        });

        Self::from_packet_receiver(
            rendezvous_id,
            peer_addr,
            outbound,
            channel_routes,
            closed,
            closed_notify,
            incoming_tx,
            incoming_rx,
            packet_rx,
            VecDeque::new(),
        )
    }

    fn from_packet_receiver(
        rendezvous_id: String,
        peer_addr: SocketAddr,
        outbound: mpsc::UnboundedSender<DirectTunnelEnvelope>,
        channel_routes: Arc<Mutex<HashMap<String, mpsc::UnboundedSender<DirectTunnelEnvelope>>>>,
        closed: Arc<AtomicBool>,
        closed_notify: Arc<Notify>,
        incoming_tx: mpsc::UnboundedSender<DirectIncomingChannel>,
        incoming_rx: mpsc::UnboundedReceiver<DirectIncomingChannel>,
        mut packet_rx: mpsc::UnboundedReceiver<ReceivedDirectTunnelEnvelope>,
        mut buffered_packets: VecDeque<ReceivedDirectTunnelEnvelope>,
    ) -> (Self, mpsc::UnboundedReceiver<DirectIncomingChannel>) {
        let routes_for_reader = channel_routes.clone();
        let closed_for_reader = closed.clone();
        let closed_notify_for_reader = closed_notify.clone();
        let rendezvous_id_for_reader = rendezvous_id.clone();
        tokio::spawn(async move {
            loop {
                let packet = if let Some(packet) = buffered_packets.pop_front() {
                    Some(packet)
                } else {
                    packet_rx.recv().await
                };
                let Some(ReceivedDirectTunnelEnvelope {
                    envelope,
                    sender_addr,
                }) = packet
                else {
                    break;
                };
                if sender_addr != peer_addr {
                    continue;
                }

                match envelope {
                    DirectTunnelEnvelope::OpenChannel {
                        rendezvous_id,
                        channel_id,
                        service_id,
                        source_device_id,
                        source_ephemeral_public_key,
                        source_open_signature,
                    } => {
                        if rendezvous_id != rendezvous_id_for_reader {
                            continue;
                        }
                        let _ = incoming_tx.send(DirectIncomingChannel {
                            rendezvous_id,
                            channel_id,
                            service_id,
                            source_device_id,
                            source_ephemeral_public_key,
                            source_open_signature,
                        });
                    }
                    envelope => {
                        route_direct_channel_message(&routes_for_reader, envelope).await;
                    }
                }
            }

            clear_all_direct_channels(&routes_for_reader, &rendezvous_id_for_reader).await;
            closed_for_reader.store(true, Ordering::SeqCst);
            closed_notify_for_reader.notify_waiters();
        });

        (
            Self {
                rendezvous_id,
                peer_addr,
                outbound,
                channel_routes,
                closed,
                closed_notify,
            },
            incoming_rx,
        )
    }

    async fn from_shared_hub(
        rendezvous_id: String,
        hub: SharedDirectSocketHub,
        peer_addr: SocketAddr,
        session_rx: mpsc::UnboundedReceiver<SharedDirectInboundPacket>,
        buffered_packets: VecDeque<ReceivedDirectTunnelEnvelope>,
    ) -> (Self, mpsc::UnboundedReceiver<DirectIncomingChannel>) {
        let (outbound, mut outbound_rx) = mpsc::unbounded_channel::<DirectTunnelEnvelope>();
        let (incoming_tx, incoming_rx) = mpsc::unbounded_channel::<DirectIncomingChannel>();
        let channel_routes = Arc::new(Mutex::new(HashMap::new()));
        let closed = Arc::new(AtomicBool::new(false));
        let closed_notify = Arc::new(Notify::new());

        let socket_for_writer = hub.socket();
        tokio::spawn(async move {
            while let Some(envelope) = outbound_rx.recv().await {
                let encoded = match serde_json::to_vec(&envelope) {
                    Ok(encoded) => encoded,
                    Err(err) => {
                        warn!("failed to encode direct tunnel envelope: {err}");
                        break;
                    }
                };
                if let Err(err) = socket_for_writer.send_to(&encoded, peer_addr).await {
                    warn!("failed to write direct UDP frame to {peer_addr}: {err}");
                    break;
                }
            }
        });

        let (packet_tx, packet_rx) = mpsc::unbounded_channel::<ReceivedDirectTunnelEnvelope>();
        tokio::spawn(async move {
            let mut session_rx = session_rx;
            while let Some(packet) = session_rx.recv().await {
                let SharedDirectInboundPacket::Tunnel {
                    envelope,
                    sender_addr,
                } = packet
                else {
                    continue;
                };
                if packet_tx
                    .send(ReceivedDirectTunnelEnvelope {
                        envelope,
                        sender_addr,
                    })
                    .is_err()
                {
                    break;
                }
            }
        });

        Self::from_packet_receiver(
            rendezvous_id,
            peer_addr,
            outbound,
            channel_routes,
            closed,
            closed_notify,
            incoming_tx,
            incoming_rx,
            packet_rx,
            buffered_packets,
        )
    }

    fn send(&self, envelope: DirectTunnelEnvelope) -> Result<()> {
        self.outbound
            .send(envelope)
            .map_err(|_| anyhow!("direct UDP connection is closed"))
    }

    async fn register_channel(
        &self,
        channel_id: impl Into<String>,
    ) -> mpsc::UnboundedReceiver<DirectTunnelEnvelope> {
        let channel_id = channel_id.into();
        let (tx, rx) = mpsc::unbounded_channel();
        let mut routes = self.channel_routes.lock().await;
        routes.insert(channel_id, tx);
        rx
    }

    async fn unregister_channel(&self, channel_id: &str) {
        let mut routes = self.channel_routes.lock().await;
        routes.remove(channel_id);
    }

    pub(crate) async fn wait_closed(&self) {
        if self.closed.load(Ordering::SeqCst) {
            return;
        }
        self.closed_notify.notified().await;
    }
}

pub async fn run_direct_probe(
    local_identity: DeviceIdentity,
    local_device_id: String,
    peer_public_key: String,
    session: DirectRendezvousSession,
    local_bind_addr: String,
    timeout_duration: Duration,
) -> Result<DirectProbeResult> {
    Ok(probe_direct_path(
        local_identity,
        local_device_id,
        peer_public_key,
        session,
        local_bind_addr,
        timeout_duration,
    )
    .await?
    .probe_result)
}

pub(crate) async fn run_direct_tcp_forward(
    local_identity: DeviceIdentity,
    local_device_id: String,
    peer_public_key: String,
    session: DirectRendezvousSession,
    resolved_service: ResolvedService,
    local_udp_bind_addr: String,
    local_tcp_bind_addr: String,
    timeout_duration: Duration,
    runtime_hook: Option<ForwardRuntimeHook>,
) -> Result<()> {
    let prepared = prepare_direct_tcp_forward(
        local_identity.clone(),
        local_device_id,
        peer_public_key,
        session,
        local_udp_bind_addr,
        timeout_duration,
        runtime_hook.clone(),
    )
    .await?;

    run_prepared_direct_tcp_forward(
        local_identity,
        resolved_service,
        prepared,
        local_tcp_bind_addr,
        runtime_hook,
    )
    .await
}

pub(crate) async fn prepare_direct_tcp_forward(
    local_identity: DeviceIdentity,
    local_device_id: String,
    peer_public_key: String,
    session: DirectRendezvousSession,
    local_udp_bind_addr: String,
    timeout_duration: Duration,
    runtime_hook: Option<ForwardRuntimeHook>,
) -> Result<PreparedDirectForward> {
    let established = match probe_direct_path(
        local_identity,
        local_device_id,
        peer_public_key,
        session.clone(),
        local_udp_bind_addr,
        timeout_duration,
    )
    .await
    {
        Ok(established) => established,
        Err(err) => {
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_failure("probe", err.to_string());
            }
            return Err(err);
        }
    };

    Ok(PreparedDirectForward {
        rendezvous_id: session.rendezvous_id,
        socket: established.socket,
        peer_addr: established.peer_addr,
        peer_device_id: established.peer_device_id,
    })
}

pub(crate) async fn run_prepared_direct_tcp_forward(
    local_identity: DeviceIdentity,
    resolved_service: ResolvedService,
    prepared: PreparedDirectForward,
    local_tcp_bind_addr: String,
    runtime_hook: Option<ForwardRuntimeHook>,
) -> Result<()> {
    let PreparedDirectForward {
        rendezvous_id,
        socket,
        peer_addr,
        peer_device_id,
    } = prepared;
    let (connection, _) = DirectConnection::from_socket(rendezvous_id.clone(), socket, peer_addr);
    let listener = match TcpListener::bind(&local_tcp_bind_addr).await {
        Ok(listener) => listener,
        Err(err) => {
            let err = anyhow!(
                "failed to bind local direct forward address {}: {}",
                local_tcp_bind_addr,
                err
            );
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_failure("local_bind", err.to_string());
            }
            return Err(err);
        }
    };
    if let Some(runtime_hook) = &runtime_hook {
        runtime_hook.note_state(
            "direct_active",
            format!(
                "direct active on {} via rendezvous {}",
                local_tcp_bind_addr, rendezvous_id
            ),
        );
    }
    info!(
        "direct-forwarding {} to service {} on device {} via {} ({})",
        local_tcp_bind_addr,
        resolved_service.service.name,
        resolved_service.service.owner_device_id,
        connection.peer_addr,
        peer_device_id
    );

    loop {
        let (stream, peer_addr) = tokio::select! {
            accept_result = listener.accept() => {
                accept_result.with_context(|| format!("failed to accept on {local_tcp_bind_addr}"))?
            }
            _ = connection.wait_closed() => {
                let err = anyhow!("direct UDP connection closed for local forward {local_tcp_bind_addr}");
                if let Some(runtime_hook) = &runtime_hook {
                    runtime_hook.note_failure("data_plane", err.to_string());
                }
                return Err(err);
            }
        };
        let connection = connection.clone();
        let device_identity = local_identity.clone();
        let resolved_service = resolved_service.clone();
        let runtime_hook = runtime_hook.clone();
        tokio::spawn(async move {
            if let Err(err) = handle_local_direct_forward_connection(
                connection,
                device_identity,
                resolved_service,
                stream,
                runtime_hook,
            )
            .await
            {
                warn!("direct forward connection from {peer_addr} failed: {err}");
            }
        });
    }
}

pub(crate) fn into_direct_forward_ingress(prepared: PreparedDirectForward) -> DirectForwardIngress {
    let PreparedDirectForward {
        rendezvous_id,
        socket,
        peer_addr,
        peer_device_id,
    } = prepared;
    let (connection, _) = DirectConnection::from_socket(rendezvous_id.clone(), socket, peer_addr);
    DirectForwardIngress {
        rendezvous_id,
        connection,
        peer_device_id,
    }
}

pub(crate) async fn bind_direct_udp_socket(local_bind_addr: &str) -> Result<Arc<UdpSocket>> {
    Ok(Arc::new(
        UdpSocket::bind(local_bind_addr)
            .await
            .with_context(|| format!("failed to bind UDP socket on {local_bind_addr}"))?,
    ))
}

pub(crate) async fn run_direct_tcp_serve_on_hub(
    local_identity: DeviceIdentity,
    relay_identity: RelayKeypair,
    local_device_id: String,
    peer_public_key: String,
    session: DirectRendezvousSession,
    published_service: PublishedServiceConfig,
    local_udp_bind_addr: String,
    hub: SharedDirectSocketHub,
    timeout_duration: Duration,
    runtime_hook: Option<PublishedServiceRuntimeHook>,
) -> Result<()> {
    let session_rx = hub.register_session(&session.rendezvous_id).await?;
    let outcome = async {
        let mut session_started = false;
        let prepared = match probe_direct_path_on_hub(
            local_identity,
            local_device_id,
            peer_public_key.clone(),
            session.clone(),
            hub.socket(),
            session_rx,
            timeout_duration,
        )
        .await
        {
            Ok(prepared) => prepared,
            Err(err) => {
                if let Some(runtime_hook) = &runtime_hook {
                    runtime_hook.note_failure("probe", err.to_string());
                }
                return Err(err);
            }
        };

        let peer_device_id = prepared.peer_device_id.clone();
        let (connection, mut incoming_rx) = DirectConnection::from_shared_hub(
            session.rendezvous_id.clone(),
            hub.clone(),
            prepared.peer_addr,
            prepared.session_rx,
            prepared.buffered_tunnel_packets,
        )
        .await;
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_session_started();
            session_started = true;
            runtime_hook.note_state(
                "direct_active",
                format!(
                    "serving rendezvous {} via {}",
                    session.rendezvous_id, local_udp_bind_addr
                ),
            );
        }
        info!(
            "direct-serve for service {} established with {} via {} on shared hub",
            published_service.name, peer_device_id, connection.peer_addr
        );

        let result = loop {
            tokio::select! {
                maybe_channel = incoming_rx.recv() => {
                    let Some(channel) = maybe_channel else {
                        break Ok(());
                    };
                    let connection = connection.clone();
                    let relay_identity = relay_identity.clone();
                    let peer_public_key = peer_public_key.clone();
                    let published_service = published_service.clone();
                    let expected_source_device_id = peer_device_id.clone();
                    let expected_service_id = session.service_id.clone();
                    let runtime_hook = runtime_hook.clone();
                    tokio::spawn(async move {
                        if let Err(err) = handle_incoming_direct_channel(
                            connection,
                            relay_identity,
                            peer_public_key,
                            expected_source_device_id,
                            expected_service_id,
                            published_service,
                            channel,
                            runtime_hook,
                        )
                        .await {
                            warn!("incoming direct channel failed: {err}");
                        }
                    });
                }
                _ = connection.wait_closed() => {
                    if let Some(runtime_hook) = &runtime_hook {
                        runtime_hook.note_failure(
                            "data_plane",
                            format!(
                                "direct UDP connection closed for service {} on rendezvous {}",
                                published_service.name, session.rendezvous_id
                            ),
                        );
                    }
                    break Ok(());
                }
                _ = tokio::signal::ctrl_c() => break Ok(()),
            }
        };

        if session_started {
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_session_ended();
            }
        }

        result
    }
    .await;
    hub.unregister_session(&session.rendezvous_id).await;
    outcome
}

pub(crate) async fn run_direct_tcp_serve(
    local_identity: DeviceIdentity,
    relay_identity: RelayKeypair,
    local_device_id: String,
    peer_public_key: String,
    session: DirectRendezvousSession,
    published_service: PublishedServiceConfig,
    local_udp_bind_addr: String,
    timeout_duration: Duration,
    runtime_hook: Option<PublishedServiceRuntimeHook>,
) -> Result<()> {
    let socket = bind_direct_udp_socket(&local_udp_bind_addr).await?;
    run_direct_tcp_serve_with_socket(
        local_identity,
        relay_identity,
        local_device_id,
        peer_public_key,
        session,
        published_service,
        local_udp_bind_addr,
        socket,
        timeout_duration,
        runtime_hook,
    )
    .await
}

pub(crate) async fn run_direct_tcp_serve_with_socket(
    local_identity: DeviceIdentity,
    relay_identity: RelayKeypair,
    local_device_id: String,
    peer_public_key: String,
    session: DirectRendezvousSession,
    published_service: PublishedServiceConfig,
    local_udp_bind_addr: String,
    socket: Arc<UdpSocket>,
    timeout_duration: Duration,
    runtime_hook: Option<PublishedServiceRuntimeHook>,
) -> Result<()> {
    let mut session_started = false;
    let established = match probe_direct_path_on_socket(
        local_identity,
        local_device_id,
        peer_public_key.clone(),
        session.clone(),
        socket,
        timeout_duration,
    )
    .await
    {
        Ok(established) => established,
        Err(err) => {
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_failure("probe", err.to_string());
            }
            return Err(err);
        }
    };

    let (connection, mut incoming_rx) = DirectConnection::from_socket(
        session.rendezvous_id.clone(),
        established.socket,
        established.peer_addr,
    );
    if let Some(runtime_hook) = &runtime_hook {
        runtime_hook.note_session_started();
        session_started = true;
        runtime_hook.note_state(
            "direct_active",
            format!(
                "serving rendezvous {} via {}",
                session.rendezvous_id, local_udp_bind_addr
            ),
        );
    }
    info!(
        "direct-serve for service {} established with {} via {}",
        published_service.name, established.peer_device_id, connection.peer_addr
    );

    let result = loop {
        tokio::select! {
            maybe_channel = incoming_rx.recv() => {
                let Some(channel) = maybe_channel else {
                    break Ok(());
                };
                let connection = connection.clone();
                let relay_identity = relay_identity.clone();
                let peer_public_key = peer_public_key.clone();
                let published_service = published_service.clone();
                let expected_source_device_id = established.peer_device_id.clone();
                let expected_service_id = session.service_id.clone();
                let runtime_hook = runtime_hook.clone();
                tokio::spawn(async move {
                    if let Err(err) = handle_incoming_direct_channel(
                        connection,
                        relay_identity,
                        peer_public_key,
                        expected_source_device_id,
                        expected_service_id,
                        published_service,
                        channel,
                        runtime_hook,
                    )
                    .await {
                        warn!("incoming direct channel failed: {err}");
                    }
                });
            }
            _ = connection.wait_closed() => {
                if let Some(runtime_hook) = &runtime_hook {
                    runtime_hook.note_failure(
                        "data_plane",
                        format!(
                            "direct UDP connection closed for service {} on rendezvous {}",
                            published_service.name, session.rendezvous_id
                        ),
                    );
                }
                break Ok(());
            }
            _ = tokio::signal::ctrl_c() => break Ok(()),
        }
    };

    if session_started {
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_session_ended();
        }
    }

    result
}

async fn probe_direct_path(
    local_identity: DeviceIdentity,
    local_device_id: String,
    peer_public_key: String,
    session: DirectRendezvousSession,
    local_bind_addr: String,
    timeout_duration: Duration,
) -> Result<EstablishedDirectPath> {
    let socket = bind_direct_udp_socket(&local_bind_addr).await?;
    probe_direct_path_on_socket(
        local_identity,
        local_device_id,
        peer_public_key,
        session,
        socket,
        timeout_duration,
    )
    .await
}

async fn probe_direct_path_on_hub(
    local_identity: DeviceIdentity,
    local_device_id: String,
    peer_public_key: String,
    session: DirectRendezvousSession,
    socket: Arc<UdpSocket>,
    mut session_rx: mpsc::UnboundedReceiver<SharedDirectInboundPacket>,
    timeout_duration: Duration,
) -> Result<PreparedSharedDirectPath> {
    let (role, peer_device_id, peer_candidates) = if session.source_device_id == local_device_id {
        (
            "source".to_string(),
            session.target_device_id.clone(),
            session.target.candidates.clone(),
        )
    } else if session.target_device_id == local_device_id {
        (
            "target".to_string(),
            session.source_device_id.clone(),
            session.source.candidates.clone(),
        )
    } else {
        return Err(anyhow!(
            "local device {} is not a participant in rendezvous {}",
            local_device_id,
            session.rendezvous_id
        ));
    };

    if device_id_from_public_key(&peer_public_key) != peer_device_id {
        return Err(anyhow!(
            "peer device {} has an invalid identity key binding",
            peer_device_id
        ));
    }

    let sorted_peer_candidates = sort_candidates(peer_candidates);
    let peer_addrs = sorted_peer_candidates
        .iter()
        .filter_map(parse_socket_addr)
        .collect::<Vec<_>>();
    if peer_addrs.is_empty() {
        return Err(anyhow!(
            "rendezvous {} does not contain any valid peer UDP candidates",
            session.rendezvous_id
        ));
    }
    let local_socket_addr = socket
        .local_addr()
        .context("failed to read bound UDP socket address")?;

    let hello_nonce = generate_token("probe");
    let hello_signature = local_identity.sign_base64(&direct_probe_hello_message(
        &session.rendezvous_id,
        &local_device_id,
        &hello_nonce,
    ));
    let hello_envelope = DirectProbeEnvelope::Hello {
        rendezvous_id: session.rendezvous_id.clone(),
        sender_device_id: local_device_id.clone(),
        hello_nonce: hello_nonce.clone(),
        signature: hello_signature,
    };
    let hello_bytes =
        serde_json::to_vec(&hello_envelope).context("failed to encode UDP probe hello")?;

    let started_at = Instant::now();
    let deadline = started_at + timeout_duration;
    let mut send_interval = time::interval(DIRECT_PROBE_SEND_INTERVAL);
    send_interval.set_missed_tick_behavior(MissedTickBehavior::Delay);
    let _ = send_interval.tick().await;
    send_probe_to_all(socket.as_ref(), &hello_bytes, &peer_addrs).await;
    let mut buffered_tunnel_packets = VecDeque::new();

    loop {
        tokio::select! {
            _ = send_interval.tick() => {
                send_probe_to_all(socket.as_ref(), &hello_bytes, &peer_addrs).await;
            }
            maybe_packet = session_rx.recv() => {
                let Some(packet) = maybe_packet else {
                    return Err(anyhow!(
                        "shared direct UDP hub closed while probing rendezvous {}",
                        session.rendezvous_id
                    ));
                };
                match packet {
                    SharedDirectInboundPacket::Probe { envelope, sender_addr } => {
                        if let Some(_result) = handle_incoming_probe_envelope(
                            socket.as_ref(),
                            &local_identity,
                            &local_device_id,
                            &peer_public_key,
                            &session.rendezvous_id,
                            &hello_nonce,
                            envelope,
                            sender_addr,
                            &sorted_peer_candidates,
                            &role,
                            &peer_device_id,
                            local_socket_addr,
                            started_at.elapsed().as_millis(),
                        )
                        .await? {
                            return Ok(PreparedSharedDirectPath {
                                peer_addr: sender_addr,
                                peer_device_id,
                                session_rx,
                                buffered_tunnel_packets,
                            });
                        }
                    }
                    SharedDirectInboundPacket::Tunnel { envelope, sender_addr } => {
                        buffered_tunnel_packets.push_back(ReceivedDirectTunnelEnvelope {
                            envelope,
                            sender_addr,
                        });
                    }
                }
            }
            _ = time::sleep_until(deadline) => {
                return Err(anyhow!(
                    "timed out waiting for a verified UDP probe acknowledgement from {}",
                    peer_device_id
                ));
            }
        }
    }
}

async fn probe_direct_path_on_socket(
    local_identity: DeviceIdentity,
    local_device_id: String,
    peer_public_key: String,
    session: DirectRendezvousSession,
    socket: Arc<UdpSocket>,
    timeout_duration: Duration,
) -> Result<EstablishedDirectPath> {
    let (role, peer_device_id, peer_candidates) = if session.source_device_id == local_device_id {
        (
            "source".to_string(),
            session.target_device_id.clone(),
            session.target.candidates.clone(),
        )
    } else if session.target_device_id == local_device_id {
        (
            "target".to_string(),
            session.source_device_id.clone(),
            session.source.candidates.clone(),
        )
    } else {
        return Err(anyhow!(
            "local device {} is not a participant in rendezvous {}",
            local_device_id,
            session.rendezvous_id
        ));
    };

    if device_id_from_public_key(&peer_public_key) != peer_device_id {
        return Err(anyhow!(
            "peer device {} has an invalid identity key binding",
            peer_device_id
        ));
    }

    let sorted_peer_candidates = sort_candidates(peer_candidates);
    let peer_addrs = sorted_peer_candidates
        .iter()
        .filter_map(parse_socket_addr)
        .collect::<Vec<_>>();
    if peer_addrs.is_empty() {
        return Err(anyhow!(
            "rendezvous {} does not contain any valid peer UDP candidates",
            session.rendezvous_id
        ));
    }
    let local_socket_addr = socket
        .local_addr()
        .context("failed to read bound UDP socket address")?;

    let hello_nonce = generate_token("probe");
    let hello_signature = local_identity.sign_base64(&direct_probe_hello_message(
        &session.rendezvous_id,
        &local_device_id,
        &hello_nonce,
    ));
    let hello_envelope = DirectProbeEnvelope::Hello {
        rendezvous_id: session.rendezvous_id.clone(),
        sender_device_id: local_device_id.clone(),
        hello_nonce: hello_nonce.clone(),
        signature: hello_signature,
    };
    let hello_bytes =
        serde_json::to_vec(&hello_envelope).context("failed to encode UDP probe hello")?;

    let started_at = Instant::now();
    let deadline = started_at + timeout_duration;
    let mut send_interval = time::interval(DIRECT_PROBE_SEND_INTERVAL);
    send_interval.set_missed_tick_behavior(MissedTickBehavior::Delay);
    let _ = send_interval.tick().await;
    send_probe_to_all(socket.as_ref(), &hello_bytes, &peer_addrs).await;

    let mut recv_buf = [0u8; 2048];
    loop {
        tokio::select! {
            _ = send_interval.tick() => {
                send_probe_to_all(socket.as_ref(), &hello_bytes, &peer_addrs).await;
            }
            recv_result = socket.recv_from(&mut recv_buf) => {
                let (size, sender_addr) = recv_result.context("failed to receive UDP probe packet")?;
                let envelope = match serde_json::from_slice::<DirectProbeEnvelope>(&recv_buf[..size]) {
                    Ok(envelope) => envelope,
                    Err(_) => continue,
                };
                if let Some(result) = handle_incoming_probe_envelope(
                    socket.as_ref(),
                    &local_identity,
                    &local_device_id,
                    &peer_public_key,
                    &session.rendezvous_id,
                    &hello_nonce,
                    envelope,
                    sender_addr,
                    &sorted_peer_candidates,
                    &role,
                    &peer_device_id,
                    local_socket_addr,
                    started_at.elapsed().as_millis(),
                )
                .await? {
                    return Ok(EstablishedDirectPath {
                        socket,
                        peer_addr: sender_addr,
                        peer_device_id,
                        probe_result: result,
                    });
                }
            }
            _ = time::sleep_until(deadline) => {
                return Err(anyhow!(
                    "timed out waiting for a verified UDP probe acknowledgement from {}",
                    peer_device_id
                ));
            }
        }
    }
}

async fn handle_incoming_probe_envelope(
    socket: &UdpSocket,
    local_identity: &DeviceIdentity,
    local_device_id: &str,
    peer_public_key: &str,
    rendezvous_id: &str,
    local_hello_nonce: &str,
    envelope: DirectProbeEnvelope,
    sender_addr: SocketAddr,
    peer_candidates: &[DirectConnectionCandidate],
    role: &str,
    peer_device_id: &str,
    local_socket_addr: SocketAddr,
    elapsed_ms: u128,
) -> Result<Option<DirectProbeResult>> {
    match envelope {
        DirectProbeEnvelope::Hello {
            rendezvous_id: incoming_rendezvous_id,
            sender_device_id,
            hello_nonce,
            signature,
        } => {
            if incoming_rendezvous_id != rendezvous_id || sender_device_id != peer_device_id {
                return Ok(None);
            }
            verify_signature_base64(
                peer_public_key,
                &direct_probe_hello_message(
                    &incoming_rendezvous_id,
                    &sender_device_id,
                    &hello_nonce,
                ),
                &signature,
            )
            .map_err(|err| anyhow!("peer UDP probe hello verification failed: {err}"))?;

            let ack_nonce = generate_token("ack");
            let ack = DirectProbeEnvelope::Ack {
                rendezvous_id: incoming_rendezvous_id.clone(),
                sender_device_id: local_device_id.to_string(),
                hello_nonce: hello_nonce.clone(),
                ack_nonce: ack_nonce.clone(),
                signature: local_identity.sign_base64(&direct_probe_ack_message(
                    &incoming_rendezvous_id,
                    local_device_id,
                    &hello_nonce,
                    &ack_nonce,
                )),
            };
            let ack_bytes =
                serde_json::to_vec(&ack).context("failed to encode UDP probe acknowledgement")?;
            socket
                .send_to(&ack_bytes, sender_addr)
                .await
                .with_context(|| {
                    format!("failed to send UDP probe acknowledgement to {sender_addr}")
                })?;
            Ok(Some(DirectProbeResult {
                rendezvous_id: rendezvous_id.to_string(),
                local_device_id: local_device_id.to_string(),
                peer_device_id: peer_device_id.to_string(),
                local_bind_addr: local_socket_addr.to_string(),
                selected_peer_addr: sender_addr.to_string(),
                selected_peer_candidate_type: peer_candidates
                    .iter()
                    .find(|candidate| candidate.addr == sender_addr.to_string())
                    .map(|candidate| candidate.candidate_type.clone()),
                role: role.to_string(),
                completion_kind: "received_hello".to_string(),
                ack_nonce,
                elapsed_ms,
            }))
        }
        DirectProbeEnvelope::Ack {
            rendezvous_id: incoming_rendezvous_id,
            sender_device_id,
            hello_nonce,
            ack_nonce,
            signature,
        } => {
            if incoming_rendezvous_id != rendezvous_id
                || sender_device_id != peer_device_id
                || hello_nonce != local_hello_nonce
            {
                return Ok(None);
            }
            verify_signature_base64(
                peer_public_key,
                &direct_probe_ack_message(
                    &incoming_rendezvous_id,
                    &sender_device_id,
                    &hello_nonce,
                    &ack_nonce,
                ),
                &signature,
            )
            .map_err(|err| anyhow!("peer UDP probe acknowledgement verification failed: {err}"))?;

            Ok(Some(DirectProbeResult {
                rendezvous_id: rendezvous_id.to_string(),
                local_device_id: local_device_id.to_string(),
                peer_device_id: peer_device_id.to_string(),
                local_bind_addr: local_socket_addr.to_string(),
                selected_peer_addr: sender_addr.to_string(),
                selected_peer_candidate_type: peer_candidates
                    .iter()
                    .find(|candidate| candidate.addr == sender_addr.to_string())
                    .map(|candidate| candidate.candidate_type.clone()),
                role: role.to_string(),
                completion_kind: "received_ack".to_string(),
                ack_nonce,
                elapsed_ms,
            }))
        }
    }
}

pub(crate) async fn handle_local_direct_forward_connection(
    connection: DirectConnection,
    device_identity: DeviceIdentity,
    resolved_service: ResolvedService,
    socket: TcpStream,
    runtime_hook: Option<ForwardRuntimeHook>,
) -> Result<()> {
    let prepared = match prepare_local_direct_forward_connection(
        connection,
        device_identity,
        resolved_service,
        socket,
        runtime_hook.clone(),
    )
    .await
    {
        Ok(prepared) => prepared,
        Err((_, err)) => return Err(err),
    };

    run_prepared_local_direct_forward_connection(prepared, runtime_hook).await
}

pub(crate) async fn prepare_local_direct_forward_connection(
    connection: DirectConnection,
    device_identity: DeviceIdentity,
    resolved_service: ResolvedService,
    socket: TcpStream,
    runtime_hook: Option<ForwardRuntimeHook>,
) -> Result<PreparedLocalDirectForward, (TcpStream, anyhow::Error)> {
    let peer_addr = socket
        .peer_addr()
        .map(|addr| addr.to_string())
        .unwrap_or_else(|_| "unknown".to_string());
    let source_device_id = device_identity.device_id();
    let ephemeral_relay_key = RelayKeypair::generate();
    let source_ephemeral_public_key = ephemeral_relay_key.public_key_base64();
    let channel_id = generate_token("dch");
    let mut channel_rx = connection.register_channel(channel_id.clone()).await;
    let source_open_signature = device_identity.sign_base64(&relay_channel_open_message(
        &channel_id,
        &resolved_service.service.service_id,
        &source_device_id,
        &source_ephemeral_public_key,
    ));

    if consume_direct_prebridge_fail_once() {
        let err =
            anyhow!("forced direct pre-bridge failure for smoke automation before channel open");
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_failure("channel_open", err.to_string());
        }
        connection.unregister_channel(&channel_id).await;
        return Err((socket, err));
    }

    if let Err(err) = connection.send(DirectTunnelEnvelope::OpenChannel {
        rendezvous_id: connection.rendezvous_id.clone(),
        channel_id: channel_id.clone(),
        service_id: resolved_service.service.service_id.clone(),
        source_device_id,
        source_ephemeral_public_key,
        source_open_signature,
    }) {
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_failure("channel_open", err.to_string());
        }
        connection.unregister_channel(&channel_id).await;
        return Err((socket, err));
    }

    let open_result = match timeout(
        Duration::from_secs(10),
        wait_for_direct_channel_open(&mut channel_rx),
    )
    .await
    {
        Ok(Ok(open_result)) => open_result,
        Ok(Err(err)) => {
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_failure("channel_open", err.to_string());
            }
            connection.unregister_channel(&channel_id).await;
            return Err((socket, err));
        }
        Err(_) => {
            let err = anyhow!("timed out waiting for remote service to accept direct channel");
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_failure("channel_open", err.to_string());
            }
            connection.unregister_channel(&channel_id).await;
            return Err((socket, err));
        }
    };

    match open_result {
        ChannelOpenState::Accepted => {
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_connection(peer_addr.clone());
            }
            let shared_secret = match ephemeral_relay_key
                .shared_secret_with_public_key(&resolved_service.target_relay_public_key)
                .map_err(|err| anyhow!("failed to derive direct shared secret: {err}"))
            {
                Ok(shared_secret) => shared_secret,
                Err(err) => {
                    if let Some(runtime_hook) = &runtime_hook {
                        runtime_hook.note_failure("channel_open", err.to_string());
                    }
                    connection.unregister_channel(&channel_id).await;
                    return Err((socket, err));
                }
            };
            let (sender, receiver) =
                secure_channel_pair(shared_secret, &channel_id, SecureChannelRole::Initiator);
            Ok(PreparedLocalDirectForward {
                peer_addr,
                channel_id,
                socket,
                channel_rx,
                secure_sender: sender,
                secure_receiver: receiver,
                connection,
            })
        }
        ChannelOpenState::Rejected(reason) => {
            connection.unregister_channel(&channel_id).await;
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_failure(
                    "channel_open",
                    format!("remote service rejected direct channel: {reason}"),
                );
            }
            Err((
                socket,
                anyhow!("remote service rejected direct channel: {reason}"),
            ))
        }
    }
}

pub(crate) async fn run_prepared_local_direct_forward_connection(
    prepared: PreparedLocalDirectForward,
    runtime_hook: Option<ForwardRuntimeHook>,
) -> Result<()> {
    let PreparedLocalDirectForward {
        peer_addr,
        connection,
        channel_id,
        socket,
        channel_rx,
        secure_sender,
        secure_receiver,
    } = prepared;

    let bridge_result = bridge_stream_with_direct_channel(
        connection,
        channel_id,
        socket,
        channel_rx,
        secure_sender,
        secure_receiver,
        runtime_hook
            .as_ref()
            .map(|hook| DirectRuntimeMetricsHook::Forward(hook.clone())),
    )
    .await;
    if let Some(runtime_hook) = &runtime_hook {
        runtime_hook.note_connection_closed(peer_addr);
        runtime_hook.clear_direct_metrics();
    }
    if let Err(err) = &bridge_result {
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_failure("data_plane", err.to_string());
        }
    }
    bridge_result
}

async fn handle_incoming_direct_channel(
    connection: DirectConnection,
    relay_identity: RelayKeypair,
    source_identity_public_key: String,
    expected_source_device_id: String,
    expected_service_id: String,
    published_service: PublishedServiceConfig,
    channel: DirectIncomingChannel,
    runtime_hook: Option<PublishedServiceRuntimeHook>,
) -> Result<()> {
    if channel.source_device_id != expected_source_device_id {
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_failure(
                "channel_open",
                "unexpected source device for direct channel",
            );
        }
        connection.send(DirectTunnelEnvelope::ChannelRejected {
            rendezvous_id: channel.rendezvous_id,
            channel_id: channel.channel_id,
            reason: "unexpected source device for direct channel".to_string(),
        })?;
        return Ok(());
    }

    if channel.service_id != expected_service_id {
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_failure("channel_open", "unexpected service for direct channel");
        }
        connection.send(DirectTunnelEnvelope::ChannelRejected {
            rendezvous_id: channel.rendezvous_id,
            channel_id: channel.channel_id,
            reason: "unexpected service for direct channel".to_string(),
        })?;
        return Ok(());
    }

    if device_id_from_public_key(&source_identity_public_key) != channel.source_device_id {
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_failure(
                "channel_open",
                "source device identity key does not match source_device_id",
            );
        }
        connection.send(DirectTunnelEnvelope::ChannelRejected {
            rendezvous_id: channel.rendezvous_id,
            channel_id: channel.channel_id,
            reason: "source device identity key does not match source_device_id".to_string(),
        })?;
        return Ok(());
    }

    if let Err(err) = verify_signature_base64(
        &source_identity_public_key,
        &relay_channel_open_message(
            &channel.channel_id,
            &channel.service_id,
            &channel.source_device_id,
            &channel.source_ephemeral_public_key,
        ),
        &channel.source_open_signature,
    )
    .map_err(|err| anyhow!("source direct open signature verification failed: {err}"))
    {
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_failure("channel_open", err.to_string());
        }
        return Err(err);
    }

    let channel_rx = connection
        .register_channel(channel.channel_id.clone())
        .await;
    let socket = match TcpStream::connect((
        published_service.target_host.as_str(),
        published_service.target_port,
    ))
    .await
    {
        Ok(socket) => socket,
        Err(err) => {
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_failure(
                    "channel_open",
                    format!("failed to connect local target: {err}"),
                );
            }
            connection.send(DirectTunnelEnvelope::ChannelRejected {
                rendezvous_id: channel.rendezvous_id,
                channel_id: channel.channel_id.clone(),
                reason: format!("failed to connect local target: {err}"),
            })?;
            connection.unregister_channel(&channel.channel_id).await;
            return Ok(());
        }
    };

    let shared_secret = match relay_identity
        .shared_secret_with_public_key(&channel.source_ephemeral_public_key)
        .map_err(|err| anyhow!("failed to derive direct shared secret: {err}"))
    {
        Ok(shared_secret) => shared_secret,
        Err(err) => {
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_failure("channel_open", err.to_string());
            }
            connection.unregister_channel(&channel.channel_id).await;
            return Err(err);
        }
    };
    let (sender, receiver) = secure_channel_pair(
        shared_secret,
        &channel.channel_id,
        SecureChannelRole::Responder,
    );

    connection.send(DirectTunnelEnvelope::ChannelAccepted {
        rendezvous_id: channel.rendezvous_id,
        channel_id: channel.channel_id.clone(),
    })?;
    if let Some(runtime_hook) = &runtime_hook {
        runtime_hook.note_connection(channel.source_device_id.clone());
    }

    let bridge_result = bridge_stream_with_direct_channel(
        connection,
        channel.channel_id,
        socket,
        channel_rx,
        sender,
        receiver,
        runtime_hook
            .as_ref()
            .map(|hook| DirectRuntimeMetricsHook::Service(hook.clone())),
    )
    .await;
    if let Some(runtime_hook) = &runtime_hook {
        runtime_hook.clear_direct_metrics();
    }
    if let Err(err) = &bridge_result {
        if let Some(runtime_hook) = &runtime_hook {
            runtime_hook.note_failure("data_plane", err.to_string());
        }
    }
    bridge_result
}

async fn wait_for_direct_channel_open(
    channel_rx: &mut mpsc::UnboundedReceiver<DirectTunnelEnvelope>,
) -> Result<ChannelOpenState> {
    while let Some(envelope) = channel_rx.recv().await {
        match envelope {
            DirectTunnelEnvelope::ChannelAccepted { .. } => {
                return Ok(ChannelOpenState::Accepted);
            }
            DirectTunnelEnvelope::ChannelRejected { reason, .. } => {
                return Ok(ChannelOpenState::Rejected(reason));
            }
            DirectTunnelEnvelope::ChannelClose { reason, .. } => {
                return Ok(ChannelOpenState::Rejected(
                    reason.unwrap_or_else(|| "channel closed before open".to_string()),
                ));
            }
            DirectTunnelEnvelope::ChannelData { .. }
            | DirectTunnelEnvelope::ChannelAck { .. }
            | DirectTunnelEnvelope::ChannelKeepalive { .. }
            | DirectTunnelEnvelope::ChannelKeepaliveAck { .. }
            | DirectTunnelEnvelope::ChannelCloseAck { .. }
            | DirectTunnelEnvelope::OpenChannel { .. } => {}
        }
    }

    bail!("direct UDP connection closed while opening channel")
}

async fn bridge_stream_with_direct_channel(
    connection: DirectConnection,
    channel_id: String,
    socket: TcpStream,
    mut channel_rx: mpsc::UnboundedReceiver<DirectTunnelEnvelope>,
    mut secure_sender: SecureSender,
    mut secure_receiver: SecureReceiver,
    metrics_hook: Option<DirectRuntimeMetricsHook>,
) -> Result<()> {
    let (mut socket_reader, mut socket_writer) = socket.into_split();
    let mut read_buffer = [0u8; DIRECT_TUNNEL_MAX_PLAINTEXT];
    let mut next_outbound_sequence = 0u64;
    let mut expected_inbound_sequence = 0u64;
    let mut pending_data = VecDeque::<PendingDirectData>::new();
    let mut pending_inbound = BTreeMap::<u64, String>::new();
    let mut pending_close: Option<PendingDirectClose> = None;
    let mut local_read_closed = false;
    let mut congestion = DirectCongestionController::default();
    let mut current_rto = DIRECT_TUNNEL_INITIAL_RTO;
    let mut smoothed_rtt_ms: Option<f64> = None;
    let mut rttvar_ms: Option<f64> = None;
    let mut ack_tracker = DirectAckTracker::default();
    let mut selective_scoreboard = DirectSelectiveAckScoreboard::default();
    let mut keepalive_nonce = 0u64;
    let mut keepalive_sent_count = 0u64;
    let mut keepalive_ack_count = 0u64;
    let mut last_peer_activity_at = Instant::now();
    let mut last_keepalive_sent_at = None;
    let mut metrics_reporter = DirectMetricsReporter::new(metrics_hook);
    let mut retransmit = time::interval(DIRECT_TUNNEL_TIMER_GRANULARITY);
    retransmit.set_missed_tick_behavior(MissedTickBehavior::Delay);
    let _ = retransmit.tick().await;
    let mut housekeeping = time::interval(DIRECT_TUNNEL_HOUSEKEEPING_INTERVAL);
    housekeeping.set_missed_tick_behavior(MissedTickBehavior::Delay);
    let _ = housekeeping.tick().await;
    metrics_reporter.force_report(
        &congestion,
        current_rto,
        smoothed_rtt_ms,
        pending_data.len(),
        pending_inbound.len(),
        keepalive_sent_count,
        keepalive_ack_count,
    );

    let result = loop {
        tokio::select! {
            read_result = socket_reader.read(&mut read_buffer), if !local_read_closed && pending_close.is_none() && pending_data.len() < congestion.send_window_size() => {
                let read = read_result.context("failed to read local tcp stream")?;
                if read == 0 {
                    local_read_closed = true;
                    maybe_start_pending_direct_close(
                        &connection,
                        &channel_id,
                        &connection.rendezvous_id,
                        &pending_data,
                        &mut pending_close,
                        local_read_closed,
                        next_outbound_sequence,
                    )?;
                } else {
                    let encrypted_data = secure_sender
                        .encrypt_to_base64(&read_buffer[..read])
                        .map_err(|err| anyhow!("failed to encrypt direct payload: {err}"))?;
                    let mut data = PendingDirectData {
                        sequence: next_outbound_sequence,
                        data_base64: encrypted_data,
                        attempts: 0,
                        last_sent_at: Instant::now(),
                    };
                    next_outbound_sequence += 1;
                    send_pending_direct_data(
                        &connection,
                        &channel_id,
                        &connection.rendezvous_id,
                        &mut data,
                    )?;
                    pending_data.push_back(data);
                }
                metrics_reporter.maybe_report(
                    &congestion,
                    current_rto,
                    smoothed_rtt_ms,
                    pending_data.len(),
                    pending_inbound.len(),
                    keepalive_sent_count,
                    keepalive_ack_count,
                );
            }
            _ = retransmit.tick(), if !pending_data.is_empty() || pending_close.is_some() => {
                let now = Instant::now();
                let mut retransmitted_data = false;
                let mut retransmitted_close = false;
                if let Some(sequence) = select_loss_recovery_sequence(
                    &pending_data,
                    &selective_scoreboard,
                    DirectLossRecoveryTrigger::Timeout { now, current_rto },
                ) {
                    if let Some(pending) = pending_data
                        .iter_mut()
                        .find(|pending| pending.sequence == sequence)
                    {
                        send_pending_direct_data(
                            &connection,
                            &channel_id,
                            &connection.rendezvous_id,
                            pending,
                        )?;
                        retransmitted_data = true;
                    }
                }
                if let Some(pending) = pending_close.as_mut() {
                    if now.duration_since(pending.last_sent_at) >= current_rto {
                        send_pending_direct_close(
                            &connection,
                            &channel_id,
                            &connection.rendezvous_id,
                            pending,
                        )?;
                        retransmitted_close = true;
                    }
                }
                if retransmitted_data {
                    congestion.on_timeout(pending_data.len());
                    current_rto = scale_duration(current_rto, 2.0)
                        .min(DIRECT_TUNNEL_MAX_RTO)
                        .max(DIRECT_TUNNEL_MIN_RTO);
                } else if retransmitted_close {
                    current_rto = scale_duration(current_rto, 2.0)
                        .min(DIRECT_TUNNEL_MAX_RTO)
                        .max(DIRECT_TUNNEL_MIN_RTO);
                }
                metrics_reporter.maybe_report(
                    &congestion,
                    current_rto,
                    smoothed_rtt_ms,
                    pending_data.len(),
                    pending_inbound.len(),
                    keepalive_sent_count,
                    keepalive_ack_count,
                );
            }
            _ = housekeeping.tick() => {
                let now = Instant::now();
                if direct_channel_peer_idle_timed_out(last_peer_activity_at, now) {
                    break Err(anyhow!(
                        "direct peer idle timeout on channel {} after {}s without peer traffic",
                        channel_id,
                        DIRECT_TUNNEL_PEER_IDLE_TIMEOUT.as_secs()
                    ));
                }

                if direct_channel_should_send_keepalive(
                    last_peer_activity_at,
                    last_keepalive_sent_at,
                    now,
                    pending_data.len(),
                    pending_close.is_some(),
                ) {
                    send_direct_channel_keepalive(
                        &connection,
                        &channel_id,
                        &connection.rendezvous_id,
                        keepalive_nonce,
                    )?;
                    keepalive_nonce = keepalive_nonce.saturating_add(1);
                    keepalive_sent_count = keepalive_sent_count.saturating_add(1);
                    last_keepalive_sent_at = Some(now);
                }
                metrics_reporter.maybe_report(
                    &congestion,
                    current_rto,
                    smoothed_rtt_ms,
                    pending_data.len(),
                    pending_inbound.len(),
                    keepalive_sent_count,
                    keepalive_ack_count,
                );
            }
            maybe_envelope = channel_rx.recv() => {
                let Some(envelope) = maybe_envelope else {
                    break Err(anyhow!("direct UDP connection closed while channel {channel_id} was active"));
                };
                last_peer_activity_at = Instant::now();
                match envelope {
                    DirectTunnelEnvelope::ChannelData {
                        sequence,
                        data_base64,
                        ..
                    } => {
                        let ready_payloads = queue_inbound_direct_payload(
                            &mut expected_inbound_sequence,
                            &mut pending_inbound,
                            sequence,
                            data_base64,
                        );
                        for payload in ready_payloads {
                            let bytes = secure_receiver
                                .decrypt_from_base64(&payload)
                                .map_err(|err| anyhow!("failed to decrypt direct payload: {err}"))?;
                            socket_writer
                                .write_all(&bytes)
                                .await
                                .context("failed to write local tcp stream")?;
                        }

                        send_direct_channel_ack(
                            &connection,
                            &channel_id,
                            &connection.rendezvous_id,
                            expected_inbound_sequence,
                            direct_channel_selective_ack_ranges(&pending_inbound),
                        )?;
                        metrics_reporter.maybe_report(
                            &congestion,
                            current_rto,
                            smoothed_rtt_ms,
                            pending_data.len(),
                            pending_inbound.len(),
                            keepalive_sent_count,
                            keepalive_ack_count,
                        );
                    }
                    DirectTunnelEnvelope::ChannelAck {
                        next_sequence,
                        selective_ranges,
                        ..
                    } => {
                        if let Some(fast_retransmit_sequence) = handle_direct_channel_ack(
                            next_sequence,
                            &selective_ranges,
                            &mut pending_data,
                            &mut congestion,
                            &mut current_rto,
                            &mut smoothed_rtt_ms,
                            &mut rttvar_ms,
                            &mut ack_tracker,
                            &mut selective_scoreboard,
                        ) {
                            if let Some(pending) = pending_data
                                .iter_mut()
                                .find(|pending| pending.sequence == fast_retransmit_sequence)
                            {
                                send_pending_direct_data(
                                    &connection,
                                    &channel_id,
                                    &connection.rendezvous_id,
                                    pending,
                                )?;
                            }
                        }
                        maybe_start_pending_direct_close(
                            &connection,
                            &channel_id,
                            &connection.rendezvous_id,
                            &pending_data,
                            &mut pending_close,
                            local_read_closed,
                            next_outbound_sequence,
                        )?;
                        metrics_reporter.maybe_report(
                            &congestion,
                            current_rto,
                            smoothed_rtt_ms,
                            pending_data.len(),
                            pending_inbound.len(),
                            keepalive_sent_count,
                            keepalive_ack_count,
                        );
                    }
                    DirectTunnelEnvelope::ChannelKeepalive { keepalive_nonce, .. } => {
                        send_direct_channel_keepalive_ack(
                            &connection,
                            &channel_id,
                            &connection.rendezvous_id,
                            keepalive_nonce,
                        )?;
                    }
                    DirectTunnelEnvelope::ChannelKeepaliveAck { .. } => {
                        keepalive_ack_count = keepalive_ack_count.saturating_add(1);
                        metrics_reporter.maybe_report(
                            &congestion,
                            current_rto,
                            smoothed_rtt_ms,
                            pending_data.len(),
                            pending_inbound.len(),
                            keepalive_sent_count,
                            keepalive_ack_count,
                        );
                    }
                    DirectTunnelEnvelope::ChannelClose {
                        final_sequence,
                        reason: _reason,
                        ..
                    } => {
                        if final_sequence > expected_inbound_sequence {
                            send_direct_channel_ack(
                                &connection,
                                &channel_id,
                                &connection.rendezvous_id,
                                expected_inbound_sequence,
                                direct_channel_selective_ack_ranges(&pending_inbound),
                            )?;
                            continue;
                        }

                        send_direct_channel_close_ack(
                            &connection,
                            &channel_id,
                            &connection.rendezvous_id,
                            final_sequence,
                        )?;
                        socket_writer
                            .shutdown()
                            .await
                            .context("failed to shutdown local tcp stream")?;
                        break Ok(());
                    }
                    DirectTunnelEnvelope::ChannelCloseAck { final_sequence, .. } => {
                        if let Some(pending) = pending_close.as_ref() {
                            if final_sequence >= pending.final_sequence {
                                socket_writer
                                    .shutdown()
                                    .await
                                    .context("failed to shutdown local tcp stream")?;
                                break Ok(());
                            }
                        }
                    }
                    DirectTunnelEnvelope::ChannelRejected { reason, .. } => {
                        socket_writer
                            .shutdown()
                            .await
                            .context("failed to shutdown local tcp stream")?;
                        break Err(anyhow!("direct channel rejected while active: {reason}"));
                    }
                    DirectTunnelEnvelope::ChannelAccepted { .. } | DirectTunnelEnvelope::OpenChannel { .. } => {}
                }
            }
        }
    };

    metrics_reporter.clear();
    connection.unregister_channel(&channel_id).await;
    result
}

fn send_pending_direct_data(
    connection: &DirectConnection,
    channel_id: &str,
    rendezvous_id: &str,
    pending: &mut PendingDirectData,
) -> Result<()> {
    pending.attempts += 1;
    if pending.attempts > DIRECT_TUNNEL_MAX_RETRANSMIT_ATTEMPTS {
        bail!(
            "timed out waiting for direct data acknowledgement on channel {} after {} attempts",
            channel_id,
            pending.attempts - 1
        );
    }
    pending.last_sent_at = Instant::now();
    connection.send(DirectTunnelEnvelope::ChannelData {
        rendezvous_id: rendezvous_id.to_string(),
        channel_id: channel_id.to_string(),
        sequence: pending.sequence,
        data_base64: pending.data_base64.clone(),
    })
}

fn maybe_start_pending_direct_close(
    connection: &DirectConnection,
    channel_id: &str,
    rendezvous_id: &str,
    pending_data: &VecDeque<PendingDirectData>,
    pending_close: &mut Option<PendingDirectClose>,
    local_read_closed: bool,
    next_outbound_sequence: u64,
) -> Result<()> {
    if pending_close.is_some() || !pending_data.is_empty() || !local_read_closed {
        return Ok(());
    }

    let mut close = PendingDirectClose {
        final_sequence: next_outbound_sequence,
        reason: Some("local tcp stream closed".to_string()),
        attempts: 0,
        last_sent_at: Instant::now(),
    };
    send_pending_direct_close(connection, channel_id, rendezvous_id, &mut close)?;
    *pending_close = Some(close);
    Ok(())
}

fn send_pending_direct_close(
    connection: &DirectConnection,
    channel_id: &str,
    rendezvous_id: &str,
    pending: &mut PendingDirectClose,
) -> Result<()> {
    pending.attempts += 1;
    if pending.attempts > DIRECT_TUNNEL_MAX_RETRANSMIT_ATTEMPTS {
        bail!(
            "timed out waiting for direct close acknowledgement on channel {} after {} attempts",
            channel_id,
            pending.attempts - 1
        );
    }
    pending.last_sent_at = Instant::now();
    connection.send(DirectTunnelEnvelope::ChannelClose {
        rendezvous_id: rendezvous_id.to_string(),
        channel_id: channel_id.to_string(),
        final_sequence: pending.final_sequence,
        reason: pending.reason.clone(),
    })
}

fn send_direct_channel_ack(
    connection: &DirectConnection,
    channel_id: &str,
    rendezvous_id: &str,
    next_sequence: u64,
    selective_ranges: Vec<DirectSelectiveAckRange>,
) -> Result<()> {
    connection.send(DirectTunnelEnvelope::ChannelAck {
        rendezvous_id: rendezvous_id.to_string(),
        channel_id: channel_id.to_string(),
        next_sequence,
        selective_ranges,
    })
}

fn send_direct_channel_keepalive(
    connection: &DirectConnection,
    channel_id: &str,
    rendezvous_id: &str,
    keepalive_nonce: u64,
) -> Result<()> {
    connection.send(DirectTunnelEnvelope::ChannelKeepalive {
        rendezvous_id: rendezvous_id.to_string(),
        channel_id: channel_id.to_string(),
        keepalive_nonce,
    })
}

fn send_direct_channel_keepalive_ack(
    connection: &DirectConnection,
    channel_id: &str,
    rendezvous_id: &str,
    keepalive_nonce: u64,
) -> Result<()> {
    connection.send(DirectTunnelEnvelope::ChannelKeepaliveAck {
        rendezvous_id: rendezvous_id.to_string(),
        channel_id: channel_id.to_string(),
        keepalive_nonce,
    })
}

fn send_direct_channel_close_ack(
    connection: &DirectConnection,
    channel_id: &str,
    rendezvous_id: &str,
    final_sequence: u64,
) -> Result<()> {
    connection.send(DirectTunnelEnvelope::ChannelCloseAck {
        rendezvous_id: rendezvous_id.to_string(),
        channel_id: channel_id.to_string(),
        final_sequence,
    })
}

fn direct_channel_should_send_keepalive(
    last_peer_activity_at: Instant,
    last_keepalive_sent_at: Option<Instant>,
    now: Instant,
    pending_outbound_packets: usize,
    close_in_flight: bool,
) -> bool {
    if close_in_flight || pending_outbound_packets > 0 {
        return false;
    }
    if now.duration_since(last_peer_activity_at) < DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER {
        return false;
    }
    match last_keepalive_sent_at {
        Some(last_keepalive_sent_at) => {
            now.duration_since(last_keepalive_sent_at) >= DIRECT_TUNNEL_KEEPALIVE_SEND_INTERVAL
        }
        None => true,
    }
}

fn direct_channel_peer_idle_timed_out(last_peer_activity_at: Instant, now: Instant) -> bool {
    now.duration_since(last_peer_activity_at) >= DIRECT_TUNNEL_PEER_IDLE_TIMEOUT
}

fn update_direct_rto(
    smoothed_rtt_ms: &mut Option<f64>,
    rttvar_ms: &mut Option<f64>,
    sample: Duration,
) -> Duration {
    let sample_ms = sample.as_secs_f64() * 1000.0;
    match (*smoothed_rtt_ms, *rttvar_ms) {
        (Some(srtt), Some(rttvar)) => {
            let next_rttvar = (0.75 * rttvar) + (0.25 * (srtt - sample_ms).abs());
            let next_srtt = (0.875 * srtt) + (0.125 * sample_ms);
            *smoothed_rtt_ms = Some(next_srtt);
            *rttvar_ms = Some(next_rttvar);
            duration_from_ms((next_srtt + (4.0 * next_rttvar)).clamp(
                DIRECT_TUNNEL_MIN_RTO.as_secs_f64() * 1000.0,
                DIRECT_TUNNEL_MAX_RTO.as_secs_f64() * 1000.0,
            ))
        }
        _ => {
            *smoothed_rtt_ms = Some(sample_ms);
            *rttvar_ms = Some(sample_ms / 2.0);
            duration_from_ms((sample_ms * 2.0).clamp(
                DIRECT_TUNNEL_MIN_RTO.as_secs_f64() * 1000.0,
                DIRECT_TUNNEL_MAX_RTO.as_secs_f64() * 1000.0,
            ))
        }
    }
}

fn queue_inbound_direct_payload(
    expected_inbound_sequence: &mut u64,
    pending_inbound: &mut BTreeMap<u64, String>,
    sequence: u64,
    data_base64: String,
) -> Vec<String> {
    if sequence < *expected_inbound_sequence {
        return Vec::new();
    }

    let max_buffered_sequence =
        expected_inbound_sequence.saturating_add(DIRECT_TUNNEL_MAX_INBOUND_REORDER_BUFFER as u64);
    if sequence > max_buffered_sequence {
        return Vec::new();
    }

    if sequence == *expected_inbound_sequence {
        let mut ready_payloads = vec![data_base64];
        *expected_inbound_sequence += 1;
        while let Some(payload) = pending_inbound.remove(expected_inbound_sequence) {
            ready_payloads.push(payload);
            *expected_inbound_sequence += 1;
        }
        return ready_payloads;
    }

    if pending_inbound.len() >= DIRECT_TUNNEL_MAX_INBOUND_REORDER_BUFFER
        && !pending_inbound.contains_key(&sequence)
    {
        return Vec::new();
    }

    pending_inbound.entry(sequence).or_insert(data_base64);
    Vec::new()
}

fn direct_channel_selective_ack_ranges(
    pending_inbound: &BTreeMap<u64, String>,
) -> Vec<DirectSelectiveAckRange> {
    let mut ranges = Vec::new();
    let mut keys = pending_inbound.keys().copied();
    let Some(mut start_sequence) = keys.next() else {
        return ranges;
    };
    let mut end_sequence = start_sequence;

    for sequence in keys {
        if sequence == end_sequence.saturating_add(1) {
            end_sequence = sequence;
            continue;
        }
        ranges.push(DirectSelectiveAckRange {
            start_sequence,
            end_sequence,
        });
        if ranges.len() >= DIRECT_TUNNEL_MAX_SELECTIVE_ACKS {
            return ranges;
        }
        start_sequence = sequence;
        end_sequence = sequence;
    }

    if ranges.len() < DIRECT_TUNNEL_MAX_SELECTIVE_ACKS {
        ranges.push(DirectSelectiveAckRange {
            start_sequence,
            end_sequence,
        });
    }
    ranges
}

fn oldest_timed_out_direct_sequence(
    pending_data: &VecDeque<PendingDirectData>,
    now: Instant,
    current_rto: Duration,
) -> Option<u64> {
    pending_data
        .iter()
        .find(|pending| now.duration_since(pending.last_sent_at) >= current_rto)
        .map(|pending| pending.sequence)
}

fn clamp_packet_count(value: f64) -> u32 {
    value.floor().clamp(0.0, u32::MAX as f64) as u32
}

fn clamp_optional_ms(value_ms: Option<f64>) -> Option<u64> {
    value_ms.map(|value_ms| value_ms.max(0.0).min(u64::MAX as f64) as u64)
}

fn covered_sequence_count(ranges: &[DirectSelectiveAckRange]) -> u128 {
    ranges.iter().fold(0u128, |total, range| {
        if range.start_sequence > range.end_sequence {
            total
        } else {
            total
                .saturating_add(u128::from(range.end_sequence - range.start_sequence))
                .saturating_add(1)
        }
    })
}

fn selective_retransmit_holes(
    pending_data: &VecDeque<PendingDirectData>,
    selective_scoreboard: &DirectSelectiveAckScoreboard,
) -> Vec<u64> {
    let Some(highest_selective_sequence) = selective_scoreboard.highest_sequence() else {
        return Vec::new();
    };
    pending_data
        .iter()
        .filter(|pending| pending.sequence <= highest_selective_sequence)
        .map(|pending| pending.sequence)
        .collect()
}

enum DirectLossRecoveryTrigger {
    Timeout {
        now: Instant,
        current_rto: Duration,
    },
    DuplicateAckBudget {
        retransmit_slots: usize,
        fallback_sequence: u64,
    },
    RecoveryProgress {
        fallback_sequence: u64,
    },
}

fn select_loss_recovery_sequence(
    pending_data: &VecDeque<PendingDirectData>,
    selective_scoreboard: &DirectSelectiveAckScoreboard,
    trigger: DirectLossRecoveryTrigger,
) -> Option<u64> {
    let hole_candidates = selective_retransmit_holes(pending_data, selective_scoreboard);
    match trigger {
        DirectLossRecoveryTrigger::Timeout { now, current_rto } => {
            for sequence in hole_candidates {
                if pending_data
                    .iter()
                    .find(|pending| pending.sequence == sequence)
                    .map(|pending| now.duration_since(pending.last_sent_at) >= current_rto)
                    .unwrap_or(false)
                {
                    return Some(sequence);
                }
            }
            oldest_timed_out_direct_sequence(pending_data, now, current_rto)
        }
        DirectLossRecoveryTrigger::DuplicateAckBudget {
            retransmit_slots,
            fallback_sequence,
        } => {
            if retransmit_slots == 0 {
                return None;
            }
            hole_candidates
                .get(retransmit_slots.saturating_sub(1))
                .copied()
                .or_else(|| hole_candidates.last().copied())
                .or(Some(fallback_sequence))
        }
        DirectLossRecoveryTrigger::RecoveryProgress { fallback_sequence } => {
            hole_candidates.first().copied().or(Some(fallback_sequence))
        }
    }
}

#[cfg(test)]
fn next_timed_out_retransmit_sequence(
    pending_data: &VecDeque<PendingDirectData>,
    selective_scoreboard: &DirectSelectiveAckScoreboard,
    now: Instant,
    current_rto: Duration,
) -> Option<u64> {
    select_loss_recovery_sequence(
        pending_data,
        selective_scoreboard,
        DirectLossRecoveryTrigger::Timeout { now, current_rto },
    )
}

fn handle_direct_channel_ack(
    next_sequence: u64,
    selective_ranges: &[DirectSelectiveAckRange],
    pending_data: &mut VecDeque<PendingDirectData>,
    congestion: &mut DirectCongestionController,
    current_rto: &mut Duration,
    smoothed_rtt_ms: &mut Option<f64>,
    rttvar_ms: &mut Option<f64>,
    ack_tracker: &mut DirectAckTracker,
    selective_scoreboard: &mut DirectSelectiveAckScoreboard,
) -> Option<u64> {
    let mut cumulative_acked_packets = 0usize;
    let mut selective_acked_packets = 0usize;
    let mut rtt_sample = None;
    while pending_data
        .front()
        .map(|pending| next_sequence > pending.sequence)
        .unwrap_or(false)
    {
        let pending = pending_data
            .pop_front()
            .expect("front exists while draining acked packets");
        if pending.attempts == 1 {
            rtt_sample = Some(pending.last_sent_at.elapsed());
        }
        cumulative_acked_packets += 1;
    }

    selective_scoreboard.advance_cumulative_ack(next_sequence);
    let new_selective_evidence = selective_scoreboard.observe_ranges(selective_ranges);

    if !pending_data.is_empty() {
        let mut selective_positions = Vec::new();
        for (index, pending) in pending_data.iter().enumerate() {
            if pending.sequence < next_sequence || !selective_scoreboard.contains(pending.sequence)
            {
                continue;
            }
            if pending.attempts == 1 && rtt_sample.is_none() {
                rtt_sample = Some(pending.last_sent_at.elapsed());
            }
            selective_positions.push(index);
        }
        selective_positions.sort_unstable();
        selective_positions.dedup();
        for index in selective_positions.into_iter().rev() {
            pending_data.remove(index);
            selective_acked_packets += 1;
        }
    }

    let acked_packets = cumulative_acked_packets + selective_acked_packets;
    let mut recovery_retransmit = None;
    if acked_packets > 0 {
        if let Some(sample) = rtt_sample {
            *current_rto = update_direct_rto(smoothed_rtt_ms, rttvar_ms, sample);
        }
        recovery_retransmit = congestion
            .on_ack_progress(
                acked_packets,
                next_sequence,
                pending_data.front().map(|pending| pending.sequence),
            )
            .and_then(|fallback_sequence| {
                select_loss_recovery_sequence(
                    pending_data,
                    selective_scoreboard,
                    DirectLossRecoveryTrigger::RecoveryProgress { fallback_sequence },
                )
            });
    }

    let has_missing_front = pending_data
        .front()
        .map(|pending| pending.sequence == next_sequence)
        .unwrap_or(false);
    if !has_missing_front {
        ack_tracker.last_ack_sequence = Some(next_sequence);
        ack_tracker.duplicate_ack_count = 0;
        ack_tracker.last_fast_retransmit_sequence = recovery_retransmit;
        return recovery_retransmit;
    }

    if cumulative_acked_packets > 0 {
        ack_tracker.last_ack_sequence = Some(next_sequence);
        ack_tracker.duplicate_ack_count = 0;
        ack_tracker.last_fast_retransmit_sequence = recovery_retransmit;
        return recovery_retransmit;
    }

    let duplicate_ack_evidence = new_selective_evidence.max(1).min(u32::MAX as usize) as u32;
    if ack_tracker.last_ack_sequence == Some(next_sequence) {
        ack_tracker.duplicate_ack_count = ack_tracker
            .duplicate_ack_count
            .saturating_add(duplicate_ack_evidence);
    } else {
        ack_tracker.last_ack_sequence = Some(next_sequence);
        ack_tracker.duplicate_ack_count = duplicate_ack_evidence;
        ack_tracker.last_fast_retransmit_sequence = None;
    }
    congestion.on_duplicate_ack();

    if let Some(sequence) = recovery_retransmit {
        ack_tracker.last_fast_retransmit_sequence = Some(sequence);
        return Some(sequence);
    }

    if ack_tracker.duplicate_ack_count >= DIRECT_TUNNEL_DUP_ACK_THRESHOLD {
        let retransmit_slots =
            (ack_tracker.duplicate_ack_count / DIRECT_TUNNEL_DUP_ACK_THRESHOLD) as usize;
        let next_candidate = select_loss_recovery_sequence(
            pending_data,
            selective_scoreboard,
            DirectLossRecoveryTrigger::DuplicateAckBudget {
                retransmit_slots,
                fallback_sequence: next_sequence,
            },
        )
        .unwrap_or(next_sequence);

        if ack_tracker.last_fast_retransmit_sequence != Some(next_candidate) {
            let recover_until_sequence = pending_data
                .back()
                .map(|pending| pending.sequence.saturating_add(1))
                .unwrap_or(next_sequence);
            congestion.on_fast_retransmit(pending_data.len(), recover_until_sequence);
            ack_tracker.last_fast_retransmit_sequence = Some(next_candidate);
            return Some(next_candidate);
        }
    }
    recovery_retransmit
}

fn duration_from_ms(value_ms: f64) -> Duration {
    Duration::from_secs_f64((value_ms.max(1.0)) / 1000.0)
}

fn scale_duration(duration: Duration, factor: f64) -> Duration {
    Duration::from_secs_f64(duration.as_secs_f64() * factor)
}

fn consume_direct_prebridge_fail_once() -> bool {
    let enabled = std::env::var(DIRECT_PREBRIDGE_FAIL_ONCE_ENV)
        .map(|value| {
            let normalized = value.trim().to_ascii_lowercase();
            !normalized.is_empty() && normalized != "0" && normalized != "false"
        })
        .unwrap_or(false);
    if !enabled {
        return false;
    }
    DIRECT_PREBRIDGE_FAIL_ONCE_FLAG
        .get_or_init(|| AtomicBool::new(true))
        .swap(false, Ordering::SeqCst)
}

fn direct_probe_rendezvous_id(envelope: &DirectProbeEnvelope) -> &str {
    match envelope {
        DirectProbeEnvelope::Hello { rendezvous_id, .. }
        | DirectProbeEnvelope::Ack { rendezvous_id, .. } => rendezvous_id,
    }
}

fn direct_tunnel_rendezvous_id(envelope: &DirectTunnelEnvelope) -> &str {
    match envelope {
        DirectTunnelEnvelope::OpenChannel { rendezvous_id, .. }
        | DirectTunnelEnvelope::ChannelAccepted { rendezvous_id, .. }
        | DirectTunnelEnvelope::ChannelRejected { rendezvous_id, .. }
        | DirectTunnelEnvelope::ChannelData { rendezvous_id, .. }
        | DirectTunnelEnvelope::ChannelAck { rendezvous_id, .. }
        | DirectTunnelEnvelope::ChannelKeepalive { rendezvous_id, .. }
        | DirectTunnelEnvelope::ChannelKeepaliveAck { rendezvous_id, .. }
        | DirectTunnelEnvelope::ChannelClose { rendezvous_id, .. }
        | DirectTunnelEnvelope::ChannelCloseAck { rendezvous_id, .. } => rendezvous_id,
    }
}

async fn route_direct_channel_message(
    routes: &Arc<Mutex<HashMap<String, mpsc::UnboundedSender<DirectTunnelEnvelope>>>>,
    envelope: DirectTunnelEnvelope,
) {
    let channel_id = envelope.channel_id().to_string();
    let channel_tx = {
        let mut routes = routes.lock().await;
        let channel_tx = routes.get(&channel_id).cloned();
        if envelope.is_terminal() {
            routes.remove(&channel_id);
        }
        channel_tx
    };

    if let Some(channel_tx) = channel_tx {
        let _ = channel_tx.send(envelope);
    }
}

async fn clear_all_direct_channels(
    routes: &Arc<Mutex<HashMap<String, mpsc::UnboundedSender<DirectTunnelEnvelope>>>>,
    rendezvous_id: &str,
) {
    let routes = {
        let mut routes = routes.lock().await;
        std::mem::take(&mut *routes)
    };
    for (channel_id, tx) in routes {
        let _ = tx.send(DirectTunnelEnvelope::ChannelClose {
            rendezvous_id: rendezvous_id.to_string(),
            channel_id,
            final_sequence: 0,
            reason: Some("direct UDP connection closed".to_string()),
        });
    }
}

async fn send_probe_to_all(socket: &UdpSocket, hello_bytes: &[u8], peer_addrs: &[SocketAddr]) {
    for peer_addr in peer_addrs {
        if let Err(err) = socket.send_to(hello_bytes, peer_addr).await {
            warn!("failed to send UDP probe hello to {peer_addr}: {err}");
        }
    }
}

fn sort_candidates(
    mut candidates: Vec<DirectConnectionCandidate>,
) -> Vec<DirectConnectionCandidate> {
    candidates.sort_by_key(|candidate| candidate_priority(&candidate.candidate_type));
    candidates
}

fn candidate_priority(candidate_type: &str) -> u8 {
    match candidate_type {
        "public" | "srflx" => 0,
        "manual" => 1,
        "local" => 2,
        _ => 3,
    }
}

fn parse_socket_addr(candidate: &DirectConnectionCandidate) -> Option<SocketAddr> {
    match candidate.addr.parse::<SocketAddr>() {
        Ok(addr) => Some(addr),
        Err(err) => {
            warn!("ignoring invalid UDP candidate {}: {err}", candidate.addr);
            None
        }
    }
}

pub fn bind_candidate(bind_addr: &str, candidate_type: &str) -> DirectConnectionCandidate {
    DirectConnectionCandidate {
        protocol: "udp".to_string(),
        addr: bind_addr.to_string(),
        candidate_type: candidate_type.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpListener;

    fn ack_range(start_sequence: u64, end_sequence: u64) -> DirectSelectiveAckRange {
        DirectSelectiveAckRange {
            start_sequence,
            end_sequence,
        }
    }

    #[test]
    fn inbound_reorder_buffer_releases_contiguous_payloads_after_gap_is_filled() {
        let mut expected_inbound_sequence = 5u64;
        let mut pending_inbound = BTreeMap::new();

        assert!(
            queue_inbound_direct_payload(
                &mut expected_inbound_sequence,
                &mut pending_inbound,
                6,
                "second".to_string(),
            )
            .is_empty()
        );
        assert_eq!(expected_inbound_sequence, 5);
        assert_eq!(pending_inbound.len(), 1);

        assert_eq!(
            queue_inbound_direct_payload(
                &mut expected_inbound_sequence,
                &mut pending_inbound,
                5,
                "first".to_string(),
            ),
            vec!["first".to_string(), "second".to_string()]
        );
        assert_eq!(expected_inbound_sequence, 7);
        assert!(pending_inbound.is_empty());
    }

    #[test]
    fn inbound_reorder_buffer_ignores_packets_that_are_too_far_ahead() {
        let mut expected_inbound_sequence = 0u64;
        let mut pending_inbound = BTreeMap::new();
        let too_far_sequence = DIRECT_TUNNEL_MAX_INBOUND_REORDER_BUFFER as u64 + 1;

        assert!(
            queue_inbound_direct_payload(
                &mut expected_inbound_sequence,
                &mut pending_inbound,
                too_far_sequence,
                "ignored".to_string(),
            )
            .is_empty()
        );
        assert!(pending_inbound.is_empty());
        assert_eq!(expected_inbound_sequence, 0);
    }

    #[test]
    fn selective_ack_ranges_compress_contiguous_sequences() {
        let pending_inbound = BTreeMap::from([
            (6, "a".to_string()),
            (7, "b".to_string()),
            (8, "c".to_string()),
            (10, "d".to_string()),
            (12, "e".to_string()),
            (13, "f".to_string()),
        ]);

        assert_eq!(
            direct_channel_selective_ack_ranges(&pending_inbound),
            vec![ack_range(6, 8), ack_range(10, 10), ack_range(12, 13)]
        );
    }

    #[test]
    fn selective_ack_ranges_cap_the_number_of_blocks() {
        let pending_inbound = BTreeMap::from_iter(
            (0..(DIRECT_TUNNEL_MAX_SELECTIVE_ACKS as u64 + 2))
                .map(|sequence| (sequence * 2, sequence.to_string())),
        );

        let ranges = direct_channel_selective_ack_ranges(&pending_inbound);
        assert_eq!(ranges.len(), DIRECT_TUNNEL_MAX_SELECTIVE_ACKS);
        assert_eq!(ranges.first(), Some(&ack_range(0, 0)));
        assert_eq!(
            ranges.last(),
            Some(&ack_range(
                (DIRECT_TUNNEL_MAX_SELECTIVE_ACKS as u64 - 1) * 2,
                (DIRECT_TUNNEL_MAX_SELECTIVE_ACKS as u64 - 1) * 2,
            ))
        );
    }

    #[test]
    fn selective_ack_scoreboard_merges_and_counts_new_coverage() {
        let mut scoreboard = DirectSelectiveAckScoreboard::default();

        assert_eq!(
            scoreboard.observe_ranges(&[ack_range(6, 7), ack_range(9, 10)]),
            4
        );
        assert_eq!(scoreboard.ranges(), &[ack_range(6, 7), ack_range(9, 10)]);

        assert_eq!(scoreboard.observe_ranges(&[ack_range(7, 9)]), 1);
        assert_eq!(scoreboard.ranges(), &[ack_range(6, 10)]);
    }

    #[test]
    fn selective_ack_scoreboard_advances_with_cumulative_ack() {
        let mut scoreboard = DirectSelectiveAckScoreboard::default();
        scoreboard.observe_ranges(&[ack_range(6, 10), ack_range(12, 13)]);

        scoreboard.advance_cumulative_ack(9);
        assert_eq!(scoreboard.ranges(), &[ack_range(9, 10), ack_range(12, 13)]);
        assert!(scoreboard.contains(10));
        assert!(!scoreboard.contains(8));

        scoreboard.advance_cumulative_ack(11);
        assert_eq!(scoreboard.ranges(), &[ack_range(12, 13)]);
    }

    #[test]
    fn selective_retransmit_holes_only_include_missing_sequences_before_highest_sack() {
        let pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 7,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 12,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
        ]);
        let mut scoreboard = DirectSelectiveAckScoreboard::default();
        scoreboard.observe_ranges(&[ack_range(6, 6), ack_range(8, 10)]);

        assert_eq!(
            selective_retransmit_holes(&pending_data, &scoreboard),
            vec![5, 7]
        );
    }

    #[test]
    fn timed_out_retransmit_prioritizes_selective_hole_before_tail_packet() {
        let now = Instant::now();
        let pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: now - Duration::from_millis(100),
            },
            PendingDirectData {
                sequence: 7,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: now - Duration::from_millis(500),
            },
            PendingDirectData {
                sequence: 11,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: now - Duration::from_millis(900),
            },
        ]);
        let mut scoreboard = DirectSelectiveAckScoreboard::default();
        scoreboard.observe_ranges(&[ack_range(6, 6), ack_range(8, 10)]);

        assert_eq!(
            next_timed_out_retransmit_sequence(
                &pending_data,
                &scoreboard,
                now,
                Duration::from_millis(250),
            ),
            Some(7)
        );
    }

    #[test]
    fn loss_recovery_selector_prefers_first_selective_hole_during_recovery_progress() {
        let pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 7,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 11,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
        ]);
        let mut scoreboard = DirectSelectiveAckScoreboard::default();
        scoreboard.observe_ranges(&[ack_range(6, 6), ack_range(8, 10)]);

        assert_eq!(
            select_loss_recovery_sequence(
                &pending_data,
                &scoreboard,
                DirectLossRecoveryTrigger::RecoveryProgress {
                    fallback_sequence: 5,
                },
            ),
            Some(5)
        );
    }

    #[test]
    fn loss_recovery_selector_uses_duplicate_ack_budget_to_reach_later_hole() {
        let pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 7,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 11,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
        ]);
        let mut scoreboard = DirectSelectiveAckScoreboard::default();
        scoreboard.observe_ranges(&[ack_range(6, 6), ack_range(8, 10)]);

        assert_eq!(
            select_loss_recovery_sequence(
                &pending_data,
                &scoreboard,
                DirectLossRecoveryTrigger::DuplicateAckBudget {
                    retransmit_slots: 2,
                    fallback_sequence: 5,
                },
            ),
            Some(7)
        );
    }

    #[test]
    fn congestion_controller_uses_slow_start_then_congestion_avoidance() {
        let mut controller = DirectCongestionController::default();

        controller.on_ack_progress(4, 4, None);
        assert_eq!(controller.send_window_size(), 8);

        controller.ssthresh_packets = 8.0;
        controller.cwnd_packets = 8.0;
        controller.on_ack_progress(8, 12, None);
        assert!(controller.cwnd_packets > 8.0);
        assert!(controller.cwnd_packets < 9.5);
        assert_eq!(controller.send_window_size(), 8);
    }

    #[test]
    fn timeout_retransmit_selects_oldest_expired_packet_only() {
        let now = Instant::now();
        let pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 7,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: now - Duration::from_millis(500),
            },
            PendingDirectData {
                sequence: 8,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: now - Duration::from_millis(900),
            },
            PendingDirectData {
                sequence: 9,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: now - Duration::from_millis(100),
            },
        ]);

        assert_eq!(
            oldest_timed_out_direct_sequence(&pending_data, now, Duration::from_millis(250),),
            Some(7)
        );
        assert_eq!(
            oldest_timed_out_direct_sequence(&pending_data, now, Duration::from_secs(2),),
            None
        );
    }

    #[test]
    fn keepalive_only_starts_after_idle_and_respects_send_interval() {
        let base = Instant::now();

        assert!(!direct_channel_should_send_keepalive(
            base,
            None,
            base + DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER - Duration::from_millis(1),
            0,
            false,
        ));
        assert!(direct_channel_should_send_keepalive(
            base,
            None,
            base + DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER,
            0,
            false,
        ));
        assert!(!direct_channel_should_send_keepalive(
            base,
            Some(base + DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER),
            base + DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER + DIRECT_TUNNEL_KEEPALIVE_SEND_INTERVAL
                - Duration::from_millis(1),
            0,
            false,
        ));
        assert!(direct_channel_should_send_keepalive(
            base,
            Some(base + DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER),
            base + DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER + DIRECT_TUNNEL_KEEPALIVE_SEND_INTERVAL,
            0,
            false,
        ));
        assert!(!direct_channel_should_send_keepalive(
            base,
            None,
            base + DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER,
            1,
            false,
        ));
        assert!(!direct_channel_should_send_keepalive(
            base,
            None,
            base + DIRECT_TUNNEL_KEEPALIVE_IDLE_AFTER,
            0,
            true,
        ));
    }

    #[test]
    fn peer_idle_timeout_uses_last_peer_activity_deadline() {
        let base = Instant::now();

        assert!(!direct_channel_peer_idle_timed_out(
            base,
            base + DIRECT_TUNNEL_PEER_IDLE_TIMEOUT - Duration::from_millis(1),
        ));
        assert!(direct_channel_peer_idle_timed_out(
            base,
            base + DIRECT_TUNNEL_PEER_IDLE_TIMEOUT,
        ));
    }

    #[test]
    fn partial_ack_during_fast_recovery_retransmits_next_gap() {
        let mut controller = DirectCongestionController::default();

        controller.on_fast_retransmit(8, 10);
        assert!(controller.fast_recovery.is_some());
        assert_eq!(controller.send_window_size(), 7);

        let retransmit = controller.on_ack_progress(1, 6, Some(6));
        assert_eq!(retransmit, Some(6));
        assert!(controller.fast_recovery.is_some());

        let retransmit = controller.on_ack_progress(4, 10, None);
        assert_eq!(retransmit, None);
        assert!(controller.fast_recovery.is_none());
        assert_eq!(controller.send_window_size(), 4);
    }

    #[test]
    fn duplicate_acks_trigger_fast_retransmit_for_missing_front_packet() {
        let mut pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 6,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
        ]);
        let mut congestion = DirectCongestionController {
            cwnd_packets: 8.0,
            ssthresh_packets: DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
            fast_recovery: None,
        };
        let mut current_rto = DIRECT_TUNNEL_INITIAL_RTO;
        let mut smoothed_rtt_ms = None;
        let mut rttvar_ms = None;
        let mut ack_tracker = DirectAckTracker::default();
        let mut selective_scoreboard = DirectSelectiveAckScoreboard::default();

        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            None
        );
        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            None
        );
        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            Some(5)
        );
        assert_eq!(congestion.send_window_size(), 5);
        assert!(congestion.fast_recovery.is_some());
    }

    #[test]
    fn selective_ack_removes_out_of_order_packets_from_outstanding_queue() {
        let mut pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 6,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 7,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 8,
                data_base64: "d".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
        ]);
        let mut congestion = DirectCongestionController::default();
        let mut current_rto = DIRECT_TUNNEL_INITIAL_RTO;
        let mut smoothed_rtt_ms = None;
        let mut rttvar_ms = None;
        let mut ack_tracker = DirectAckTracker::default();
        let mut selective_scoreboard = DirectSelectiveAckScoreboard::default();

        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[ack_range(6, 6), ack_range(8, 8)],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            None
        );
        assert_eq!(
            pending_data
                .iter()
                .map(|pending| pending.sequence)
                .collect::<Vec<_>>(),
            vec![5, 7]
        );
    }

    #[test]
    fn selective_ack_progress_still_triggers_fast_retransmit_for_front_gap() {
        let mut pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 6,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 7,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 8,
                data_base64: "d".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
        ]);
        let mut congestion = DirectCongestionController {
            cwnd_packets: 8.0,
            ssthresh_packets: DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
            fast_recovery: None,
        };
        let mut current_rto = DIRECT_TUNNEL_INITIAL_RTO;
        let mut smoothed_rtt_ms = None;
        let mut rttvar_ms = None;
        let mut ack_tracker = DirectAckTracker::default();
        let mut selective_scoreboard = DirectSelectiveAckScoreboard::default();

        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[ack_range(6, 6)],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            None
        );
        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[ack_range(7, 7)],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            None
        );
        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[ack_range(8, 8)],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            Some(5)
        );
        assert_eq!(
            pending_data
                .iter()
                .map(|pending| pending.sequence)
                .collect::<Vec<_>>(),
            vec![5]
        );
        assert!(congestion.fast_recovery.is_some());
    }

    #[test]
    fn single_ack_with_three_new_selective_ranges_triggers_fast_retransmit() {
        let mut pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 6,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 7,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 8,
                data_base64: "d".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
        ]);
        let mut congestion = DirectCongestionController {
            cwnd_packets: 8.0,
            ssthresh_packets: DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
            fast_recovery: None,
        };
        let mut current_rto = DIRECT_TUNNEL_INITIAL_RTO;
        let mut smoothed_rtt_ms = None;
        let mut rttvar_ms = None;
        let mut ack_tracker = DirectAckTracker::default();
        let mut selective_scoreboard = DirectSelectiveAckScoreboard::default();

        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[ack_range(6, 8)],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            Some(5)
        );
        assert_eq!(
            pending_data
                .iter()
                .map(|pending| pending.sequence)
                .collect::<Vec<_>>(),
            vec![5]
        );
        assert!(congestion.fast_recovery.is_some());
    }

    #[test]
    fn selective_ack_range_evidence_accumulates_across_ack_updates_for_same_gap() {
        let mut pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 6,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 7,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 8,
                data_base64: "d".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
        ]);
        let mut congestion = DirectCongestionController {
            cwnd_packets: 8.0,
            ssthresh_packets: DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
            fast_recovery: None,
        };
        let mut current_rto = DIRECT_TUNNEL_INITIAL_RTO;
        let mut smoothed_rtt_ms = None;
        let mut rttvar_ms = None;
        let mut ack_tracker = DirectAckTracker::default();
        let mut selective_scoreboard = DirectSelectiveAckScoreboard::default();

        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[ack_range(6, 7)],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            None
        );
        assert_eq!(ack_tracker.duplicate_ack_count, 2);
        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[ack_range(8, 8)],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            Some(5)
        );
        assert_eq!(
            pending_data
                .iter()
                .map(|pending| pending.sequence)
                .collect::<Vec<_>>(),
            vec![5]
        );
        assert!(congestion.fast_recovery.is_some());
    }

    #[test]
    fn duplicate_ack_budget_can_progress_to_later_selective_hole() {
        let mut pending_data = VecDeque::from([
            PendingDirectData {
                sequence: 5,
                data_base64: "a".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 7,
                data_base64: "b".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
            PendingDirectData {
                sequence: 11,
                data_base64: "c".to_string(),
                attempts: 1,
                last_sent_at: Instant::now(),
            },
        ]);
        let mut congestion = DirectCongestionController {
            cwnd_packets: 8.0,
            ssthresh_packets: DIRECT_TUNNEL_MAX_WINDOW_SIZE as f64,
            fast_recovery: None,
        };
        let mut current_rto = DIRECT_TUNNEL_INITIAL_RTO;
        let mut smoothed_rtt_ms = None;
        let mut rttvar_ms = None;
        let mut ack_tracker = DirectAckTracker::default();
        let mut selective_scoreboard = DirectSelectiveAckScoreboard::default();

        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[ack_range(6, 6), ack_range(8, 10)],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            Some(5)
        );
        assert_eq!(ack_tracker.last_fast_retransmit_sequence, Some(5));

        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            None
        );

        assert_eq!(
            handle_direct_channel_ack(
                5,
                &[],
                &mut pending_data,
                &mut congestion,
                &mut current_rto,
                &mut smoothed_rtt_ms,
                &mut rttvar_ms,
                &mut ack_tracker,
                &mut selective_scoreboard,
            ),
            Some(7)
        );
        assert_eq!(ack_tracker.last_fast_retransmit_sequence, Some(7));
    }

    #[tokio::test]
    async fn prepare_local_direct_forward_connection_returns_socket_on_pre_bridge_failure() {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("bind local test listener");
        let listener_addr = listener.local_addr().expect("listener addr");
        let client_connect =
            tokio::spawn(async move { tokio::net::TcpStream::connect(listener_addr).await });
        let (mut server_stream, _) = listener.accept().await.expect("accept test stream");
        let client_stream = client_connect
            .await
            .expect("join client connect")
            .expect("connect client stream");

        let (outbound, outbound_rx) = mpsc::unbounded_channel();
        drop(outbound_rx);
        let direct_connection = DirectConnection {
            rendezvous_id: "rvz_test".to_string(),
            peer_addr: "127.0.0.1:9".parse().expect("peer addr"),
            outbound,
            channel_routes: std::sync::Arc::new(tokio::sync::Mutex::new(HashMap::new())),
            closed: std::sync::Arc::new(AtomicBool::new(false)),
            closed_notify: std::sync::Arc::new(Notify::new()),
        };
        let device_identity = DeviceIdentity::generate();
        let resolved_service = ResolvedService {
            service: minipunch_core::ServiceDefinition {
                service_id: "svc_test".to_string(),
                owner_device_id: "dev_target".to_string(),
                name: "test".to_string(),
                protocol: "tcp".to_string(),
            },
            target_relay_public_key: RelayKeypair::generate().public_key_base64(),
        };

        let (returned_stream, err) = match prepare_local_direct_forward_connection(
            direct_connection,
            device_identity,
            resolved_service,
            client_stream,
            None,
        )
        .await
        {
            Ok(_) => panic!("expected direct pre-bridge setup to fail"),
            Err((returned_stream, err)) => (returned_stream, err),
        };

        returned_stream
            .writable()
            .await
            .expect("returned stream should remain usable");
        let mut returned_stream = returned_stream;
        returned_stream
            .write_all(b"ping")
            .await
            .expect("write via returned stream");
        let mut buffer = [0u8; 4];
        server_stream
            .read_exact(&mut buffer)
            .await
            .expect("read from server side");
        assert_eq!(&buffer, b"ping");
        assert!(
            err.to_string().contains("direct UDP connection is closed"),
            "unexpected error: {err}"
        );
    }
}
