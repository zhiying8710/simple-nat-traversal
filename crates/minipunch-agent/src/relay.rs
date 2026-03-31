use std::collections::HashMap;
use std::future::pending;
use std::sync::Arc;
use std::sync::Mutex as StdMutex;
use std::sync::atomic::{AtomicBool, AtomicI64, AtomicUsize, Ordering};
use std::time::Duration;

use anyhow::{Context, Result, anyhow, bail};
use futures_util::{SinkExt, StreamExt};
use minipunch_core::{
    DeviceIdentity, RelayEnvelope, RelayKeypair, RelayTransportFrame, SecureChannelRole,
    SecureReceiver, SecureSender, device_id_from_public_key, generate_token,
    relay_channel_open_message, secure_channel_pair, service_id, unix_timestamp_now,
    verify_signature_base64,
};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::{Mutex, Notify, mpsc, watch};
use tokio::task::JoinHandle;
use tokio::time::timeout;
use tokio_tungstenite::connect_async;
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::protocol::Message;
use tracing::{info, warn};

use crate::ResolvedService;
use crate::client::ControlPlaneClient;
use crate::config::PublishedServiceConfig;
use crate::runtime_state::{ForwardRuntimeHook, RuntimeStateWriter};

const RELAY_MAX_BATCH_SIZE: usize = 16;

#[derive(Clone)]
pub struct RelayConnection {
    inner: Arc<RelayConnectionInner>,
}

#[derive(Clone)]
pub(crate) struct RelayForwarderTracker {
    active_connections: Arc<AtomicUsize>,
    last_became_idle_at: Arc<AtomicI64>,
}

impl RelayForwarderTracker {
    pub(crate) fn new() -> Self {
        Self {
            active_connections: Arc::new(AtomicUsize::new(0)),
            last_became_idle_at: Arc::new(AtomicI64::new(unix_timestamp_now())),
        }
    }

    pub(crate) fn note_connection_started(&self) {
        self.active_connections.fetch_add(1, Ordering::SeqCst);
    }

    pub(crate) fn note_connection_finished(&self) {
        let previous = self.active_connections.fetch_sub(1, Ordering::SeqCst);
        if previous <= 1 {
            self.active_connections.store(0, Ordering::SeqCst);
            self.last_became_idle_at
                .store(unix_timestamp_now(), Ordering::SeqCst);
        }
    }

    pub(crate) fn active_connections(&self) -> usize {
        self.active_connections.load(Ordering::SeqCst)
    }

    pub(crate) fn idle_for_secs(&self) -> u64 {
        if self.active_connections() > 0 {
            return 0;
        }
        let now = unix_timestamp_now();
        now.saturating_sub(self.last_became_idle_at.load(Ordering::SeqCst)) as u64
    }
}

struct RelayTrackedConnectionGuard {
    tracker: Option<RelayForwarderTracker>,
}

impl RelayTrackedConnectionGuard {
    fn new(tracker: Option<RelayForwarderTracker>) -> Self {
        if let Some(tracker) = &tracker {
            tracker.note_connection_started();
        }
        Self { tracker }
    }
}

impl Drop for RelayTrackedConnectionGuard {
    fn drop(&mut self) {
        if let Some(tracker) = &self.tracker {
            tracker.note_connection_finished();
        }
    }
}

struct RelayConnectionInner {
    outbound: mpsc::UnboundedSender<RelayEnvelope>,
    channel_routes: Arc<Mutex<HashMap<String, mpsc::UnboundedSender<RelayEnvelope>>>>,
    closed: AtomicBool,
    closed_notify: Notify,
    reader_task: StdMutex<Option<JoinHandle<()>>>,
    writer_task: StdMutex<Option<JoinHandle<()>>>,
}

impl RelayConnectionInner {
    fn mark_closed(&self) {
        if self.closed.swap(true, Ordering::SeqCst) {
            return;
        }
        self.closed_notify.notify_waiters();
    }

    fn shutdown_tasks(&self) {
        if let Some(task) = self.writer_task.lock().expect("writer task lock").take() {
            task.abort();
        }
        if let Some(task) = self.reader_task.lock().expect("reader task lock").take() {
            task.abort();
        }
    }
}

