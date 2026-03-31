use std::path::PathBuf;
use std::time::{Duration, Instant};

use anyhow::{Result, anyhow};
use eframe::egui::{self, Color32, RichText, TextEdit};
use minipunch_agent::AgentRuntime;
use minipunch_agent::config::{
    AgentConfig, DEFAULT_DIRECT_CANDIDATE_TYPE, DEFAULT_DIRECT_WAIT_SECONDS,
    DEFAULT_FORWARD_TRANSPORT, LocalForwardConfig, PublishedServiceConfig,
};
use minipunch_agent::runtime_state::{
    RuntimeStateSnapshot, load_runtime_state_for_config, runtime_state_path_for_config,
};
use minipunch_agent::status::{AgentStatusReport, status_report_from_config};
use tokio::runtime::Runtime;
use tokio::sync::watch;
use tokio::task::JoinHandle;

mod autostart;
mod tray;

use autostart::{AutostartStatus, detect_autostart, disable_autostart, enable_autostart};
use tray::{DesktopTray, TrayCommand};

fn main() -> Result<(), eframe::Error> {
    let launch_args = DesktopLaunchArgs::parse();
    let options = eframe::NativeOptions::default();
    eframe::run_native(
        "MiniPunch Desktop",
        options,
        Box::new(move |cc| {
            configure_theme(&cc.egui_ctx);
            Ok(Box::new(MiniPunchDesktop::new(launch_args.clone())))
        }),
    )
}

#[derive(Clone, Debug, Default)]
struct DesktopLaunchArgs {
    config_path: Option<PathBuf>,
    background: bool,
    start_agent: bool,
}

impl DesktopLaunchArgs {
    fn parse() -> Self {
        let mut args = std::env::args().skip(1);
        let mut launch = Self::default();
        while let Some(arg) = args.next() {
            match arg.as_str() {
                "--config" => {
                    if let Some(value) = args.next() {
                        launch.config_path = Some(PathBuf::from(value));
                    }
                }
                "--background" => launch.background = true,
                "--start-agent" => launch.start_agent = true,
                _ => {}
            }
        }
        launch
    }
}

#[derive(Clone, Debug)]
struct PublishedServiceDraft {
    name: String,
    target_host: String,
    target_port: String,
    allowed_device_ids: String,
    direct_enabled: bool,
    direct_udp_bind_addr: String,
    direct_candidate_type: String,
    direct_wait_seconds: String,
}

impl PublishedServiceDraft {
    fn suggested() -> Self {
        Self {
            name: "ssh".to_string(),
            target_host: "127.0.0.1".to_string(),
            target_port: "22".to_string(),
            allowed_device_ids: String::new(),
            direct_enabled: false,
            direct_udp_bind_addr: String::new(),
            direct_candidate_type: DEFAULT_DIRECT_CANDIDATE_TYPE.to_string(),
            direct_wait_seconds: DEFAULT_DIRECT_WAIT_SECONDS.to_string(),
        }
    }

    fn from_config(service: &PublishedServiceConfig) -> Self {
        Self {
            name: service.name.clone(),
            target_host: service.target_host.clone(),
            target_port: service.target_port.to_string(),
            allowed_device_ids: service.allowed_device_ids.join(","),
            direct_enabled: service.direct_enabled,
            direct_udp_bind_addr: service.direct_udp_bind_addr.clone(),
            direct_candidate_type: service.direct_candidate_type.clone(),
            direct_wait_seconds: service.direct_wait_seconds.to_string(),
        }
    }

    fn to_config(&self, index: usize) -> Result<PublishedServiceConfig> {
        let name = self.name.trim();
        if name.is_empty() {
            return Err(anyhow!(
                "published service #{} name cannot be empty",
                index + 1
            ));
        }

        let target_host = self.target_host.trim();
        if target_host.is_empty() {
            return Err(anyhow!(
                "published service {} target host cannot be empty",
                name
            ));
        }

        let target_port = self
            .target_port
            .trim()
            .parse::<u16>()
            .map_err(|err| anyhow!("published service {} has invalid port: {err}", name))?;
        if target_port == 0 {
            return Err(anyhow!(
                "published service {} target port must be greater than 0",
                name
            ));
        }

        let direct_candidate_type = if self.direct_candidate_type.trim().is_empty() {
            DEFAULT_DIRECT_CANDIDATE_TYPE.to_string()
        } else {
            self.direct_candidate_type.trim().to_string()
        };
        let direct_wait_seconds = if self.direct_wait_seconds.trim().is_empty() {
            DEFAULT_DIRECT_WAIT_SECONDS
        } else {
            self.direct_wait_seconds
                .trim()
                .parse::<u64>()
                .map_err(|err| {
                    anyhow!(
                        "published service {} has invalid direct wait seconds: {err}",
                        name
                    )
                })?
        };
        if direct_wait_seconds == 0 {
            return Err(anyhow!(
                "published service {} direct wait seconds must be greater than 0",
                name
            ));
        }
        let direct_udp_bind_addr = self.direct_udp_bind_addr.trim().to_string();
        if self.direct_enabled && direct_udp_bind_addr.is_empty() {
            return Err(anyhow!(
                "published service {} enables direct transport but UDP bind is empty",
                name
            ));
        }

        Ok(PublishedServiceConfig {
            name: name.to_string(),
            target_host: target_host.to_string(),
            target_port,
            allowed_device_ids: split_csv(&self.allowed_device_ids),
            direct_enabled: self.direct_enabled,
            direct_udp_bind_addr,
            direct_candidate_type,
            direct_wait_seconds,
        })
    }
}

#[derive(Clone, Debug)]
struct ForwardRuleDraft {
    name: String,
    target_device_id: String,
    service_name: String,
    local_bind_addr: String,
    enabled: bool,
    transport_mode: String,
    direct_udp_bind_addr: String,
    direct_candidate_type: String,
    direct_wait_seconds: String,
}

impl ForwardRuleDraft {
    fn suggested() -> Self {
        Self {
            name: "office-ssh".to_string(),
            target_device_id: String::new(),
            service_name: "ssh".to_string(),
            local_bind_addr: "127.0.0.1:10022".to_string(),
            enabled: true,
            transport_mode: DEFAULT_FORWARD_TRANSPORT.to_string(),
            direct_udp_bind_addr: String::new(),
            direct_candidate_type: DEFAULT_DIRECT_CANDIDATE_TYPE.to_string(),
            direct_wait_seconds: DEFAULT_DIRECT_WAIT_SECONDS.to_string(),
        }
    }

    fn from_config(rule: &LocalForwardConfig) -> Self {
        Self {
            name: rule.name.clone(),
            target_device_id: rule.target_device_id.clone(),
            service_name: rule.service_name.clone(),
            local_bind_addr: rule.local_bind_addr.clone(),
            enabled: rule.enabled,
            transport_mode: rule.transport_mode.clone(),
            direct_udp_bind_addr: rule.direct_udp_bind_addr.clone(),
            direct_candidate_type: rule.direct_candidate_type.clone(),
            direct_wait_seconds: rule.direct_wait_seconds.to_string(),
        }
    }

