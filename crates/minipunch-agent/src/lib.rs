pub mod client;
pub mod config;
pub mod direct;
pub mod relay;
pub mod runtime_state;
pub mod status;

use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, anyhow};
use minipunch_core::{
    DeviceIdentity, DirectConnectionCandidate, DirectRendezvousSession, NetworkSnapshot,
    PendingDirectRendezvousResponse, RegisterDeviceRequest, RelayKeypair, ServiceDefinition,
    StartDirectRendezvousRequest, UpdateDirectRendezvousCandidatesRequest, UpsertServiceRequest,
    device_id_from_public_key, registration_message, relay_key_binding_message,
    verify_signature_base64,
};
use tokio::io::AsyncWriteExt;
use tokio::net::TcpListener;
use tokio::sync::{Mutex, watch};
use tokio::task::JoinSet;
use tokio::time::{self, MissedTickBehavior};
use tracing::{info, warn};

use crate::client::ControlPlaneClient;
use crate::config::{
    AgentConfig, DEFAULT_DIRECT_CANDIDATE_TYPE, DEFAULT_FORWARD_TRANSPORT, LocalForwardConfig,
    PublishedServiceConfig,
};
use crate::direct::{
    DirectConnection, DirectForwardIngress, DirectProbeResult, PreparedDirectForward,
    SharedDirectSocketHub, bind_candidate, bind_direct_udp_socket, into_direct_forward_ingress,
    prepare_direct_tcp_forward, prepare_local_direct_forward_connection, run_direct_probe,
    run_direct_tcp_forward, run_direct_tcp_serve, run_direct_tcp_serve_on_hub,
    run_prepared_local_direct_forward_connection,
};
use crate::relay::{
    RelayConnection, RelayForwarderTracker, handle_local_forward_connection, run_local_forwarder,
    run_local_forwarder_loop, run_relay_service, run_relay_service_loop, spawn_heartbeat_task,
};
use crate::runtime_state::{ForwardRuntimeHook, PublishedServiceRuntimeHook, RuntimeStateWriter};
use crate::status::{AgentStatusReport, build_status_report};

pub const CLIENT_VERSION: &str = env!("CARGO_PKG_VERSION");
const RUNTIME_HEARTBEAT_INTERVAL: Duration = Duration::from_secs(30);
const RUNTIME_RETRY_INITIAL_BACKOFF: Duration = Duration::from_secs(1);
const RUNTIME_RETRY_MAX_BACKOFF: Duration = Duration::from_secs(15);
const AUTO_DIRECT_RETRY_INITIAL_BACKOFF: Duration = Duration::from_secs(5);
const AUTO_DIRECT_RETRY_MAX_BACKOFF: Duration = Duration::from_secs(60);
const AUTO_DIRECT_RETRY_IDLE_GRACE: Duration = Duration::from_secs(3);
const AUTO_DIRECT_RETRY_BUSY_POLL_INTERVAL: Duration = Duration::from_secs(2);

#[derive(Debug, Clone)]
pub struct ResolvedService {
    pub service: ServiceDefinition,
    pub target_relay_public_key: String,
}

#[derive(Clone)]
enum AutoForwardIngress {
    Relay(AutoRelayIngress),
    Direct(AutoDirectIngress),
}

#[derive(Clone)]
struct AutoRelayIngress {
    relay: RelayConnection,
    resolved_service: ResolvedService,
    runtime_hook: Option<ForwardRuntimeHook>,
    tracker: RelayForwarderTracker,
}

#[derive(Clone)]
struct AutoDirectIngress {
    connection: DirectConnection,
    resolved_service: ResolvedService,
    runtime_hook: Option<ForwardRuntimeHook>,
    handoff_relay_fallback: Option<AutoRelayHandoffFallback>,
}

#[derive(Clone)]
struct AutoRelayHandoffFallback {
    relay: Arc<Mutex<Option<RelayConnection>>>,
    runtime_hook: Option<ForwardRuntimeHook>,
    tracker: RelayForwarderTracker,
}

impl AutoRelayHandoffFallback {
    fn new(
        relay: RelayConnection,
        runtime_hook: Option<ForwardRuntimeHook>,
        tracker: RelayForwarderTracker,
    ) -> Self {
        Self {
            relay: Arc::new(Mutex::new(Some(relay))),
            runtime_hook,
            tracker,
        }
    }

    async fn relay(&self) -> Option<RelayConnection> {
        self.relay.lock().await.clone()
    }

    async fn deactivate(&self) {
        self.relay.lock().await.take();
    }

    fn active_connections(&self) -> usize {
        self.tracker.active_connections()
    }
}

pub struct AgentRuntime {
    config_path: PathBuf,
    config: AgentConfig,
    identity: DeviceIdentity,
    relay_identity: RelayKeypair,
    client: ControlPlaneClient,
}

impl AgentRuntime {
    pub async fn init(
        config_path: impl AsRef<Path>,
        server_url: &str,
        join_token: &str,
        device_name: &str,
    ) -> Result<Self> {
        let config_path = config_path.as_ref().to_path_buf();
        let mut config = AgentConfig::load_or_default(&config_path)?;
        config.server_url = server_url.to_string();
        config.device_name = device_name.to_string();
        let identity = load_or_create_identity(&mut config)?;
        let relay_identity = load_or_create_relay_identity(&mut config)?;
        let client = ControlPlaneClient::new(config.server_url.clone());
        let mut runtime = Self {
            config_path,
            config,
            identity,
            relay_identity,
            client,
        };
        runtime
            .register(Some(join_token.to_string()))
            .await
            .context("failed to join minipunch network")?;
        runtime.save()?;
        Ok(runtime)
    }

    pub async fn load(config_path: impl AsRef<Path>) -> Result<Self> {
        let config_path = config_path.as_ref().to_path_buf();
        let mut config = AgentConfig::load(&config_path)?;
        if config.server_url.trim().is_empty() {
            return Err(anyhow!("server_url is missing from config"));
        }
        if config.device_name.trim().is_empty() {
            return Err(anyhow!("device_name is missing from config"));
        }
        let identity = load_or_create_identity(&mut config)?;
        let relay_identity = load_or_create_relay_identity(&mut config)?;
        let client = ControlPlaneClient::new(config.server_url.clone());
        Ok(Self {
            config_path,
            config,
            identity,
            relay_identity,
            client,
        })
    }

    pub fn config(&self) -> &AgentConfig {
        &self.config
    }

    pub async fn ensure_session(&mut self) -> Result<()> {
        if self.config.has_valid_session() {
            return Ok(());
        }
        self.register(None).await?;
        self.save()
    }

    pub async fn heartbeat(&mut self) -> Result<()> {
        self.ensure_session().await?;
        let session_token = self
            .config
            .session_token
            .clone()
            .ok_or_else(|| anyhow!("missing session token after ensure_session"))?;
        self.client.heartbeat(&session_token).await?;
        Ok(())
    }

    pub async fn publish_service(
        &mut self,
        mut service_config: PublishedServiceConfig,
    ) -> Result<ServiceDefinition> {
        validate_published_service_config(&service_config)?;
        service_config.direct_candidate_type =
            normalize_direct_candidate_type(&service_config.direct_candidate_type);
        self.ensure_session().await?;
        let session_token = self
            .config
            .session_token
            .clone()
            .ok_or_else(|| anyhow!("missing session token after ensure_session"))?;
        let request = UpsertServiceRequest {
            name: service_config.name.clone(),
            allowed_device_ids: service_config.allowed_device_ids.clone(),
        };
        let service = self.client.upsert_service(&session_token, &request).await?;

        if let Some(existing) = self
            .config
            .published_services
            .iter_mut()
            .find(|service| service.name == service_config.name)
        {
            *existing = service_config;
        } else {
            self.config.published_services.push(service_config);
        }
        self.save()?;
        Ok(service)
    }

    pub async fn network_snapshot(&mut self) -> Result<NetworkSnapshot> {
        self.ensure_session().await?;
        let session_token = self
            .config
            .session_token
            .clone()
            .ok_or_else(|| anyhow!("missing session token after ensure_session"))?;
        self.client.network_snapshot(&session_token).await
    }

    pub async fn resolve_service(
        &mut self,
        target_device_id: &str,
        service_name: &str,
    ) -> Result<ResolvedService> {
        self.ensure_session().await?;
        let session_token = self.current_session_token()?;
        resolve_service_with_session(&self.client, &session_token, target_device_id, service_name)
            .await
    }

    pub async fn status_report(&mut self) -> AgentStatusReport {
        match self.network_snapshot().await {
            Ok(snapshot) => build_status_report(&self.config, Some(&snapshot), None),
            Err(err) => build_status_report(&self.config, None, Some(err.to_string())),
        }
    }

    pub async fn relay_serve(&mut self) -> Result<()> {
        self.ensure_session().await?;
        let session_token = self
            .config
            .session_token
            .clone()
            .ok_or_else(|| anyhow!("missing session token after ensure_session"))?;
        let local_device_id = self
            .config
            .device_id
            .clone()
            .ok_or_else(|| anyhow!("device is not joined yet"))?;
        run_relay_service(
            self.config.server_url.clone(),
            session_token,
            self.client.clone(),
            self.relay_identity.clone(),
            local_device_id,
            self.config.published_services.clone(),
        )
        .await
    }

    pub async fn start_direct_rendezvous(
        &mut self,
        target_device_id: String,
        service_name: String,
        source_candidates: Vec<DirectConnectionCandidate>,
    ) -> Result<DirectRendezvousSession> {
        self.ensure_session().await?;
        let session_token = self.current_session_token()?;
        self.client
            .start_direct_rendezvous(
                &session_token,
                &StartDirectRendezvousRequest {
                    target_device_id,
                    service_name,
                    source_candidates,
                },
            )
            .await
    }

    pub async fn pending_direct_rendezvous(&mut self) -> Result<PendingDirectRendezvousResponse> {
        self.ensure_session().await?;
        let session_token = self.current_session_token()?;
        self.client.pending_direct_rendezvous(&session_token).await
    }

