use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use anyhow::{Context, Result};
use minipunch_core::unix_timestamp_now;
use serde::{Deserialize, Serialize};
use tracing::warn;

use crate::config::AgentConfig;

const MAX_RECENT_EVENTS: usize = 24;
pub const RUNTIME_STATE_STALE_AFTER_SECS: i64 = 75;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RuntimeEventRecord {
    pub timestamp: i64,
    pub level: String,
    pub message: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ForwardRuntimeObservation {
    pub name: String,
    pub configured_transport: String,
    pub active_transport: Option<String>,
    pub state: String,
    pub detail: String,
    pub updated_at: i64,
    pub last_error: Option<String>,
    #[serde(default)]
    pub direct_attempt_count: u64,
    #[serde(default)]
    pub relay_fallback_count: u64,
    #[serde(default)]
    pub direct_connection_count: u64,
    #[serde(default)]
    pub relay_connection_count: u64,
    #[serde(default)]
    pub active_connection_count: u64,
    #[serde(default)]
    pub last_transport_switch_at: Option<i64>,
    #[serde(default)]
    pub last_peer: Option<String>,
    #[serde(default)]
    pub last_connection_opened_at: Option<i64>,
    #[serde(default)]
    pub last_connection_closed_at: Option<i64>,
    #[serde(default)]
    pub last_failure_transport: Option<String>,
    #[serde(default)]
    pub last_failure_stage: Option<String>,
    #[serde(default)]
    pub last_failure_error: Option<String>,
    #[serde(default)]
    pub last_failure_at: Option<i64>,
    #[serde(default)]
    pub direct_metrics: Option<DirectTransportMetrics>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PublishedServiceRuntimeObservation {
    pub name: String,
    pub direct_enabled: bool,
    pub active_transport: Option<String>,
    pub state: String,
    pub detail: String,
    pub updated_at: i64,
    pub last_error: Option<String>,
    #[serde(default)]
    pub direct_session_count: u64,
    #[serde(default)]
    pub active_session_count: u64,
    #[serde(default)]
    pub direct_connection_count: u64,
    #[serde(default)]
    pub relay_connection_count: u64,
    #[serde(default)]
    pub last_transport_switch_at: Option<i64>,
    #[serde(default)]
    pub last_peer: Option<String>,
    #[serde(default)]
    pub last_failure_transport: Option<String>,
    #[serde(default)]
    pub last_failure_stage: Option<String>,
    #[serde(default)]
    pub last_failure_error: Option<String>,
    #[serde(default)]
    pub last_failure_at: Option<i64>,
    #[serde(default)]
    pub direct_metrics: Option<DirectTransportMetrics>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct DirectTransportMetrics {
    pub window_packets: u32,
    pub ssthresh_packets: u32,
    pub rto_ms: u64,
    #[serde(default)]
    pub smoothed_rtt_ms: Option<u64>,
    pub pending_outbound_packets: u32,
    pub pending_inbound_packets: u32,
    pub fast_recovery: bool,
    #[serde(default)]
    pub keepalive_sent_count: u64,
    #[serde(default)]
    pub keepalive_ack_count: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RuntimeStateSnapshot {
    pub pid: u32,
    pub config_path: String,
    pub device_name: String,
    pub device_id: Option<String>,
    pub status: String,
    pub status_detail: String,
    pub started_at: i64,
    pub updated_at: i64,
    pub session_expires_at: Option<i64>,
    pub last_prepare_ok_at: Option<i64>,
    pub last_heartbeat_ok_at: Option<i64>,
    pub last_error: Option<String>,
    pub last_restart_reason: Option<String>,
    pub restart_count: u64,
    pub enabled_forward_rules: Vec<String>,
    pub published_services: Vec<String>,
    #[serde(default)]
    pub forward_observations: Vec<ForwardRuntimeObservation>,
    #[serde(default)]
    pub published_service_observations: Vec<PublishedServiceRuntimeObservation>,
    pub recent_events: Vec<RuntimeEventRecord>,
}

impl RuntimeStateSnapshot {
    pub fn observed_state(&self) -> String {
        if runtime_state_is_stale(self) {
            "stale".to_string()
        } else {
            self.status.clone()
        }
    }
}

pub fn runtime_state_is_stale(snapshot: &RuntimeStateSnapshot) -> bool {
    !matches!(snapshot.status.as_str(), "stopped" | "failed")
        && snapshot.updated_at + RUNTIME_STATE_STALE_AFTER_SECS < unix_timestamp_now()
}

pub fn runtime_state_path_for_config(config_path: &Path) -> PathBuf {
    let file_stem = config_path
        .file_stem()
        .and_then(|value| value.to_str())
        .filter(|value| !value.trim().is_empty())
        .unwrap_or("agent");
    let file_name = format!("{file_stem}.runtime.json");
    match config_path.parent() {
        Some(parent) => parent.join(file_name),
        None => PathBuf::from(file_name),
    }
}

pub fn load_runtime_state_for_config(config_path: &Path) -> Result<RuntimeStateSnapshot> {
    let path = runtime_state_path_for_config(config_path);
    let raw = fs::read_to_string(&path)
        .with_context(|| format!("failed to read runtime state {}", path.display()))?;
    serde_json::from_str(&raw)
        .with_context(|| format!("failed to parse runtime state {}", path.display()))
}

#[derive(Clone)]
pub(crate) struct RuntimeStateWriter {
    path: PathBuf,
    snapshot: Arc<Mutex<RuntimeStateSnapshot>>,
}

impl RuntimeStateWriter {
    pub(crate) fn new(config_path: &Path, config: &AgentConfig) -> Self {
        let now = unix_timestamp_now();
        let writer = Self {
            path: runtime_state_path_for_config(config_path),
            snapshot: Arc::new(Mutex::new(RuntimeStateSnapshot {
                pid: std::process::id(),
                config_path: config_path.display().to_string(),
                device_name: config.device_name.clone(),
                device_id: config.device_id.clone(),
                status: "starting".to_string(),
                status_detail: String::new(),
                started_at: now,
                updated_at: now,
                session_expires_at: config.session_expires_at,
                last_prepare_ok_at: None,
                last_heartbeat_ok_at: None,
                last_error: None,
                last_restart_reason: None,
                restart_count: 0,
                enabled_forward_rules: Vec::new(),
                published_services: Vec::new(),
                forward_observations: Vec::new(),
                published_service_observations: Vec::new(),
                recent_events: Vec::new(),
            })),
        };
        writer.sync_config(config);
        writer.mark_starting(config, "runtime booted and is preparing background tasks");
        writer
    }

    pub(crate) fn mark_starting(&self, config: &AgentConfig, detail: impl Into<String>) {
        self.sync_config(config);
        let detail = detail.into();
        self.update_snapshot(|snapshot| {
            clear_all_direct_metrics(snapshot);
            snapshot.status = "starting".to_string();
            snapshot.status_detail = detail.clone();
            push_event(snapshot, "info", detail);
        });
    }

    pub(crate) fn mark_prepare_failed(
        &self,
        config: &AgentConfig,
        err: &str,
        retry_delay: Duration,
    ) {
        self.sync_config(config);
        let detail = format!(
            "runtime preparation failed; retrying in {}s",
            retry_delay.as_secs()
        );
        self.update_snapshot(|snapshot| {
            snapshot.status = "retrying".to_string();
            snapshot.status_detail = format!("{detail}: {err}");
            snapshot.last_error = Some(err.to_string());
            push_event(snapshot, "error", format!("{detail}: {err}"));
        });
    }

    pub(crate) fn mark_running(&self, config: &AgentConfig) {
        self.sync_config(config);
        let now = unix_timestamp_now();
        let detail = format!(
            "relay service and {} enabled forward rules are active",
            self.snapshot
                .lock()
                .expect("runtime snapshot lock")
                .enabled_forward_rules
                .len()
        );
        self.update_snapshot(|snapshot| {
            snapshot.status = "running".to_string();
            snapshot.status_detail = detail.clone();
            snapshot.last_prepare_ok_at = Some(now);
            snapshot.last_error = None;
            snapshot.updated_at = now;
            push_event(snapshot, "info", detail);
        });
    }

    pub(crate) fn note_heartbeat_ok(&self) {
        let now = unix_timestamp_now();
        self.update_snapshot(|snapshot| {
            snapshot.last_heartbeat_ok_at = Some(now);
            snapshot.updated_at = now;
        });
    }

    pub(crate) fn mark_restarting(
        &self,
        config: &AgentConfig,
        reason: &str,
        retry_delay: Duration,
    ) {
        self.sync_config(config);
        self.update_snapshot(|snapshot| {
            clear_all_direct_metrics(snapshot);
            snapshot.restart_count += 1;
            snapshot.last_restart_reason = Some(reason.to_string());
            snapshot.last_error = Some(reason.to_string());
            snapshot.status = "restarting".to_string();
            snapshot.status_detail =
                format!("runtime restarting in {}s: {reason}", retry_delay.as_secs());
            push_event(
                snapshot,
                "warn",
                format!("runtime restarting in {}s: {reason}", retry_delay.as_secs()),
            );
        });
    }

    pub(crate) fn mark_stopped(&self, config: &AgentConfig, detail: impl Into<String>) {
        self.sync_config(config);
        let detail = detail.into();
        self.update_snapshot(|snapshot| {
            clear_all_direct_metrics(snapshot);
            snapshot.status = "stopped".to_string();
            snapshot.status_detail = detail.clone();
            push_event(snapshot, "info", detail);
        });
    }

    pub(crate) fn note_forward_observation(
        &self,
        name: &str,
        configured_transport: &str,
        active_transport: Option<&str>,
        state: &str,
        detail: impl Into<String>,
        last_error: Option<String>,
    ) {
        let detail = detail.into();
        self.update_snapshot(|snapshot| {
            let now = unix_timestamp_now();
            let observation = snapshot
                .forward_observations
                .iter_mut()
                .find(|observation| observation.name == name);
            let Some(observation) = observation else {
                return;
            };

            let active_transport = active_transport.map(|value| value.to_string());
            let changed = observation.configured_transport != configured_transport
                || observation.active_transport != active_transport
                || observation.state != state
                || observation.detail != detail
                || observation.last_error != last_error;
            if !changed {
                observation.updated_at = now;
                snapshot.updated_at = now;
                return;
            }

            if state == "direct_connecting" && observation.state != "direct_connecting" {
                observation.direct_attempt_count += 1;
            }
            if state == "relay_fallback" && observation.state != "relay_fallback" {
                observation.relay_fallback_count += 1;
            }
            if observation.active_transport != active_transport {
                observation.last_transport_switch_at = Some(now);
            }
            observation.configured_transport = configured_transport.to_string();
            observation.active_transport = active_transport.clone();
            if active_transport.as_deref() != Some("direct") {
                observation.direct_metrics = None;
            }
            observation.state = state.to_string();
            observation.detail = detail.clone();
            observation.updated_at = now;
            observation.last_error = last_error.clone();
            snapshot.updated_at = now;
            let transport_label = active_transport.unwrap_or_else(|| "none".to_string());
            let message = if let Some(last_error) = last_error {
                format!(
                    "forward {} state={} configured={} active={} detail={} error={}",
                    name, state, configured_transport, transport_label, detail, last_error
                )
            } else {
                format!(
                    "forward {} state={} configured={} active={} detail={}",
                    name, state, configured_transport, transport_label, detail
                )
            };
            push_event(snapshot, "info", message);
        });
    }

    pub(crate) fn note_forward_connection(
        &self,
        name: &str,
        transport: &str,
        peer: impl Into<String>,
    ) {
        let peer = peer.into();
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .forward_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            let now = unix_timestamp_now();
            match transport {
                "direct" => observation.direct_connection_count += 1,
                "relay" => observation.relay_connection_count += 1,
                _ => {}
            }
            observation.active_connection_count += 1;
            observation.last_peer = Some(peer.clone());
            observation.last_connection_opened_at = Some(now);
            observation.updated_at = now;
            snapshot.updated_at = now;
            push_event(
                snapshot,
                "info",
                format!(
                    "forward {} accepted {} connection from {}",
                    name, transport, peer
                ),
            );
        });
    }

    pub(crate) fn note_forward_connection_closed(&self, name: &str, peer: impl Into<String>) {
        let peer = peer.into();
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .forward_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            let now = unix_timestamp_now();
            observation.active_connection_count =
                observation.active_connection_count.saturating_sub(1);
            observation.last_peer = Some(peer);
            observation.last_connection_closed_at = Some(now);
            observation.updated_at = now;
            snapshot.updated_at = now;
        });
    }

    pub(crate) fn note_forward_failure(
        &self,
        name: &str,
        transport: &str,
        stage: &str,
        error: impl Into<String>,
    ) {
        let error = error.into();
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .forward_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            let now = unix_timestamp_now();
            observation.last_error = Some(error.clone());
            observation.last_failure_transport = Some(transport.to_string());
            observation.last_failure_stage = Some(stage.to_string());
            observation.last_failure_error = Some(error.clone());
            observation.last_failure_at = Some(now);
            observation.updated_at = now;
            snapshot.updated_at = now;
            push_event(
                snapshot,
                "warn",
                format!(
                    "forward {} {} failure at {}: {}",
                    name, transport, stage, error
                ),
            );
        });
    }

    pub(crate) fn note_forward_direct_metrics(&self, name: &str, metrics: DirectTransportMetrics) {
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .forward_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            if observation.active_transport.as_deref() != Some("direct")
                || observation.direct_metrics.as_ref() == Some(&metrics)
            {
                return;
            }
            let now = unix_timestamp_now();
            observation.direct_metrics = Some(metrics);
            observation.updated_at = now;
            snapshot.updated_at = now;
        });
    }

    pub(crate) fn clear_forward_direct_metrics(&self, name: &str) {
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .forward_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            if observation.direct_metrics.is_none() {
                return;
            }
            let now = unix_timestamp_now();
            observation.direct_metrics = None;
            observation.updated_at = now;
            snapshot.updated_at = now;
        });
    }

    pub(crate) fn note_published_service_observation(
        &self,
        name: &str,
        direct_enabled: bool,
        active_transport: Option<&str>,
        state: &str,
        detail: impl Into<String>,
        last_error: Option<String>,
    ) {
        let detail = detail.into();
        self.update_snapshot(|snapshot| {
            let now = unix_timestamp_now();
            let observation = snapshot
                .published_service_observations
                .iter_mut()
                .find(|observation| observation.name == name);
            let Some(observation) = observation else {
                return;
            };

            let active_transport = active_transport.map(|value| value.to_string());
            let changed = observation.direct_enabled != direct_enabled
                || observation.active_transport != active_transport
                || observation.state != state
                || observation.detail != detail
                || observation.last_error != last_error;
            if !changed {
                observation.updated_at = now;
                snapshot.updated_at = now;
                return;
            }

            if observation.active_transport != active_transport {
                observation.last_transport_switch_at = Some(now);
            }
            observation.direct_enabled = direct_enabled;
            observation.active_transport = active_transport.clone();
            if active_transport.as_deref() != Some("direct") {
                observation.direct_metrics = None;
            }
            observation.state = state.to_string();
            observation.detail = detail.clone();
            observation.updated_at = now;
            observation.last_error = last_error.clone();
            snapshot.updated_at = now;
            let transport_label = active_transport.unwrap_or_else(|| "none".to_string());
            let message = if let Some(last_error) = last_error {
                format!(
                    "service {} state={} direct_enabled={} active={} detail={} error={}",
                    name, state, direct_enabled, transport_label, detail, last_error
                )
            } else {
                format!(
                    "service {} state={} direct_enabled={} active={} detail={}",
                    name, state, direct_enabled, transport_label, detail
                )
            };
            push_event(snapshot, "info", message);
        });
    }

    pub(crate) fn note_published_service_connection(
        &self,
        name: &str,
        transport: &str,
        peer: impl Into<String>,
    ) {
        let peer = peer.into();
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .published_service_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            let now = unix_timestamp_now();
            match transport {
                "direct" => observation.direct_connection_count += 1,
                "relay" => observation.relay_connection_count += 1,
                _ => {}
            }
            observation.last_peer = Some(peer.clone());
            observation.updated_at = now;
            snapshot.updated_at = now;
            push_event(
                snapshot,
                "info",
                format!(
                    "service {} accepted {} connection from {}",
                    name, transport, peer
                ),
            );
        });
    }

    pub(crate) fn note_published_service_session_started(&self, name: &str) {
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .published_service_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            let now = unix_timestamp_now();
            observation.direct_session_count += 1;
            observation.active_session_count += 1;
            observation.updated_at = now;
            snapshot.updated_at = now;
        });
    }

    pub(crate) fn note_published_service_session_ended(&self, name: &str) {
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .published_service_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            let now = unix_timestamp_now();
            observation.active_session_count = observation.active_session_count.saturating_sub(1);
            observation.updated_at = now;
            snapshot.updated_at = now;
        });
    }

    pub(crate) fn note_published_service_failure(
        &self,
        name: &str,
        transport: &str,
        stage: &str,
        error: impl Into<String>,
    ) {
        let error = error.into();
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .published_service_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            let now = unix_timestamp_now();
            observation.last_error = Some(error.clone());
            observation.last_failure_transport = Some(transport.to_string());
            observation.last_failure_stage = Some(stage.to_string());
            observation.last_failure_error = Some(error.clone());
            observation.last_failure_at = Some(now);
            observation.updated_at = now;
            snapshot.updated_at = now;
            push_event(
                snapshot,
                "warn",
                format!(
                    "service {} {} failure at {}: {}",
                    name, transport, stage, error
                ),
            );
        });
    }

    pub(crate) fn note_published_service_direct_metrics(
        &self,
        name: &str,
        metrics: DirectTransportMetrics,
    ) {
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .published_service_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            if observation.active_transport.as_deref() != Some("direct")
                || observation.direct_metrics.as_ref() == Some(&metrics)
            {
                return;
            }
            let now = unix_timestamp_now();
            observation.direct_metrics = Some(metrics);
            observation.updated_at = now;
            snapshot.updated_at = now;
        });
    }

    pub(crate) fn clear_published_service_direct_metrics(&self, name: &str) {
        self.update_snapshot(|snapshot| {
            let Some(observation) = snapshot
                .published_service_observations
                .iter_mut()
                .find(|observation| observation.name == name)
            else {
                return;
            };
            if observation.direct_metrics.is_none() {
                return;
            }
            let now = unix_timestamp_now();
            observation.direct_metrics = None;
            observation.updated_at = now;
            snapshot.updated_at = now;
        });
    }

    fn sync_config(&self, config: &AgentConfig) {
        self.update_snapshot(|snapshot| {
            snapshot.device_name = config.device_name.clone();
            snapshot.device_id = config.device_id.clone();
            snapshot.session_expires_at = config.session_expires_at;
            snapshot.enabled_forward_rules = config
                .forward_rules
                .iter()
                .filter(|rule| rule.enabled)
                .map(|rule| {
                    if rule.transport_mode.trim().eq_ignore_ascii_case("auto") {
                        format!("{} [auto]", rule.name)
                    } else {
                        rule.name.clone()
                    }
                })
                .collect();
            snapshot.published_services = config
                .published_services
                .iter()
                .map(|service| {
                    if service.direct_enabled {
                        format!("{} [direct]", service.name)
                    } else {
                        service.name.clone()
                    }
                })
                .collect();
            snapshot.forward_observations =
                build_forward_observations(&snapshot.forward_observations, config);
            snapshot.published_service_observations = build_published_service_observations(
                &snapshot.published_service_observations,
                config,
            );
            snapshot.updated_at = unix_timestamp_now();
        });
    }

    fn persist(&self) {
        if let Err(err) = self.persist_inner() {
            warn!(
                "failed to persist runtime state {}: {err}",
                self.path.display()
            );
        }
    }

    fn persist_inner(&self) -> Result<()> {
        let snapshot = self.snapshot.lock().expect("runtime snapshot lock").clone();
        if let Some(parent) = self.path.parent() {
            fs::create_dir_all(parent)
                .with_context(|| format!("failed to create {}", parent.display()))?;
        }
        let encoded =
            serde_json::to_string_pretty(&snapshot).context("failed to encode runtime state")?;
        fs::write(&self.path, encoded)
            .with_context(|| format!("failed to write {}", self.path.display()))
    }

    fn update_snapshot(&self, update: impl FnOnce(&mut RuntimeStateSnapshot)) {
        {
            let mut snapshot = self.snapshot.lock().expect("runtime snapshot lock");
            update(&mut snapshot);
        }
        self.persist();
    }
}