    fn to_config(&self, index: usize) -> Result<LocalForwardConfig> {
        let name = self.name.trim();
        if name.is_empty() {
            return Err(anyhow!("forward rule #{} name cannot be empty", index + 1));
        }
        if self.target_device_id.trim().is_empty() {
            return Err(anyhow!(
                "forward rule {} target device cannot be empty",
                name
            ));
        }
        if self.service_name.trim().is_empty() {
            return Err(anyhow!(
                "forward rule {} service name cannot be empty",
                name
            ));
        }
        if self.local_bind_addr.trim().is_empty() {
            return Err(anyhow!(
                "forward rule {} local bind address cannot be empty",
                name
            ));
        }
        let transport_mode = if self.transport_mode.trim().is_empty() {
            DEFAULT_FORWARD_TRANSPORT.to_string()
        } else {
            self.transport_mode.trim().to_ascii_lowercase()
        };
        if transport_mode != "relay" && transport_mode != "auto" {
            return Err(anyhow!(
                "forward rule {} transport must be relay or auto",
                name
            ));
        }
        let direct_candidate_type = if self.direct_candidate_type.trim().is_empty() {
            DEFAULT_DIRECT_CANDIDATE_TYPE.to_string()
        } else {
            self.direct_candidate_type.trim().to_string()
        };
        let direct_wait_seconds = if self.direct_wait_seconds.trim().is_empty() {
            DEFAULT_DIRECT_WAIT_SECONDS
        } else {
            self.direct_wait_seconds
                .trim()
                .parse::<u64>()
                .map_err(|err| {
                    anyhow!(
                        "forward rule {} has invalid direct wait seconds: {err}",
                        name
                    )
                })?
        };
        if direct_wait_seconds == 0 {
            return Err(anyhow!(
                "forward rule {} direct wait seconds must be greater than 0",
                name
            ));
        }
        let direct_udp_bind_addr = self.direct_udp_bind_addr.trim().to_string();
        if transport_mode == "auto" && direct_udp_bind_addr.is_empty() {
            return Err(anyhow!(
                "forward rule {} uses auto transport but UDP bind is empty",
                name
            ));
        }

        Ok(LocalForwardConfig {
            name: name.to_string(),
            target_device_id: self.target_device_id.trim().to_string(),
            service_name: self.service_name.trim().to_string(),
            local_bind_addr: self.local_bind_addr.trim().to_string(),
            enabled: self.enabled,
            transport_mode,
            direct_udp_bind_addr,
            direct_candidate_type,
            direct_wait_seconds,
        })
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ManagedAgentState {
    Stopped,
    Running,
    Stopping,
    Failed,
}

impl ManagedAgentState {
    fn as_state_str(self) -> &'static str {
        match self {
            Self::Stopped => "stopped",
            Self::Running => "running",
            Self::Stopping => "stopping",
            Self::Failed => "failed",
        }
    }
}

struct MiniPunchDesktop {
    launch_args: DesktopLaunchArgs,
    runtime: Runtime,
    config_path: String,
    server_url: String,
    join_token: String,
    device_name: String,
    device_id: String,
    session_summary: String,
    status_report: AgentStatusReport,
    managed_agent_state: ManagedAgentState,
    managed_agent_note: String,
    managed_agent_shutdown: Option<watch::Sender<bool>>,
    managed_agent_task: Option<JoinHandle<Result<()>>>,
    observed_runtime: Option<RuntimeStateSnapshot>,
    observed_runtime_note: String,
    last_runtime_poll_at: Instant,
    tray: Option<DesktopTray>,
    tray_note: String,
    tray_init_attempted: bool,
    window_visible: bool,
    pending_quit: bool,
    launch_actions_applied: bool,
    autostart_status: AutostartStatus,
    service_drafts: Vec<PublishedServiceDraft>,
    forward_drafts: Vec<ForwardRuleDraft>,
    output: String,
}

impl MiniPunchDesktop {
    fn new(launch_args: DesktopLaunchArgs) -> Self {
        let config_path = launch_args.config_path.clone().unwrap_or_else(|| {
            AgentConfig::default_path()
                .ok()
                .unwrap_or_else(|| PathBuf::from("agent.toml"))
        });
        let autostart_status =
            detect_autostart(&config_path).unwrap_or_else(|err| AutostartStatus {
                enabled: false,
                entry_path: PathBuf::from("unavailable"),
                platform_label: "unavailable",
                detail: format!("failed to inspect autostart: {err}"),
            });
        let mut app = Self {
            launch_args,
            runtime: Runtime::new().expect("tokio runtime"),
            config_path: config_path.display().to_string(),
            server_url: String::new(),
            join_token: String::new(),
            device_name: default_device_name(),
            device_id: "尚未加入网络".to_string(),
            session_summary: "没有已保存会话".to_string(),
            status_report: status_report_from_config(&AgentConfig::default()),
            managed_agent_state: ManagedAgentState::Stopped,
            managed_agent_note: "desktop has not started a managed agent yet".to_string(),
            managed_agent_shutdown: None,
            managed_agent_task: None,
            observed_runtime: None,
            observed_runtime_note: "no local run observation yet".to_string(),
            last_runtime_poll_at: Instant::now()
                .checked_sub(Duration::from_secs(10))
                .unwrap_or_else(Instant::now),
            tray: None,
            tray_note: "tray not initialized yet".to_string(),
            tray_init_attempted: false,
            window_visible: true,
            pending_quit: false,
            launch_actions_applied: false,
            autostart_status,
            service_drafts: Vec::new(),
            forward_drafts: Vec::new(),
            output: "欢迎使用 MiniPunch Desktop。\n先 Load Config，然后直接在 GUI 里编辑 published services 和 forward rules，再 Save Config。".to_string(),
        };
        let _ = app.load_config_from_disk_internal();
        app
    }

    fn current_config_path(&self) -> PathBuf {
        PathBuf::from(self.config_path.trim())
    }

    fn refresh_autostart_status(&mut self) {
        self.autostart_status =
            detect_autostart(&self.current_config_path()).unwrap_or_else(|err| AutostartStatus {
                enabled: false,
                entry_path: self.current_config_path(),
                platform_label: "unavailable",
                detail: format!("failed to inspect autostart: {err}"),
            });
    }

    fn toggle_autostart(&mut self) {
        let config_path = self.current_config_path();
        let result = if self.autostart_status.enabled {
            disable_autostart(&config_path)
        } else {
            enable_autostart(&config_path)
        };
        match result {
            Ok(status) => {
                self.autostart_status = status.clone();
                self.output = format!(
                    "autostart {} via {} at {}\n{}",
                    if status.enabled {
                        "enabled"
                    } else {
                        "disabled"
                    },
                    status.platform_label,
                    status.entry_path.display(),
                    status.detail
                );
            }
            Err(err) => {
                self.refresh_autostart_status();
                self.output = format!("error: {err}");
            }
        }
    }

    fn ensure_tray_initialized(&mut self, ctx: &egui::Context) {
        if self.tray_init_attempted {
            return;
        }
        self.tray_init_attempted = true;
        match DesktopTray::create(ctx.clone()) {
            Ok(tray) => {
                self.tray = Some(tray);
                self.tray_note = "tray ready; close requests now minimize to tray".to_string();
            }
            Err(err) => {
                self.tray_note = format!("tray unavailable: {err}");
            }
        }
    }