    pub async fn direct_rendezvous(
        &mut self,
        rendezvous_id: &str,
    ) -> Result<DirectRendezvousSession> {
        self.ensure_session().await?;
        let session_token = self.current_session_token()?;
        self.client
            .direct_rendezvous(&session_token, rendezvous_id)
            .await
    }

    pub async fn update_direct_rendezvous_candidates(
        &mut self,
        rendezvous_id: &str,
        candidates: Vec<DirectConnectionCandidate>,
    ) -> Result<DirectRendezvousSession> {
        self.ensure_session().await?;
        let session_token = self.current_session_token()?;
        self.client
            .update_direct_rendezvous_candidates(
                &session_token,
                rendezvous_id,
                &UpdateDirectRendezvousCandidatesRequest { candidates },
            )
            .await
    }

    pub async fn direct_probe(
        &mut self,
        rendezvous_id: &str,
        local_bind_addr: String,
        wait_timeout: Duration,
    ) -> Result<DirectProbeResult> {
        self.ensure_session().await?;
        let session = self
            .wait_for_ready_rendezvous(rendezvous_id, wait_timeout)
            .await?;
        let peer_device_id =
            if self.config.device_id.as_deref() == Some(session.source_device_id.as_str()) {
                session.target_device_id.clone()
            } else if self.config.device_id.as_deref() == Some(session.target_device_id.as_str()) {
                session.source_device_id.clone()
            } else {
                return Err(anyhow!(
                    "local device is not a participant in rendezvous {}",
                    rendezvous_id
                ));
            };
        let peer_public_key = self.lookup_device_public_key(&peer_device_id).await?;
        let local_device_id = self
            .config
            .device_id
            .clone()
            .ok_or_else(|| anyhow!("device is not joined yet"))?;
        run_direct_probe(
            self.identity.clone(),
            local_device_id,
            peer_public_key,
            session,
            local_bind_addr,
            wait_timeout,
        )
        .await
    }

    pub async fn direct_connect(
        &mut self,
        target_device_id: String,
        service_name: String,
        local_bind_addr: String,
        candidate_type: String,
        wait_timeout: Duration,
    ) -> Result<DirectProbeResult> {
        let session = self
            .start_direct_rendezvous(
                target_device_id,
                service_name,
                vec![bind_candidate(&local_bind_addr, &candidate_type)],
            )
            .await?;
        self.direct_probe(&session.rendezvous_id, local_bind_addr, wait_timeout)
            .await
    }

    pub async fn direct_tcp_forward(
        &mut self,
        target_device_id: String,
        service_name: String,
        local_tcp_bind_addr: String,
        local_udp_bind_addr: String,
        candidate_type: String,
        wait_timeout: Duration,
    ) -> Result<()> {
        let resolved_service = self
            .resolve_service(&target_device_id, &service_name)
            .await?;
        let session = self
            .start_direct_rendezvous(
                target_device_id.clone(),
                service_name,
                vec![bind_candidate(&local_udp_bind_addr, &candidate_type)],
            )
            .await?;
        let session = self
            .wait_for_ready_rendezvous(&session.rendezvous_id, wait_timeout)
            .await?;
        let peer_public_key = self.lookup_device_public_key(&target_device_id).await?;
        let local_device_id = self
            .config
            .device_id
            .clone()
            .ok_or_else(|| anyhow!("device is not joined yet"))?;
        run_direct_tcp_forward(
            self.identity.clone(),
            local_device_id,
            peer_public_key,
            session,
            resolved_service,
            local_udp_bind_addr,
            local_tcp_bind_addr,
            wait_timeout,
            None,
        )
        .await
    }

    pub async fn direct_serve(
        &mut self,
        service_name: String,
        local_bind_addr: String,
        candidate_type: String,
        wait_timeout: Duration,
    ) -> Result<()> {
        self.ensure_session().await?;
        let local_device_id = self
            .config
            .device_id
            .clone()
            .ok_or_else(|| anyhow!("device is not joined yet"))?;
        let mut completed = HashSet::new();

        loop {
            let pending = self.pending_direct_rendezvous().await?;
            for attempt in pending.attempts {
                if attempt.target_device_id != local_device_id
                    || attempt.service_name != service_name
                    || completed.contains(&attempt.rendezvous_id)
                {
                    continue;
                }
                if attempt.status == "expired" {
                    completed.insert(attempt.rendezvous_id.clone());
                    continue;
                }

                if attempt.target.announced_at.is_none() || attempt.target.candidates.is_empty() {
                    self.update_direct_rendezvous_candidates(
                        &attempt.rendezvous_id,
                        vec![bind_candidate(&local_bind_addr, &candidate_type)],
                    )
                    .await?;
                }

                match self
                    .direct_probe(
                        &attempt.rendezvous_id,
                        local_bind_addr.clone(),
                        wait_timeout,
                    )
                    .await
                {
                    Ok(result) => {
                        println!("{}", serde_json::to_string_pretty(&result)?);
                        completed.insert(attempt.rendezvous_id.clone());
                    }
                    Err(err) => {
                        warn!(
                            "direct serve attempt {} for service {} failed: {}",
                            attempt.rendezvous_id, service_name, err
                        );
                    }
                }
            }

            tokio::select! {
                _ = tokio::signal::ctrl_c() => return Ok(()),
                _ = time::sleep(Duration::from_millis(500)) => {}
            }
        }
    }

    pub async fn direct_tcp_serve(
        &mut self,
        service_name: String,
        local_udp_bind_addr: String,
        candidate_type: String,
        wait_timeout: Duration,
    ) -> Result<()> {
        self.ensure_session().await?;
        let local_device_id = self
            .config
            .device_id
            .clone()
            .ok_or_else(|| anyhow!("device is not joined yet"))?;
        let published_service = self.published_service_config(&service_name)?;
        let mut handled = HashSet::new();

        loop {
            let pending = self.pending_direct_rendezvous().await?;
            let attempts = pending
                .attempts
                .into_iter()
                .filter(|attempt| {
                    attempt.target_device_id == local_device_id
                        && attempt.service_name == service_name
                        && !handled.contains(&attempt.rendezvous_id)
                })
                .collect::<Vec<_>>();

            for attempt in attempts {
                if attempt.status == "expired" {
                    handled.insert(attempt.rendezvous_id);
                    continue;
                }

                if attempt.target.announced_at.is_none() || attempt.target.candidates.is_empty() {
                    self.update_direct_rendezvous_candidates(
                        &attempt.rendezvous_id,
                        vec![bind_candidate(&local_udp_bind_addr, &candidate_type)],
                    )
                    .await?;
                }

                let serve_result = async {
                    let session = self
                        .wait_for_ready_rendezvous(&attempt.rendezvous_id, wait_timeout)
                        .await?;
                    let peer_public_key = self
                        .lookup_device_public_key(&session.source_device_id)
                        .await?;
                    run_direct_tcp_serve(
                        self.identity.clone(),
                        self.relay_identity.clone(),
                        local_device_id.clone(),
                        peer_public_key,
                        session,
                        published_service.clone(),
                        local_udp_bind_addr.clone(),
                        wait_timeout,
                        None,
                    )
                    .await
                }
                .await;

                handled.insert(attempt.rendezvous_id.clone());
                match serve_result {
                    Ok(()) => {
                        info!(
                            "direct tcp serve session {} for service {} ended; waiting for the next rendezvous",
                            attempt.rendezvous_id, service_name
                        );
                    }
                    Err(err) => {
                        warn!(
                            "direct tcp serve session {} for service {} failed: {}",
                            attempt.rendezvous_id, service_name, err
                        );
                    }
                }
            }

            tokio::select! {
                _ = tokio::signal::ctrl_c() => return Ok(()),
                _ = time::sleep(Duration::from_millis(500)) => {}
            }
        }
    }

    pub async fn auto_forward_service(
        &mut self,
        target_device_id: String,
        service_name: String,
        local_bind_addr: String,
        direct_udp_bind_addr: String,
        direct_candidate_type: String,
        direct_wait_timeout: Duration,
    ) -> Result<()> {
        self.ensure_session().await?;
        let session_token = self.current_session_token()?;
        let heartbeat_task = spawn_heartbeat_task(self.client.clone(), session_token.clone());
        let run_result = tokio::select! {
            result = run_auto_forward_loop(
                self.client.clone(),
                self.config.server_url.clone(),
                session_token,
                self.identity.clone(),
                format!("manual:{}:{}", target_device_id, service_name),
                target_device_id,
                service_name,
                local_bind_addr,
                direct_udp_bind_addr,
                direct_candidate_type,
                direct_wait_timeout,
                None,
            ) => result,
            _ = tokio::signal::ctrl_c() => Ok(()),
        };
        heartbeat_task.abort();
        run_result
    }

    pub async fn auto_serve(
        &mut self,
        service_name: String,
        direct_udp_bind_addr: String,
        direct_candidate_type: String,
        direct_wait_timeout: Duration,
    ) -> Result<()> {
        self.ensure_session().await?;
        let session_token = self.current_session_token()?;
        let local_device_id = self
            .config
            .device_id
            .clone()
            .ok_or_else(|| anyhow!("device is not joined yet"))?;
        let published_service = self.published_service_config(&service_name)?;
        let heartbeat_task = spawn_heartbeat_task(self.client.clone(), session_token.clone());
        let run_result = tokio::select! {
            result = run_auto_service_loop(
                self.client.clone(),
                self.config.server_url.clone(),
                session_token,
                self.identity.clone(),
                self.relay_identity.clone(),
                local_device_id,
                published_service,
                direct_udp_bind_addr,
                direct_candidate_type,
                direct_wait_timeout,
            ) => result,
            _ = tokio::signal::ctrl_c() => Ok(()),
        };
        heartbeat_task.abort();
        run_result
    }

