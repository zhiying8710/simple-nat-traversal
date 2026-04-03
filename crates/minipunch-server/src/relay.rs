use std::collections::HashMap;
use std::sync::Arc;

use minipunch_core::{RelayEnvelope, ServiceDefinition, generate_token};
use tokio::sync::{Mutex, mpsc};
use tracing::warn;

use crate::db::Database;

#[derive(Clone)]
pub struct RelayHub {
    inner: Arc<Mutex<RelayHubState>>,
}

struct RelayHubState {
    device_connections: HashMap<String, HashMap<String, RelayDeviceConnection>>,
    channels: HashMap<String, RelayChannel>,
    next_connection_sequence: u64,
    preferred_legacy_incoming_by_device: HashMap<String, String>,
}

#[derive(Clone)]
struct RelayDeviceConnection {
    sender: mpsc::UnboundedSender<RelayEnvelope>,
    incoming_channel_support: IncomingChannelSupport,
    registration_sequence: u64,
    originated_open_channel: bool,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum IncomingChannelSupport {
    ExplicitYes,
    ExplicitNo,
    LegacyUnknown,
}

impl IncomingChannelSupport {
    fn from_query(incoming: Option<bool>) -> Self {
        match incoming {
            Some(true) => Self::ExplicitYes,
            Some(false) => Self::ExplicitNo,
            None => Self::LegacyUnknown,
        }
    }
}

struct RelayChannel {
    source_device_id: String,
    source_connection_id: String,
    target_device_id: String,
    target_connection_id: String,
    accepted: bool,
}

impl RelayHub {
    pub fn new() -> Self {
        Self {
            inner: Arc::new(Mutex::new(RelayHubState {
                device_connections: HashMap::new(),
                channels: HashMap::new(),
                next_connection_sequence: 0,
                preferred_legacy_incoming_by_device: HashMap::new(),
            })),
        }
    }

    pub async fn register_device(
        &self,
        device_id: String,
        sender: mpsc::UnboundedSender<RelayEnvelope>,
        incoming: Option<bool>,
    ) -> String {
        let connection_id = generate_token("relay");
        let mut inner = self.inner.lock().await;
        let registration_sequence = inner.next_connection_sequence;
        inner.next_connection_sequence = inner.next_connection_sequence.wrapping_add(1);
        let device_id_key = device_id.clone();
        inner
            .device_connections
            .entry(device_id)
            .or_default()
            .insert(
                connection_id.clone(),
                RelayDeviceConnection {
                    sender,
                    incoming_channel_support: IncomingChannelSupport::from_query(incoming),
                    registration_sequence,
                    originated_open_channel: false,
                },
            );
        maybe_assign_preferred_legacy_connection(&mut inner, &device_id_key, &connection_id);
        connection_id
    }

    pub async fn disconnect_device(&self, device_id: &str, connection_id: &str) {
        let notifications = {
            let mut inner = self.inner.lock().await;
            remove_connection_entry(&mut inner, device_id, connection_id);

            let affected_channel_ids = inner
                .channels
                .iter()
                .filter(|(_, channel)| {
                    (channel.source_device_id == device_id
                        && channel.source_connection_id == connection_id)
                        || (channel.target_device_id == device_id
                            && channel.target_connection_id == connection_id)
                })
                .map(|(channel_id, _)| channel_id.clone())
                .collect::<Vec<_>>();

            let mut notifications = Vec::new();
            for channel_id in affected_channel_ids {
                if let Some(channel) = inner.channels.remove(&channel_id) {
                    let (peer_device_id, peer_connection_id) = if channel.source_device_id
                        == device_id
                        && channel.source_connection_id == connection_id
                    {
                        (channel.target_device_id, channel.target_connection_id)
                    } else {
                        (channel.source_device_id, channel.source_connection_id)
                    };
                    if let Some(peer_tx) =
                        lookup_connection_sender(&inner, &peer_device_id, &peer_connection_id)
                    {
                        notifications.push((
                            peer_tx,
                            RelayEnvelope::ChannelClose {
                                channel_id,
                                reason: Some("peer disconnected".to_string()),
                            },
                        ));
                    }
                }
            }
            notifications
        };

        for (tx, envelope) in notifications {
            let _ = tx.send(envelope);
        }
    }

    pub async fn reset(&self) {
        let mut inner = self.inner.lock().await;
        inner.device_connections.clear();
        inner.channels.clear();
        inner.preferred_legacy_incoming_by_device.clear();
    }

    pub async fn handle_envelope(
        &self,
        db: &Database,
        sender_device_id: &str,
        sender_connection_id: &str,
        envelope: RelayEnvelope,
    ) {
        match envelope {
            RelayEnvelope::OpenChannel {
                channel_id,
                service_id,
                source_ephemeral_public_key,
                source_open_signature,
            } => {
                self.note_connection_opened_channel(sender_device_id, sender_connection_id)
                    .await;
                self.handle_open_channel(
                    db,
                    sender_device_id,
                    sender_connection_id,
                    channel_id,
                    service_id,
                    source_ephemeral_public_key,
                    source_open_signature,
                )
                .await;
            }
            RelayEnvelope::ChannelAccepted { channel_id } => {
                self.handle_channel_accepted(sender_device_id, sender_connection_id, channel_id)
                    .await;
            }
            RelayEnvelope::ChannelRejected { channel_id, reason } => {
                self.handle_channel_rejected(
                    sender_device_id,
                    sender_connection_id,
                    channel_id,
                    reason,
                )
                .await;
            }
            RelayEnvelope::ChannelData {
                channel_id,
                data_base64,
            } => {
                self.handle_channel_data(
                    sender_device_id,
                    sender_connection_id,
                    channel_id,
                    data_base64,
                )
                .await;
            }
            RelayEnvelope::ChannelClose { channel_id, reason } => {
                self.handle_channel_close(
                    sender_device_id,
                    sender_connection_id,
                    channel_id,
                    reason,
                )
                .await;
            }
            RelayEnvelope::Ping => {
                self.send_to_connection(
                    sender_device_id,
                    sender_connection_id,
                    RelayEnvelope::Pong,
                )
                .await;
            }
            RelayEnvelope::Pong
            | RelayEnvelope::Ready { .. }
            | RelayEnvelope::IncomingChannel { .. } => {}
        }
    }