    fn process_tray_commands(&mut self, ctx: &egui::Context) {
        let Some(tray) = &self.tray else {
            return;
        };
        for command in tray.drain_commands() {
            match command {
                TrayCommand::ToggleWindow => {
                    if self.window_visible {
                        self.hide_window_to_tray(ctx, "window hidden to tray from tray icon");
                    } else {
                        self.show_window_from_tray(ctx, "window restored from tray icon");
                    }
                }
                TrayCommand::ShowWindow => {
                    self.show_window_from_tray(ctx, "window restored from tray menu");
                }
                TrayCommand::HideWindow => {
                    self.hide_window_to_tray(ctx, "window hidden to tray from tray menu");
                }
                TrayCommand::ToggleManagedAgent => {
                    if matches!(self.managed_agent_state, ManagedAgentState::Running) {
                        self.stop_managed_agent();
                    } else if !matches!(self.managed_agent_state, ManagedAgentState::Stopping) {
                        self.start_managed_agent();
                    }
                }
                TrayCommand::ToggleAutostart => self.toggle_autostart(),
                TrayCommand::Quit => self.request_quit(ctx, "quit requested from tray"),
            }
        }
    }

    fn sync_tray_state(&self) {
        let Some(tray) = &self.tray else {
            return;
        };
        tray.sync_state(
            self.window_visible,
            matches!(self.managed_agent_state, ManagedAgentState::Running),
            matches!(self.managed_agent_state, ManagedAgentState::Stopping),
            self.autostart_status.enabled,
        );
    }

    fn apply_launch_actions(&mut self, ctx: &egui::Context) {
        if self.launch_actions_applied {
            return;
        }
        self.launch_actions_applied = true;

        if self.launch_args.start_agent {
            self.start_managed_agent();
        }

        if self.launch_args.background {
            if self.tray.is_some() {
                self.hide_window_to_tray(
                    ctx,
                    "desktop launched in background mode and moved to tray",
                );
            } else {
                self.output = format!(
                    "{}\ntray is unavailable, so background launch kept the window visible",
                    self.output
                );
            }
        }
    }

    fn handle_close_request(&mut self, ctx: &egui::Context) {
        if self.pending_quit {
            ctx.send_viewport_cmd(egui::ViewportCommand::Close);
            return;
        }
        if self.tray.is_none() {
            return;
        }
        if ctx.input(|input| input.viewport().close_requested()) {
            ctx.send_viewport_cmd(egui::ViewportCommand::CancelClose);
            self.hide_window_to_tray(ctx, "close request redirected to tray");
        }
    }

    fn hide_window_to_tray(&mut self, ctx: &egui::Context, note: impl Into<String>) {
        if self.tray.is_none() {
            self.output = "tray is unavailable; cannot hide window to tray".to_string();
            return;
        }
        ctx.send_viewport_cmd(egui::ViewportCommand::Visible(false));
        self.window_visible = false;
        self.output = note.into();
    }

    fn show_window_from_tray(&mut self, ctx: &egui::Context, note: impl Into<String>) {
        ctx.send_viewport_cmd(egui::ViewportCommand::Visible(true));
        ctx.send_viewport_cmd(egui::ViewportCommand::Minimized(false));
        ctx.send_viewport_cmd(egui::ViewportCommand::Focus);
        self.window_visible = true;
        self.output = note.into();
    }

    fn request_quit(&mut self, ctx: &egui::Context, note: impl Into<String>) {
        self.pending_quit = true;
        self.output = note.into();
        ctx.send_viewport_cmd(egui::ViewportCommand::Close);
    }

    fn load_config_from_disk(&mut self) {
        self.output = render_result(self.load_config_from_disk_internal());
    }

    fn load_config_from_disk_internal(&mut self) -> Result<String> {
        let path = self.current_config_path();
        let config = AgentConfig::load_or_default(&path)?;
        self.apply_config_to_ui(&config);
        self.refresh_autostart_status();
        self.refresh_runtime_observation();
        Ok(format!(
            "loaded config {}\nservices: {}\nforward rules: {}",
            path.display(),
            self.service_drafts.len(),
            self.forward_drafts.len()
        ))
    }

    fn apply_config_to_ui(&mut self, config: &AgentConfig) {
        self.server_url = config.server_url.clone();
        self.device_name = if config.device_name.trim().is_empty() {
            default_device_name()
        } else {
            config.device_name.clone()
        };
        self.device_id = config
            .device_id
            .clone()
            .unwrap_or_else(|| "尚未加入网络".to_string());
        self.session_summary = render_session_summary(config);
        self.status_report = status_report_from_config(config);
        self.service_drafts = config
            .published_services
            .iter()
            .map(PublishedServiceDraft::from_config)
            .collect();
        self.forward_drafts = config
            .forward_rules
            .iter()
            .map(ForwardRuleDraft::from_config)
            .collect();
    }

    fn save_full_config(&mut self) {
        let result = (|| -> Result<String> {
            let published_services = self.collect_published_services()?;
            let forward_rules = self.collect_forward_rules()?;
            let config =
                self.save_sections_to_disk(Some(published_services), Some(forward_rules))?;
            Ok(format!(
                "saved config {}\nservices: {}\nforward rules: {}",
                self.current_config_path().display(),
                config.published_services.len(),
                config.forward_rules.len()
            ))
        })();
        self.output = render_result(result);
    }

    fn save_sections_to_disk(
        &mut self,
        published_services: Option<Vec<PublishedServiceConfig>>,
        forward_rules: Option<Vec<LocalForwardConfig>>,
    ) -> Result<AgentConfig> {
        let path = self.current_config_path();
        let mut config = AgentConfig::load_or_default(&path)?;
        config.server_url = self.server_url.trim().to_string();
        if self.device_name.trim().is_empty() {
            self.device_name = default_device_name();
        }
        config.device_name = self.device_name.trim().to_string();
        if let Some(published_services) = published_services {
            config.published_services = published_services;
        }
        if let Some(forward_rules) = forward_rules {
            config.forward_rules = forward_rules;
        }
        config.save(&path)?;
        self.device_id = config
            .device_id
            .clone()
            .unwrap_or_else(|| "尚未加入网络".to_string());
        self.session_summary = render_session_summary(&config);
        self.status_report = status_report_from_config(&config);
        self.refresh_autostart_status();
        self.refresh_runtime_observation();
        Ok(config)
    }

    fn collect_published_services(&self) -> Result<Vec<PublishedServiceConfig>> {
        self.service_drafts
            .iter()
            .enumerate()
            .map(|(index, draft)| draft.to_config(index))
            .collect()
    }

    fn collect_forward_rules(&self) -> Result<Vec<LocalForwardConfig>> {
        self.forward_drafts
            .iter()
            .enumerate()
            .map(|(index, draft)| draft.to_config(index))
            .collect()
    }