impl Drop for RelayConnection {
    fn drop(&mut self) {
        if Arc::strong_count(&self.inner) != 1 {
            return;
        }
        self.inner.mark_closed();
        self.inner.shutdown_tasks();
    }
}

#[derive(Debug, Clone)]
pub struct RelayIncomingChannel {
    pub channel_id: String,
    pub source_device_id: String,
    pub source_identity_public_key: String,
    pub source_ephemeral_public_key: String,
    pub source_open_signature: String,
    pub service_id: String,
}

#[derive(Clone)]
struct PublishedServiceDirectory {
    services_by_id: Arc<HashMap<String, PublishedServiceConfig>>,
}

impl PublishedServiceDirectory {
    fn new(local_device_id: &str, published_services: &[PublishedServiceConfig]) -> Self {
        let services_by_id = published_services
            .iter()
            .cloned()
            .map(|service| (service_id(local_device_id, &service.name), service))
            .collect::<HashMap<_, _>>();
        Self {
            services_by_id: Arc::new(services_by_id),
        }
    }

    fn resolve(&self, service_id: &str) -> Option<&PublishedServiceConfig> {
        self.services_by_id.get(service_id)
    }
}

impl RelayConnection {
    pub async fn connect(
        server_url: &str,
        session_token: &str,
    ) -> Result<(Self, mpsc::UnboundedReceiver<RelayIncomingChannel>)> {
        let ws_url = websocket_url(server_url)?;
        let mut request = ws_url
            .into_client_request()
            .map_err(|err| anyhow!("failed to build websocket request: {err}"))?;
        request.headers_mut().insert(
            "authorization",
            format!("Bearer {session_token}")
                .parse()
                .map_err(|err| anyhow!("failed to encode authorization header: {err}"))?,
        );

        let (stream, _) = connect_async(request)
            .await
            .context("failed to connect relay websocket")?;
        let (mut ws_writer, mut ws_reader) = stream.split();

        let (outbound, mut outbound_rx) = mpsc::unbounded_channel::<RelayEnvelope>();
        let (incoming_tx, incoming_rx) = mpsc::unbounded_channel::<RelayIncomingChannel>();
        let channel_routes = Arc::new(Mutex::new(HashMap::new()));
        let inner = Arc::new(RelayConnectionInner {
            outbound,
            channel_routes: channel_routes.clone(),
            closed: AtomicBool::new(false),
            closed_notify: Notify::new(),
            reader_task: StdMutex::new(None),
            writer_task: StdMutex::new(None),
        });

        let closed_for_writer = inner.clone();
        let writer = tokio::spawn(async move {
            loop {
                let closed = closed_for_writer.closed_notify.notified();
                tokio::pin!(closed);
                if closed_for_writer.closed.load(Ordering::SeqCst) {
                    break;
                }
                tokio::select! {
                    maybe_frame = next_relay_transport_frame(&mut outbound_rx) => {
                        let Some(frame) = maybe_frame else {
                            break;
                        };
                        if closed_for_writer.closed.load(Ordering::SeqCst) {
                            break;
                        }
                        let encoded = match serde_json::to_vec(&frame) {
                            Ok(encoded) => encoded,
                            Err(err) => {
                                warn!("failed to encode relay envelope: {err}");
                                break;
                            }
                        };
                        if let Err(err) = ws_writer.send(Message::Binary(encoded.into())).await {
                            warn!("failed to write relay websocket frame: {err}");
                            break;
                        }
                    }
                    _ = &mut closed => {
                        if closed_for_writer.closed.load(Ordering::SeqCst) {
                            break;
                        }
                    }
                }
            }
        });

        let routes_for_reader = channel_routes.clone();
        let outbound_for_reader = inner.outbound.clone();
        let closed_for_reader = inner.clone();
        let reader = tokio::spawn(async move {
            loop {
                let closed = closed_for_reader.closed_notify.notified();
                tokio::pin!(closed);
                if closed_for_reader.closed.load(Ordering::SeqCst) {
                    break;
                }
                tokio::select! {
                    message = ws_reader.next() => {
                        let Some(message) = message else {
                            break;
                        };
                        match message {
                            Ok(Message::Text(text)) => {
                                match decode_relay_transport_frame(text.as_bytes()) {
                                    Ok(envelopes) => {
                                        for envelope in envelopes {
                                            match envelope {
                                                RelayEnvelope::IncomingChannel {
                                                    channel_id,
                                                    source_device_id,
                                                    source_identity_public_key,
                                                    source_ephemeral_public_key,
                                                    source_open_signature,
                                                    service_id,
                                                } => {
                                                    let _ = incoming_tx.send(RelayIncomingChannel {
                                                        channel_id,
                                                        source_device_id,
                                                        source_identity_public_key,
                                                        source_ephemeral_public_key,
                                                        source_open_signature,
                                                        service_id,
                                                    });
                                                }
                                                envelope => {
                                                    route_channel_message(&routes_for_reader, envelope).await;
                                                }
                                            }
                                        }
                                    }
                                    Err(err) => {
                                        warn!("failed to decode relay websocket frame: {err}");
                                    }
                                }
                            }
                            Ok(Message::Binary(bytes)) => {
                                match decode_relay_transport_frame(&bytes) {
                                    Ok(envelopes) => {
                                        for envelope in envelopes {
                                            match envelope {
                                                RelayEnvelope::IncomingChannel {
                                                    channel_id,
                                                    source_device_id,
                                                    source_identity_public_key,
                                                    source_ephemeral_public_key,
                                                    source_open_signature,
                                                    service_id,
                                                } => {
                                                    let _ = incoming_tx.send(RelayIncomingChannel {
                                                        channel_id,
                                                        source_device_id,
                                                        source_identity_public_key,
                                                        source_ephemeral_public_key,
                                                        source_open_signature,
                                                        service_id,
                                                    });
                                                }
                                                envelope => {
                                                    route_channel_message(&routes_for_reader, envelope).await;
                                                }
                                            }
                                        }
                                    }
                                    Err(err) => {
                                        warn!("failed to decode relay websocket frame: {err}");
                                    }
                                }
                            }
                            Ok(Message::Close(_)) => {
                                break;
                            }
                            Ok(Message::Ping(_)) => {
                                let _ = outbound_for_reader.send(RelayEnvelope::Pong);
                            }
                            Ok(Message::Pong(_)) | Ok(Message::Frame(_)) => {}
                            Err(err) => {
                                warn!("relay websocket read failed: {err}");
                                break;
                            }
                        }
                    }
                    _ = &mut closed => {
                        if closed_for_reader.closed.load(Ordering::SeqCst) {
                            break;
                        }
                    }
                }
            }

            closed_for_reader.mark_closed();
            clear_all_channels(&routes_for_reader).await;
        });

        *inner.writer_task.lock().expect("writer task lock") = Some(writer);
        *inner.reader_task.lock().expect("reader task lock") = Some(reader);

        Ok((Self { inner }, incoming_rx))
    }