    pub async fn forward_service(
        &mut self,
        target_device_id: String,
        service_name: String,
        local_bind_addr: String,
    ) -> Result<()> {
        self.ensure_session().await?;
        let session_token = self
            .config
            .session_token
            .clone()
            .ok_or_else(|| anyhow!("missing session token after ensure_session"))?;
        let resolved_service = self
            .resolve_service(&target_device_id, &service_name)
            .await?;
        run_local_forwarder(
            self.config.server_url.clone(),
            session_token,
            self.client.clone(),
            self.identity.clone(),
            resolved_service,
            local_bind_addr,
        )
        .await
    }

    pub fn upsert_forward_rule(
        &mut self,
        name: String,
        target_device_id: String,
        service_name: String,
        local_bind_addr: String,
        enabled: bool,
        transport_mode: String,
        direct_udp_bind_addr: String,
        direct_candidate_type: String,
        direct_wait_seconds: u64,
    ) -> Result<()> {
        let transport_mode = normalize_forward_transport_mode(&transport_mode)?;
        let direct_candidate_type = normalize_direct_candidate_type(&direct_candidate_type);
        validate_forward_rule(
            &name,
            &target_device_id,
            &service_name,
            &local_bind_addr,
            &transport_mode,
            &direct_udp_bind_addr,
            &direct_candidate_type,
            direct_wait_seconds,
        )?;
        if let Some(existing) = self
            .config
            .forward_rules
            .iter_mut()
            .find(|rule| rule.name == name)
        {
            existing.target_device_id = target_device_id;
            existing.service_name = service_name;
            existing.local_bind_addr = local_bind_addr;
            existing.enabled = enabled;
            existing.transport_mode = transport_mode;
            existing.direct_udp_bind_addr = direct_udp_bind_addr;
            existing.direct_candidate_type = direct_candidate_type;
            existing.direct_wait_seconds = direct_wait_seconds;
        } else {
            self.config.forward_rules.push(LocalForwardConfig {
                name,
                target_device_id,
                service_name,
                local_bind_addr,
                enabled,
                transport_mode,
                direct_udp_bind_addr,
                direct_candidate_type,
                direct_wait_seconds,
            });
        }
        self.save()
    }

    pub fn delete_forward_rule(&mut self, name: &str) -> Result<()> {
        let original_len = self.config.forward_rules.len();
        self.config.forward_rules.retain(|rule| rule.name != name);
        if self.config.forward_rules.len() == original_len {
            return Err(anyhow!("forward rule {} was not found", name));
        }
        self.save()
    }

    pub async fn run(&mut self) -> Result<()> {
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        tokio::spawn(async move {
            let _ = tokio::signal::ctrl_c().await;
            let _ = shutdown_tx.send(true);
        });
        self.run_until_shutdown(shutdown_rx).await
    }

    pub async fn run_until_shutdown(
        &mut self,
        mut shutdown_rx: watch::Receiver<bool>,
    ) -> Result<()> {
        let mut runtime_state = RuntimeStateWriter::new(&self.config_path, &self.config);
        let mut restart_backoff = RUNTIME_RETRY_INITIAL_BACKOFF;
        let mut force_session_refresh = false;

        loop {
            if shutdown_requested(&shutdown_rx) {
                runtime_state
                    .mark_stopped(&self.config, "shutdown requested before next iteration");
                return Ok(());
            }
            runtime_state.mark_starting(
                &self.config,
                if force_session_refresh {
                    "refreshing session and preparing runtime after a previous failure"
                } else {
                    "preparing relay service and enabled forward rules"
                },
            );
            if let Err(err) = self.prepare_runtime_iteration(force_session_refresh).await {
                warn!("agent runtime preparation failed: {err}");
                runtime_state.mark_prepare_failed(&self.config, &err.to_string(), restart_backoff);
                force_session_refresh = true;
                if wait_for_restart_or_shutdown(restart_backoff, &mut shutdown_rx).await {
                    runtime_state.mark_stopped(
                        &self.config,
                        "shutdown requested while waiting to retry runtime preparation",
                    );
                    return Ok(());
                }
                restart_backoff = next_retry_delay(restart_backoff);
                continue;
            }

            let session_token = self.current_session_token()?;
            runtime_state.mark_running(&self.config);
            let outcome = self
                .run_runtime_iteration(session_token.clone(), &mut shutdown_rx, &mut runtime_state)
                .await;
            match outcome {
                RuntimeIterationOutcome::Shutdown => {
                    runtime_state.mark_stopped(&self.config, "shutdown requested");
                    return Ok(());
                }
                RuntimeIterationOutcome::Restart(reason) => {
                    warn!("agent runtime restarting: {reason}");
                    runtime_state.mark_restarting(
                        &self.config,
                        &reason,
                        RUNTIME_RETRY_INITIAL_BACKOFF,
                    );
                    force_session_refresh = true;
                    restart_backoff = RUNTIME_RETRY_INITIAL_BACKOFF;
                }
            }

            if wait_for_restart_or_shutdown(restart_backoff, &mut shutdown_rx).await {
                runtime_state.mark_stopped(
                    &self.config,
                    "shutdown requested while waiting for runtime restart",
                );
                return Ok(());
            }
            restart_backoff = next_retry_delay(restart_backoff);
        }
    }

    pub fn save(&self) -> Result<()> {
        self.config.save(&self.config_path)
    }

    fn current_session_token(&self) -> Result<String> {
        self.config
            .session_token
            .clone()
            .ok_or_else(|| anyhow!("missing session token"))
    }

    fn invalidate_session(&mut self) {
        self.config.session_token = None;
        self.config.session_expires_at = None;
    }

    async fn sync_published_services(&self) -> Result<()> {
        let session_token = self.current_session_token()?;
        for service in &self.config.published_services {
            self.client
                .upsert_service(
                    &session_token,
                    &UpsertServiceRequest {
                        name: service.name.clone(),
                        allowed_device_ids: service.allowed_device_ids.clone(),
                    },
                )
                .await
                .with_context(|| format!("failed to sync published service {}", service.name))?;
        }
        if !self.config.published_services.is_empty() {
            info!(
                "synchronized {} published services",
                self.config.published_services.len()
            );
        }
        Ok(())
    }

    async fn prepare_runtime_iteration(&mut self, force_session_refresh: bool) -> Result<()> {
        if force_session_refresh {
            self.invalidate_session();
            self.save()?;
        }
        validate_runtime_config(&self.config)?;
        self.ensure_session().await?;
        self.sync_published_services().await?;
        self.save()
    }

    async fn run_runtime_iteration(
        &self,
        session_token: String,
        shutdown_rx: &mut watch::Receiver<bool>,
        runtime_state: &mut RuntimeStateWriter,
    ) -> RuntimeIterationOutcome {
        let mut tasks = JoinSet::new();
        let runtime_state_handle = runtime_state.clone();
        let local_device_id = match &self.config.device_id {
            Some(device_id) => device_id.clone(),
            None => {
                return RuntimeIterationOutcome::Restart("device is not joined yet".to_string());
            }
        };
        tasks.spawn(run_relay_service_supervisor(
            self.config.server_url.clone(),
            session_token.clone(),
            self.relay_identity.clone(),
            local_device_id.clone(),
            self.config.published_services.clone(),
            Some(runtime_state_handle.clone()),
        ));

        let direct_services = self
            .config
            .published_services
            .iter()
            .filter(|service| service.direct_enabled)
            .cloned()
            .collect::<Vec<_>>();
        for service in direct_services {
            tasks.spawn(run_direct_service_supervisor(
                self.client.clone(),
                session_token.clone(),
                self.identity.clone(),
                self.relay_identity.clone(),
                local_device_id.clone(),
                service.clone(),
                service.direct_udp_bind_addr.clone(),
                service.direct_candidate_type.clone(),
                Duration::from_secs(service.direct_wait_seconds),
                Some(runtime_state_handle.clone()),
            ));
        }

        let forward_rules = self
            .config
            .forward_rules
            .iter()
            .filter(|rule| rule.enabled)
            .cloned()
            .collect::<Vec<_>>();
        for rule in forward_rules {
            if forward_transport_is_auto(&rule.transport_mode) {
                tasks.spawn(run_auto_forward_loop(
                    self.client.clone(),
                    self.config.server_url.clone(),
                    session_token.clone(),
                    self.identity.clone(),
                    rule.name.clone(),
                    rule.target_device_id.clone(),
                    rule.service_name.clone(),
                    rule.local_bind_addr.clone(),
                    rule.direct_udp_bind_addr.clone(),
                    rule.direct_candidate_type.clone(),
                    Duration::from_secs(rule.direct_wait_seconds),
                    Some(runtime_state_handle.clone()),
                ));
            } else {
                tasks.spawn(run_forward_rule_supervisor(
                    self.client.clone(),
                    self.config.server_url.clone(),
                    session_token.clone(),
                    self.identity.clone(),
                    rule,
                    Some(runtime_state_handle.clone()),
                ));
            }
        }

        let mut heartbeat = time::interval(RUNTIME_HEARTBEAT_INTERVAL);
        heartbeat.set_missed_tick_behavior(MissedTickBehavior::Delay);
        let _ = heartbeat.tick().await;

        loop {
            tokio::select! {
                changed = shutdown_rx.changed() => {
                    if changed.is_err() || shutdown_requested(shutdown_rx) {
                        drain_runtime_tasks(&mut tasks).await;
                        return RuntimeIterationOutcome::Shutdown;
                    }
                }
                _ = heartbeat.tick() => {
                    if !self.config.has_valid_session() {
                        drain_runtime_tasks(&mut tasks).await;
                        return RuntimeIterationOutcome::Restart(
                            "current session is nearing expiry and will be refreshed".to_string(),
                        );
                    }

                    if let Err(err) = self.client.heartbeat(&session_token).await {
                        drain_runtime_tasks(&mut tasks).await;
                        return RuntimeIterationOutcome::Restart(format!("heartbeat failed: {err}"));
                    }
                    runtime_state.note_heartbeat_ok();
                }
                maybe_task = tasks.join_next() => {
                    let reason = match maybe_task {
                        Some(Ok(Ok(()))) => "agent background task exited unexpectedly".to_string(),
                        Some(Ok(Err(err))) => format!("agent background task failed: {err}"),
                        Some(Err(err)) => format!("agent background task crashed: {err}"),
                        None => "all agent background tasks exited unexpectedly".to_string(),
                    };
                    drain_runtime_tasks(&mut tasks).await;
                    return RuntimeIterationOutcome::Restart(reason);
                }
            }
        }
    }

