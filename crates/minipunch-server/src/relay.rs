use std::collections::HashMap;
use std::sync::Arc;

use minipunch_core::{RelayEnvelope, ServiceDefinition};
use tokio::sync::{Mutex, mpsc};
use tracing::warn;

use crate::db::Database;

#[derive(Clone)]
pub struct RelayHub {
    inner: Arc<Mutex<RelayHubState>>,
}

struct RelayHubState {
    devices: HashMap<String, mpsc::UnboundedSender<RelayEnvelope>>,
    channels: HashMap<String, RelayChannel>,
}

struct RelayChannel {
    source_device_id: String,
    target_device_id: String,
    accepted: bool,
}

impl RelayHub {
    pub fn new() -> Self {
        Self {
            inner: Arc::new(Mutex::new(RelayHubState {
                devices: HashMap::new(),
                channels: HashMap::new(),
            })),
        }
    }

    pub async fn register_device(
        &self,
        device_id: String,
        sender: mpsc::UnboundedSender<RelayEnvelope>,
    ) {
        let mut inner = self.inner.lock().await;
        inner.devices.insert(device_id, sender);
    }

    pub async fn disconnect_device(&self, device_id: &str) {
        let notifications = {
            let mut inner = self.inner.lock().await;
            inner.devices.remove(device_id);

            let affected_channel_ids = inner
                .channels
                .iter()
                .filter(|(_, channel)| {
                    channel.source_device_id == device_id || channel.target_device_id == device_id
                })
                .map(|(channel_id, _)| channel_id.clone())
                .collect::<Vec<_>>();

            let mut notifications = Vec::new();
            for channel_id in affected_channel_ids {
                if let Some(channel) = inner.channels.remove(&channel_id) {
                    let peer_device_id = if channel.source_device_id == device_id {
                        channel.target_device_id
                    } else {
                        channel.source_device_id
                    };
                    if let Some(peer_tx) = inner.devices.get(&peer_device_id).cloned() {
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
        inner.devices.clear();
        inner.channels.clear();
    }

    pub async fn handle_envelope(
        &self,
        db: &Database,
        sender_device_id: &str,
        envelope: RelayEnvelope,
    ) {
        match envelope {
            RelayEnvelope::OpenChannel {
                channel_id,
                service_id,
                source_ephemeral_public_key,
                source_open_signature,
            } => {
                self.handle_open_channel(
                    db,
                    sender_device_id,
                    channel_id,
                    service_id,
                    source_ephemeral_public_key,
                    source_open_signature,
                )
                .await;
            }
            RelayEnvelope::ChannelAccepted { channel_id } => {
                self.handle_channel_accepted(sender_device_id, channel_id)
                    .await;
            }
            RelayEnvelope::ChannelRejected { channel_id, reason } => {
                self.handle_channel_rejected(sender_device_id, channel_id, reason)
                    .await;
            }
            RelayEnvelope::ChannelData {
                channel_id,
                data_base64,
            } => {
                self.handle_channel_data(sender_device_id, channel_id, data_base64)
                    .await;
            }
            RelayEnvelope::ChannelClose { channel_id, reason } => {
                self.handle_channel_close(sender_device_id, channel_id, reason)
                    .await;
            }
            RelayEnvelope::Ping => {
                self.send_to_device(sender_device_id, RelayEnvelope::Pong)
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
                self.send_to_device(
                    source_device_id,
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
                self.send_to_device(
                    source_device_id,
                    RelayEnvelope::ChannelRejected {
                        channel_id,
                        reason: err.to_string(),
                    },
                )
                .await;
                return;
            }
        };

        let target_tx = {
            let mut inner = self.inner.lock().await;
            if inner.channels.contains_key(&channel_id) {
                drop(inner);
                self.send_to_device(
                    source_device_id,
                    RelayEnvelope::ChannelRejected {
                        channel_id,
                        reason: "channel already exists".to_string(),
                    },
                )
                .await;
                return;
            }

            let Some(target_tx) = inner.devices.get(&service.owner_device_id).cloned() else {
                drop(inner);
                self.send_to_device(
                    source_device_id,
                    RelayEnvelope::ChannelRejected {
                        channel_id,
                        reason: "target device is not connected to relay".to_string(),
                    },
                )
                .await;
                return;
            };

            inner.channels.insert(
                channel_id.clone(),
                RelayChannel {
                    source_device_id: source_device_id.to_string(),
                    target_device_id: service.owner_device_id.clone(),
                    accepted: false,
                },
            );
            target_tx
        };

        if target_tx
            .send(incoming_channel_from_service(
                source_device_id.to_string(),
                source_identity_public_key,
                source_ephemeral_public_key,
                source_open_signature,
                service,
                channel_id.clone(),
            ))
            .is_err()
        {
            self.remove_channel(&channel_id).await;
            self.send_to_device(
                source_device_id,
                RelayEnvelope::ChannelRejected {
                    channel_id,
                    reason: "target relay channel closed".to_string(),
                },
            )
            .await;
        }
    }

    async fn handle_channel_accepted(&self, sender_device_id: &str, channel_id: String) {
        let source_tx = {
            let mut inner = self.inner.lock().await;
            let Some(channel) = inner.channels.get_mut(&channel_id) else {
                return;
            };
            if channel.target_device_id != sender_device_id {
                return;
            }
            channel.accepted = true;
            let source_device_id = channel.source_device_id.clone();
            inner.devices.get(&source_device_id).cloned()
        };

        if let Some(source_tx) = source_tx {
            let _ = source_tx.send(RelayEnvelope::ChannelAccepted { channel_id });
        }
    }

    async fn handle_channel_rejected(
        &self,
        sender_device_id: &str,
        channel_id: String,
        reason: String,
    ) {
        let source_tx = {
            let mut inner = self.inner.lock().await;
            let Some(channel) = inner.channels.get(&channel_id) else {
                return;
            };
            if channel.target_device_id != sender_device_id {
                return;
            }
            let source_tx = inner.devices.get(&channel.source_device_id).cloned();
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
            if channel.source_device_id == sender_device_id {
                inner.devices.get(&channel.target_device_id).cloned()
            } else if channel.target_device_id == sender_device_id {
                inner.devices.get(&channel.source_device_id).cloned()
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
        channel_id: String,
        reason: Option<String>,
    ) {
        let peer_tx = {
            let mut inner = self.inner.lock().await;
            let Some(channel) = inner.channels.get(&channel_id) else {
                return;
            };
            let peer_device_id = if channel.source_device_id == sender_device_id {
                channel.target_device_id.clone()
            } else if channel.target_device_id == sender_device_id {
                channel.source_device_id.clone()
            } else {
                return;
            };
            let peer_tx = inner.devices.get(&peer_device_id).cloned();
            inner.channels.remove(&channel_id);
            peer_tx
        };

        if let Some(peer_tx) = peer_tx {
            let _ = peer_tx.send(RelayEnvelope::ChannelClose { channel_id, reason });
        }
    }

    async fn send_to_device(&self, device_id: &str, envelope: RelayEnvelope) {
        let tx = {
            let inner = self.inner.lock().await;
            inner.devices.get(device_id).cloned()
        };
        if let Some(tx) = tx {
            let _ = tx.send(envelope);
        }
    }

    async fn remove_channel(&self, channel_id: &str) {
        let mut inner = self.inner.lock().await;
        inner.channels.remove(channel_id);
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