    pub fn send(&self, envelope: RelayEnvelope) -> Result<()> {
        self.inner
            .outbound
            .send(envelope)
            .map_err(|_| anyhow!("relay websocket is closed"))
    }

    pub async fn register_channel(
        &self,
        channel_id: impl Into<String>,
    ) -> mpsc::UnboundedReceiver<RelayEnvelope> {
        let channel_id = channel_id.into();
        let (tx, rx) = mpsc::unbounded_channel();
        let mut routes = self.inner.channel_routes.lock().await;
        routes.insert(channel_id, tx);
        rx
    }

    pub async fn unregister_channel(&self, channel_id: &str) {
        let mut routes = self.inner.channel_routes.lock().await;
        routes.remove(channel_id);
    }

    pub async fn wait_closed(&self) {
        loop {
            let closed = self.inner.closed_notify.notified();
            tokio::pin!(closed);
            if self.inner.closed.load(Ordering::SeqCst) {
                return;
            }
            closed.await;
            if self.inner.closed.load(Ordering::SeqCst) {
                return;
            }
        }
    }
}

async fn next_relay_transport_frame(
    receiver: &mut mpsc::UnboundedReceiver<RelayEnvelope>,
) -> Option<RelayTransportFrame> {
    let first = receiver.recv().await?;
    let mut batch = vec![first];
    while batch.len() < RELAY_MAX_BATCH_SIZE {
        match receiver.try_recv() {
            Ok(envelope) => batch.push(envelope),
            Err(_) => break,
        }
    }
    RelayTransportFrame::from_envelopes(batch)
}