    async fn register(&mut self, join_token: Option<String>) -> Result<()> {
        let device_id = self.identity.device_id();
        let nonce = minipunch_core::generate_token("nonce");
        let message = registration_message(
            &device_id,
            &self.config.device_name,
            std::env::consts::OS,
            &nonce,
        );
        let request = RegisterDeviceRequest {
            join_token,
            device_id: device_id.clone(),
            device_name: self.config.device_name.clone(),
            os: std::env::consts::OS.to_string(),
            version: CLIENT_VERSION.to_string(),
            public_key: self.identity.public_key_base64(),
            relay_public_key: self.relay_identity.public_key_base64(),
            relay_public_key_signature: self.identity.sign_base64(&relay_key_binding_message(
                &device_id,
                &self.relay_identity.public_key_base64(),
            )),
            nonce,
            signature: self.identity.sign_base64(&message),
        };
        let response = self.client.register_device(&request).await?;
        self.config.device_id = Some(response.device_id);
        self.config.session_token = Some(response.session_token);
        self.config.session_expires_at = Some(response.session_expires_at);
        self.config.private_key_base64 = Some(self.identity.private_key_base64());
        self.config.relay_private_key_base64 = Some(self.relay_identity.private_key_base64());
        Ok(())
    }

    async fn wait_for_ready_rendezvous(
        &mut self,
        rendezvous_id: &str,
        wait_timeout: Duration,
    ) -> Result<DirectRendezvousSession> {
        let deadline = tokio::time::Instant::now() + wait_timeout;
        loop {
            let session = self.direct_rendezvous(rendezvous_id).await?;
            match session.status.as_str() {
                "ready" => return Ok(session),
                "expired" => {
                    return Err(anyhow!(
                        "direct rendezvous {} has already expired",
                        rendezvous_id
                    ));
                }
                _ => {
                    if tokio::time::Instant::now() >= deadline {
                        return Err(anyhow!(
                            "timed out waiting for direct rendezvous {} to become ready",
                            rendezvous_id
                        ));
                    }
                    time::sleep(Duration::from_millis(250)).await;
                }
            }
        }
    }

    async fn lookup_device_public_key(&mut self, device_id: &str) -> Result<String> {
        let snapshot = self.network_snapshot().await?;
        let device = snapshot
            .devices
            .into_iter()
            .find(|device| device.device_id == device_id)
            .ok_or_else(|| anyhow!("device {} was not found in network snapshot", device_id))?;
        if device_id_from_public_key(&device.identity_public_key) != device.device_id {
            return Err(anyhow!(
                "device {} has an invalid identity key binding",
                device_id
            ));
        }
        Ok(device.identity_public_key)
    }

    fn published_service_config(&self, service_name: &str) -> Result<PublishedServiceConfig> {
        self.config
            .published_services
            .iter()
            .find(|service| service.name == service_name)
            .cloned()
            .ok_or_else(|| {
                anyhow!(
                    "published service {} was not found in local config",
                    service_name
                )
            })
    }
}

fn load_or_create_identity(config: &mut AgentConfig) -> Result<DeviceIdentity> {
    match &config.private_key_base64 {
        Some(private_key) => DeviceIdentity::from_private_key_base64(private_key)
            .map_err(|err| anyhow!(err.to_string())),
        None => {
            let identity = DeviceIdentity::generate();
            config.private_key_base64 = Some(identity.private_key_base64());
            Ok(identity)
        }
    }
}

fn load_or_create_relay_identity(config: &mut AgentConfig) -> Result<RelayKeypair> {
    match &config.relay_private_key_base64 {
        Some(private_key) => RelayKeypair::from_private_key_base64(private_key)
            .map_err(|err| anyhow!(err.to_string())),
        None => {
            let identity = RelayKeypair::generate();
            config.relay_private_key_base64 = Some(identity.private_key_base64());
            Ok(identity)
        }
    }
}

#[derive(Debug)]
enum RuntimeIterationOutcome {
    Shutdown,
    Restart(String),
}

async fn resolve_service_with_session(
    client: &ControlPlaneClient,
    session_token: &str,
    target_device_id: &str,
    service_name: &str,
) -> Result<ResolvedService> {
    let snapshot = client.network_snapshot(session_token).await?;
    let service = snapshot
        .services
        .into_iter()
        .find(|service| service.owner_device_id == target_device_id && service.name == service_name)
        .ok_or_else(|| {
            anyhow!(
                "service {} on device {} was not found or is not accessible",
                service_name,
                target_device_id
            )
        })?;
    let target_device = snapshot
        .devices
        .into_iter()
        .find(|device| device.device_id == target_device_id)
        .ok_or_else(|| anyhow!("target device {} was not found", target_device_id))?;

    if device_id_from_public_key(&target_device.identity_public_key) != target_device.device_id {
        return Err(anyhow!(
            "target device {} has an invalid identity key binding",
            target_device_id
        ));
    }
    verify_signature_base64(
        &target_device.identity_public_key,
        &relay_key_binding_message(&target_device.device_id, &target_device.relay_public_key),
        &target_device.relay_public_key_signature,
    )
    .map_err(|err| anyhow!("target device relay key verification failed: {err}"))?;

    Ok(ResolvedService {
        service,
        target_relay_public_key: target_device.relay_public_key,
    })
}

async fn wait_for_ready_rendezvous_with_session(
    client: &ControlPlaneClient,
    session_token: &str,
    rendezvous_id: &str,
    wait_timeout: Duration,
) -> Result<DirectRendezvousSession> {
    let deadline = tokio::time::Instant::now() + wait_timeout;
    loop {
        let session = client
            .direct_rendezvous(session_token, rendezvous_id)
            .await?;
        match session.status.as_str() {
            "ready" => return Ok(session),
            "expired" => {
                return Err(anyhow!(
                    "direct rendezvous {} has already expired",
                    rendezvous_id
                ));
            }
            _ => {
                if tokio::time::Instant::now() >= deadline {
                    return Err(anyhow!(
                        "timed out waiting for direct rendezvous {} to become ready",
                        rendezvous_id
                    ));
                }
                time::sleep(Duration::from_millis(250)).await;
            }
        }
    }
}

async fn lookup_device_public_key_with_session(
    client: &ControlPlaneClient,
    session_token: &str,
    device_id: &str,
) -> Result<String> {
    let snapshot = client.network_snapshot(session_token).await?;
    let device = snapshot
        .devices
        .into_iter()
        .find(|device| device.device_id == device_id)
        .ok_or_else(|| anyhow!("device {} was not found in network snapshot", device_id))?;
    if device_id_from_public_key(&device.identity_public_key) != device.device_id {
        return Err(anyhow!(
            "device {} has an invalid identity key binding",
            device_id
        ));
    }
    Ok(device.identity_public_key)
}

async fn prepare_auto_direct_forward(
    client: &ControlPlaneClient,
    session_token: &str,
    device_identity: &DeviceIdentity,
    rule_name: &str,
    target_device_id: &str,
    service_name: &str,
    direct_udp_bind_addr: &str,
    direct_candidate_type: &str,
    direct_wait_timeout: Duration,
    runtime_state: Option<&RuntimeStateWriter>,
    observation_transport: Option<&str>,
    start_detail: String,
    ready_detail_prefix: &str,
) -> Result<PreparedDirectForward> {
    if let Some(runtime_state) = runtime_state {
        runtime_state.note_forward_observation(
            rule_name,
            "auto",
            observation_transport,
            "direct_connecting",
            start_detail,
            None,
        );
    }

    let session = match client
        .start_direct_rendezvous(
            session_token,
            &StartDirectRendezvousRequest {
                target_device_id: target_device_id.to_string(),
                service_name: service_name.to_string(),
                source_candidates: vec![bind_candidate(
                    direct_udp_bind_addr,
                    direct_candidate_type,
                )],
            },
        )
        .await
    {
        Ok(session) => session,
        Err(err) => {
            if let Some(runtime_state) = runtime_state {
                runtime_state.note_forward_failure(
                    rule_name,
                    "direct",
                    "rendezvous_start",
                    err.to_string(),
                );
            }
            return Err(err);
        }
    };

    if let Some(runtime_state) = runtime_state {
        runtime_state.note_forward_observation(
            rule_name,
            "auto",
            observation_transport,
            "direct_connecting",
            format!(
                "waiting for rendezvous {} to become ready",
                session.rendezvous_id
            ),
            None,
        );
    }

    let session = match wait_for_ready_rendezvous_with_session(
        client,
        session_token,
        &session.rendezvous_id,
        direct_wait_timeout,
    )
    .await
    {
        Ok(session) => session,
        Err(err) => {
            if let Some(runtime_state) = runtime_state {
                runtime_state.note_forward_failure(
                    rule_name,
                    "direct",
                    "rendezvous_wait",
                    err.to_string(),
                );
            }
            return Err(err);
        }
    };

    let peer_public_key = match lookup_device_public_key_with_session(
        client,
        session_token,
        target_device_id,
    )
    .await
    {
        Ok(peer_public_key) => peer_public_key,
        Err(err) => {
            if let Some(runtime_state) = runtime_state {
                runtime_state.note_forward_failure(
                    rule_name,
                    "direct",
                    "peer_lookup",
                    err.to_string(),
                );
            }
            return Err(err);
        }
    };

    info!(
        "auto-forward prepared direct transport for {}:{} via rendezvous {}",
        target_device_id, service_name, session.rendezvous_id
    );
    if let Some(runtime_state) = runtime_state {
        runtime_state.note_forward_observation(
            rule_name,
            "auto",
            observation_transport,
            "direct_connecting",
            format!(
                "rendezvous {} is ready; {} via {}",
                session.rendezvous_id, ready_detail_prefix, direct_udp_bind_addr
            ),
            None,
        );
    }

    prepare_direct_tcp_forward(
        device_identity.clone(),
        device_identity.device_id(),
        peer_public_key,
        session,
        direct_udp_bind_addr.to_string(),
        direct_wait_timeout,
        runtime_state.map(|runtime_state| {
            ForwardRuntimeHook::new(
                runtime_state.clone(),
                rule_name.to_string(),
                "auto",
                "direct",
            )
        }),
    )
    .await
    .with_context(|| {
        format!(
            "failed to prepare direct forward for {}:{}",
            target_device_id, service_name
        )
    })
}