    fn join_network(&mut self) {
        let result = self.runtime.block_on(async {
            let runtime = AgentRuntime::init(
                self.current_config_path(),
                self.server_url.trim(),
                self.join_token.trim(),
                self.device_name.trim(),
            )
            .await?;
            Ok::<_, anyhow::Error>(serde_json::to_string_pretty(runtime.config())?)
        });
        if let Ok(_) = result {
            self.join_token.clear();
            if let Ok(config) = AgentConfig::load_or_default(&self.current_config_path()) {
                self.device_id = config
                    .device_id
                    .clone()
                    .unwrap_or_else(|| "尚未加入网络".to_string());
                self.session_summary = render_session_summary(&config);
            }
        }
        self.output = render_result(result);
    }

    fn heartbeat(&mut self) {
        let result = self.runtime.block_on(async {
            let mut runtime = AgentRuntime::load(self.current_config_path()).await?;
            runtime.heartbeat().await?;
            Ok::<_, anyhow::Error>("heartbeat ok".to_string())
        });
        self.output = render_result(result);
    }

    fn load_network(&mut self) {
        let result = self.runtime.block_on(async {
            let mut runtime = AgentRuntime::load(self.current_config_path()).await?;
            let snapshot = runtime.network_snapshot().await?;
            Ok::<_, anyhow::Error>(serde_json::to_string_pretty(&snapshot)?)
        });
        self.output = render_result(result);
    }

    fn refresh_status(&mut self) {
        let path = self.current_config_path();
        let local_config = match AgentConfig::load_or_default(&path) {
            Ok(config) => config,
            Err(err) => {
                self.output = format!("error: {err}");
                return;
            }
        };

        let result = self.runtime.block_on(async {
            let mut runtime = AgentRuntime::load(path).await?;
            Ok::<_, anyhow::Error>(runtime.status_report().await)
        });

        match result {
            Ok(report) => {
                self.device_id = report
                    .device_id
                    .clone()
                    .unwrap_or_else(|| "尚未加入网络".to_string());
                self.session_summary = if report.session.has_token {
                    match report.session.expires_at {
                        Some(expires_at) if report.session.is_valid => {
                            format!("active, expires_at={expires_at}")
                        }
                        Some(expires_at) => format!("stale, expires_at={expires_at}"),
                        None => "stored session token without expiry".to_string(),
                    }
                } else {
                    "没有已保存会话".to_string()
                };
                self.status_report = report.clone();
                self.output =
                    render_result(serde_json::to_string_pretty(&report).map_err(Into::into));
            }
            Err(err) => {
                let report = status_report_from_config(&local_config);
                self.device_id = report
                    .device_id
                    .clone()
                    .unwrap_or_else(|| "尚未加入网络".to_string());
                self.session_summary = render_session_summary(&local_config);
                self.status_report = report;
                self.output = format!(
                    "status refresh fell back to local config only: {err}\nconfig path: {}",
                    self.current_config_path().display()
                );
            }
        }
        self.refresh_runtime_observation();
    }

    fn start_managed_agent(&mut self) {
        self.reconcile_managed_agent();
        if self.managed_agent_task.is_some() {
            self.output = "error: desktop-managed agent is already running".to_string();
            return;
        }

        let published_services = match self.collect_published_services() {
            Ok(services) => services,
            Err(err) => {
                self.output = format!("error: {err}");
                return;
            }
        };
        let forward_rules = match self.collect_forward_rules() {
            Ok(rules) => rules,
            Err(err) => {
                self.output = format!("error: {err}");
                return;
            }
        };
        if let Err(err) = self.save_sections_to_disk(Some(published_services), Some(forward_rules))
        {
            self.output = format!("error: {err}");
            return;
        }

        let path = self.current_config_path();
        let (shutdown_tx, shutdown_rx) = watch::channel(false);
        let task = self.runtime.spawn(async move {
            let mut runtime = AgentRuntime::load(path).await?;
            runtime.run_until_shutdown(shutdown_rx).await
        });

        self.managed_agent_shutdown = Some(shutdown_tx);
        self.managed_agent_task = Some(task);
        self.managed_agent_state = ManagedAgentState::Running;
        self.managed_agent_note =
            "desktop started a managed run task using the current saved config".to_string();
        self.refresh_runtime_observation();
        self.output = format!(
            "managed agent started from desktop\nconfig path: {}",
            self.current_config_path().display()
        );
    }

    fn stop_managed_agent(&mut self) {
        self.reconcile_managed_agent();
        let Some(shutdown_tx) = &self.managed_agent_shutdown else {
            self.output = "error: desktop-managed agent is not running".to_string();
            return;
        };

        let _ = shutdown_tx.send(true);
        self.managed_agent_state = ManagedAgentState::Stopping;
        self.managed_agent_note =
            "stop requested; waiting for the managed run task to exit".to_string();
        self.refresh_runtime_observation();
        self.output = "managed agent stop requested".to_string();
    }

    fn reconcile_managed_agent(&mut self) {
        let Some(task) = self.managed_agent_task.as_ref() else {
            return;
        };
        if !task.is_finished() {
            return;
        }

        let task = self
            .managed_agent_task
            .take()
            .expect("finished task handle must exist");
        let result = self.runtime.block_on(task);
        self.managed_agent_shutdown = None;

        match result {
            Ok(Ok(())) => {
                let was_stopping = matches!(self.managed_agent_state, ManagedAgentState::Stopping);
                self.managed_agent_state = ManagedAgentState::Stopped;
                self.managed_agent_note = if was_stopping {
                    "managed run task stopped cleanly after a desktop stop request".to_string()
                } else {
                    "managed run task exited cleanly".to_string()
                };
            }
            Ok(Err(err)) => {
                self.managed_agent_state = ManagedAgentState::Failed;
                self.managed_agent_note = err.to_string();
                self.output = format!("managed agent failed: {err}");
            }
            Err(err) => {
                self.managed_agent_state = ManagedAgentState::Failed;
                self.managed_agent_note = format!("managed task join error: {err}");
                self.output = self.managed_agent_note.clone();
            }
        }
        self.refresh_runtime_observation();
    }

    fn refresh_runtime_observation(&mut self) {
        self.last_runtime_poll_at = Instant::now();
        let config_path = self.current_config_path();
        let runtime_path = runtime_state_path_for_config(&config_path);
        if !runtime_path.exists() {
            self.observed_runtime = None;
            self.observed_runtime_note =
                format!("no runtime state file yet at {}", runtime_path.display());
            return;
        }

        match load_runtime_state_for_config(&config_path) {
            Ok(snapshot) => {
                self.observed_runtime_note =
                    format!("observing local run state from {}", runtime_path.display());
                self.observed_runtime = Some(snapshot);
            }
            Err(err) => {
                self.observed_runtime = None;
                self.observed_runtime_note = format!(
                    "failed to load runtime state {}: {err}",
                    runtime_path.display()
                );
            }
        }
    }

    fn maybe_poll_runtime_observation(&mut self) {
        if self.last_runtime_poll_at.elapsed() < Duration::from_secs(1) {
            return;
        }
        self.refresh_runtime_observation();
    }