    async fn handle_open_channel(
        &self,
        db: &Database,
        source_device_id: &str,
        source_connection_id: &str,
        channel_id: String,
        service_id: String,
        source_ephemeral_public_key: String,
        source_open_signature: String,
    ) {
        let service = match db
            .authorize_relay_service(source_device_id, &service_id)
            .await
        {
            Ok(service) => service,
            Err(err) => {
                self.send_to_connection(
                    source_device_id,
                    source_connection_id,
                    RelayEnvelope::ChannelRejected {
                        channel_id,
                        reason: err.to_string(),
                    },
                )
                .await;
                return;
            }
        };
        let source_identity_public_key = match db.device_identity_public_key(source_device_id).await
        {
            Ok(public_key) => public_key,
            Err(err) => {
                self.send_to_connection(
                    source_device_id,
                    source_connection_id,
                    RelayEnvelope::ChannelRejected {
                        channel_id,
                        reason: err.to_string(),
                    },
                )
                .await;
                return;
            }
        };

        let incoming_channel = incoming_channel_from_service(
            source_device_id.to_string(),
            source_identity_public_key,
            source_ephemeral_public_key,
            source_open_signature,
            service.clone(),
            channel_id.clone(),
        );

        loop {
            let (target_connection_id, target_tx) = {
                let mut inner = self.inner.lock().await;
                if inner.channels.contains_key(&channel_id) {
                    drop(inner);
                    self.send_to_connection(
                        source_device_id,
                        source_connection_id,
                        RelayEnvelope::ChannelRejected {
                            channel_id,
                            reason: "channel already exists".to_string(),
                        },
                    )
                    .await;
                    return;
                }

                let Some((target_connection_id, target_tx)) =
                    pick_incoming_connection(&mut inner, &service.owner_device_id)
                else {
                    drop(inner);
                    self.send_to_connection(
                        source_device_id,
                        source_connection_id,
                        RelayEnvelope::ChannelRejected {
                            channel_id,
                            reason: "target device has no relay service connection".to_string(),
                        },
                    )
                    .await;
                    return;
                };

                inner.channels.insert(
                    channel_id.clone(),
                    RelayChannel {
                        source_device_id: source_device_id.to_string(),
                        source_connection_id: source_connection_id.to_string(),
                        target_device_id: service.owner_device_id.clone(),
                        target_connection_id: target_connection_id.clone(),
                        accepted: false,
                    },
                );
                (target_connection_id, target_tx)
            };

            if target_tx.send(incoming_channel.clone()).is_ok() {
                return;
            }

            let mut inner = self.inner.lock().await;
            remove_connection_entry(&mut inner, &service.owner_device_id, &target_connection_id);
            inner.channels.remove(&channel_id);
        }
    }

    async fn handle_channel_accepted(
        &self,
        sender_device_id: &str,
        sender_connection_id: &str,
        channel_id: String,
    ) {
        let source_tx = {
            let mut inner = self.inner.lock().await;
            let Some(channel) = inner.channels.get_mut(&channel_id) else {
                return;
            };
            if channel.target_device_id != sender_device_id
                || channel.target_connection_id != sender_connection_id
            {
                return;
            }
            channel.accepted = true;
            let source_device_id = channel.source_device_id.clone();
            let source_connection_id = channel.source_connection_id.clone();
            lookup_connection_sender(&inner, &source_device_id, &source_connection_id)
        };

        if let Some(source_tx) = source_tx {
            let _ = source_tx.send(RelayEnvelope::ChannelAccepted { channel_id });
        }
    }

    async fn handle_channel_rejected(
        &self,
        sender_device_id: &str,
        sender_connection_id: &str,
        channel_id: String,
        reason: String,
    ) {
        let source_tx = {
            let mut inner = self.inner.lock().await;
            let Some((source_device_id, source_connection_id)) =
                inner.channels.get(&channel_id).and_then(|channel| {
                    if channel.target_device_id != sender_device_id
                        || channel.target_connection_id != sender_connection_id
                    {
                        None
                    } else {
                        Some((
                            channel.source_device_id.clone(),
                            channel.source_connection_id.clone(),
                        ))
                    }
                })
            else {
                return;
            };
            let source_tx =
                lookup_connection_sender(&inner, &source_device_id, &source_connection_id);
            inner.channels.remove(&channel_id);
            source_tx
        };

        if let Some(source_tx) = source_tx {
            let _ = source_tx.send(RelayEnvelope::ChannelRejected { channel_id, reason });
        }
    }