async fn run_auto_forward_accept_loop(
    listener: TcpListener,
    local_bind_addr: String,
    device_identity: DeviceIdentity,
    ingress_rx: watch::Receiver<Option<AutoForwardIngress>>,
) {
    info!("auto-forward ingress is listening on {local_bind_addr}");

    loop {
        let (stream, peer_addr) = match listener.accept().await {
            Ok(result) => result,
            Err(err) => {
                warn!(
                    "auto-forward ingress accept failed on {}: {}",
                    local_bind_addr, err
                );
                time::sleep(Duration::from_millis(200)).await;
                continue;
            }
        };
        let ingress = ingress_rx.borrow().clone();
        let device_identity = device_identity.clone();
        tokio::spawn(async move {
            match ingress {
                Some(AutoForwardIngress::Relay(ingress)) => {
                    if let Err(err) = handle_local_forward_connection(
                        ingress.relay,
                        device_identity,
                        ingress.resolved_service,
                        stream,
                        ingress.runtime_hook,
                        Some(ingress.tracker),
                    )
                    .await
                    {
                        warn!("relay auto-forward connection from {peer_addr} failed: {err}");
                    }
                }
                Some(AutoForwardIngress::Direct(ingress)) => {
                    if let Err(err) =
                        handle_local_auto_direct_connection(ingress, device_identity, stream).await
                    {
                        warn!("direct auto-forward connection from {peer_addr} failed: {err}");
                    }
                }
                None => {
                    warn!(
                        "auto-forward rejected connection from {} because no transport is active",
                        peer_addr
                    );
                    let mut stream = stream;
                    let _ = stream.shutdown().await;
                }
            }
        });
    }
}

async fn handle_local_auto_direct_connection(
    ingress: AutoDirectIngress,
    device_identity: DeviceIdentity,
    stream: tokio::net::TcpStream,
) -> Result<()> {
    let direct_runtime_hook = ingress.runtime_hook.clone();
    let resolved_service = ingress.resolved_service.clone();
    let peer_addr = stream
        .peer_addr()
        .map(|addr| addr.to_string())
        .unwrap_or_else(|_| "unknown".to_string());

    let prepared = match prepare_local_direct_forward_connection(
        ingress.connection.clone(),
        device_identity.clone(),
        resolved_service.clone(),
        stream,
        direct_runtime_hook.clone(),
    )
    .await
    {
        Ok(prepared) => prepared,
        Err((stream, err)) => {
            if let Some(fallback) = ingress.handoff_relay_fallback.clone() {
                let draining_connections = fallback.active_connections();
                if draining_connections > 0 {
                    if let Some(runtime_hook) = &direct_runtime_hook {
                        runtime_hook.note_state(
                            "direct_handoff_fallback",
                            format!(
                                "direct handoff failed before channel open; serving this connection via relay while {} relay connection(s) still drain",
                                draining_connections
                            ),
                        );
                    }
                    if let Some(relay) = fallback.relay().await {
                        info!(
                            "auto-forward direct handoff is falling back to relay for a new connection on {} while {} relay connection(s) still drain",
                            peer_addr, draining_connections
                        );
                        return handle_local_forward_connection(
                            relay,
                            device_identity,
                            resolved_service,
                            stream,
                            fallback.runtime_hook.clone(),
                            Some(fallback.tracker.clone()),
                        )
                        .await;
                    }
                }
            }
            return Err(err);
        }
    };

    run_prepared_local_direct_forward_connection(prepared, direct_runtime_hook).await
}

fn spawn_relay_drain_task(
    fallback: AutoRelayHandoffFallback,
    direct_runtime_hook: Option<ForwardRuntimeHook>,
    tracker: RelayForwarderTracker,
    rule_name: String,
    target_device_id: String,
    service_name: String,
    local_bind_addr: String,
) {
    tokio::spawn(async move {
        while tracker.active_connections() > 0 {
            time::sleep(AUTO_DIRECT_RETRY_BUSY_POLL_INTERVAL).await;
        }
        fallback.deactivate().await;
        if let Some(runtime_hook) = &direct_runtime_hook {
            runtime_hook.note_state(
                "direct_active",
                format!(
                    "relay drain finished; direct is now the sole ingress on {}",
                    local_bind_addr
                ),
            );
        }
        info!(
            "relay drain finished for auto-forward rule {} ({}:{})",
            rule_name, target_device_id, service_name
        );
    });
}

async fn run_auto_service_loop(
    client: ControlPlaneClient,
    server_url: String,
    session_token: String,
    identity: DeviceIdentity,
    relay_identity: RelayKeypair,
    local_device_id: String,
    published_service: PublishedServiceConfig,
    direct_udp_bind_addr: String,
    direct_candidate_type: String,
    direct_wait_timeout: Duration,
) -> Result<()> {
    let mut tasks = JoinSet::new();
    tasks.spawn(run_relay_service_supervisor(
        server_url,
        session_token.clone(),
        relay_identity.clone(),
        local_device_id.clone(),
        vec![published_service.clone()],
        None,
    ));
    tasks.spawn(run_direct_service_supervisor(
        client,
        session_token,
        identity,
        relay_identity,
        local_device_id,
        published_service,
        direct_udp_bind_addr,
        direct_candidate_type,
        direct_wait_timeout,
        None,
    ));

    let outcome = match tasks.join_next().await {
        Some(Ok(Ok(()))) => Err(anyhow!("auto-serve background task exited unexpectedly")),
        Some(Ok(Err(err))) => Err(anyhow!("auto-serve background task failed: {err}")),
        Some(Err(err)) => Err(anyhow!("auto-serve background task crashed: {err}")),
        None => Err(anyhow!(
            "all auto-serve background tasks exited unexpectedly"
        )),
    };
    drain_runtime_tasks(&mut tasks).await;
    outcome
}

async fn run_relay_service_supervisor(
    server_url: String,
    session_token: String,
    relay_identity: RelayKeypair,
    local_device_id: String,
    published_services: Vec<PublishedServiceConfig>,
    runtime_state: Option<RuntimeStateWriter>,
) -> Result<()> {
    let mut retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;

    loop {
        match run_relay_service_loop(
            server_url.clone(),
            session_token.clone(),
            relay_identity.clone(),
            local_device_id.clone(),
            published_services.clone(),
            runtime_state.clone(),
        )
        .await
        {
            Ok(()) => warn!("relay service loop closed, will retry"),
            Err(err) => warn!("relay service loop failed: {err}"),
        }

        time::sleep(retry_delay).await;
        retry_delay = next_retry_delay(retry_delay);
    }
}

struct DirectServiceSessionTaskResult {
    rendezvous_id: String,
    outcome: Result<()>,
}

async fn run_direct_service_session(
    client: ControlPlaneClient,
    session_token: String,
    local_identity: DeviceIdentity,
    relay_identity: RelayKeypair,
    local_device_id: String,
    published_service: PublishedServiceConfig,
    direct_udp_bind_addr: String,
    direct_candidate_type: String,
    direct_wait_timeout: Duration,
    runtime_state: Option<RuntimeStateWriter>,
    direct_hub: SharedDirectSocketHub,
    session: DirectRendezvousSession,
    concurrent_session_count: usize,
) -> DirectServiceSessionTaskResult {
    let rendezvous_id = session.rendezvous_id.clone();
    let outcome = async {
        if let Some(runtime_state) = &runtime_state {
            let detail = if concurrent_session_count > 0 {
                format!(
                    "serving {} direct session(s); preparing rendezvous {} via {}",
                    concurrent_session_count, rendezvous_id, direct_udp_bind_addr
                )
            } else {
                format!(
                    "preparing rendezvous {} via {}",
                    rendezvous_id, direct_udp_bind_addr
                )
            };
            runtime_state.note_published_service_observation(
                &published_service.name,
                published_service.direct_enabled,
                if concurrent_session_count > 0 {
                    Some("direct")
                } else {
                    None
                },
                "direct_connecting",
                detail,
                None,
            );
        }

        if session.target.announced_at.is_none()
            || session.target.candidates.len() != 1
            || session.target.candidates[0].addr != direct_udp_bind_addr
            || session.target.candidates[0].candidate_type != direct_candidate_type
        {
            client
                .update_direct_rendezvous_candidates(
                    &session_token,
                    &rendezvous_id,
                    &UpdateDirectRendezvousCandidatesRequest {
                        candidates: vec![bind_candidate(
                            &direct_udp_bind_addr,
                            &direct_candidate_type,
                        )],
                    },
                )
                .await
                .map_err(|err| {
                    if let Some(runtime_state) = &runtime_state {
                        runtime_state.note_published_service_failure(
                            &published_service.name,
                            "direct",
                            "announce_candidate",
                            err.to_string(),
                        );
                    }
                    err
                })?;
        }

        let session = wait_for_ready_rendezvous_with_session(
            &client,
            &session_token,
            &rendezvous_id,
            direct_wait_timeout,
        )
        .await
        .map_err(|err| {
            if let Some(runtime_state) = &runtime_state {
                runtime_state.note_published_service_failure(
                    &published_service.name,
                    "direct",
                    "rendezvous_wait",
                    err.to_string(),
                );
            }
            err
        })?;

        let peer_public_key = lookup_device_public_key_with_session(
            &client,
            &session_token,
            &session.source_device_id,
        )
        .await
        .map_err(|err| {
            if let Some(runtime_state) = &runtime_state {
                runtime_state.note_published_service_failure(
                    &published_service.name,
                    "direct",
                    "peer_lookup",
                    err.to_string(),
                );
            }
            err
        })?;

        if let Some(runtime_state) = &runtime_state {
            let detail = if concurrent_session_count > 0 {
                format!(
                    "serving {} direct session(s); probing rendezvous {} via {}",
                    concurrent_session_count, rendezvous_id, direct_udp_bind_addr
                )
            } else {
                format!(
                    "rendezvous {} is ready; probing direct path via {}",
                    rendezvous_id, direct_udp_bind_addr
                )
            };
            runtime_state.note_published_service_observation(
                &published_service.name,
                published_service.direct_enabled,
                Some("direct"),
                "direct_connecting",
                detail,
                None,
            );
        }

        run_direct_tcp_serve_on_hub(
            local_identity,
            relay_identity,
            local_device_id,
            peer_public_key,
            session,
            published_service.clone(),
            direct_udp_bind_addr,
            direct_hub,
            direct_wait_timeout,
            runtime_state.as_ref().map(|runtime_state| {
                PublishedServiceRuntimeHook::new(
                    runtime_state.clone(),
                    published_service.name.clone(),
                    published_service.direct_enabled,
                    "direct",
                )
            }),
        )
        .await
    }
    .await;

    DirectServiceSessionTaskResult {
        rendezvous_id,
        outcome,
    }
}