    fn publish_configured_services(&mut self) {
        let services = match self.collect_published_services() {
            Ok(services) => services,
            Err(err) => {
                self.output = format!("error: {err}");
                return;
            }
        };

        let save_result = self.save_sections_to_disk(Some(services.clone()), None);
        if let Err(err) = save_result {
            self.output = format!("error: {err}");
            return;
        }

        let result = self.runtime.block_on(async {
            let mut runtime = AgentRuntime::load(self.current_config_path()).await?;
            let mut published = Vec::with_capacity(services.len());
            for service in services {
                let response = runtime.publish_service(service).await?;
                published.push(response);
            }
            Ok::<_, anyhow::Error>(serde_json::to_string_pretty(&published)?)
        });
        self.output = render_result(result);
    }

    fn render_left_column(&mut self, ui: &mut egui::Ui) {
        self.render_control_plane_card(ui);
        ui.add_space(12.0);
        self.render_published_services_card(ui);
    }

    fn render_right_column(&mut self, ui: &mut egui::Ui) {
        self.render_runtime_status_card(ui);
        ui.add_space(12.0);
        self.render_forward_rules_card(ui);
        ui.add_space(12.0);
        self.render_output_card(ui);
    }

    fn render_control_plane_card(&mut self, ui: &mut egui::Ui) {
        egui::Frame::group(ui.style())
            .fill(Color32::from_rgb(245, 238, 226))
            .show(ui, |ui| {
                ui.label(RichText::new("Control Plane").strong());
                ui.add_space(6.0);

                ui.horizontal(|ui| {
                    ui.label("Config Path");
                    ui.add(TextEdit::singleline(&mut self.config_path).desired_width(340.0));
                });
                ui.horizontal(|ui| {
                    ui.label("Server URL");
                    ui.add(TextEdit::singleline(&mut self.server_url).desired_width(340.0));
                });
                ui.horizontal(|ui| {
                    ui.label("Join Token");
                    ui.add(TextEdit::singleline(&mut self.join_token).desired_width(340.0));
                });
                ui.horizontal(|ui| {
                    ui.label("Device Name");
                    ui.add(TextEdit::singleline(&mut self.device_name).desired_width(340.0));
                });

                ui.add_space(8.0);
                ui.horizontal_wrapped(|ui| {
                    if ui.button("Load Config").clicked() {
                        self.load_config_from_disk();
                    }
                    if ui.button("Save Config").clicked() {
                        self.save_full_config();
                    }
                    if ui.button("Join Network").clicked() {
                        self.join_network();
                    }
                    if ui.button("Heartbeat").clicked() {
                        self.heartbeat();
                    }
                    if ui.button("Load Network").clicked() {
                        self.load_network();
                    }
                    if ui.button("Refresh Status").clicked() {
                        self.refresh_status();
                    }
                });
            });
    }