fn push_event(snapshot: &mut RuntimeStateSnapshot, level: &str, message: String) {
    snapshot.recent_events.push(RuntimeEventRecord {
        timestamp: unix_timestamp_now(),
        level: level.to_string(),
        message,
    });
    if snapshot.recent_events.len() > MAX_RECENT_EVENTS {
        let excess = snapshot.recent_events.len() - MAX_RECENT_EVENTS;
        snapshot.recent_events.drain(0..excess);
    }
}

fn clear_all_direct_metrics(snapshot: &mut RuntimeStateSnapshot) {
    for observation in &mut snapshot.forward_observations {
        observation.direct_metrics = None;
    }
    for observation in &mut snapshot.published_service_observations {
        observation.direct_metrics = None;
    }
}

fn build_forward_observations(
    previous: &[ForwardRuntimeObservation],
    config: &AgentConfig,
) -> Vec<ForwardRuntimeObservation> {
    config
        .forward_rules
        .iter()
        .filter(|rule| rule.enabled)
        .map(|rule| {
            if let Some(existing) = previous
                .iter()
                .find(|observation| observation.name == rule.name)
            {
                let mut next = existing.clone();
                next.configured_transport = rule.transport_mode.clone();
                next
            } else {
                ForwardRuntimeObservation {
                    name: rule.name.clone(),
                    configured_transport: rule.transport_mode.clone(),
                    active_transport: None,
                    state: "configured".to_string(),
                    detail: if rule.transport_mode.trim().eq_ignore_ascii_case("auto") {
                        "waiting for direct attempt or relay fallback".to_string()
                    } else {
                        "waiting for relay forwarder".to_string()
                    },
                    updated_at: unix_timestamp_now(),
                    last_error: None,
                    direct_attempt_count: 0,
                    relay_fallback_count: 0,
                    direct_connection_count: 0,
                    relay_connection_count: 0,
                    active_connection_count: 0,
                    last_transport_switch_at: None,
                    last_peer: None,
                    last_connection_opened_at: None,
                    last_connection_closed_at: None,
                    last_failure_transport: None,
                    last_failure_stage: None,
                    last_failure_error: None,
                    last_failure_at: None,
                    direct_metrics: None,
                }
            }
        })
        .collect()
}