async fn run_direct_service_supervisor(
    client: ControlPlaneClient,
    session_token: String,
    local_identity: DeviceIdentity,
    relay_identity: RelayKeypair,
    local_device_id: String,
    published_service: PublishedServiceConfig,
    direct_udp_bind_addr: String,
    direct_candidate_type: String,
    direct_wait_timeout: Duration,
    runtime_state: Option<RuntimeStateWriter>,
) -> Result<()> {
    let mut handled = HashSet::new();
    let mut active_sessions = HashSet::new();
    let mut session_tasks = JoinSet::<DirectServiceSessionTaskResult>::new();
    let mut retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;
    let direct_socket = bind_direct_udp_socket(&direct_udp_bind_addr)
        .await
        .map_err(|err| {
            if let Some(runtime_state) = &runtime_state {
                runtime_state.note_published_service_failure(
                    &published_service.name,
                    "direct",
                    "bind_hub",
                    err.to_string(),
                );
            }
            err
        })?;
    let direct_hub = SharedDirectSocketHub::new(direct_socket);

    if let Some(runtime_state) = &runtime_state {
        runtime_state.note_published_service_observation(
            &published_service.name,
            published_service.direct_enabled,
            None,
            if published_service.direct_enabled {
                "waiting_direct"
            } else {
                "relay_only"
            },
            if published_service.direct_enabled {
                format!("waiting for direct rendezvous on {}", direct_udp_bind_addr)
            } else {
                "relay-only published service".to_string()
            },
            None,
        );
    }

    loop {
        while let Some(join_result) = session_tasks.try_join_next() {
            match join_result {
                Ok(task_result) => {
                    active_sessions.remove(&task_result.rendezvous_id);
                    handled.insert(task_result.rendezvous_id.clone());
                    match task_result.outcome {
                        Ok(()) => {
                            info!(
                                "direct auto-serve session {} for service {} ended",
                                task_result.rendezvous_id, published_service.name
                            );
                            if active_sessions.is_empty() {
                                if let Some(runtime_state) = &runtime_state {
                                    runtime_state.note_published_service_observation(
                                        &published_service.name,
                                        published_service.direct_enabled,
                                        None,
                                        "waiting_direct",
                                        format!(
                                            "direct session {} ended; waiting for the next rendezvous",
                                            task_result.rendezvous_id
                                        ),
                                        None,
                                    );
                                }
                            }
                        }
                        Err(err) => {
                            warn!(
                                "direct auto-serve session {} for service {} failed: {}",
                                task_result.rendezvous_id, published_service.name, err
                            );
                            if active_sessions.is_empty() {
                                if let Some(runtime_state) = &runtime_state {
                                    runtime_state.note_published_service_observation(
                                        &published_service.name,
                                        published_service.direct_enabled,
                                        None,
                                        "waiting_direct",
                                        format!(
                                            "direct session {} failed; waiting for the next rendezvous",
                                            task_result.rendezvous_id
                                        ),
                                        Some(err.to_string()),
                                    );
                                }
                            }
                        }
                    }
                }
                Err(err) => {
                    warn!(
                        "direct auto-serve background task crashed for service {}: {}",
                        published_service.name, err
                    );
                }
            }
        }

        let pending = match client.pending_direct_rendezvous(&session_token).await {
            Ok(pending) => {
                retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;
                pending
            }
            Err(err) => {
                warn!("direct auto-serve failed to fetch pending rendezvous: {err}");
                if let Some(runtime_state) = &runtime_state {
                    runtime_state.note_published_service_failure(
                        &published_service.name,
                        "direct",
                        "pending_fetch",
                        err.to_string(),
                    );
                    runtime_state.note_published_service_observation(
                        &published_service.name,
                        published_service.direct_enabled,
                        None,
                        "waiting_direct",
                        "failed to fetch pending direct rendezvous; retrying".to_string(),
                        Some(err.to_string()),
                    );
                }
                time::sleep(retry_delay).await;
                retry_delay = next_retry_delay(retry_delay);
                continue;
            }
        };
        let attempts = pending
            .attempts
            .into_iter()
            .filter(|attempt| {
                attempt.target_device_id == local_device_id
                    && attempt.service_name == published_service.name
                    && !handled.contains(&attempt.rendezvous_id)
                    && !active_sessions.contains(&attempt.rendezvous_id)
            })
            .collect::<Vec<_>>();

        if attempts.is_empty() {
            if active_sessions.is_empty() {
                if let Some(runtime_state) = &runtime_state {
                    runtime_state.note_published_service_observation(
                        &published_service.name,
                        published_service.direct_enabled,
                        None,
                        "waiting_direct",
                        format!(
                            "waiting for pending direct rendezvous on {}",
                            direct_udp_bind_addr
                        ),
                        None,
                    );
                }
            }
            time::sleep(Duration::from_millis(500)).await;
            continue;
        }

        for attempt in attempts {
            if attempt.status == "expired" {
                handled.insert(attempt.rendezvous_id);
                continue;
            }

            let concurrent_session_count = active_sessions.len();
            active_sessions.insert(attempt.rendezvous_id.clone());
            info!(
                "auto-serve queued direct transport for service {} on rendezvous {} (concurrent sessions: {})",
                published_service.name, attempt.rendezvous_id, concurrent_session_count
            );
            session_tasks.spawn(run_direct_service_session(
                client.clone(),
                session_token.clone(),
                local_identity.clone(),
                relay_identity.clone(),
                local_device_id.clone(),
                published_service.clone(),
                direct_udp_bind_addr.clone(),
                direct_candidate_type.clone(),
                direct_wait_timeout,
                runtime_state.clone(),
                direct_hub.clone(),
                attempt,
                concurrent_session_count,
            ));
            retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;
            if let Some(runtime_state) = &runtime_state {
                runtime_state.note_published_service_observation(
                    &published_service.name,
                    published_service.direct_enabled,
                    if concurrent_session_count > 0 {
                        Some("direct")
                    } else {
                        None
                    },
                    "direct_connecting",
                    if concurrent_session_count > 0 {
                        format!(
                            "serving {} direct session(s); polling and preparing additional rendezvous on shared hub {}",
                            concurrent_session_count, direct_udp_bind_addr
                        )
                    } else {
                        format!(
                            "preparing direct rendezvous on shared hub {}",
                            direct_udp_bind_addr
                        )
                    },
                    None,
                );
            }
        }

        if !active_sessions.is_empty() {
            time::sleep(Duration::from_millis(200)).await;
        } else {
            time::sleep(Duration::from_millis(500)).await;
        }
    }
}

async fn run_forward_rule_supervisor(
    client: ControlPlaneClient,
    server_url: String,
    session_token: String,
    device_identity: DeviceIdentity,
    rule: LocalForwardConfig,
    runtime_state: Option<RuntimeStateWriter>,
) -> Result<()> {
    let mut retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;

    if let Some(runtime_state) = &runtime_state {
        runtime_state.note_forward_observation(
            &rule.name,
            &rule.transport_mode,
            None,
            "resolving",
            format!(
                "resolving {} on {} via relay",
                rule.service_name, rule.target_device_id
            ),
            None,
        );
    }

    loop {
        let resolved_service = match resolve_service_with_session(
            &client,
            &session_token,
            &rule.target_device_id,
            &rule.service_name,
        )
        .await
        {
            Ok(resolved_service) => {
                retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;
                resolved_service
            }
            Err(err) => {
                warn!(
                    "forward rule {} failed to resolve {} on {}: {}",
                    rule.name, rule.service_name, rule.target_device_id, err
                );
                if let Some(runtime_state) = &runtime_state {
                    runtime_state.note_forward_observation(
                        &rule.name,
                        &rule.transport_mode,
                        None,
                        "resolve_retry",
                        format!(
                            "failed to resolve {} on {}; retrying in {}s",
                            rule.service_name,
                            rule.target_device_id,
                            retry_delay.as_secs()
                        ),
                        Some(err.to_string()),
                    );
                }
                time::sleep(retry_delay).await;
                retry_delay = next_retry_delay(retry_delay);
                continue;
            }
        };

        if let Some(runtime_state) = &runtime_state {
            runtime_state.note_forward_observation(
                &rule.name,
                &rule.transport_mode,
                Some("relay"),
                "relay_active",
                format!("listening on {} via relay", rule.local_bind_addr),
                None,
            );
        }

        if let Err(err) = run_local_forwarder_loop(
            server_url.clone(),
            session_token.clone(),
            device_identity.clone(),
            resolved_service,
            rule.local_bind_addr.clone(),
            runtime_state.as_ref().map(|runtime_state| {
                ForwardRuntimeHook::new(
                    runtime_state.clone(),
                    rule.name.clone(),
                    rule.transport_mode.clone(),
                    "relay",
                )
            }),
        )
        .await
        {
            warn!("forward rule {} stopped: {err}", rule.name);
            if let Some(runtime_state) = &runtime_state {
                runtime_state.note_forward_observation(
                    &rule.name,
                    &rule.transport_mode,
                    Some("relay"),
                    "relay_retry",
                    format!(
                        "relay forwarder stopped; retrying in {}s",
                        retry_delay.as_secs()
                    ),
                    Some(err.to_string()),
                );
            }
        } else {
            warn!("forward rule {} exited unexpectedly", rule.name);
            if let Some(runtime_state) = &runtime_state {
                runtime_state.note_forward_observation(
                    &rule.name,
                    &rule.transport_mode,
                    Some("relay"),
                    "relay_retry",
                    format!(
                        "relay forwarder exited unexpectedly; retrying in {}s",
                        retry_delay.as_secs()
                    ),
                    None,
                );
            }
        }

        time::sleep(retry_delay).await;
        retry_delay = next_retry_delay(retry_delay);
    }
}