    fn render_runtime_status_card(&mut self, ui: &mut egui::Ui) {
        egui::Frame::group(ui.style())
            .fill(Color32::from_rgb(239, 236, 247))
            .show(ui, |ui| {
                ui.horizontal(|ui| {
                    ui.label(RichText::new("Runtime Status").strong());
                    if ui.button("Start Agent").clicked() {
                        self.start_managed_agent();
                    }
                    if ui.button("Stop Agent").clicked() {
                        self.stop_managed_agent();
                    }
                    if ui.button("Refresh Status").clicked() {
                        self.refresh_status();
                    }
                    if ui
                        .add_enabled(self.tray.is_some(), egui::Button::new("Hide To Tray"))
                        .clicked()
                    {
                        self.hide_window_to_tray(ui.ctx(), "window hidden to tray from GUI");
                    }
                    let autostart_label = if self.autostart_status.enabled {
                        "Disable Autostart"
                    } else {
                        "Enable Autostart"
                    };
                    if ui.button(autostart_label).clicked() {
                        self.toggle_autostart();
                    }
                });
                ui.label(
                    RichText::new(
                        "这里展示的是本地配置和最新网络快照推导出的状态，能快速看出设备是否在线、服务是否已同步、forward 规则是否可解析。",
                    )
                    .color(Color32::from_rgb(82, 72, 98)),
                );
                ui.add_space(8.0);

                ui.horizontal_wrapped(|ui| {
                    state_badge(
                        ui,
                        "Managed Run",
                        self.managed_agent_state.as_state_str(),
                    );
                    state_badge(ui, "Observed Run", &self.observed_runtime_state());
                    let local_state = match self.status_report.local_device_online {
                        Some(true) => "online",
                        Some(false) => "offline",
                        None => "unknown",
                    };
                    state_badge(ui, "Local Device", local_state);
                    state_badge(
                        ui,
                        "Snapshot",
                        if self.status_report.snapshot_error.is_some() {
                            "degraded"
                        } else {
                            "fresh"
                        },
                    );
                    state_badge(
                        ui,
                        "Devices",
                        &self.status_report.network_device_count.to_string(),
                    );
                    state_badge(
                        ui,
                        "Services",
                        &self.status_report.network_service_count.to_string(),
                    );
                    state_badge(
                        ui,
                        "Tray",
                        if self.tray.is_some() { "ready" } else { "unavailable" },
                    );
                    state_badge(
                        ui,
                        "Autostart",
                        if self.autostart_status.enabled {
                            "enabled"
                        } else {
                            "disabled"
                        },
                    );
                });

                ui.add_space(6.0);
                ui.label(
                    RichText::new(format!("managed note: {}", self.managed_agent_note))
                        .color(Color32::from_rgb(84, 76, 101)),
                );
                ui.label(
                    RichText::new(format!("observed note: {}", self.observed_runtime_note))
                        .color(Color32::from_rgb(84, 76, 101)),
                );
                ui.label(
                    RichText::new(format!(
                        "tray note: {} | autostart({}): {} [{}]",
                        self.tray_note,
                        self.autostart_status.platform_label,
                        self.autostart_status.detail,
                        self.autostart_status.entry_path.display()
                    ))
                    .color(Color32::from_rgb(84, 76, 101)),
                );

                if let Some(runtime) = &self.observed_runtime {
                    ui.add_space(8.0);
                    ui.label(RichText::new("Observed Local Run").strong());
                    ui.horizontal_wrapped(|ui| {
                        state_badge(ui, "Observed Run", &runtime.observed_state());
                        state_badge(ui, "PID", &runtime.pid.to_string());
                        state_badge(ui, "Restarts", &runtime.restart_count.to_string());
                        state_badge(
                            ui,
                            "Forwards",
                            &runtime.enabled_forward_rules.len().to_string(),
                        );
                    });
                    ui.add_space(6.0);
                    ui.label(format!("detail: {}", runtime.status_detail));
                    ui.label(format!("started_at: {}", runtime.started_at));
                    ui.label(format!("updated_at: {}", runtime.updated_at));
                    if let Some(last_prepare_ok_at) = runtime.last_prepare_ok_at {
                        ui.label(format!("last_prepare_ok_at: {last_prepare_ok_at}"));
                    }
                    if let Some(last_heartbeat_ok_at) = runtime.last_heartbeat_ok_at {
                        ui.label(format!("last_heartbeat_ok_at: {last_heartbeat_ok_at}"));
                    }
                    if let Some(reason) = &runtime.last_restart_reason {
                        ui.colored_label(
                            Color32::from_rgb(145, 89, 49),
                            format!("last restart reason: {reason}"),
                        );
                    }
                    if let Some(last_error) = &runtime.last_error {
                        ui.colored_label(
                            Color32::from_rgb(154, 71, 52),
                            format!("last error: {last_error}"),
                        );
                    }
                    if !runtime.enabled_forward_rules.is_empty() {
                        ui.label(format!(
                            "enabled forward rules: {}",
                            runtime.enabled_forward_rules.join(", ")
                        ));
                    }
                    if !runtime.published_services.is_empty() {
                        ui.label(format!(
                            "published services: {}",
                            runtime.published_services.join(", ")
                        ));
                    }

                    if !runtime.forward_observations.is_empty() {
                        ui.add_space(8.0);
                        ui.label(RichText::new("Observed Forward Transports").strong());
                        for observation in &runtime.forward_observations {
                            let transport = observation
                                .active_transport
                                .as_deref()
                                .unwrap_or("pending");
                            let mut detail = format!(
                                "configured={} | active={} | {} | direct_attempts={} | relay_fallbacks={} | active_conn={} | direct_conn={} | relay_conn={}",
                                observation.configured_transport,
                                transport,
                                observation.detail,
                                observation.direct_attempt_count,
                                observation.relay_fallback_count,
                                observation.active_connection_count,
                                observation.direct_connection_count,
                                observation.relay_connection_count
                            );
                            if let Some(last_peer) = &observation.last_peer {
                                detail.push_str(&format!(" | last_peer={last_peer}"));
                            }
                            if let Some(opened_at) = observation.last_connection_opened_at {
                                detail.push_str(&format!(" | opened_at={opened_at}"));
                            }
                            if let Some(closed_at) = observation.last_connection_closed_at {
                                detail.push_str(&format!(" | closed_at={closed_at}"));
                            }
                            if let Some(last_switch) = observation.last_transport_switch_at {
                                detail.push_str(&format!(" | switched_at={last_switch}"));
                            }
                            if let Some(metrics) = &observation.direct_metrics {
                                detail.push_str(&format!(
                                    " | direct_metrics=cwnd:{} ssthresh:{} rto_ms:{} srtt_ms:{} out:{} in:{} fr:{} ka:{}/{}",
                                    metrics.window_packets,
                                    metrics.ssthresh_packets,
                                    metrics.rto_ms,
                                    metrics
                                        .smoothed_rtt_ms
                                        .map(|value| value.to_string())
                                        .unwrap_or_else(|| "-".to_string()),
                                    metrics.pending_outbound_packets,
                                    metrics.pending_inbound_packets,
                                    metrics.fast_recovery,
                                    metrics.keepalive_sent_count,
                                    metrics.keepalive_ack_count
                                ));
                            }
                            if let (Some(transport), Some(stage), Some(at)) = (
                                &observation.last_failure_transport,
                                &observation.last_failure_stage,
                                observation.last_failure_at,
                            ) {
                                detail.push_str(&format!(
                                    " | last_failure={}::{}@{}",
                                    transport, stage, at
                                ));
                            }
                            if let Some(last_failure_error) = &observation.last_failure_error {
                                detail.push_str(&format!(
                                    " | last_failure_error={last_failure_error}"
                                ));
                            }
                            if let Some(last_error) = &observation.last_error {
                                detail.push_str(&format!(" | error={last_error}"));
                            }
                            render_status_row(
                                ui,
                                &observation.name,
                                &observation.state,
                                &detail,
                            );
                        }
                    }

                    if !runtime.published_service_observations.is_empty() {
                        ui.add_space(8.0);
                        ui.label(RichText::new("Observed Service Transports").strong());
                        for observation in &runtime.published_service_observations {
                            let transport = observation
                                .active_transport
                                .as_deref()
                                .unwrap_or("pending");
                            let mut detail = format!(
                                "direct_enabled={} | active={} | {} | direct_sessions={} | active_sessions={} | direct_conn={} | relay_conn={}",
                                observation.direct_enabled,
                                transport,
                                observation.detail,
                                observation.direct_session_count,
                                observation.active_session_count,
                                observation.direct_connection_count,
                                observation.relay_connection_count
                            );
                            if let Some(last_peer) = &observation.last_peer {
                                detail.push_str(&format!(" | last_peer={last_peer}"));
                            }
                            if let Some(last_switch) = observation.last_transport_switch_at {
                                detail.push_str(&format!(" | switched_at={last_switch}"));
                            }
                            if let Some(metrics) = &observation.direct_metrics {
                                detail.push_str(&format!(
                                    " | direct_metrics=cwnd:{} ssthresh:{} rto_ms:{} srtt_ms:{} out:{} in:{} fr:{} ka:{}/{}",
                                    metrics.window_packets,
                                    metrics.ssthresh_packets,
                                    metrics.rto_ms,
                                    metrics
                                        .smoothed_rtt_ms
                                        .map(|value| value.to_string())
                                        .unwrap_or_else(|| "-".to_string()),
                                    metrics.pending_outbound_packets,
                                    metrics.pending_inbound_packets,
                                    metrics.fast_recovery,
                                    metrics.keepalive_sent_count,
                                    metrics.keepalive_ack_count
                                ));
                            }
                            if let (Some(transport), Some(stage), Some(at)) = (
                                &observation.last_failure_transport,
                                &observation.last_failure_stage,
                                observation.last_failure_at,
                            ) {
                                detail.push_str(&format!(
                                    " | last_failure={}::{}@{}",
                                    transport, stage, at
                                ));
                            }
                            if let Some(last_failure_error) = &observation.last_failure_error {
                                detail.push_str(&format!(
                                    " | last_failure_error={last_failure_error}"
                                ));
                            }
                            if let Some(last_error) = &observation.last_error {
                                detail.push_str(&format!(" | error={last_error}"));
                            }
                            render_status_row(
                                ui,
                                &observation.name,
                                &observation.state,
                                &detail,
                            );
                        }
                    }

                    ui.add_space(8.0);
                    ui.label(RichText::new("Recent Runtime Events").strong());
                    for event in runtime.recent_events.iter().rev().take(8) {
                        render_status_row(
                            ui,
                            &format!("{} {}", event.level, event.timestamp),
                            runtime_event_state(&event.level),
                            &event.message,
                        );
                    }
                }

                if let Some(error) = &self.status_report.snapshot_error {
                    ui.add_space(6.0);
                    ui.colored_label(
                        Color32::from_rgb(154, 71, 52),
                        format!("control-plane snapshot unavailable: {error}"),
                    );
                }

                ui.add_space(8.0);
                ui.label(RichText::new("Published Services").strong());
                if self.status_report.published_services.is_empty() {
                    ui.label("没有已配置的 published service。");
                } else {
                    for service in &self.status_report.published_services {
                        render_status_row(
                            ui,
                            &service.name,
                            &service.state,
                            &format!(
                                "{}:{} | {}",
                                service.target_host, service.target_port, service.detail
                            ),
                        );
                    }
                }

                ui.add_space(8.0);
                ui.label(RichText::new("Forward Rules").strong());
                if self.status_report.forward_rules.is_empty() {
                    ui.label("没有已配置的 forward rule。");
                } else {
                    for rule in &self.status_report.forward_rules {
                        let title = match &rule.target_device_name {
                            Some(device_name) => format!("{} -> {}", rule.name, device_name),
                            None => rule.name.clone(),
                        };
                        let detail = format!(
                            "{} -> {} | {}",
                            rule.local_bind_addr, rule.service_name, rule.detail
                        );
                        render_status_row(ui, &title, &rule.state, &detail);
                    }
                }
            });
    }

