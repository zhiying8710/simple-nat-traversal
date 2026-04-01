use axum::extract::{
    Path, State,
    ws::{Message, WebSocket, WebSocketUpgrade},
};
use axum::http::{HeaderMap, StatusCode};
use axum::{
    Json, Router,
    response::Response,
    routing::{get, post},
};
use futures_util::{SinkExt, StreamExt};
use minipunch_core::{
    AdminClearDevicesResponse, AdminDevicesResponse, BootstrapInitResponse, CreateJoinTokenRequest, DirectRendezvousSession,
    HeartbeatResponse, JoinTokenResponse, NetworkSnapshot, PendingDirectRendezvousResponse,
    RegisterDeviceRequest, RegisterDeviceResponse, RelayEnvelope, RelayTransportFrame,
    ServiceDefinition, StartDirectRendezvousRequest, UpdateDirectRendezvousCandidatesRequest,
    UpsertServiceRequest,
};
use tokio::sync::mpsc;
use tracing::{debug, warn};

use crate::db::Database;
use crate::error::{Result, ServerError};
use crate::relay::RelayHub;

const RELAY_MAX_BATCH_SIZE: usize = 16;

#[derive(Clone)]
pub struct AppState {
    pub db: Database,
    pub relay_hub: RelayHub,
}

pub fn build_router(db: Database) -> Router {
    let relay_hub = RelayHub::new();
    Router::new()
        .route("/healthz", get(healthz))
        .route("/api/v1/bootstrap/init", post(bootstrap_init))
        .route("/api/v1/admin/join-tokens", post(create_join_token))
        .route(
            "/api/v1/admin/devices",
            get(admin_devices).delete(clear_devices),
        )
        .route("/api/v1/devices/register", post(register_device))
        .route("/api/v1/devices/heartbeat", post(heartbeat))
        .route("/api/v1/services/upsert", post(upsert_service))
        .route("/api/v1/network", get(network_snapshot))
        .route(
            "/api/v1/direct/rendezvous/start",
            post(start_direct_rendezvous),
        )
        .route(
            "/api/v1/direct/rendezvous/pending",
            get(pending_direct_rendezvous),
        )
        .route(
            "/api/v1/direct/rendezvous/{rendezvous_id}",
            get(get_direct_rendezvous),
        )
        .route(
            "/api/v1/direct/rendezvous/{rendezvous_id}/candidates",
            post(update_direct_rendezvous_candidates),
        )
        .route("/api/v1/relay/ws", get(relay_ws))
        .with_state(AppState { db, relay_hub })
}

async fn healthz() -> StatusCode {
    StatusCode::OK
}

async fn bootstrap_init(State(state): State<AppState>) -> Result<Json<BootstrapInitResponse>> {
    let response = state.db.bootstrap_init().await?;
    Ok(Json(response))
}

async fn create_join_token(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(request): Json<CreateJoinTokenRequest>,
) -> Result<Json<JoinTokenResponse>> {
    let admin_token = admin_token_from_headers(&headers)?;
    let response = state.db.create_join_token(admin_token, request).await?;
    Ok(Json(response))
}