    async fn handle_channel_data(
        &self,
        sender_device_id: &str,
        sender_connection_id: &str,
        channel_id: String,
        data_base64: String,
    ) {
        let peer_tx = {
            let inner = self.inner.lock().await;
            let Some(channel) = inner.channels.get(&channel_id) else {
                return;
            };
            if !channel.accepted {
                return;
            }
            if channel.source_device_id == sender_device_id
                && channel.source_connection_id == sender_connection_id
            {
                lookup_connection_sender(
                    &inner,
                    &channel.target_device_id,
                    &channel.target_connection_id,
                )
            } else if channel.target_device_id == sender_device_id
                && channel.target_connection_id == sender_connection_id
            {
                lookup_connection_sender(
                    &inner,
                    &channel.source_device_id,
                    &channel.source_connection_id,
                )
            } else {
                None
            }
        };

        if let Some(peer_tx) = peer_tx {
            if peer_tx
                .send(RelayEnvelope::ChannelData {
                    channel_id,
                    data_base64,
                })
                .is_err()
            {
                warn!("relay peer closed while forwarding channel data");
            }
        }
    }

    async fn handle_channel_close(
        &self,
        sender_device_id: &str,
        sender_connection_id: &str,
        channel_id: String,
        reason: Option<String>,
    ) {
        let peer_tx = {
            let mut inner = self.inner.lock().await;
            let Some(channel) = inner.channels.get(&channel_id) else {
                return;
            };
            let (peer_device_id, peer_connection_id) = if channel.source_device_id
                == sender_device_id
                && channel.source_connection_id == sender_connection_id
            {
                (
                    channel.target_device_id.clone(),
                    channel.target_connection_id.clone(),
                )
            } else if channel.target_device_id == sender_device_id
                && channel.target_connection_id == sender_connection_id
            {
                (
                    channel.source_device_id.clone(),
                    channel.source_connection_id.clone(),
                )
            } else {
                return;
            };
            let peer_tx = lookup_connection_sender(&inner, &peer_device_id, &peer_connection_id);
            inner.channels.remove(&channel_id);
            peer_tx
        };

        if let Some(peer_tx) = peer_tx {
            let _ = peer_tx.send(RelayEnvelope::ChannelClose { channel_id, reason });
        }
    }

    async fn send_to_connection(
        &self,
        device_id: &str,
        connection_id: &str,
        envelope: RelayEnvelope,
    ) {
        let tx = {
            let inner = self.inner.lock().await;
            lookup_connection_sender(&inner, device_id, connection_id)
        };
        if let Some(tx) = tx {
            let _ = tx.send(envelope);
        }
    }

