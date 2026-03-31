use minipunch_core::NetworkSnapshot;
use serde::Serialize;

use crate::config::{AgentConfig, LocalForwardConfig, PublishedServiceConfig};

#[derive(Debug, Clone, Serialize)]
pub struct AgentStatusReport {
    pub device_name: String,
    pub device_id: Option<String>,
    pub session: SessionStatus,
    pub local_device_online: Option<bool>,
    pub network_device_count: usize,
    pub network_service_count: usize,
    pub snapshot_error: Option<String>,
    pub published_services: Vec<PublishedServiceStatus>,
    pub forward_rules: Vec<ForwardRuleStatus>,
}

#[derive(Debug, Clone, Serialize)]
pub struct SessionStatus {
    pub has_token: bool,
    pub expires_at: Option<i64>,
    pub is_valid: bool,
}

#[derive(Debug, Clone, Serialize)]
pub struct PublishedServiceStatus {
    pub name: String,
    pub target_host: String,
    pub target_port: u16,
    pub allowed_device_count: usize,
    pub direct_enabled: bool,
    pub direct_udp_bind_addr: String,
    pub state: String,
    pub detail: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct ForwardRuleStatus {
    pub name: String,
    pub enabled: bool,
    pub target_device_id: String,
    pub target_device_name: Option<String>,
    pub service_name: String,
    pub local_bind_addr: String,
    pub transport_mode: String,
    pub direct_udp_bind_addr: String,
    pub state: String,
    pub detail: String,
}

pub fn status_report_from_config(config: &AgentConfig) -> AgentStatusReport {
    build_status_report(config, None, None)
}

pub fn build_status_report(
    config: &AgentConfig,
    snapshot: Option<&NetworkSnapshot>,
    snapshot_error: Option<String>,
) -> AgentStatusReport {
    let local_device = snapshot.and_then(|snapshot| {
        config.device_id.as_deref().and_then(|device_id| {
            snapshot
                .devices
                .iter()
                .find(|device| device.device_id == device_id)
        })
    });

    let published_services = config
        .published_services
        .iter()
        .map(|service| build_published_service_status(config, snapshot, service))
        .collect();
    let forward_rules = config
        .forward_rules
        .iter()
        .map(|rule| build_forward_rule_status(config, snapshot, rule))
        .collect();

    AgentStatusReport {
        device_name: config.device_name.clone(),
        device_id: config.device_id.clone(),
        session: SessionStatus {
            has_token: config.session_token.is_some(),
            expires_at: config.session_expires_at,
            is_valid: config.has_valid_session(),
        },
        local_device_online: local_device.map(|device| device.is_online),
        network_device_count: snapshot.map(|snapshot| snapshot.devices.len()).unwrap_or(0),
        network_service_count: snapshot
            .map(|snapshot| snapshot.services.len())
            .unwrap_or(0),
        snapshot_error,
        published_services,
        forward_rules,
    }
}

fn build_published_service_status(
    config: &AgentConfig,
    snapshot: Option<&NetworkSnapshot>,
    service: &PublishedServiceConfig,
) -> PublishedServiceStatus {
    let direct_detail = if service.direct_enabled {
        if service.direct_udp_bind_addr.trim().is_empty() {
            return PublishedServiceStatus {
                name: service.name.clone(),
                target_host: service.target_host.clone(),
                target_port: service.target_port,
                allowed_device_count: service.allowed_device_ids.len(),
                direct_enabled: true,
                direct_udp_bind_addr: service.direct_udp_bind_addr.clone(),
                state: "misconfigured".to_string(),
                detail: "direct transport is enabled but direct UDP bind address is empty"
                    .to_string(),
            };
        }
        format!(
            "direct enabled on {} (type={}, wait={}s)",
            service.direct_udp_bind_addr,
            service.direct_candidate_type,
            service.direct_wait_seconds
        )
    } else {
        "relay only".to_string()
    };

    let (state, detail) = match snapshot {
        Some(snapshot) => {
            match config.device_id.as_deref() {
                Some(device_id) => match snapshot.services.iter().find(|remote| {
                    remote.owner_device_id == device_id && remote.name == service.name
                }) {
                    Some(remote) => {
                        let local_online = snapshot
                            .devices
                            .iter()
                            .find(|device| device.device_id == device_id)
                            .map(|device| device.is_online)
                            .unwrap_or(false);
                        if local_online {
                            (
                                "ready".to_string(),
                                format!(
                                    "published on control plane as {} | {}",
                                    remote.service_id, direct_detail
                                ),
                            )
                        } else {
                            (
                                "registered_offline".to_string(),
                                format!(
                                    "service is published, but this device currently looks offline | {}",
                                    direct_detail
                                ),
                            )
                        }
                    }
                    None => (
                        "not_synced".to_string(),
                        format!(
                            "service exists in local config, but is not visible in the latest network snapshot | {}",
                            direct_detail
                        ),
                    ),
                },
                None => (
                    "not_joined".to_string(),
                    format!("device has not joined the network yet | {direct_detail}"),
                ),
            }
        }
        None => (
            "unknown".to_string(),
            format!("status not refreshed from control plane yet | {direct_detail}"),
        ),
    };

    PublishedServiceStatus {
        name: service.name.clone(),
        target_host: service.target_host.clone(),
        target_port: service.target_port,
        allowed_device_count: service.allowed_device_ids.len(),
        direct_enabled: service.direct_enabled,
        direct_udp_bind_addr: service.direct_udp_bind_addr.clone(),
        state,
        detail,
    }
}

fn build_forward_rule_status(
    config: &AgentConfig,
    snapshot: Option<&NetworkSnapshot>,
    rule: &LocalForwardConfig,
) -> ForwardRuleStatus {
    let transport_detail = if rule.transport_mode.trim().eq_ignore_ascii_case("auto") {
        if rule.direct_udp_bind_addr.trim().is_empty() {
            return ForwardRuleStatus {
                name: rule.name.clone(),
                enabled: rule.enabled,
                target_device_id: rule.target_device_id.clone(),
                target_device_name: None,
                service_name: rule.service_name.clone(),
                local_bind_addr: rule.local_bind_addr.clone(),
                transport_mode: rule.transport_mode.clone(),
                direct_udp_bind_addr: rule.direct_udp_bind_addr.clone(),
                state: "misconfigured".to_string(),
                detail: "auto transport requires a direct UDP bind address".to_string(),
            };
        }
        format!(
            "transport=auto via {} (type={}, wait={}s)",
            rule.direct_udp_bind_addr, rule.direct_candidate_type, rule.direct_wait_seconds
        )
    } else {
        "transport=relay".to_string()
    };

    let (state, detail, target_device_name) = if !rule.enabled {
        (
            "disabled".to_string(),
            format!("rule is disabled and will not be started by run mode | {transport_detail}"),
            None,
        )
    } else {
        match snapshot {
            Some(snapshot) => {
                let target_device = snapshot
                    .devices
                    .iter()
                    .find(|device| device.device_id == rule.target_device_id);
                match target_device {
                    Some(target_device) if !target_device.is_online => (
                        "target_offline".to_string(),
                        format!(
                            "target device is known but offline; run mode will keep retrying in the background | {}",
                            transport_detail
                        ),
                        Some(target_device.device_name.clone()),
                    ),
                    Some(target_device) => {
                        let service = snapshot.services.iter().find(|service| {
                            service.owner_device_id == rule.target_device_id
                                && service.name == rule.service_name
                        });
                        match service {
                            Some(service) => (
                                "ready".to_string(),
                                format!(
                                    "{} can forward to {} / {} ({}) | {}",
                                    rule.local_bind_addr,
                                    target_device.device_name,
                                    service.name,
                                    service.service_id,
                                    transport_detail
                                ),
                                Some(target_device.device_name.clone()),
                            ),
                            None => (
                                "service_missing".to_string(),
                                format!(
                                    "target device is online, but this service is not accessible; run mode will keep resolving in the background | {}",
                                    transport_detail
                                ),
                                Some(target_device.device_name.clone()),
                            ),
                        }
                    }
                    None => (
                        if config.device_id.is_none() {
                            "not_joined".to_string()
                        } else {
                            "target_missing".to_string()
                        },
                        format!(
                            "target device is not visible in the latest network snapshot | {}",
                            transport_detail
                        ),
                        None,
                    ),
                }
            }
            None => (
                "unknown".to_string(),
                format!("status not refreshed from control plane yet | {transport_detail}"),
                None,
            ),
        }
    };

    ForwardRuleStatus {
        name: rule.name.clone(),
        enabled: rule.enabled,
        target_device_id: rule.target_device_id.clone(),
        target_device_name,
        service_name: rule.service_name.clone(),
        local_bind_addr: rule.local_bind_addr.clone(),
        transport_mode: rule.transport_mode.clone(),
        direct_udp_bind_addr: rule.direct_udp_bind_addr.clone(),
        state,
        detail,
    }
}