async fn admin_devices(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<AdminDevicesResponse>> {
    let admin_token = admin_token_from_headers(&headers)?;
    let response = state.db.admin_devices(admin_token).await?;
    Ok(Json(response))
}

async fn clear_devices(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<AdminClearDevicesResponse>> {
    let admin_token = admin_token_from_headers(&headers)?;
    let response = state.db.clear_devices(admin_token).await?;
    state.relay_hub.reset().await;
    Ok(Json(response))
}

async fn register_device(
    State(state): State<AppState>,
    Json(request): Json<RegisterDeviceRequest>,
) -> Result<Json<RegisterDeviceResponse>> {
    let response = state.db.register_device(request).await?;
    Ok(Json(response))
}

async fn heartbeat(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<HeartbeatResponse>> {
    let session_token = bearer_token_from_headers(&headers)?;
    let response = state.db.heartbeat(session_token).await?;
    Ok(Json(response))
}

async fn upsert_service(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(request): Json<UpsertServiceRequest>,
) -> Result<Json<ServiceDefinition>> {
    let session_token = bearer_token_from_headers(&headers)?;
    let response = state.db.upsert_service(session_token, request).await?;
    Ok(Json(response))
}

async fn network_snapshot(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<NetworkSnapshot>> {
    let session_token = bearer_token_from_headers(&headers)?;
    let response = state.db.network_snapshot(session_token).await?;
    Ok(Json(response))
}

async fn start_direct_rendezvous(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(request): Json<StartDirectRendezvousRequest>,
) -> Result<Json<DirectRendezvousSession>> {
    let session_token = bearer_token_from_headers(&headers)?;
    let response = state
        .db
        .start_direct_rendezvous(session_token, request)
        .await?;
    Ok(Json(response))
}

async fn pending_direct_rendezvous(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<PendingDirectRendezvousResponse>> {
    let session_token = bearer_token_from_headers(&headers)?;
    let response = state.db.pending_direct_rendezvous(session_token).await?;
    Ok(Json(response))
}

async fn get_direct_rendezvous(
    Path(rendezvous_id): Path<String>,
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<DirectRendezvousSession>> {
    let session_token = bearer_token_from_headers(&headers)?;
    let response = state
        .db
        .direct_rendezvous(session_token, &rendezvous_id)
        .await?;
    Ok(Json(response))
}

async fn update_direct_rendezvous_candidates(
    Path(rendezvous_id): Path<String>,
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(request): Json<UpdateDirectRendezvousCandidatesRequest>,
) -> Result<Json<DirectRendezvousSession>> {
    let session_token = bearer_token_from_headers(&headers)?;
    let response = state
        .db
        .update_direct_rendezvous_candidates(session_token, &rendezvous_id, request)
        .await?;
    Ok(Json(response))
}

async fn relay_ws(
    ws: WebSocketUpgrade,
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Response> {
    let session_token = bearer_token_from_headers(&headers)?.to_string();
    let device_id = state.db.session_device_id(&session_token).await?;
    state.db.touch_device(&device_id).await?;
    Ok(ws.on_upgrade(move |socket| handle_relay_socket(state, device_id, socket)))
}

async fn handle_relay_socket(state: AppState, device_id: String, socket: WebSocket) {
    let (sender, mut receiver) = mpsc::unbounded_channel::<RelayEnvelope>();
    state
        .relay_hub
        .register_device(device_id.clone(), sender.clone())
        .await;
    let _ = sender.send(RelayEnvelope::Ready {
        device_id: device_id.clone(),
    });

    let (mut socket_writer, mut socket_reader) = socket.split();
    let writer = tokio::spawn(async move {
        while let Some(frame) = next_relay_transport_frame(&mut receiver).await {
            let encoded = match serde_json::to_vec(&frame) {
                Ok(encoded) => encoded,
                Err(err) => {
                    warn!("failed to encode relay envelope: {err}");
                    break;
                }
            };
            if let Err(err) = socket_writer.send(Message::Binary(encoded.into())).await {
                warn!("failed to write relay message: {err}");
                break;
            }
        }
    });

    while let Some(message) = socket_reader.next().await {
        match message {
            Ok(Message::Text(text)) => match decode_relay_transport_frame(text.as_bytes()) {
                Ok(envelopes) => {
                    if envelopes.is_empty() {
                        continue;
                    }
                    if let Err(err) = state.db.touch_device(&device_id).await {
                        warn!("failed to touch relay device presence: {err}");
                    }
                    for envelope in envelopes {
                        state
                            .relay_hub
                            .handle_envelope(&state.db, &device_id, envelope)
                            .await;
                    }
                }
                Err(err) => {
                    warn!("failed to decode relay envelope: {err}");
                }
            },
            Ok(Message::Binary(bytes)) => match decode_relay_transport_frame(&bytes) {
                Ok(envelopes) => {
                    if envelopes.is_empty() {
                        continue;
                    }
                    if let Err(err) = state.db.touch_device(&device_id).await {
                        warn!("failed to touch relay device presence: {err}");
                    }
                    for envelope in envelopes {
                        state
                            .relay_hub
                            .handle_envelope(&state.db, &device_id, envelope)
                            .await;
                    }
                }
                Err(err) => {
                    warn!("failed to decode relay envelope: {err}");
                }
            },
            Ok(Message::Close(_)) => {
                debug!("relay socket closed for {device_id}");
                break;
            }
            Ok(Message::Ping(_)) | Ok(Message::Pong(_)) => {}
            Err(err) => {
                warn!("relay socket read failed: {err}");
                break;
            }
        }
    }

    state.relay_hub.disconnect_device(&device_id).await;
    writer.abort();
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

fn admin_token_from_headers(headers: &HeaderMap) -> Result<&str> {
    headers
        .get("x-admin-token")
        .and_then(|value| value.to_str().ok())
        .ok_or(ServerError::Unauthorized)
}

fn bearer_token_from_headers(headers: &HeaderMap) -> Result<&str> {
    let auth_header = headers
        .get("authorization")
        .and_then(|value| value.to_str().ok())
        .ok_or(ServerError::Unauthorized)?;
    auth_header
        .strip_prefix("Bearer ")
        .ok_or(ServerError::Unauthorized)
}