    fn render_published_services_card(&mut self, ui: &mut egui::Ui) {
        egui::Frame::group(ui.style())
            .fill(Color32::from_rgb(229, 241, 233))
            .show(ui, |ui| {
                ui.horizontal(|ui| {
                    ui.label(RichText::new("Published TCP Services").strong());
                    if ui.button("Add Service").clicked() {
                        self.service_drafts.push(PublishedServiceDraft::suggested());
                    }
                    if ui.button("Push Services To Server").clicked() {
                        self.publish_configured_services();
                    }
                });
                ui.label(
                    RichText::new(
                        "这里编辑的是本机准备发布给其他设备访问的 TCP 服务定义。可以为某个 service 开启 direct responder，`run` 模式会按这些配置自动托管。",
                    )
                    .color(Color32::from_rgb(74, 83, 74)),
                );
                ui.add_space(8.0);

                if self.service_drafts.is_empty() {
                    ui.label("还没有 published service。点击 Add Service 添加。");
                    return;
                }

                let mut remove_index = None;
                for (index, service) in self.service_drafts.iter_mut().enumerate() {
                    egui::Frame::group(ui.style())
                        .fill(Color32::from_rgb(241, 248, 243))
                        .show(ui, |ui| {
                            ui.horizontal(|ui| {
                                let title = if service.name.trim().is_empty() {
                                    format!("Service {}", index + 1)
                                } else {
                                    service.name.trim().to_string()
                                };
                                ui.label(RichText::new(title).strong());
                                if ui.button("Delete").clicked() {
                                    remove_index = Some(index);
                                }
                            });
                            ui.horizontal(|ui| {
                                ui.label("Name");
                                ui.add(TextEdit::singleline(&mut service.name).desired_width(160.0));
                                ui.label("Host");
                                ui.add(
                                    TextEdit::singleline(&mut service.target_host)
                                        .desired_width(160.0),
                                );
                            });
                            ui.horizontal(|ui| {
                                ui.label("Port");
                                ui.add(
                                    TextEdit::singleline(&mut service.target_port)
                                        .desired_width(120.0),
                                );
                                ui.label("Allow Device IDs");
                                ui.add(
                                    TextEdit::singleline(&mut service.allowed_device_ids)
                                        .desired_width(260.0),
                                );
                            });
                            ui.horizontal(|ui| {
                                ui.checkbox(&mut service.direct_enabled, "Enable Direct");
                                ui.label("UDP Bind");
                                ui.add(
                                    TextEdit::singleline(&mut service.direct_udp_bind_addr)
                                        .desired_width(180.0),
                                );
                                ui.label("Candidate Type");
                                ui.add(
                                    TextEdit::singleline(&mut service.direct_candidate_type)
                                        .desired_width(90.0),
                                );
                                ui.label("Wait(s)");
                                ui.add(
                                    TextEdit::singleline(&mut service.direct_wait_seconds)
                                        .desired_width(70.0),
                                );
                            });
                        });
                    ui.add_space(8.0);
                }

                if let Some(index) = remove_index {
                    self.service_drafts.remove(index);
                }
            });
    }

    fn render_forward_rules_card(&mut self, ui: &mut egui::Ui) {
        egui::Frame::group(ui.style())
            .fill(Color32::from_rgb(235, 240, 247))
            .show(ui, |ui| {
                ui.horizontal(|ui| {
                    ui.label(RichText::new("Forward Rules").strong());
                    if ui.button("Add Forward").clicked() {
                        self.forward_drafts.push(ForwardRuleDraft::suggested());
                    }
                });
                ui.label(
                    RichText::new(
                        "这里编辑的是本机回环监听到远端服务的映射规则。现在可以给某条规则选 `relay` 或 `auto`，`run` 会按配置决定是否先试 direct 再回 relay。",
                    )
                    .color(Color32::from_rgb(70, 78, 92)),
                );
                ui.add_space(8.0);

                if self.forward_drafts.is_empty() {
                    ui.label("还没有 forward rule。点击 Add Forward 添加。");
                    return;
                }

                let mut remove_index = None;
                for (index, rule) in self.forward_drafts.iter_mut().enumerate() {
                    egui::Frame::group(ui.style())
                        .fill(Color32::from_rgb(243, 246, 251))
                        .show(ui, |ui| {
                            ui.horizontal(|ui| {
                                let title = if rule.name.trim().is_empty() {
                                    format!("Forward {}", index + 1)
                                } else {
                                    rule.name.trim().to_string()
                                };
                                ui.label(RichText::new(title).strong());
                                ui.checkbox(&mut rule.enabled, "Enabled");
                                if ui.button("Delete").clicked() {
                                    remove_index = Some(index);
                                }
                            });
                            ui.horizontal(|ui| {
                                ui.label("Name");
                                ui.add(TextEdit::singleline(&mut rule.name).desired_width(150.0));
                                ui.label("Target Device");
                                ui.add(
                                    TextEdit::singleline(&mut rule.target_device_id)
                                        .desired_width(220.0),
                                );
                            });
                            ui.horizontal(|ui| {
                                ui.label("Service");
                                ui.add(
                                    TextEdit::singleline(&mut rule.service_name)
                                        .desired_width(150.0),
                                );
                                ui.label("Local Bind");
                                ui.add(
                                    TextEdit::singleline(&mut rule.local_bind_addr)
                                        .desired_width(220.0),
                                );
                            });
                            ui.horizontal(|ui| {
                                ui.label("Transport");
                                ui.add(
                                    TextEdit::singleline(&mut rule.transport_mode)
                                        .desired_width(90.0),
                                );
                                ui.label("UDP Bind");
                                ui.add(
                                    TextEdit::singleline(&mut rule.direct_udp_bind_addr)
                                        .desired_width(180.0),
                                );
                                ui.label("Candidate Type");
                                ui.add(
                                    TextEdit::singleline(&mut rule.direct_candidate_type)
                                        .desired_width(90.0),
                                );
                                ui.label("Wait(s)");
                                ui.add(
                                    TextEdit::singleline(&mut rule.direct_wait_seconds)
                                        .desired_width(70.0),
                                );
                            });
                        });
                    ui.add_space(8.0);
                }

                if let Some(index) = remove_index {
                    self.forward_drafts.remove(index);
                }
            });
    }