fn build_published_service_observations(
    previous: &[PublishedServiceRuntimeObservation],
    config: &AgentConfig,
) -> Vec<PublishedServiceRuntimeObservation> {
    config
        .published_services
        .iter()
        .map(|service| {
            if let Some(existing) = previous
                .iter()
                .find(|observation| observation.name == service.name)
            {
                let mut next = existing.clone();
                next.direct_enabled = service.direct_enabled;
                next
            } else {
                PublishedServiceRuntimeObservation {
                    name: service.name.clone(),
                    direct_enabled: service.direct_enabled,
                    active_transport: None,
                    state: if service.direct_enabled {
                        "waiting_direct".to_string()
                    } else {
                        "relay_only".to_string()
                    },
                    detail: if service.direct_enabled {
                        format!(
                            "waiting for direct rendezvous on {}",
                            service.direct_udp_bind_addr
                        )
                    } else {
                        "relay-only published service".to_string()
                    },
                    updated_at: unix_timestamp_now(),
                    last_error: None,
                    direct_session_count: 0,
                    active_session_count: 0,
                    direct_connection_count: 0,
                    relay_connection_count: 0,
                    last_transport_switch_at: None,
                    last_peer: None,
                    last_failure_transport: None,
                    last_failure_stage: None,
                    last_failure_error: None,
                    last_failure_at: None,
                    direct_metrics: None,
                }
            }
        })
        .collect()
}