async fn run_auto_forward_loop(
    client: ControlPlaneClient,
    server_url: String,
    session_token: String,
    device_identity: DeviceIdentity,
    rule_name: String,
    target_device_id: String,
    service_name: String,
    local_bind_addr: String,
    direct_udp_bind_addr: String,
    direct_candidate_type: String,
    direct_wait_timeout: Duration,
    runtime_state: Option<RuntimeStateWriter>,
) -> Result<()> {
    let listener = match TcpListener::bind(&local_bind_addr).await {
        Ok(listener) => listener,
        Err(err) => {
            let err = anyhow!(
                "failed to bind local auto-forward address {}: {}",
                local_bind_addr,
                err
            );
            if let Some(runtime_state) = &runtime_state {
                runtime_state.note_forward_failure(
                    &rule_name,
                    "auto",
                    "local_bind",
                    err.to_string(),
                );
            }
            return Err(err);
        }
    };
    let (ingress_tx, ingress_rx) = watch::channel::<Option<AutoForwardIngress>>(None);
    tokio::spawn(run_auto_forward_accept_loop(
        listener,
        local_bind_addr.clone(),
        device_identity.clone(),
        ingress_rx,
    ));

    let mut selection_retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;
    let mut direct_retry_delay = AUTO_DIRECT_RETRY_INITIAL_BACKOFF;

    if let Some(runtime_state) = &runtime_state {
        runtime_state.note_forward_observation(
            &rule_name,
            "auto",
            None,
            "resolving",
            format!(
                "resolving {} on {} for auto transport",
                service_name, target_device_id
            ),
            None,
        );
    }

    'selection: loop {
        let _ = ingress_tx.send(None);
        let resolved_service = match resolve_service_with_session(
            &client,
            &session_token,
            &target_device_id,
            &service_name,
        )
        .await
        {
            Ok(resolved_service) => {
                selection_retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;
                resolved_service
            }
            Err(err) => {
                warn!(
                    "auto-forward failed to resolve {} on {}: {}",
                    service_name, target_device_id, err
                );
                if let Some(runtime_state) = &runtime_state {
                    runtime_state.note_forward_observation(
                        &rule_name,
                        "auto",
                        None,
                        "resolve_retry",
                        format!(
                            "failed to resolve {} on {}; retrying in {}s",
                            service_name,
                            target_device_id,
                            selection_retry_delay.as_secs()
                        ),
                        Some(err.to_string()),
                    );
                }
                time::sleep(selection_retry_delay).await;
                selection_retry_delay = next_retry_delay(selection_retry_delay);
                continue;
            }
        };

        let direct_runtime_hook = runtime_state.as_ref().map(|runtime_state| {
            ForwardRuntimeHook::new(runtime_state.clone(), rule_name.clone(), "auto", "direct")
        });
        let relay_runtime_hook = runtime_state.as_ref().map(|runtime_state| {
            ForwardRuntimeHook::new(runtime_state.clone(), rule_name.clone(), "auto", "relay")
        });

        let direct_attempt = prepare_auto_direct_forward(
            &client,
            &session_token,
            &device_identity,
            &rule_name,
            &target_device_id,
            &service_name,
            &direct_udp_bind_addr,
            &direct_candidate_type,
            direct_wait_timeout,
            runtime_state.as_ref(),
            Some("direct"),
            format!("starting direct rendezvous via {}", direct_udp_bind_addr),
            "probing direct path",
        )
        .await;

        match direct_attempt {
            Ok(prepared) => {
                let DirectForwardIngress {
                    rendezvous_id,
                    connection,
                    peer_device_id,
                } = into_direct_forward_ingress(prepared);
                let _ = ingress_tx.send(Some(AutoForwardIngress::Direct(AutoDirectIngress {
                    connection: connection.clone(),
                    resolved_service: resolved_service.clone(),
                    runtime_hook: direct_runtime_hook.clone(),
                    handoff_relay_fallback: None,
                })));
                if let Some(runtime_hook) = &direct_runtime_hook {
                    runtime_hook.note_state(
                        "direct_active",
                        format!(
                            "direct active on {} via rendezvous {}",
                            local_bind_addr, rendezvous_id
                        ),
                    );
                }
                direct_retry_delay = AUTO_DIRECT_RETRY_INITIAL_BACKOFF;
                selection_retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;
                info!(
                    "auto-forward selected direct transport for {}:{} on {} via {} ({})",
                    target_device_id, service_name, local_bind_addr, rendezvous_id, peer_device_id
                );

                connection.wait_closed().await;
                let _ = ingress_tx.send(None);
                if let Some(runtime_state) = &runtime_state {
                    runtime_state.note_forward_failure(
                        &rule_name,
                        "direct",
                        "data_plane",
                        format!(
                            "direct UDP connection closed for local forward {}",
                            local_bind_addr
                        ),
                    );
                    runtime_state.note_forward_observation(
                        &rule_name,
                        "auto",
                        None,
                        "direct_retry",
                        "direct path closed; retrying transport selection".to_string(),
                        None,
                    );
                }
                continue 'selection;
            }
            Err(err) => {
                warn!(
                    "direct auto-forward for {}:{} failed, falling back to relay: {}",
                    target_device_id, service_name, err
                );
                if let Some(runtime_state) = &runtime_state {
                    runtime_state.note_forward_observation(
                        &rule_name,
                        "auto",
                        Some("relay"),
                        "relay_fallback",
                        format!(
                            "direct failed; relay fallback is active on {} and will retry direct in {}s",
                            local_bind_addr,
                            direct_retry_delay.as_secs()
                        ),
                        None,
                    );
                    runtime_state.note_forward_observation(
                        &rule_name,
                        "auto",
                        Some("relay"),
                        "relay_active",
                        format!(
                            "listening on {} via relay; retrying direct in {}s",
                            local_bind_addr,
                            direct_retry_delay.as_secs()
                        ),
                        None,
                    );
                }
            }
        }

        info!(
            "auto-forward selected relay transport for {}:{} on {}",
            target_device_id, service_name, local_bind_addr
        );
        let relay_tracker = RelayForwarderTracker::new();
        let relay = match RelayConnection::connect(&server_url, &session_token).await {
            Ok((relay, _)) => relay,
            Err(err) => {
                warn!(
                    "auto-forward failed to establish relay websocket for {}:{}: {}",
                    target_device_id, service_name, err
                );
                if let Some(runtime_state) = &runtime_state {
                    runtime_state.note_forward_observation(
                        &rule_name,
                        "auto",
                        None,
                        "relay_retry",
                        format!(
                            "failed to establish relay fallback; retrying in {}s",
                            selection_retry_delay.as_secs()
                        ),
                        Some(err.to_string()),
                    );
                }
                time::sleep(selection_retry_delay).await;
                selection_retry_delay = next_retry_delay(selection_retry_delay);
                continue;
            }
        };
        let _ = ingress_tx.send(Some(AutoForwardIngress::Relay(AutoRelayIngress {
            relay: relay.clone(),
            resolved_service: resolved_service.clone(),
            runtime_hook: relay_runtime_hook.clone(),
            tracker: relay_tracker.clone(),
        })));
        let mut relay_retry_wait = direct_retry_delay;

        loop {
            let relay_closed = tokio::select! {
                _ = relay.wait_closed() => true,
                _ = time::sleep(relay_retry_wait) => false,
            };

            if relay_closed {
                let _ = ingress_tx.send(None);
                warn!(
                    "relay auto-forward for {}:{} closed, retrying selection",
                    target_device_id, service_name
                );
                if let Some(runtime_state) = &runtime_state {
                    runtime_state.note_forward_observation(
                        &rule_name,
                        "auto",
                        Some("relay"),
                        "relay_retry",
                        format!(
                            "relay forwarder stopped; retrying in {}s",
                            selection_retry_delay.as_secs()
                        ),
                        Some(format!(
                            "relay websocket closed for local forward {}",
                            local_bind_addr
                        )),
                    );
                }
                time::sleep(selection_retry_delay).await;
                selection_retry_delay = next_retry_delay(selection_retry_delay);
                continue 'selection;
            }

            let active_connections = relay_tracker.active_connections();
            let idle_for_secs = relay_tracker.idle_for_secs();
            let prewarm_start_detail = if active_connections > 0 {
                info!(
                    "auto-forward relay path for {}:{} still has {} active connection(s); prewarming direct in parallel",
                    target_device_id, service_name, active_connections
                );
                format!(
                    "relay still has {} active connection(s); prewarming direct rendezvous in parallel via {}",
                    active_connections, direct_udp_bind_addr
                )
            } else {
                let idle_grace_secs = AUTO_DIRECT_RETRY_IDLE_GRACE.as_secs();
                if idle_for_secs < idle_grace_secs {
                    let remaining_idle_secs = idle_grace_secs - idle_for_secs;
                    let defer_secs = remaining_idle_secs
                        .min(AUTO_DIRECT_RETRY_BUSY_POLL_INTERVAL.as_secs().max(1));
                    if let Some(runtime_state) = &runtime_state {
                        runtime_state.note_forward_observation(
                            &rule_name,
                            "auto",
                            Some("relay"),
                            "direct_retry_deferred",
                            format!(
                                "relay has been idle for {}s; waiting {}s more before retrying direct",
                                idle_for_secs, defer_secs
                            ),
                            None,
                        );
                    }
                    relay_retry_wait = Duration::from_secs(defer_secs.max(1));
                    continue;
                }

                info!(
                    "auto-forward relay path for {}:{} is idle; prewarming direct handoff",
                    target_device_id, service_name
                );
                format!(
                    "relay has been idle for {}s; prewarming direct rendezvous via {}",
                    idle_for_secs, direct_udp_bind_addr
                )
            };
            let prepared = match prepare_auto_direct_forward(
                &client,
                &session_token,
                &device_identity,
                &rule_name,
                &target_device_id,
                &service_name,
                &direct_udp_bind_addr,
                &direct_candidate_type,
                direct_wait_timeout,
                runtime_state.as_ref(),
                Some("relay"),
                prewarm_start_detail,
                "prewarming direct path",
            )
            .await
            {
                Ok(prepared) => prepared,
                Err(err) => {
                    warn!(
                        "direct prewarm for {}:{} failed while relay stayed active: {}",
                        target_device_id, service_name, err
                    );
                    direct_retry_delay = next_auto_direct_retry_delay(direct_retry_delay);
                    if let Some(runtime_state) = &runtime_state {
                        runtime_state.note_forward_observation(
                            &rule_name,
                            "auto",
                            Some("relay"),
                            "relay_active",
                            format!(
                                "direct prewarm failed; keeping relay active on {} and retrying direct in {}s",
                                local_bind_addr,
                                direct_retry_delay.as_secs()
                            ),
                            None,
                        );
                    }
                    relay_retry_wait = direct_retry_delay;
                    continue;
                }
            };

            let draining_relay_connections = relay_tracker.active_connections();
            if let Some(runtime_state) = &runtime_state {
                let direct_ready_detail = if draining_relay_connections > 0 {
                    format!(
                        "direct path is ready; switching ingress on {} to direct while draining {} relay connection(s)",
                        local_bind_addr, draining_relay_connections
                    )
                } else {
                    format!(
                        "direct path is ready; switching ingress on {} from relay to direct",
                        local_bind_addr
                    )
                };
                runtime_state.note_forward_observation(
                    &rule_name,
                    "auto",
                    Some("relay"),
                    "direct_ready",
                    direct_ready_detail,
                    None,
                );
            }

            let DirectForwardIngress {
                rendezvous_id,
                connection,
                peer_device_id,
            } = into_direct_forward_ingress(prepared);
            let handoff_relay_fallback = if draining_relay_connections > 0 {
                Some(AutoRelayHandoffFallback::new(
                    relay.clone(),
                    relay_runtime_hook.clone(),
                    relay_tracker.clone(),
                ))
            } else {
                None
            };
            let _ = ingress_tx.send(Some(AutoForwardIngress::Direct(AutoDirectIngress {
                connection: connection.clone(),
                resolved_service: resolved_service.clone(),
                runtime_hook: direct_runtime_hook.clone(),
                handoff_relay_fallback: handoff_relay_fallback.clone(),
            })));
            if let Some(runtime_hook) = &direct_runtime_hook {
                runtime_hook.note_state(
                    "direct_active",
                    format!(
                        "direct active on {} via rendezvous {}",
                        local_bind_addr, rendezvous_id
                    ),
                );
            }
            if draining_relay_connections > 0 {
                info!(
                    "auto-forward switched ingress to direct for {}:{} while draining {} relay connection(s)",
                    target_device_id, service_name, draining_relay_connections
                );
                spawn_relay_drain_task(
                    handoff_relay_fallback
                        .expect("handoff fallback exists while draining relay connections"),
                    direct_runtime_hook.clone(),
                    relay_tracker.clone(),
                    rule_name.clone(),
                    target_device_id.clone(),
                    service_name.clone(),
                    local_bind_addr.clone(),
                );
            }
            drop(relay);
            direct_retry_delay = AUTO_DIRECT_RETRY_INITIAL_BACKOFF;
            selection_retry_delay = RUNTIME_RETRY_INITIAL_BACKOFF;
            info!(
                "auto-forward switched ingress to direct for {}:{} on {} via {} ({})",
                target_device_id, service_name, local_bind_addr, rendezvous_id, peer_device_id
            );

            connection.wait_closed().await;
            let _ = ingress_tx.send(None);
            if let Some(runtime_state) = &runtime_state {
                runtime_state.note_forward_failure(
                    &rule_name,
                    "direct",
                    "data_plane",
                    format!(
                        "direct UDP connection closed for local forward {}",
                        local_bind_addr
                    ),
                );
                runtime_state.note_forward_observation(
                    &rule_name,
                    "auto",
                    None,
                    "direct_retry",
                    "direct handoff path closed; retrying transport selection".to_string(),
                    None,
                );
            }
            continue 'selection;
        }
    }
}