fn decode_relay_transport_frame(payload: &[u8]) -> serde_json::Result<Vec<RelayEnvelope>> {
    Ok(serde_json::from_slice::<RelayTransportFrame>(payload)?.into_envelopes())
}

pub async fn run_relay_service(
    server_url: String,
    session_token: String,
    heartbeat_client: ControlPlaneClient,
    relay_identity: RelayKeypair,
    local_device_id: String,
    published_services: Vec<PublishedServiceConfig>,
) -> Result<()> {
    let heartbeat_task = spawn_heartbeat_task(heartbeat_client, session_token.clone());
    let run_result = tokio::select! {
        result = run_relay_service_loop(
            server_url,
            session_token,
            relay_identity,
            local_device_id,
            published_services,
            None,
        ) => result,
        _ = tokio::signal::ctrl_c() => Ok(()),
    };
    heartbeat_task.abort();
    run_result
}

pub(crate) async fn run_relay_service_loop(
    server_url: String,
    session_token: String,
    relay_identity: RelayKeypair,
    local_device_id: String,
    published_services: Vec<PublishedServiceConfig>,
    runtime_state: Option<RuntimeStateWriter>,
) -> Result<()> {
    let (relay, mut incoming_rx) = RelayConnection::connect(&server_url, &session_token).await?;
    let published_services = PublishedServiceDirectory::new(&local_device_id, &published_services);

    while let Some(channel) = incoming_rx.recv().await {
        let relay = relay.clone();
        let relay_identity = relay_identity.clone();
        let runtime_state = runtime_state.clone();
        let published_services = published_services.clone();
        tokio::spawn(async move {
            if let Err(err) = handle_incoming_channel(
                relay,
                relay_identity,
                channel,
                published_services,
                runtime_state,
            )
            .await
            {
                warn!("incoming relay channel failed: {err}");
            }
        });
    }

    Ok(())
}

pub async fn run_local_forwarder(
    server_url: String,
    session_token: String,
    heartbeat_client: ControlPlaneClient,
    device_identity: DeviceIdentity,
    resolved_service: ResolvedService,
    local_bind_addr: String,
) -> Result<()> {
    let heartbeat_task = spawn_heartbeat_task(heartbeat_client.clone(), session_token.clone());
    let run_result = tokio::select! {
        result = run_local_forwarder_loop(
            server_url,
            session_token,
            device_identity,
            resolved_service,
            local_bind_addr,
            None,
        ) => result,
        _ = tokio::signal::ctrl_c() => Ok(()),
    };
    heartbeat_task.abort();
    run_result
}

pub(crate) async fn run_local_forwarder_loop(
    server_url: String,
    session_token: String,
    device_identity: DeviceIdentity,
    resolved_service: ResolvedService,
    local_bind_addr: String,
    runtime_hook: Option<ForwardRuntimeHook>,
) -> Result<()> {
    run_local_forwarder_loop_with_control(
        server_url,
        session_token,
        device_identity,
        resolved_service,
        local_bind_addr,
        runtime_hook,
        None,
        None,
    )
    .await
}