    fn render_output_card(&mut self, ui: &mut egui::Ui) {
        egui::Frame::group(ui.style())
            .fill(Color32::from_rgb(248, 248, 247))
            .show(ui, |ui| {
                ui.label(RichText::new("Output").strong());
                egui::ScrollArea::vertical()
                    .max_height(320.0)
                    .show(ui, |ui| {
                        ui.add(
                            TextEdit::multiline(&mut self.output)
                                .desired_rows(16)
                                .desired_width(f32::INFINITY)
                                .font(egui::TextStyle::Monospace)
                                .interactive(false),
                        );
                    });
            });
    }
}

impl eframe::App for MiniPunchDesktop {
    fn update(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        self.reconcile_managed_agent();
        self.ensure_tray_initialized(ctx);
        self.process_tray_commands(ctx);
        self.apply_launch_actions(ctx);
        self.handle_close_request(ctx);
        self.maybe_poll_runtime_observation();
        self.sync_tray_state();
        let repaint_after = if self.managed_agent_task.is_some() {
            Duration::from_millis(500)
        } else {
            Duration::from_secs(1)
        };
        ctx.request_repaint_after(repaint_after);

        egui::TopBottomPanel::top("top").show(ctx, |ui| {
            ui.horizontal(|ui| {
                ui.heading(
                    RichText::new("MiniPunch")
                        .size(24.0)
                        .color(Color32::from_rgb(35, 62, 90)),
                );
                ui.label(RichText::new("Desktop Config Workbench").color(Color32::from_rgb(131, 93, 38)));
            });
            ui.label(
                RichText::new(
                    "这一版桌面端已经能直接读写 publish / forward 配置，并保留加入网络、心跳、网络快照和服务发布能力。",
                )
                .color(Color32::from_rgb(76, 76, 76)),
            );
            ui.add_space(6.0);
            ui.horizontal_wrapped(|ui| {
                status_chip(
                    ui,
                    "Device",
                    &self.device_id,
                    Color32::from_rgb(221, 233, 246),
                    Color32::from_rgb(34, 64, 92),
                );
                status_chip(
                    ui,
                    "Session",
                    &self.session_summary,
                    Color32::from_rgb(233, 239, 225),
                    Color32::from_rgb(53, 82, 40),
                );
                status_chip(
                    ui,
                    "Published",
                    &self.service_drafts.len().to_string(),
                    Color32::from_rgb(229, 241, 233),
                    Color32::from_rgb(49, 86, 64),
                );
                status_chip(
                    ui,
                    "Forwards",
                    &self.forward_drafts.len().to_string(),
                    Color32::from_rgb(235, 240, 247),
                    Color32::from_rgb(54, 66, 96),
                );
                status_chip(
                    ui,
                    "Managed Run",
                    self.managed_agent_state.as_state_str(),
                    Color32::from_rgb(239, 236, 247),
                    Color32::from_rgb(84, 76, 101),
                );
                status_chip(
                    ui,
                    "Observed Run",
                    &self.observed_runtime_state(),
                    Color32::from_rgb(238, 238, 247),
                    Color32::from_rgb(76, 76, 101),
                );
            });
        });

        egui::CentralPanel::default().show(ctx, |ui| {
            ui.spacing_mut().item_spacing = egui::vec2(10.0, 10.0);
            egui::ScrollArea::vertical().show(ui, |ui| {
                if ui.available_width() >= 980.0 {
                    ui.columns(2, |columns| {
                        self.render_left_column(&mut columns[0]);
                        self.render_right_column(&mut columns[1]);
                    });
                } else {
                    self.render_left_column(ui);
                    ui.add_space(12.0);
                    self.render_right_column(ui);
                }
            });
        });
    }
}

fn configure_theme(ctx: &egui::Context) {
    let mut visuals = egui::Visuals::light();
    visuals.widgets.active.bg_fill = Color32::from_rgb(50, 97, 83);
    visuals.widgets.hovered.bg_fill = Color32::from_rgb(85, 136, 118);
    visuals.selection.bg_fill = Color32::from_rgb(190, 210, 201);
    visuals.panel_fill = Color32::from_rgb(251, 248, 243);
    ctx.set_visuals(visuals);
}

fn render_result(result: Result<String>) -> String {
    match result {
        Ok(value) => value,
        Err(err) => format!("error: {err}"),
    }
}

fn render_session_summary(config: &AgentConfig) -> String {
    match (&config.session_token, config.session_expires_at) {
        (Some(_), Some(expires_at)) if config.has_valid_session() => {
            format!("active, expires_at={expires_at}")
        }
        (Some(_), Some(expires_at)) => format!("stale, expires_at={expires_at}"),
        (Some(_), None) => "stored session token without expiry".to_string(),
        _ => "没有已保存会话".to_string(),
    }
}

impl MiniPunchDesktop {
    fn observed_runtime_state(&self) -> String {
        self.observed_runtime
            .as_ref()
            .map(RuntimeStateSnapshot::observed_state)
            .unwrap_or_else(|| "missing".to_string())
    }
}

fn split_csv(raw: &str) -> Vec<String> {
    raw.split(',')
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned)
        .collect()
}

fn status_chip(ui: &mut egui::Ui, label: &str, value: &str, fill: Color32, text_color: Color32) {
    egui::Frame::group(ui.style()).fill(fill).show(ui, |ui| {
        ui.horizontal(|ui| {
            ui.label(RichText::new(label).strong().color(text_color));
            ui.label(RichText::new(value).color(text_color));
        });
    });
}

fn state_badge(ui: &mut egui::Ui, label: &str, state: &str) {
    let (fill, text_color) = state_colors(state);
    egui::Frame::group(ui.style()).fill(fill).show(ui, |ui| {
        ui.horizontal(|ui| {
            ui.label(RichText::new(label).strong().color(text_color));
            ui.label(RichText::new(state).color(text_color));
        });
    });
}

fn render_status_row(ui: &mut egui::Ui, title: &str, state: &str, detail: &str) {
    let (fill, text_color) = state_colors(state);
    egui::Frame::group(ui.style()).fill(fill).show(ui, |ui| {
        ui.horizontal_wrapped(|ui| {
            ui.label(RichText::new(title).strong().color(text_color));
            ui.label(RichText::new(state).strong().color(text_color));
        });
        ui.label(RichText::new(detail).color(text_color));
    });
    ui.add_space(6.0);
}

fn state_colors(state: &str) -> (Color32, Color32) {
    match state {
        "ready" | "online" | "fresh" | "running" => (
            Color32::from_rgb(225, 240, 227),
            Color32::from_rgb(42, 92, 54),
        ),
        "starting" | "unknown" => (
            Color32::from_rgb(231, 236, 246),
            Color32::from_rgb(54, 66, 96),
        ),
        "disabled" | "stopped" => (
            Color32::from_rgb(236, 236, 236),
            Color32::from_rgb(90, 90, 90),
        ),
        "offline" | "target_offline" | "registered_offline" | "degraded" | "stopping"
        | "retrying" | "restarting" | "stale" => (
            Color32::from_rgb(246, 233, 222),
            Color32::from_rgb(131, 83, 46),
        ),
        "service_missing" | "target_missing" | "not_synced" | "not_joined" | "failed"
        | "missing" => (
            Color32::from_rgb(248, 226, 222),
            Color32::from_rgb(142, 65, 49),
        ),
        _ => (
            Color32::from_rgb(231, 236, 246),
            Color32::from_rgb(54, 66, 96),
        ),
    }
}

fn runtime_event_state(level: &str) -> &'static str {
    match level {
        "error" => "failed",
        "warn" => "degraded",
        "info" => "running",
        _ => "unknown",
    }
}

fn default_device_name() -> String {
    hostname::get()
        .ok()
        .and_then(|name| name.into_string().ok())
        .filter(|name| !name.trim().is_empty())
        .unwrap_or_else(|| "minipunch-device".to_string())
}