#[derive(Clone)]
pub(crate) struct ForwardRuntimeHook {
    writer: RuntimeStateWriter,
    rule_name: String,
    configured_transport: String,
    transport: String,
}

impl ForwardRuntimeHook {
    pub(crate) fn new(
        writer: RuntimeStateWriter,
        rule_name: impl Into<String>,
        configured_transport: impl Into<String>,
        transport: impl Into<String>,
    ) -> Self {
        Self {
            writer,
            rule_name: rule_name.into(),
            configured_transport: configured_transport.into(),
            transport: transport.into(),
        }
    }

    pub(crate) fn note_state(&self, state: &str, detail: impl Into<String>) {
        self.writer.note_forward_observation(
            &self.rule_name,
            &self.configured_transport,
            Some(&self.transport),
            state,
            detail,
            None,
        );
    }

    pub(crate) fn note_connection(&self, peer: impl Into<String>) {
        self.writer
            .note_forward_connection(&self.rule_name, &self.transport, peer);
    }

    pub(crate) fn note_connection_closed(&self, peer: impl Into<String>) {
        self.writer
            .note_forward_connection_closed(&self.rule_name, peer);
    }

    pub(crate) fn note_failure(&self, stage: &str, error: impl Into<String>) {
        self.writer
            .note_forward_failure(&self.rule_name, &self.transport, stage, error);
    }