pub(crate) async fn run_local_forwarder_loop_with_control(
    server_url: String,
    session_token: String,
    device_identity: DeviceIdentity,
    resolved_service: ResolvedService,
    local_bind_addr: String,
    runtime_hook: Option<ForwardRuntimeHook>,
    tracker: Option<RelayForwarderTracker>,
    mut shutdown_rx: Option<watch::Receiver<bool>>,
) -> Result<()> {
    let (relay, _) = RelayConnection::connect(&server_url, &session_token).await?;
    let listener = TcpListener::bind(&local_bind_addr)
        .await
        .with_context(|| format!("failed to bind local forward address {local_bind_addr}"))?;
    info!(
        "forwarding {} to service {} on device {}",
        local_bind_addr, resolved_service.service.name, resolved_service.service.owner_device_id
    );

    loop {
        let (stream, peer_addr) = tokio::select! {
            accept_result = listener.accept() => {
                accept_result.with_context(|| format!("failed to accept on {local_bind_addr}"))?
            }
            _ = relay.wait_closed() => {
                bail!("relay websocket closed for local forward {local_bind_addr}");
            }
            shutdown_requested = wait_for_forwarder_shutdown(&mut shutdown_rx) => {
                if shutdown_requested {
                    return Ok(());
                }
                continue;
            }
        };
        let relay = relay.clone();
        let device_identity = device_identity.clone();
        let resolved_service = resolved_service.clone();
        let runtime_hook = runtime_hook.clone();
        let tracker = tracker.clone();
        tokio::spawn(async move {
            if let Err(err) = handle_local_forward_connection(
                relay,
                device_identity,
                resolved_service,
                stream,
                runtime_hook,
                tracker,
            )
            .await
            {
                warn!("forward connection from {peer_addr} failed: {err}");
            }
        });
    }
}

async fn handle_incoming_channel(
    relay: RelayConnection,
    relay_identity: RelayKeypair,
    channel: RelayIncomingChannel,
    published_services: PublishedServiceDirectory,
    runtime_state: Option<RuntimeStateWriter>,
) -> Result<()> {
    if device_id_from_public_key(&channel.source_identity_public_key) != channel.source_device_id {
        relay.send(RelayEnvelope::ChannelRejected {
            channel_id: channel.channel_id.clone(),
            reason: "source device identity key does not match source_device_id".to_string(),
        })?;
        return Ok(());
    }
    verify_signature_base64(
        &channel.source_identity_public_key,
        &relay_channel_open_message(
            &channel.channel_id,
            &channel.service_id,
            &channel.source_device_id,
            &channel.source_ephemeral_public_key,
        ),
        &channel.source_open_signature,
    )
    .map_err(|err| anyhow!("source relay open signature verification failed: {err}"))?;

    let Some(published_service) = published_services.resolve(&channel.service_id).cloned() else {
        relay.send(RelayEnvelope::ChannelRejected {
            channel_id: channel.channel_id.clone(),
            reason: "service is not published locally".to_string(),
        })?;
        return Ok(());
    };

    let channel_rx = relay.register_channel(channel.channel_id.clone()).await;
    let socket = match TcpStream::connect((
        published_service.target_host.as_str(),
        published_service.target_port,
    ))
    .await
    {
        Ok(socket) => socket,
        Err(err) => {
            relay.send(RelayEnvelope::ChannelRejected {
                channel_id: channel.channel_id.clone(),
                reason: format!("failed to connect local target: {err}"),
            })?;
            relay.unregister_channel(&channel.channel_id).await;
            return Ok(());
        }
    };
    let shared_secret = relay_identity
        .shared_secret_with_public_key(&channel.source_ephemeral_public_key)
        .map_err(|err| anyhow!("failed to derive relay shared secret: {err}"))?;
    let (sender, receiver) = secure_channel_pair(
        shared_secret,
        &channel.channel_id,
        SecureChannelRole::Responder,
    );

    relay.send(RelayEnvelope::ChannelAccepted {
        channel_id: channel.channel_id.clone(),
    })?;
    if let Some(runtime_state) = &runtime_state {
        runtime_state.note_published_service_connection(
            &published_service.name,
            "relay",
            channel.source_device_id.clone(),
        );
    }
    bridge_stream_with_channel(
        relay,
        channel.channel_id,
        socket,
        channel_rx,
        sender,
        receiver,
    )
    .await
}

