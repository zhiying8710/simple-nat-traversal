use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ErrorResponse {
    pub error: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BootstrapInitResponse {
    pub admin_token: String,
    pub first_join_token: String,
    pub join_token_expires_at: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreateJoinTokenRequest {
    pub expires_in_minutes: Option<u64>,
    pub note: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JoinTokenResponse {
    pub join_token: String,
    pub expires_at: i64,
    pub note: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RegisterDeviceRequest {
    pub join_token: Option<String>,
    pub device_id: String,
    pub device_name: String,
    pub os: String,
    pub version: String,
    pub public_key: String,
    pub relay_public_key: String,
    pub relay_public_key_signature: String,
    pub nonce: String,
    pub signature: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RegisterDeviceResponse {
    pub device_id: String,
    pub session_token: String,
    pub session_expires_at: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HeartbeatResponse {
    pub device_id: String,
    pub seen_at: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UpsertServiceRequest {
    pub name: String,
    pub allowed_device_ids: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ServiceDefinition {
    pub service_id: String,
    pub owner_device_id: String,
    pub name: String,
    pub protocol: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceSummary {
    pub device_id: String,
    pub device_name: String,
    pub os: String,
    pub identity_public_key: String,
    pub relay_public_key: String,
    pub relay_public_key_signature: String,
    pub last_seen_at: i64,
    pub is_online: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NetworkSnapshot {
    pub requester_device_id: String,
    pub devices: Vec<DeviceSummary>,
    pub services: Vec<ServiceDefinition>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminDevicesResponse {
    pub devices: Vec<DeviceSummary>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DirectConnectionCandidate {
    pub protocol: String,
    pub addr: String,
    pub candidate_type: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StartDirectRendezvousRequest {
    pub target_device_id: String,
    pub service_name: String,
    #[serde(default)]
    pub source_candidates: Vec<DirectConnectionCandidate>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UpdateDirectRendezvousCandidatesRequest {
    #[serde(default)]
    pub candidates: Vec<DirectConnectionCandidate>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DirectRendezvousParticipant {
    pub device_id: String,
    pub announced_at: Option<i64>,
    pub candidates: Vec<DirectConnectionCandidate>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DirectRendezvousSession {
    pub rendezvous_id: String,
    pub service_id: String,
    pub service_name: String,
    pub source_device_id: String,
    pub target_device_id: String,
    pub status: String,
    pub created_at: i64,
    pub updated_at: i64,
    pub expires_at: i64,
    pub source: DirectRendezvousParticipant,
    pub target: DirectRendezvousParticipant,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PendingDirectRendezvousResponse {
    pub attempts: Vec<DirectRendezvousSession>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct DirectSelectiveAckRange {
    pub start_sequence: u64,
    pub end_sequence: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum DirectProbeEnvelope {
    Hello {
        rendezvous_id: String,
        sender_device_id: String,
        hello_nonce: String,
        signature: String,
    },
    Ack {
        rendezvous_id: String,
        sender_device_id: String,
        hello_nonce: String,
        ack_nonce: String,
        signature: String,
    },
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum DirectTunnelEnvelope {
    OpenChannel {
        rendezvous_id: String,
        channel_id: String,
        service_id: String,
        source_device_id: String,
        source_ephemeral_public_key: String,
        source_open_signature: String,
    },
    ChannelAccepted {
        rendezvous_id: String,
        channel_id: String,
    },
    ChannelRejected {
        rendezvous_id: String,
        channel_id: String,
        reason: String,
    },
    ChannelData {
        rendezvous_id: String,
        channel_id: String,
        sequence: u64,
        data_base64: String,
    },
    ChannelAck {
        rendezvous_id: String,
        channel_id: String,
        next_sequence: u64,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        selective_ranges: Vec<DirectSelectiveAckRange>,
    },
    ChannelKeepalive {
        rendezvous_id: String,
        channel_id: String,
        keepalive_nonce: u64,
    },
    ChannelKeepaliveAck {
        rendezvous_id: String,
        channel_id: String,
        keepalive_nonce: u64,
    },
    ChannelClose {
        rendezvous_id: String,
        channel_id: String,
        final_sequence: u64,
        reason: Option<String>,
    },
    ChannelCloseAck {
        rendezvous_id: String,
        channel_id: String,
        final_sequence: u64,
    },
}

impl DirectTunnelEnvelope {
    pub fn channel_id(&self) -> &str {
        match self {
            DirectTunnelEnvelope::OpenChannel { channel_id, .. }
            | DirectTunnelEnvelope::ChannelAccepted { channel_id, .. }
            | DirectTunnelEnvelope::ChannelRejected { channel_id, .. }
            | DirectTunnelEnvelope::ChannelData { channel_id, .. }
            | DirectTunnelEnvelope::ChannelAck { channel_id, .. }
            | DirectTunnelEnvelope::ChannelKeepalive { channel_id, .. }
            | DirectTunnelEnvelope::ChannelKeepaliveAck { channel_id, .. }
            | DirectTunnelEnvelope::ChannelClose { channel_id, .. }
            | DirectTunnelEnvelope::ChannelCloseAck { channel_id, .. } => channel_id,
        }
    }

    pub fn is_terminal(&self) -> bool {
        matches!(
            self,
            DirectTunnelEnvelope::ChannelRejected { .. }
                | DirectTunnelEnvelope::ChannelCloseAck { .. }
        )
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum RelayEnvelope {
    Ready {
        device_id: String,
    },
    OpenChannel {
        channel_id: String,
        service_id: String,
        source_ephemeral_public_key: String,
        source_open_signature: String,
    },
    IncomingChannel {
        channel_id: String,
        source_device_id: String,
        source_identity_public_key: String,
        source_ephemeral_public_key: String,
        source_open_signature: String,
        service_id: String,
    },
    ChannelAccepted {
        channel_id: String,
    },
    ChannelRejected {
        channel_id: String,
        reason: String,
    },
    ChannelData {
        channel_id: String,
        data_base64: String,
    },
    ChannelClose {
        channel_id: String,
        reason: Option<String>,
    },
    Ping,
    Pong,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum RelayTransportFrame {
    Single(RelayEnvelope),
    Batch(Vec<RelayEnvelope>),
}

impl RelayTransportFrame {
    pub fn from_envelopes(mut envelopes: Vec<RelayEnvelope>) -> Option<Self> {
        match envelopes.len() {
            0 => None,
            1 => Some(Self::Single(envelopes.remove(0))),
            _ => Some(Self::Batch(envelopes)),
        }
    }

    pub fn into_envelopes(self) -> Vec<RelayEnvelope> {
        match self {
            Self::Single(envelope) => vec![envelope],
            Self::Batch(envelopes) => envelopes,
        }
    }
}

impl RelayEnvelope {
    pub fn channel_id(&self) -> Option<&str> {
        match self {
            RelayEnvelope::OpenChannel { channel_id, .. }
            | RelayEnvelope::IncomingChannel { channel_id, .. }
            | RelayEnvelope::ChannelAccepted { channel_id }
            | RelayEnvelope::ChannelRejected { channel_id, .. }
            | RelayEnvelope::ChannelData { channel_id, .. }
            | RelayEnvelope::ChannelClose { channel_id, .. } => Some(channel_id),
            RelayEnvelope::Ready { .. } | RelayEnvelope::Ping | RelayEnvelope::Pong => None,
        }
    }

    pub fn is_terminal(&self) -> bool {
        matches!(
            self,
            RelayEnvelope::ChannelRejected { .. } | RelayEnvelope::ChannelClose { .. }
        )
    }
}