    pub(crate) fn note_direct_metrics(&self, metrics: DirectTransportMetrics) {
        self.writer
            .note_forward_direct_metrics(&self.rule_name, metrics);
    }

    pub(crate) fn clear_direct_metrics(&self) {
        self.writer.clear_forward_direct_metrics(&self.rule_name);
    }
}

#[derive(Clone)]
pub(crate) struct PublishedServiceRuntimeHook {
    writer: RuntimeStateWriter,
    service_name: String,
    direct_enabled: bool,
    transport: String,
}

impl PublishedServiceRuntimeHook {
    pub(crate) fn new(
        writer: RuntimeStateWriter,
        service_name: impl Into<String>,
        direct_enabled: bool,
        transport: impl Into<String>,
    ) -> Self {
        Self {
            writer,
            service_name: service_name.into(),
            direct_enabled,
            transport: transport.into(),
        }
    }

    pub(crate) fn note_state(&self, state: &str, detail: impl Into<String>) {
        self.writer.note_published_service_observation(
            &self.service_name,
            self.direct_enabled,
            Some(&self.transport),
            state,
            detail,
            None,
        );
    }

    pub(crate) fn note_connection(&self, peer: impl Into<String>) {
        self.writer
            .note_published_service_connection(&self.service_name, &self.transport, peer);
    }

    pub(crate) fn note_session_started(&self) {
        self.writer
            .note_published_service_session_started(&self.service_name);
    }

    pub(crate) fn note_session_ended(&self) {
        self.writer
            .note_published_service_session_ended(&self.service_name);
    }

    pub(crate) fn note_failure(&self, stage: &str, error: impl Into<String>) {
        self.writer.note_published_service_failure(
            &self.service_name,
            &self.transport,
            stage,
            error,
        );
    }

    pub(crate) fn note_direct_metrics(&self, metrics: DirectTransportMetrics) {
        self.writer
            .note_published_service_direct_metrics(&self.service_name, metrics);
    }

    pub(crate) fn clear_direct_metrics(&self) {
        self.writer
            .clear_published_service_direct_metrics(&self.service_name);
    }
}