pub(crate) async fn handle_local_forward_connection(
    relay: RelayConnection,
    device_identity: DeviceIdentity,
    resolved_service: ResolvedService,
    socket: TcpStream,
    runtime_hook: Option<ForwardRuntimeHook>,
    tracker: Option<RelayForwarderTracker>,
) -> Result<()> {
    let _connection_guard = RelayTrackedConnectionGuard::new(tracker);
    let peer_addr = socket
        .peer_addr()
        .map(|addr| addr.to_string())
        .unwrap_or_else(|_| "unknown".to_string());
    let source_device_id = device_identity.device_id();
    let ephemeral_relay_key = RelayKeypair::generate();
    let source_ephemeral_public_key = ephemeral_relay_key.public_key_base64();
    let channel_id = generate_token("ch");
    let mut channel_rx = relay.register_channel(channel_id.clone()).await;
    let source_open_signature = device_identity.sign_base64(&relay_channel_open_message(
        &channel_id,
        &resolved_service.service.service_id,
        &source_device_id,
        &source_ephemeral_public_key,
    ));
    relay.send(RelayEnvelope::OpenChannel {
        channel_id: channel_id.clone(),
        service_id: resolved_service.service.service_id.clone(),
        source_ephemeral_public_key,
        source_open_signature,
    })?;

    let open_result = timeout(
        Duration::from_secs(10),
        wait_for_channel_open(&mut channel_rx),
    )
    .await
    .map_err(|_| anyhow!("timed out waiting for remote service to accept relay channel"))??;

    match open_result {
        ChannelOpenState::Accepted => {
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_connection(peer_addr.clone());
            }
            let shared_secret = ephemeral_relay_key
                .shared_secret_with_public_key(&resolved_service.target_relay_public_key)
                .map_err(|err| anyhow!("failed to derive relay shared secret: {err}"))?;
            let (sender, receiver) =
                secure_channel_pair(shared_secret, &channel_id, SecureChannelRole::Initiator);
            let bridge_result =
                bridge_stream_with_channel(relay, channel_id, socket, channel_rx, sender, receiver)
                    .await;
            if let Some(runtime_hook) = &runtime_hook {
                runtime_hook.note_connection_closed(peer_addr);
            }
            bridge_result
        }
        ChannelOpenState::Rejected(reason) => {
            relay.unregister_channel(&channel_id).await;
            bail!("remote service rejected relay channel: {reason}");
        }
    }
}

async fn wait_for_channel_open(
    channel_rx: &mut mpsc::UnboundedReceiver<RelayEnvelope>,
) -> Result<ChannelOpenState> {
    while let Some(envelope) = channel_rx.recv().await {
        match envelope {
            RelayEnvelope::ChannelAccepted { .. } => {
                return Ok(ChannelOpenState::Accepted);
            }
            RelayEnvelope::ChannelRejected { reason, .. } => {
                return Ok(ChannelOpenState::Rejected(reason));
            }
            RelayEnvelope::ChannelClose { reason, .. } => {
                return Ok(ChannelOpenState::Rejected(
                    reason.unwrap_or_else(|| "channel closed before open".to_string()),
                ));
            }
            RelayEnvelope::ChannelData { .. }
            | RelayEnvelope::OpenChannel { .. }
            | RelayEnvelope::IncomingChannel { .. }
            | RelayEnvelope::Ready { .. }
            | RelayEnvelope::Ping
            | RelayEnvelope::Pong => {}
        }
    }

    bail!("relay websocket closed while opening channel")
}