fn next_retry_delay(current: Duration) -> Duration {
    std::cmp::min(current.saturating_mul(2), RUNTIME_RETRY_MAX_BACKOFF)
}

fn next_auto_direct_retry_delay(current: Duration) -> Duration {
    std::cmp::min(current.saturating_mul(2), AUTO_DIRECT_RETRY_MAX_BACKOFF)
}

async fn wait_for_restart_or_shutdown(
    delay: Duration,
    shutdown_rx: &mut watch::Receiver<bool>,
) -> bool {
    if shutdown_requested(shutdown_rx) {
        return true;
    }
    tokio::select! {
        changed = shutdown_rx.changed() => changed.is_err() || shutdown_requested(shutdown_rx),
        _ = time::sleep(delay) => false,
    }
}

fn shutdown_requested(shutdown_rx: &watch::Receiver<bool>) -> bool {
    *shutdown_rx.borrow()
}

async fn drain_runtime_tasks(tasks: &mut JoinSet<Result<()>>) {
    tasks.abort_all();
    while let Some(joined) = tasks.join_next().await {
        if let Err(err) = joined {
            if err.is_cancelled() {
                continue;
            }
            warn!("agent background task aborted with join error: {err}");
        }
    }
}

fn validate_runtime_config(config: &AgentConfig) -> Result<()> {
    for service in &config.published_services {
        validate_published_service_config(service)?;
    }
    for rule in &config.forward_rules {
        validate_forward_rule(
            &rule.name,
            &rule.target_device_id,
            &rule.service_name,
            &rule.local_bind_addr,
            &rule.transport_mode,
            &rule.direct_udp_bind_addr,
            &rule.direct_candidate_type,
            rule.direct_wait_seconds,
        )?;
    }
    Ok(())
}

fn validate_published_service_config(service: &PublishedServiceConfig) -> Result<()> {
    if service.name.trim().is_empty() {
        return Err(anyhow!("published service name cannot be empty"));
    }
    if service.target_host.trim().is_empty() {
        return Err(anyhow!(
            "published service {} target_host cannot be empty",
            service.name
        ));
    }
    if service.target_port == 0 {
        return Err(anyhow!(
            "published service {} target_port must be greater than 0",
            service.name
        ));
    }
    if service.direct_enabled {
        if service.direct_udp_bind_addr.trim().is_empty() {
            return Err(anyhow!(
                "published service {} enables direct transport but direct_udp_bind_addr is empty",
                service.name
            ));
        }
        if service.direct_wait_seconds == 0 {
            return Err(anyhow!(
                "published service {} direct_wait_seconds must be greater than 0",
                service.name
            ));
        }
    }
    Ok(())
}

fn validate_forward_rule(
    name: &str,
    target_device_id: &str,
    service_name: &str,
    local_bind_addr: &str,
    transport_mode: &str,
    direct_udp_bind_addr: &str,
    direct_candidate_type: &str,
    direct_wait_seconds: u64,
) -> Result<()> {
    if name.trim().is_empty() {
        return Err(anyhow!("forward rule name cannot be empty"));
    }
    if target_device_id.trim().is_empty() {
        return Err(anyhow!("target_device_id cannot be empty"));
    }
    if service_name.trim().is_empty() {
        return Err(anyhow!("service_name cannot be empty"));
    }
    if local_bind_addr.trim().is_empty() {
        return Err(anyhow!("local_bind_addr cannot be empty"));
    }
    let transport_mode = normalize_forward_transport_mode(transport_mode)?;
    if forward_transport_is_auto(&transport_mode) {
        if direct_udp_bind_addr.trim().is_empty() {
            return Err(anyhow!(
                "forward rule {} uses auto transport but direct_udp_bind_addr is empty",
                name
            ));
        }
        if direct_candidate_type.trim().is_empty() {
            return Err(anyhow!(
                "forward rule {} uses auto transport but direct_candidate_type is empty",
                name
            ));
        }
        if direct_wait_seconds == 0 {
            return Err(anyhow!(
                "forward rule {} uses auto transport but direct_wait_seconds is 0",
                name
            ));
        }
    }
    Ok(())
}

fn normalize_forward_transport_mode(mode: &str) -> Result<String> {
    let normalized = if mode.trim().is_empty() {
        DEFAULT_FORWARD_TRANSPORT.to_string()
    } else {
        mode.trim().to_ascii_lowercase()
    };
    match normalized.as_str() {
        "relay" | "auto" => Ok(normalized),
        _ => Err(anyhow!(
            "unsupported transport_mode {}; expected relay or auto",
            mode
        )),
    }
}

fn forward_transport_is_auto(mode: &str) -> bool {
    mode.trim().eq_ignore_ascii_case("auto")
}

fn normalize_direct_candidate_type(candidate_type: &str) -> String {
    if candidate_type.trim().is_empty() {
        DEFAULT_DIRECT_CANDIDATE_TYPE.to_string()
    } else {
        candidate_type.trim().to_string()
    }
}