    async fn note_connection_opened_channel(&self, device_id: &str, connection_id: &str) {
        let mut inner = self.inner.lock().await;
        let removed_preferred = inner
            .preferred_legacy_incoming_by_device
            .get(device_id)
            .map(|preferred| preferred == connection_id)
            .unwrap_or(false);
        let Some((connection_registration_sequence, incoming_channel_support)) = inner
            .device_connections
            .get(device_id)
            .and_then(|connections| connections.get(connection_id))
            .map(|connection| {
                (
                    connection.registration_sequence,
                    connection.incoming_channel_support,
                )
            })
        else {
            return;
        };
        if let Some(connection) = inner
            .device_connections
            .get_mut(device_id)
            .and_then(|connections| connections.get_mut(connection_id))
        {
            connection.originated_open_channel = true;
            if incoming_channel_support == IncomingChannelSupport::LegacyUnknown {
                connection.incoming_channel_support = IncomingChannelSupport::ExplicitNo;
            }
        }
        if removed_preferred {
            inner.preferred_legacy_incoming_by_device.remove(device_id);
            maybe_promote_newer_legacy_connection(
                &mut inner,
                device_id,
                connection_registration_sequence,
            );
        }
    }
}

fn lookup_connection_sender(
    inner: &RelayHubState,
    device_id: &str,
    connection_id: &str,
) -> Option<mpsc::UnboundedSender<RelayEnvelope>> {
    inner
        .device_connections
        .get(device_id)?
        .get(connection_id)
        .map(|connection| connection.sender.clone())
}

fn is_eligible_legacy_incoming_connection(connection: &RelayDeviceConnection) -> bool {
    connection.incoming_channel_support == IncomingChannelSupport::LegacyUnknown
        && !connection.originated_open_channel
        && !connection.sender.is_closed()
}

fn refresh_preferred_legacy_connection(inner: &mut RelayHubState, device_id: &str) {
    let Some(preferred_connection_id) = inner
        .preferred_legacy_incoming_by_device
        .get(device_id)
        .cloned()
    else {
        return;
    };
    let preferred_state = inner
        .device_connections
        .get(device_id)
        .and_then(|connections| connections.get(&preferred_connection_id))
        .map(|connection| {
            (
                is_eligible_legacy_incoming_connection(connection),
                connection.registration_sequence,
            )
        });
    match preferred_state {
        Some((true, _)) => {}
        Some((false, removed_registration_sequence)) => {
            inner.preferred_legacy_incoming_by_device.remove(device_id);
            maybe_promote_newer_legacy_connection(inner, device_id, removed_registration_sequence);
        }
        None => {
            inner.preferred_legacy_incoming_by_device.remove(device_id);
        }
    }
}

fn pick_incoming_connection(
    inner: &mut RelayHubState,
    device_id: &str,
) -> Option<(String, mpsc::UnboundedSender<RelayEnvelope>)> {
    let Some(connections) = inner.device_connections.get(device_id) else {
        return None;
    };

    if let Some((connection_id, connection)) = connections
        .iter()
        .filter(|(_, connection)| {
            connection.incoming_channel_support == IncomingChannelSupport::ExplicitYes
                && !connection.sender.is_closed()
        })
        .max_by_key(|(_, connection)| connection.registration_sequence)
    {
        return Some((connection_id.clone(), connection.sender.clone()));
    }

    refresh_preferred_legacy_connection(inner, device_id);

    let preferred_connection_id = inner.preferred_legacy_incoming_by_device.get(device_id)?;
    let connection = inner
        .device_connections
        .get(device_id)?
        .get(preferred_connection_id)?;
    if !is_eligible_legacy_incoming_connection(connection) {
        return None;
    }
    Some((preferred_connection_id.clone(), connection.sender.clone()))
}

fn remove_connection_entry(inner: &mut RelayHubState, device_id: &str, connection_id: &str) {
    let removed_registration_sequence = inner
        .device_connections
        .get(device_id)
        .and_then(|connections| connections.get(connection_id))
        .map(|connection| connection.registration_sequence);
    let removed_preferred = inner
        .preferred_legacy_incoming_by_device
        .get(device_id)
        .map(|preferred| preferred == connection_id)
        .unwrap_or(false);

    let remove_device_entry = match inner.device_connections.get_mut(device_id) {
        Some(connections) => {
            connections.remove(connection_id);
            connections.is_empty()
        }
        None => false,
    };
    if remove_device_entry {
        inner.device_connections.remove(device_id);
        inner.preferred_legacy_incoming_by_device.remove(device_id);
    } else if removed_preferred {
        inner.preferred_legacy_incoming_by_device.remove(device_id);
        if let Some(removed_registration_sequence) = removed_registration_sequence {
            maybe_promote_newer_legacy_connection(inner, device_id, removed_registration_sequence);
        }
    }
}

fn maybe_assign_preferred_legacy_connection(
    inner: &mut RelayHubState,
    device_id: &str,
    connection_id: &str,
) {
    refresh_preferred_legacy_connection(inner, device_id);

    if inner
        .preferred_legacy_incoming_by_device
        .contains_key(device_id)
    {
        return;
    }
    let Some(connection) = inner
        .device_connections
        .get(device_id)
        .and_then(|connections| connections.get(connection_id))
    else {
        return;
    };
    if is_eligible_legacy_incoming_connection(connection) {
        inner
            .preferred_legacy_incoming_by_device
            .insert(device_id.to_string(), connection_id.to_string());
    }
}

fn maybe_promote_newer_legacy_connection(
    inner: &mut RelayHubState,
    device_id: &str,
    removed_registration_sequence: u64,
) {
    let Some(connections) = inner.device_connections.get(device_id) else {
        return;
    };
    let replacement = connections
        .iter()
        .filter(|(_, connection)| {
            connection.incoming_channel_support == IncomingChannelSupport::LegacyUnknown
                && !connection.originated_open_channel
                && !connection.sender.is_closed()
                && connection.registration_sequence > removed_registration_sequence
        })
        .max_by_key(|(_, connection)| connection.registration_sequence)
        .map(|(connection_id, _)| connection_id.clone());
    if let Some(replacement) = replacement {
        inner
            .preferred_legacy_incoming_by_device
            .insert(device_id.to_string(), replacement);
    }
}

fn incoming_channel_from_service(
    source_device_id: String,
    source_identity_public_key: String,
    source_ephemeral_public_key: String,
    source_open_signature: String,
    service: ServiceDefinition,
    channel_id: String,
) -> RelayEnvelope {
    RelayEnvelope::IncomingChannel {
        channel_id,
        source_device_id,
        source_identity_public_key,
        source_ephemeral_public_key,
        source_open_signature,
        service_id: service.service_id,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use minipunch_core::{
        CreateJoinTokenRequest, DeviceIdentity, RegisterDeviceRequest, RegisterDeviceResponse,
        RelayKeypair, UpsertServiceRequest, registration_message, relay_channel_open_message,
        relay_key_binding_message, service_id,
    };

    fn test_db_path(name: &str) -> std::path::PathBuf {
        let mut path = std::env::temp_dir();
        path.push(format!(
            "minipunch-relay-test-{name}-{}.db",
            generate_token("tmp")
        ));
        path
    }

    async fn register_test_device(
        db: &Database,
        join_token: String,
        device_name: &str,
        identity: &DeviceIdentity,
        relay_identity: &RelayKeypair,
    ) -> RegisterDeviceResponse {
        let device_id = identity.device_id();
        let nonce = generate_token("nonce");
        db.register_device(RegisterDeviceRequest {
            join_token: Some(join_token),
            device_id: device_id.clone(),
            device_name: device_name.to_string(),
            os: "test-os".to_string(),
            version: "test-version".to_string(),
            public_key: identity.public_key_base64(),
            relay_public_key: relay_identity.public_key_base64(),
            relay_public_key_signature: identity.sign_base64(&relay_key_binding_message(
                &device_id,
                &relay_identity.public_key_base64(),
            )),
            nonce: nonce.clone(),
            signature: identity.sign_base64(&registration_message(
                &device_id,
                device_name,
                "test-os",
                &nonce,
            )),
        })
        .await
        .expect("register test device")
    }

    #[tokio::test]
    async fn routes_channels_by_connection_and_prefers_service_capable_target_socket() {
        let db_path = test_db_path("multi-connection-routing");
        let db = Database::open(&db_path).await.expect("open test db");
        let bootstrap = db.bootstrap_init().await.expect("bootstrap server");
        let source_join = db
            .create_join_token(
                &bootstrap.admin_token,
                CreateJoinTokenRequest {
                    expires_in_minutes: None,
                    note: Some("source".to_string()),
                },
            )
            .await
            .expect("create source join token");

        let source_identity = DeviceIdentity::generate();
        let source_relay_identity = RelayKeypair::generate();
        let source = register_test_device(
            &db,
            source_join.join_token,
            "source",
            &source_identity,
            &source_relay_identity,
        )
        .await;

        let target_identity = DeviceIdentity::generate();
        let target_relay_identity = RelayKeypair::generate();
        let target = register_test_device(
            &db,
            bootstrap.first_join_token,
            "target",
            &target_identity,
            &target_relay_identity,
        )
        .await;

        db.upsert_service(
            &target.session_token,
            UpsertServiceRequest {
                name: "ssh".to_string(),
                allowed_device_ids: vec![source.device_id.clone()],
            },
        )
        .await
        .expect("publish test service");

        let hub = RelayHub::new();

        let (source_a_tx, mut source_a_rx) = mpsc::unbounded_channel();
        let source_a_connection_id = hub
            .register_device(source.device_id.clone(), source_a_tx, Some(false))
            .await;
        let (source_b_tx, mut source_b_rx) = mpsc::unbounded_channel();
        let source_b_connection_id = hub
            .register_device(source.device_id.clone(), source_b_tx, Some(false))
            .await;
        let (target_forward_tx, mut target_forward_rx) = mpsc::unbounded_channel();
        let _target_forward_connection_id = hub
            .register_device(target.device_id.clone(), target_forward_tx, Some(false))
            .await;
        let (target_service_tx, mut target_service_rx) = mpsc::unbounded_channel();
        let target_service_connection_id = hub
            .register_device(target.device_id.clone(), target_service_tx, Some(true))
            .await;

        let channel_id = generate_token("ch");
        let service_id = service_id(&target.device_id, "ssh");
        let source_ephemeral_key = RelayKeypair::generate();
        let source_ephemeral_public_key = source_ephemeral_key.public_key_base64();
        hub.handle_envelope(
            &db,
            &source.device_id,
            &source_a_connection_id,
            RelayEnvelope::OpenChannel {
                channel_id: channel_id.clone(),
                service_id: service_id.clone(),
                source_ephemeral_public_key: source_ephemeral_public_key.clone(),
                source_open_signature: source_identity.sign_base64(&relay_channel_open_message(
                    &channel_id,
                    &service_id,
                    &source.device_id,
                    &source_ephemeral_public_key,
                )),
            },
        )
        .await;

        let incoming = target_service_rx
            .recv()
            .await
            .expect("service-capable target connection should receive incoming channel");
        match incoming {
            RelayEnvelope::IncomingChannel {
                channel_id: received_channel_id,
                source_device_id,
                service_id: received_service_id,
                ..
            } => {
                assert_eq!(received_channel_id, channel_id);
                assert_eq!(source_device_id, source.device_id);
                assert_eq!(received_service_id, service_id);
            }
            other => panic!("expected incoming channel, got {other:?}"),
        }
        assert!(target_forward_rx.try_recv().is_err());

        hub.handle_envelope(
            &db,
            &target.device_id,
            &target_service_connection_id,
            RelayEnvelope::ChannelAccepted {
                channel_id: channel_id.clone(),
            },
        )
        .await;

        let accepted = source_a_rx
            .recv()
            .await
            .expect("source connection A should receive acceptance");
        match accepted {
            RelayEnvelope::ChannelAccepted {
                channel_id: accepted_channel_id,
            } => assert_eq!(accepted_channel_id, channel_id),
            other => panic!("expected channel accepted, got {other:?}"),
        }
        assert!(source_b_rx.try_recv().is_err());

        hub.handle_envelope(
            &db,
            &source.device_id,
            &source_a_connection_id,
            RelayEnvelope::ChannelData {
                channel_id: channel_id.clone(),
                data_base64: "source-to-target".to_string(),
            },
        )
        .await;
        let forwarded_to_target = target_service_rx
            .recv()
            .await
            .expect("target service connection should receive channel data");
        match forwarded_to_target {
            RelayEnvelope::ChannelData {
                channel_id: forwarded_channel_id,
                data_base64,
            } => {
                assert_eq!(forwarded_channel_id, channel_id);
                assert_eq!(data_base64, "source-to-target");
            }
            other => panic!("expected target channel data, got {other:?}"),
        }

        hub.handle_envelope(
            &db,
            &target.device_id,
            &target_service_connection_id,
            RelayEnvelope::ChannelData {
                channel_id: channel_id.clone(),
                data_base64: "target-to-source".to_string(),
            },
        )
        .await;
        let forwarded_to_source = source_a_rx
            .recv()
            .await
            .expect("source connection A should receive reply data");
        match forwarded_to_source {
            RelayEnvelope::ChannelData {
                channel_id: forwarded_channel_id,
                data_base64,
            } => {
                assert_eq!(forwarded_channel_id, channel_id);
                assert_eq!(data_base64, "target-to-source");
            }
            other => panic!("expected source channel data, got {other:?}"),
        }
        assert!(source_b_rx.try_recv().is_err());

        hub.disconnect_device(&source.device_id, &source_a_connection_id)
            .await;
        let close_notice = target_service_rx
            .recv()
            .await
            .expect("target service connection should receive close on source disconnect");
        match close_notice {
            RelayEnvelope::ChannelClose {
                channel_id: closed_channel_id,
                reason,
            } => {
                assert_eq!(closed_channel_id, channel_id);
                assert_eq!(reason.as_deref(), Some("peer disconnected"));
            }
            other => panic!("expected channel close, got {other:?}"),
        }

        let new_channel_id = generate_token("ch");
        let new_source_ephemeral_key = RelayKeypair::generate();
        let new_source_ephemeral_public_key = new_source_ephemeral_key.public_key_base64();
        hub.handle_envelope(
            &db,
            &source.device_id,
            &source_b_connection_id,
            RelayEnvelope::OpenChannel {
                channel_id: new_channel_id.clone(),
                service_id: service_id.clone(),
                source_ephemeral_public_key: new_source_ephemeral_public_key.clone(),
                source_open_signature: source_identity.sign_base64(&relay_channel_open_message(
                    &new_channel_id,
                    &service_id,
                    &source.device_id,
                    &new_source_ephemeral_public_key,
                )),
            },
        )
        .await;
        let second_incoming = target_service_rx
            .recv()
            .await
            .expect("remaining source connection should still work");
        match second_incoming {
            RelayEnvelope::IncomingChannel { channel_id, .. } => {
                assert_eq!(channel_id, new_channel_id);
            }
            other => panic!("expected second incoming channel, got {other:?}"),
        }

        let _ = std::fs::remove_file(db_path);
    }

    #[tokio::test]
    async fn incoming_channels_prefer_newest_live_service_connection() {
        let db_path = test_db_path("prefer-newest-service-connection");
        let db = Database::open(&db_path).await.expect("open test db");
        let bootstrap = db.bootstrap_init().await.expect("bootstrap server");
        let source_join = db
            .create_join_token(
                &bootstrap.admin_token,
                CreateJoinTokenRequest {
                    expires_in_minutes: None,
                    note: Some("source".to_string()),
                },
            )
            .await
            .expect("create source join token");

        let source_identity = DeviceIdentity::generate();
        let source_relay_identity = RelayKeypair::generate();
        let source = register_test_device(
            &db,
            source_join.join_token,
            "source",
            &source_identity,
            &source_relay_identity,
        )
        .await;

        let target_identity = DeviceIdentity::generate();
        let target_relay_identity = RelayKeypair::generate();
        let target = register_test_device(
            &db,
            bootstrap.first_join_token,
            "target",
            &target_identity,
            &target_relay_identity,
        )
        .await;

        db.upsert_service(
            &target.session_token,
            UpsertServiceRequest {
                name: "ssh".to_string(),
                allowed_device_ids: vec![source.device_id.clone()],
            },
        )
        .await
        .expect("publish test service");

        let hub = RelayHub::new();
        let (source_tx, _source_rx) = mpsc::unbounded_channel();
        let source_connection_id = hub
            .register_device(source.device_id.clone(), source_tx, Some(false))
            .await;
        let (target_old_tx, mut target_old_rx) = mpsc::unbounded_channel();
        let _target_old_connection_id = hub
            .register_device(target.device_id.clone(), target_old_tx, Some(true))
            .await;
        let (target_new_tx, mut target_new_rx) = mpsc::unbounded_channel();
        let _target_new_connection_id = hub
            .register_device(target.device_id.clone(), target_new_tx, Some(true))
            .await;

        let service_id = service_id(&target.device_id, "ssh");
        let first_channel_id = generate_token("ch");
        let first_ephemeral_key = RelayKeypair::generate();
        let first_ephemeral_public_key = first_ephemeral_key.public_key_base64();
        hub.handle_envelope(
            &db,
            &source.device_id,
            &source_connection_id,
            RelayEnvelope::OpenChannel {
                channel_id: first_channel_id.clone(),
                service_id: service_id.clone(),
                source_ephemeral_public_key: first_ephemeral_public_key.clone(),
                source_open_signature: source_identity.sign_base64(&relay_channel_open_message(
                    &first_channel_id,
                    &service_id,
                    &source.device_id,
                    &first_ephemeral_public_key,
                )),
            },
        )
        .await;

        match target_new_rx
            .recv()
            .await
            .expect("newest live service connection should receive the first incoming channel")
        {
            RelayEnvelope::IncomingChannel { channel_id, .. } => {
                assert_eq!(channel_id, first_channel_id);
            }
            other => panic!("expected first incoming channel on newest socket, got {other:?}"),
        }
        assert!(target_old_rx.try_recv().is_err());

        drop(target_new_rx);

        let second_channel_id = generate_token("ch");
        let second_ephemeral_key = RelayKeypair::generate();
        let second_ephemeral_public_key = second_ephemeral_key.public_key_base64();
        hub.handle_envelope(
            &db,
            &source.device_id,
            &source_connection_id,
            RelayEnvelope::OpenChannel {
                channel_id: second_channel_id.clone(),
                service_id: service_id.clone(),
                source_ephemeral_public_key: second_ephemeral_public_key.clone(),
                source_open_signature: source_identity.sign_base64(&relay_channel_open_message(
                    &second_channel_id,
                    &service_id,
                    &source.device_id,
                    &second_ephemeral_public_key,
                )),
            },
        )
        .await;

        match target_old_rx
            .recv()
            .await
            .expect("older live service connection should receive fallback incoming channel")
        {
            RelayEnvelope::IncomingChannel { channel_id, .. } => {
                assert_eq!(channel_id, second_channel_id);
            }
            other => panic!("expected fallback incoming channel on older socket, got {other:?}"),
        }

        let _ = std::fs::remove_file(db_path);
    }

    #[tokio::test]
    async fn legacy_connections_without_query_prefer_oldest_socket_for_incoming_channels() {
        let db_path = test_db_path("legacy-prefers-oldest-socket");
        let db = Database::open(&db_path).await.expect("open test db");
        let bootstrap = db.bootstrap_init().await.expect("bootstrap server");
        let source_join = db
            .create_join_token(
                &bootstrap.admin_token,
                CreateJoinTokenRequest {
                    expires_in_minutes: None,
                    note: Some("source".to_string()),
                },
            )
            .await
            .expect("create source join token");

        let source_identity = DeviceIdentity::generate();
        let source_relay_identity = RelayKeypair::generate();
        let source = register_test_device(
            &db,
            source_join.join_token,
            "source",
            &source_identity,
            &source_relay_identity,
        )
        .await;

        let target_identity = DeviceIdentity::generate();
        let target_relay_identity = RelayKeypair::generate();
        let target = register_test_device(
            &db,
            bootstrap.first_join_token,
            "target",
            &target_identity,
            &target_relay_identity,
        )
        .await;

        db.upsert_service(
            &target.session_token,
            UpsertServiceRequest {
                name: "ssh".to_string(),
                allowed_device_ids: vec![source.device_id.clone()],
            },
        )
        .await
        .expect("publish test service");

        let hub = RelayHub::new();
        let (source_tx, _source_rx) = mpsc::unbounded_channel();
        let source_connection_id = hub
            .register_device(source.device_id.clone(), source_tx, None)
            .await;
        let (target_service_tx, mut target_service_rx) = mpsc::unbounded_channel();
        let _target_service_connection_id = hub
            .register_device(target.device_id.clone(), target_service_tx, None)
            .await;
        let (target_forward_tx, mut target_forward_rx) = mpsc::unbounded_channel();
        let target_forward_connection_id = hub
            .register_device(target.device_id.clone(), target_forward_tx, None)
            .await;

        let channel_id = generate_token("ch");
        let service_id = service_id(&target.device_id, "ssh");
        let source_ephemeral_key = RelayKeypair::generate();
        let source_ephemeral_public_key = source_ephemeral_key.public_key_base64();
        hub.handle_envelope(
            &db,
            &source.device_id,
            &source_connection_id,
            RelayEnvelope::OpenChannel {
                channel_id: channel_id.clone(),
                service_id: service_id.clone(),
                source_ephemeral_public_key: source_ephemeral_public_key.clone(),
                source_open_signature: source_identity.sign_base64(&relay_channel_open_message(
                    &channel_id,
                    &service_id,
                    &source.device_id,
                    &source_ephemeral_public_key,
                )),
            },
        )
        .await;

        match target_service_rx
            .recv()
            .await
            .expect("oldest legacy socket should receive incoming channel")
        {
            RelayEnvelope::IncomingChannel {
                channel_id: received_channel_id,
                ..
            } => assert_eq!(received_channel_id, channel_id),
            other => panic!("expected incoming channel on legacy service socket, got {other:?}"),
        }
        assert!(target_forward_rx.try_recv().is_err());

        hub.note_connection_opened_channel(&target.device_id, &target_forward_connection_id)
            .await;

        let second_channel_id = generate_token("ch");
        let second_source_ephemeral_key = RelayKeypair::generate();
        let second_source_ephemeral_public_key = second_source_ephemeral_key.public_key_base64();
        hub.handle_envelope(
            &db,
            &source.device_id,
            &source_connection_id,
            RelayEnvelope::OpenChannel {
                channel_id: second_channel_id.clone(),
                service_id: service_id.clone(),
                source_ephemeral_public_key: second_source_ephemeral_public_key.clone(),
                source_open_signature: source_identity.sign_base64(&relay_channel_open_message(
                    &second_channel_id,
                    &service_id,
                    &source.device_id,
                    &second_source_ephemeral_public_key,
                )),
            },
        )
        .await;

        match target_service_rx
            .recv()
            .await
            .expect("legacy forward socket should be excluded after originating channels")
        {
            RelayEnvelope::IncomingChannel {
                channel_id: received_channel_id,
                ..
            } => assert_eq!(received_channel_id, second_channel_id),
            other => {
                panic!("expected second incoming channel on legacy service socket, got {other:?}")
            }
        }
        assert!(target_forward_rx.try_recv().is_err());

        let _ = std::fs::remove_file(db_path);
    }

    #[tokio::test]
    async fn legacy_service_reconnect_supersedes_older_idle_forward_socket() {
        let db_path = test_db_path("legacy-service-reconnect");
        let db = Database::open(&db_path).await.expect("open test db");
        let bootstrap = db.bootstrap_init().await.expect("bootstrap server");
        let source_join = db
            .create_join_token(
                &bootstrap.admin_token,
                CreateJoinTokenRequest {
                    expires_in_minutes: None,
                    note: Some("source".to_string()),
                },
            )
            .await
            .expect("create source join token");

        let source_identity = DeviceIdentity::generate();
        let source_relay_identity = RelayKeypair::generate();
        let source = register_test_device(
            &db,
            source_join.join_token,
            "source",
            &source_identity,
            &source_relay_identity,
        )
        .await;

        let target_identity = DeviceIdentity::generate();
        let target_relay_identity = RelayKeypair::generate();
        let target = register_test_device(
            &db,
            bootstrap.first_join_token,
            "target",
            &target_identity,
            &target_relay_identity,
        )
        .await;

        db.upsert_service(
            &target.session_token,
            UpsertServiceRequest {
                name: "ssh".to_string(),
                allowed_device_ids: vec![source.device_id.clone()],
            },
        )
        .await
        .expect("publish test service");

        let hub = RelayHub::new();
        let (source_tx, _source_rx) = mpsc::unbounded_channel();
        let source_connection_id = hub
            .register_device(source.device_id.clone(), source_tx, None)
            .await;
        let (target_old_service_tx, mut target_old_service_rx) = mpsc::unbounded_channel();
        let target_old_service_connection_id = hub
            .register_device(target.device_id.clone(), target_old_service_tx, None)
            .await;
        let (target_forward_tx, mut target_forward_rx) = mpsc::unbounded_channel();
        let _target_forward_connection_id = hub
            .register_device(target.device_id.clone(), target_forward_tx, None)
            .await;
        let (target_new_service_tx, mut target_new_service_rx) = mpsc::unbounded_channel();
        let _target_new_service_connection_id = hub
            .register_device(target.device_id.clone(), target_new_service_tx, None)
            .await;

        hub.disconnect_device(&target.device_id, &target_old_service_connection_id)
            .await;
        assert!(target_old_service_rx.try_recv().is_err());

        let channel_id = generate_token("ch");
        let service_id = service_id(&target.device_id, "ssh");
        let source_ephemeral_key = RelayKeypair::generate();
        let source_ephemeral_public_key = source_ephemeral_key.public_key_base64();
        hub.handle_envelope(
            &db,
            &source.device_id,
            &source_connection_id,
            RelayEnvelope::OpenChannel {
                channel_id: channel_id.clone(),
                service_id: service_id.clone(),
                source_ephemeral_public_key: source_ephemeral_public_key.clone(),
                source_open_signature: source_identity.sign_base64(&relay_channel_open_message(
                    &channel_id,
                    &service_id,
                    &source.device_id,
                    &source_ephemeral_public_key,
                )),
            },
        )
        .await;

        match target_new_service_rx
            .recv()
            .await
            .expect("new legacy service reconnect should take over preferred incoming routing")
        {
            RelayEnvelope::IncomingChannel {
                channel_id: received_channel_id,
                ..
            } => assert_eq!(received_channel_id, channel_id),
            other => panic!(
                "expected incoming channel on reconnected legacy service socket, got {other:?}"
            ),
        }
        assert!(target_forward_rx.try_recv().is_err());

        let _ = std::fs::remove_file(db_path);
    }

    #[tokio::test]
    async fn closed_preferred_legacy_socket_yields_to_reconnected_service_before_disconnect() {
        let db_path = test_db_path("legacy-closed-preferred-reconnect");
        let db = Database::open(&db_path).await.expect("open test db");
        let bootstrap = db.bootstrap_init().await.expect("bootstrap server");
        let source_join = db
            .create_join_token(
                &bootstrap.admin_token,
                CreateJoinTokenRequest {
                    expires_in_minutes: None,
                    note: Some("source".to_string()),
                },
            )
            .await
            .expect("create source join token");

        let source_identity = DeviceIdentity::generate();
        let source_relay_identity = RelayKeypair::generate();
        let source = register_test_device(
            &db,
            source_join.join_token,
            "source",
            &source_identity,
            &source_relay_identity,
        )
        .await;

        let target_identity = DeviceIdentity::generate();
        let target_relay_identity = RelayKeypair::generate();
        let target = register_test_device(
            &db,
            bootstrap.first_join_token,
            "target",
            &target_identity,
            &target_relay_identity,
        )
        .await;

        db.upsert_service(
            &target.session_token,
            UpsertServiceRequest {
                name: "ssh".to_string(),
                allowed_device_ids: vec![source.device_id.clone()],
            },
        )
        .await
        .expect("publish test service");

        let hub = RelayHub::new();
        let (source_tx, _source_rx) = mpsc::unbounded_channel();
        let source_connection_id = hub
            .register_device(source.device_id.clone(), source_tx, None)
            .await;
        let (target_old_service_tx, target_old_service_rx) = mpsc::unbounded_channel();
        let _target_old_service_connection_id = hub
            .register_device(target.device_id.clone(), target_old_service_tx, None)
            .await;
        let (target_forward_tx, mut target_forward_rx) = mpsc::unbounded_channel();
        let _target_forward_connection_id = hub
            .register_device(target.device_id.clone(), target_forward_tx, None)
            .await;

        drop(target_old_service_rx);

        let (target_new_service_tx, mut target_new_service_rx) = mpsc::unbounded_channel();
        let _target_new_service_connection_id = hub
            .register_device(target.device_id.clone(), target_new_service_tx, None)
            .await;

        let channel_id = generate_token("ch");
        let service_id = service_id(&target.device_id, "ssh");
        let source_ephemeral_key = RelayKeypair::generate();
        let source_ephemeral_public_key = source_ephemeral_key.public_key_base64();
        hub.handle_envelope(
            &db,
            &source.device_id,
            &source_connection_id,
            RelayEnvelope::OpenChannel {
                channel_id: channel_id.clone(),
                service_id: service_id.clone(),
                source_ephemeral_public_key: source_ephemeral_public_key.clone(),
                source_open_signature: source_identity.sign_base64(&relay_channel_open_message(
                    &channel_id,
                    &service_id,
                    &source.device_id,
                    &source_ephemeral_public_key,
                )),
            },
        )
        .await;

        match target_new_service_rx
            .recv()
            .await
            .expect("reconnected legacy service should replace the closed preferred socket")
        {
            RelayEnvelope::IncomingChannel {
                channel_id: received_channel_id,
                ..
            } => assert_eq!(received_channel_id, channel_id),
            other => panic!(
                "expected incoming channel on reconnected legacy service socket, got {other:?}"
            ),
        }
        assert!(target_forward_rx.try_recv().is_err());

        let _ = std::fs::remove_file(db_path);
    }
}