async fn bridge_stream_with_channel(
    relay: RelayConnection,
    channel_id: String,
    socket: TcpStream,
    mut channel_rx: mpsc::UnboundedReceiver<RelayEnvelope>,
    mut secure_sender: SecureSender,
    mut secure_receiver: SecureReceiver,
) -> Result<()> {
    let (mut socket_reader, mut socket_writer) = socket.into_split();
    let relay_for_reader = relay.clone();
    let channel_id_for_reader = channel_id.clone();
    let reader_task = tokio::spawn(async move {
        let mut buffer = [0u8; 16 * 1024];
        loop {
            let read = socket_reader
                .read(&mut buffer)
                .await
                .context("failed to read local tcp stream")?;
            if read == 0 {
                let _ = relay_for_reader.send(RelayEnvelope::ChannelClose {
                    channel_id: channel_id_for_reader.clone(),
                    reason: Some("local tcp stream closed".to_string()),
                });
                return Ok::<(), anyhow::Error>(());
            }
            let encrypted_data = secure_sender
                .encrypt_to_base64(&buffer[..read])
                .map_err(|err| anyhow!("failed to encrypt relay payload: {err}"))?;
            relay_for_reader.send(RelayEnvelope::ChannelData {
                channel_id: channel_id_for_reader.clone(),
                data_base64: encrypted_data,
            })?;
        }
    });

    let writer_task = tokio::spawn(async move {
        while let Some(envelope) = channel_rx.recv().await {
            match envelope {
                RelayEnvelope::ChannelData { data_base64, .. } => {
                    let bytes = secure_receiver
                        .decrypt_from_base64(&data_base64)
                        .map_err(|err| anyhow!("failed to decrypt relay payload: {err}"))?;
                    socket_writer
                        .write_all(&bytes)
                        .await
                        .context("failed to write local tcp stream")?;
                }
                RelayEnvelope::ChannelClose { .. } | RelayEnvelope::ChannelRejected { .. } => {
                    socket_writer
                        .shutdown()
                        .await
                        .context("failed to shutdown local tcp stream")?;
                    return Ok::<(), anyhow::Error>(());
                }
                RelayEnvelope::ChannelAccepted { .. }
                | RelayEnvelope::OpenChannel { .. }
                | RelayEnvelope::IncomingChannel { .. }
                | RelayEnvelope::Ready { .. }
                | RelayEnvelope::Ping
                | RelayEnvelope::Pong => {}
            }
        }

        Ok::<(), anyhow::Error>(())
    });

    let result = race_bridge_tasks(reader_task, writer_task).await;
    relay.unregister_channel(&channel_id).await;
    result
}

async fn race_bridge_tasks(
    reader_task: JoinHandle<Result<()>>,
    writer_task: JoinHandle<Result<()>>,
) -> Result<()> {
    tokio::pin!(reader_task);
    tokio::pin!(writer_task);

    tokio::select! {
        reader_result = &mut reader_task => {
            writer_task.abort();
            reader_result.map_err(|err| anyhow!("relay reader task failed: {err}"))?
        }
        writer_result = &mut writer_task => {
            reader_task.abort();
            writer_result.map_err(|err| anyhow!("relay writer task failed: {err}"))?
        }
    }
}

pub fn spawn_heartbeat_task(client: ControlPlaneClient, session_token: String) -> JoinHandle<()> {
    tokio::spawn(async move {
        loop {
            tokio::time::sleep(Duration::from_secs(30)).await;
            if let Err(err) = client.heartbeat(&session_token).await {
                warn!("relay heartbeat failed: {err}");
            }
        }
    })
}

async fn route_channel_message(
    routes: &Arc<Mutex<HashMap<String, mpsc::UnboundedSender<RelayEnvelope>>>>,
    envelope: RelayEnvelope,
) {
    let Some(channel_id) = envelope.channel_id().map(ToOwned::to_owned) else {
        return;
    };

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

async fn clear_all_channels(
    routes: &Arc<Mutex<HashMap<String, mpsc::UnboundedSender<RelayEnvelope>>>>,
) {
    let routes = {
        let mut routes = routes.lock().await;
        std::mem::take(&mut *routes)
    };
    for (channel_id, tx) in routes {
        let _ = tx.send(RelayEnvelope::ChannelClose {
            channel_id,
            reason: Some("relay websocket closed".to_string()),
        });
    }
}

fn websocket_url(server_url: &str) -> Result<String> {
    if let Some(rest) = server_url.strip_prefix("https://") {
        return Ok(format!("wss://{rest}/api/v1/relay/ws"));
    }
    if let Some(rest) = server_url.strip_prefix("http://") {
        return Ok(format!("ws://{rest}/api/v1/relay/ws"));
    }
    bail!("server_url must start with http:// or https://")
}

enum ChannelOpenState {
    Accepted,
    Rejected(String),
}

async fn wait_for_forwarder_shutdown(shutdown_rx: &mut Option<watch::Receiver<bool>>) -> bool {
    let Some(shutdown_rx) = shutdown_rx.as_mut() else {
        return pending::<bool>().await;
    };
    if *shutdown_rx.borrow() {
        return true;
    }
    shutdown_rx.changed().await.is_err() || *shutdown_rx.borrow()
}
