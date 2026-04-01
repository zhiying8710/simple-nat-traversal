use anyhow::{Context, Result, anyhow};
use hostname::get as hostname_get;
use minipunch_agent::AgentRuntime;
use minipunch_agent::config::{
    AgentConfig, DEFAULT_DIRECT_CANDIDATE_TYPE, DEFAULT_DIRECT_WAIT_SECONDS,
    DEFAULT_FORWARD_TRANSPORT, LocalForwardConfig, PublishedServiceConfig,
};
use minipunch_agent::runtime_state::{
    RuntimeStateSnapshot, load_runtime_state_for_config, runtime_state_is_stale,
    runtime_state_path_for_config,
};
use minipunch_agent::status::{AgentStatusReport, build_status_report};
use minipunch_core::{NetworkSnapshot, ServiceDefinition};
use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use tauri::image::Image;
use tauri::menu::MenuBuilder;
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{App, AppHandle, Manager, State, Window, WindowEvent};
use tokio::sync::watch;
use tokio::task::JoinHandle;

use crate::autostart::{AutostartStatus, detect_autostart, disable_autostart, enable_autostart};

const INITIAL_OUTPUT: &str =
    "欢迎使用 MiniPunch 桌面端。\n先加载配置，然后在页面里编辑已发布服务和转发规则，最后保存配置。";
const INITIAL_NETWORK_NOTE: &str = "尚未读取在线设备快照。";
#[derive(Debug, Clone, Default)]
pub struct DesktopLaunchArgs {
    pub config_path: Option<PathBuf>,
    pub background: bool,
    pub start_agent: bool,
    pub autostart: bool,
}

impl DesktopLaunchArgs {
    pub fn parse() -> Self {
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
                "--autostart" => {
                    launch.autostart = true;
                    launch.background = true;
                    launch.start_agent = true;
                }
                _ => {}
            }
        }
        launch
    }

    fn uses_boot_managed_agent_mode(&self) -> bool {
        self.autostart || (self.background && self.start_agent)
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PublishedServiceDraft {
    pub name: String,
    pub target_host: String,
    pub target_port: String,
    pub allowed_device_ids: String,
    pub direct_enabled: bool,
    pub direct_udp_bind_addr: String,
    pub direct_candidate_type: String,
    pub direct_wait_seconds: String,
}

impl PublishedServiceDraft {
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
            return Err(anyhow!("第 {} 个已发布服务的名称不能为空", index + 1));
        }
        let target_host = self.target_host.trim();
        if target_host.is_empty() {
            return Err(anyhow!("已发布服务 {} 的目标主机不能为空", name));
        }
        let target_port = self
            .target_port
            .trim()
            .parse::<u16>()
            .map_err(|err| anyhow!("已发布服务 {} 的端口无效：{err}", name))?;
        if target_port == 0 {
            return Err(anyhow!("已发布服务 {} 的目标端口必须大于 0", name));
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
                .map_err(|err| anyhow!("已发布服务 {} 的直连等待秒数无效：{err}", name))?
        };
        if direct_wait_seconds == 0 {
            return Err(anyhow!("已发布服务 {} 的直连等待秒数必须大于 0", name));
        }
        let direct_udp_bind_addr = self.direct_udp_bind_addr.trim().to_string();
        if self.direct_enabled && direct_udp_bind_addr.is_empty() {
            return Err(anyhow!(
                "已发布服务 {} 启用了直连，但 UDP 绑定地址为空",
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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ForwardRuleDraft {
    pub name: String,
    pub target_device_id: String,
    pub service_name: String,
    pub local_bind_addr: String,
    pub enabled: bool,
    pub transport_mode: String,
    pub direct_udp_bind_addr: String,
    pub direct_candidate_type: String,
    pub direct_wait_seconds: String,
}

impl ForwardRuleDraft {
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
            return Err(anyhow!("第 {} 条转发规则的名称不能为空", index + 1));
        }
        if self.target_device_id.trim().is_empty() {
            return Err(anyhow!("转发规则 {} 的目标设备不能为空", name));
        }
        if self.service_name.trim().is_empty() {
            return Err(anyhow!("转发规则 {} 的服务名不能为空", name));
        }
        if self.local_bind_addr.trim().is_empty() {
            return Err(anyhow!("转发规则 {} 的本地绑定地址不能为空", name));
        }
        let transport_mode = if self.transport_mode.trim().is_empty() {
            DEFAULT_FORWARD_TRANSPORT.to_string()
        } else {
            self.transport_mode.trim().to_ascii_lowercase()
        };
        if transport_mode != "relay" && transport_mode != "auto" {
            return Err(anyhow!("转发规则 {} 的传输模式必须是 relay 或 auto", name));
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
                .map_err(|err| anyhow!("转发规则 {} 的直连等待秒数无效：{err}", name))?
        };
        if direct_wait_seconds == 0 {
            return Err(anyhow!("转发规则 {} 的直连等待秒数必须大于 0", name));
        }
        let direct_udp_bind_addr = self.direct_udp_bind_addr.trim().to_string();
        if transport_mode == "auto" && direct_udp_bind_addr.is_empty() {
            return Err(anyhow!(
                "转发规则 {} 使用 auto 模式时，UDP 绑定地址不能为空",
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

#[derive(Debug, Clone, Serialize)]
pub struct AutostartStatusView {
    pub enabled: bool,
    pub entry_path: String,
    pub platform_label: String,
    pub detail: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct DesktopSnapshot {
    pub config_path: String,
    pub server_url: String,
    pub device_name: String,
    pub device_id: String,
    pub session_summary: String,
    pub status_report: AgentStatusReport,
    pub network_snapshot: Option<NetworkSnapshot>,
    pub network_snapshot_note: String,
    pub managed_agent_state: String,
    pub managed_agent_note: String,
    pub observed_runtime: Option<RuntimeStateSnapshot>,
    pub observed_runtime_note: String,
    pub autostart_status: AutostartStatusView,
    pub service_drafts: Vec<PublishedServiceDraft>,
    pub forward_drafts: Vec<ForwardRuleDraft>,
    pub raw_config_preview: String,
    pub raw_config_note: String,
    pub output: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct DesktopActionResponse {
    pub notice_level: String,
    pub notice_title: String,
    pub notice_message: String,
    pub clear_join_token: bool,
    pub snapshot: DesktopSnapshot,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ManagedAgentState {
    Stopped,
    Running,
    Stopping,
    Failed,
}

impl ManagedAgentState {
    fn as_str(self) -> &'static str {
        match self {
            Self::Stopped => "stopped",
            Self::Running => "running",
            Self::Stopping => "stopping",
            Self::Failed => "failed",
        }
    }
}

struct ManagedAgentController {
    state: ManagedAgentState,
    note: String,
    shutdown: Option<watch::Sender<bool>>,
    task: Option<JoinHandle<Result<()>>>,
}

impl ManagedAgentController {
    fn new() -> Self {
        Self {
            state: ManagedAgentState::Stopped,
            note: "桌面端尚未启动受管 Agent。".to_string(),
            shutdown: None,
            task: None,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ManagedAgentStartTrigger {
    Interactive,
    BootAutostart,
}

impl ManagedAgentStartTrigger {
    fn exits_process_on_failure(self) -> bool {
        matches!(self, Self::BootAutostart)
    }

    fn requires_completed_boot_config(self) -> bool {
        matches!(self, Self::BootAutostart)
    }
}

struct DesktopStateInner {
    current_config_path: PathBuf,
    network_snapshot: Option<NetworkSnapshot>,
    network_snapshot_note: String,
    snapshot_error: Option<String>,
    output: String,
    managed_agent: ManagedAgentController,
    quitting: bool,
}

pub struct SharedDesktopState {
    inner: Mutex<DesktopStateInner>,
}

impl SharedDesktopState {
    pub fn new(initial_config_path: PathBuf) -> Self {
        Self {
            inner: Mutex::new(DesktopStateInner {
                current_config_path: initial_config_path,
                network_snapshot: None,
                network_snapshot_note: INITIAL_NETWORK_NOTE.to_string(),
                snapshot_error: None,
                output: INITIAL_OUTPUT.to_string(),
                managed_agent: ManagedAgentController::new(),
                quitting: false,
            }),
        }
    }
}

#[derive(Debug, Deserialize)]
pub struct DesktopSnapshotRequest {
    pub config_path: Option<String>,
}

#[derive(Debug, Deserialize)]
pub struct SaveConfigRequest {
    pub config_path: String,
    pub server_url: String,
    pub device_name: String,
    pub service_drafts: Vec<PublishedServiceDraft>,
    pub forward_drafts: Vec<ForwardRuleDraft>,
}

#[derive(Debug, Deserialize)]
pub struct JoinNetworkRequest {
    pub config_path: String,
    pub server_url: String,
    pub join_token: String,
    pub device_name: String,
}

#[derive(Debug, Deserialize)]
pub struct SimpleConfigRequest {
    pub config_path: String,
}

type CommandResult<T> = std::result::Result<T, String>;

pub fn default_config_path() -> PathBuf {
    AgentConfig::default_path()
        .ok()
        .unwrap_or_else(|| PathBuf::from("agent.toml"))
}

pub fn setup(app: &mut App, launch_args: DesktopLaunchArgs) -> Result<()> {
    if let Some(config_path) = &launch_args.config_path {
        let state = app.state::<SharedDesktopState>();
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        inner.current_config_path = config_path.clone();
    }

    install_tray(app.handle())?;

    if launch_args.start_agent {
        let app_handle = app.handle().clone();
        let start_trigger = if launch_args.uses_boot_managed_agent_mode() {
            ManagedAgentStartTrigger::BootAutostart
        } else {
            ManagedAgentStartTrigger::Interactive
        };
        tauri::async_runtime::spawn(async move {
            if let Err(err) = start_managed_agent_for_current_path(&app_handle, start_trigger).await
            {
                note_managed_agent_start_failure(&app_handle, &err.to_string());
                if start_trigger.exits_process_on_failure() {
                    eprintln!("failed to start MiniPunch agent during autostart: {err}");
                    request_quit(&app_handle);
                }
            }
        });
    }

    if launch_args.background {
        if let Some(window) = app.get_webview_window("main") {
            let _ = window.hide();
        }
    }
    Ok(())
}

pub fn handle_window_event(window: &Window, event: &WindowEvent) {
    if !matches!(window.label(), "main") {
        return;
    }
    if let WindowEvent::CloseRequested { api, .. } = event {
        let state = window.state::<SharedDesktopState>();
        let should_hide = {
            let inner = state.inner.lock().expect("desktop state poisoned");
            !inner.quitting
        };
        if should_hide {
            api.prevent_close();
            let _ = window.hide();
        }
    }
}

fn install_tray(app: &AppHandle) -> Result<()> {
    let menu = MenuBuilder::new(app)
        .text("show_window", "显示窗口")
        .text("hide_window", "隐藏到托盘")
        .separator()
        .text("toggle_agent", "切换本地 Agent")
        .text("toggle_autostart", "切换开机自启")
        .separator()
        .text("quit_app", "退出")
        .build()?;

    let icon = Image::from_bytes(include_bytes!("../icons/icon.png"))?;
    TrayIconBuilder::with_id("main-tray")
        .icon(icon)
        .menu(&menu)
        .tooltip("MiniPunch 桌面端")
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| {
            let event_id = event.id.as_ref().to_string();
            let app_handle = app.clone();
            tauri::async_runtime::spawn(async move {
                match event_id.as_str() {
                    "show_window" => {
                        let _ = reveal_main_window(&app_handle);
                    }
                    "hide_window" => {
                        let _ = hide_main_window(&app_handle);
                    }
                    "toggle_agent" => {
                        let _ = toggle_managed_agent(&app_handle).await;
                    }
                    "toggle_autostart" => {
                        let _ = toggle_autostart_for_current_path(&app_handle).await;
                    }
                    "quit_app" => {
                        request_quit(&app_handle);
                    }
                    _ => {}
                }
            });
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Down,
                ..
            } = event
            {
                let _ = toggle_main_window(tray.app_handle());
            }
        })
        .build(app)?;
    Ok(())
}

pub fn reveal_main_window(app: &AppHandle) -> Result<()> {
    let window = app
        .get_webview_window("main")
        .ok_or_else(|| anyhow!("main window is missing"))?;
    let _ = window.unminimize();
    window.show()?;
    window.set_focus()?;
    Ok(())
}

fn hide_main_window(app: &AppHandle) -> Result<()> {
    let window = app
        .get_webview_window("main")
        .ok_or_else(|| anyhow!("main window is missing"))?;
    window.hide()?;
    Ok(())
}

fn toggle_main_window(app: &AppHandle) -> Result<()> {
    let window = app
        .get_webview_window("main")
        .ok_or_else(|| anyhow!("main window is missing"))?;
    if window.is_visible()? {
        window.hide()?;
    } else {
        reveal_main_window(app)?;
    }
    Ok(())
}

fn request_quit(app: &AppHandle) {
    {
        let state = app.state::<SharedDesktopState>();
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        inner.quitting = true;
    }
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.close();
    }
    app.exit(0);
}

async fn toggle_managed_agent(app: &AppHandle) -> Result<()> {
    reconcile_managed_agent(app).await?;
    let is_running = {
        let state = app.state::<SharedDesktopState>();
        let inner = state.inner.lock().expect("desktop state poisoned");
        matches!(
            inner.managed_agent.state,
            ManagedAgentState::Running | ManagedAgentState::Stopping
        )
    };
    if is_running {
        stop_managed_agent_for_current_path(app).await
    } else {
        start_managed_agent_for_current_path(app, ManagedAgentStartTrigger::Interactive).await
    }
}

async fn toggle_autostart_for_current_path(app: &AppHandle) -> Result<()> {
    let config_path = current_config_path(app)?;
    let status = detect_autostart(&config_path)?;
    let next = if status.enabled {
        disable_autostart(&config_path)?
    } else {
        enable_autostart(&config_path)?
    };
    let state = app.state::<SharedDesktopState>();
    let mut inner = state.inner.lock().expect("desktop state poisoned");
    inner.output = format!(
        "自启动已{}，方式：{}，入口：{}\n{}",
        if next.enabled { "启用" } else { "禁用" },
        next.platform_label,
        next.entry_path.display(),
        next.detail
    );
    Ok(())
}

fn current_config_path(app: &AppHandle) -> Result<PathBuf> {
    let state = app.state::<SharedDesktopState>();
    let inner = state.inner.lock().expect("desktop state poisoned");
    Ok(inner.current_config_path.clone())
}

fn resolve_config_path(state: &SharedDesktopState, requested: Option<String>) -> PathBuf {
    let requested = requested
        .and_then(|value| {
            let trimmed = value.trim().to_string();
            (!trimmed.is_empty()).then_some(PathBuf::from(trimmed))
        })
        .unwrap_or_else(default_config_path);
    let mut inner = state.inner.lock().expect("desktop state poisoned");
    inner.current_config_path = requested.clone();
    requested
}

async fn reconcile_managed_agent(app: &AppHandle) -> Result<()> {
    let finished_task = {
        let state = app.state::<SharedDesktopState>();
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        let is_finished = inner
            .managed_agent
            .task
            .as_ref()
            .map(|task| task.is_finished())
            .unwrap_or(false);
        if is_finished {
            Some((
                inner
                    .managed_agent
                    .task
                    .take()
                    .expect("finished task must exist"),
                matches!(inner.managed_agent.state, ManagedAgentState::Stopping),
            ))
        } else {
            None
        }
    };

    let Some((task, was_stopping)) = finished_task else {
        return Ok(());
    };

    let result = task.await;
    let state = app.state::<SharedDesktopState>();
    let mut inner = state.inner.lock().expect("desktop state poisoned");
    inner.managed_agent.shutdown = None;
    match result {
        Ok(Ok(())) => {
            inner.managed_agent.state = ManagedAgentState::Stopped;
            inner.managed_agent.note = if was_stopping {
                "桌面端请求停止后，受管运行任务已正常退出。".to_string()
            } else {
                "受管运行任务已正常退出。".to_string()
            };
        }
        Ok(Err(err)) => {
            inner.managed_agent.state = ManagedAgentState::Failed;
            inner.managed_agent.note = err.to_string();
            inner.output = format!("受管 Agent 运行失败：{err}");
        }
        Err(err) => {
            inner.managed_agent.state = ManagedAgentState::Failed;
            inner.managed_agent.note = format!("受管任务 Join 失败：{err}");
            inner.output = inner.managed_agent.note.clone();
        }
    }
    Ok(())
}

fn note_managed_agent_start_failure(app: &AppHandle, message: &str) {
    let state = app.state::<SharedDesktopState>();
    let mut inner = state.inner.lock().expect("desktop state poisoned");
    inner.managed_agent.state = ManagedAgentState::Failed;
    inner.managed_agent.note = message.to_string();
    inner.output = format!("受管 Agent 启动失败：{message}");
}

async fn start_managed_agent_for_current_path(
    app: &AppHandle,
    trigger: ManagedAgentStartTrigger,
) -> Result<()> {
    let path = current_config_path(app)?;
    start_managed_agent_for_path(app, path, trigger).await
}

async fn start_managed_agent_for_path(
    app: &AppHandle,
    path: PathBuf,
    trigger: ManagedAgentStartTrigger,
) -> Result<()> {
    reconcile_managed_agent(app).await?;
    {
        let state = app.state::<SharedDesktopState>();
        let inner = state.inner.lock().expect("desktop state poisoned");
        if inner.managed_agent.task.is_some() {
            return Err(anyhow!("桌面端受管 Agent 已经在运行"));
        }
    }

    let runtime = preflight_managed_agent_start(&path, trigger).await?;
    let (shutdown_tx, shutdown_rx) = watch::channel(false);
    let task = tokio::spawn(async move {
        let mut runtime = runtime;
        runtime.run_until_shutdown(shutdown_rx).await
    });

    let state = app.state::<SharedDesktopState>();
    let mut inner = state.inner.lock().expect("desktop state poisoned");
    inner.managed_agent.state = ManagedAgentState::Running;
    inner.managed_agent.note = "桌面端已按当前保存的配置启动受管运行任务。".to_string();
    inner.managed_agent.shutdown = Some(shutdown_tx);
    inner.managed_agent.task = Some(task);
    inner.output = format!(
        "已从桌面端启动受管 Agent。\n配置路径：{}",
        inner.current_config_path.display()
    );
    Ok(())
}

async fn preflight_managed_agent_start(
    path: &Path,
    trigger: ManagedAgentStartTrigger,
) -> Result<AgentRuntime> {
    if trigger.requires_completed_boot_config() {
        let config = AgentConfig::load(path)
            .with_context(|| format!("读取自启动配置失败：{}", path.display()))?;
        validate_boot_ready_config(&config)?;
    }
    AgentRuntime::load(path).await.with_context(|| {
        if trigger.requires_completed_boot_config() {
            format!("自启动配置预检失败：{}", path.display())
        } else {
            format!("读取受管 Agent 配置失败：{}", path.display())
        }
    })
}

fn validate_boot_ready_config(config: &AgentConfig) -> Result<()> {
    if config.server_url.trim().is_empty() {
        return Err(anyhow!(
            "当前配置缺少服务器地址，无法在开机自启时拉起 Agent"
        ));
    }
    if config.device_name.trim().is_empty() {
        return Err(anyhow!("当前配置缺少设备名称，无法在开机自启时拉起 Agent"));
    }
    if config.device_id.as_deref().unwrap_or("").trim().is_empty() {
        return Err(anyhow!(
            "当前配置尚未完成入网，缺少设备 ID；请先完成“加入网络”后再开启开机自启"
        ));
    }
    if config
        .private_key_base64
        .as_deref()
        .unwrap_or("")
        .trim()
        .is_empty()
    {
        return Err(anyhow!(
            "当前配置缺少设备身份私钥，无法在开机自启时恢复 Agent 身份"
        ));
    }
    if config
        .relay_private_key_base64
        .as_deref()
        .unwrap_or("")
        .trim()
        .is_empty()
    {
        return Err(anyhow!(
            "当前配置缺少 relay 私钥，无法在开机自启时恢复数据面身份"
        ));
    }
    Ok(())
}

async fn stop_managed_agent_for_current_path(app: &AppHandle) -> Result<()> {
    reconcile_managed_agent(app).await?;
    let state = app.state::<SharedDesktopState>();
    let mut inner = state.inner.lock().expect("desktop state poisoned");
    let Some(shutdown_tx) = inner.managed_agent.shutdown.as_ref() else {
        return Err(anyhow!("桌面端受管 Agent 当前未运行"));
    };
    let _ = shutdown_tx.send(true);
    inner.managed_agent.state = ManagedAgentState::Stopping;
    inner.managed_agent.note = "已请求停止，正在等待受管运行任务退出。".to_string();
    inner.output = "已发送停止受管 Agent 的请求。".to_string();
    Ok(())
}

fn collect_published_services_from_drafts(
    drafts: &[PublishedServiceDraft],
) -> Result<Vec<PublishedServiceConfig>> {
    drafts
        .iter()
        .enumerate()
        .map(|(index, draft)| draft.to_config(index))
        .collect()
}

fn collect_forward_rules_from_drafts(
    drafts: &[ForwardRuleDraft],
) -> Result<Vec<LocalForwardConfig>> {
    drafts
        .iter()
        .enumerate()
        .map(|(index, draft)| draft.to_config(index))
        .collect()
}

fn save_config_from_drafts(request: &SaveConfigRequest) -> Result<AgentConfig> {
    let path = PathBuf::from(request.config_path.trim());
    let published_services = collect_published_services_from_drafts(&request.service_drafts)?;
    let forward_rules = collect_forward_rules_from_drafts(&request.forward_drafts)?;
    let mut config = AgentConfig::load_or_default(&path)?;
    config.server_url = request.server_url.trim().to_string();
    config.device_name = if request.device_name.trim().is_empty() {
        default_device_name()
    } else {
        request.device_name.trim().to_string()
    };
    config.published_services = published_services;
    config.forward_rules = forward_rules;
    config.save(&path)?;
    Ok(config)
}

fn render_session_summary(config: &AgentConfig) -> String {
    match (&config.session_token, config.session_expires_at) {
        (Some(_), Some(expires_at)) if config.has_valid_session() => {
            format!("设备会话有效，过期时间[{}]", expires_at)
        }
        (Some(_), Some(expires_at)) => format!("设备会话已过期，过期时间[{}]", expires_at),
        (Some(_), None) => "已保存设备会话令牌，但没有过期时间。".to_string(),
        _ => "没有已保存设备会话".to_string(),
    }
}

fn default_device_name() -> String {
    hostname_get()
        .ok()
        .and_then(|name| name.into_string().ok())
        .filter(|name| !name.trim().is_empty())
        .unwrap_or_else(|| "minipunch-device".to_string())
}

fn split_csv(raw: &str) -> Vec<String> {
    raw.split(',')
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned)
        .collect()
}

fn should_ignore_runtime_snapshot(snapshot: &RuntimeStateSnapshot) -> bool {
    runtime_state_is_stale(snapshot) || matches!(snapshot.status.as_str(), "stopped" | "failed")
}

fn runtime_snapshot_for_config(config_path: &Path) -> (Option<RuntimeStateSnapshot>, String) {
    let runtime_path = runtime_state_path_for_config(config_path);
    if !runtime_path.exists() {
        return (
            None,
            format!("还没有运行态文件：{}", runtime_path.display()),
        );
    }
    match load_runtime_state_for_config(config_path) {
        Ok(snapshot) => {
            if should_ignore_runtime_snapshot(&snapshot) {
                (
                    None,
                    format!(
                        "检测到上一次运行遗留的本地运行态文件（{}），本次启动已忽略。",
                        runtime_path.display()
                    ),
                )
            } else {
                (
                    Some(snapshot),
                    format!("正在读取本地运行态文件：{}", runtime_path.display()),
                )
            }
        }
        Err(err) => (
            None,
            format!("读取运行态文件失败：{}：{err}", runtime_path.display()),
        ),
    }
}

fn raw_config_preview_for_path(config_path: &Path) -> (String, String) {
    match std::fs::read_to_string(config_path) {
        Ok(contents) => (contents, format!("原始配置来源：{}", config_path.display())),
        Err(err) => (
            String::new(),
            format!("读取原始配置失败：{}：{err}", config_path.display()),
        ),
    }
}

fn map_autostart_status(config_path: &Path) -> AutostartStatusView {
    match detect_autostart(config_path) {
        Ok(status) => autostart_status_view(status),
        Err(err) => AutostartStatusView {
            enabled: false,
            entry_path: config_path.display().to_string(),
            platform_label: "unavailable".to_string(),
            detail: format!("读取自启动状态失败：{err}"),
        },
    }
}

fn autostart_status_view(status: AutostartStatus) -> AutostartStatusView {
    AutostartStatusView {
        enabled: status.enabled,
        entry_path: status.entry_path.display().to_string(),
        platform_label: status.platform_label.to_string(),
        detail: status.detail,
    }
}

fn build_snapshot(state: &SharedDesktopState, config_path: &Path) -> Result<DesktopSnapshot> {
    let config = AgentConfig::load_or_default(config_path)?;
    let (
        network_snapshot,
        network_snapshot_note,
        snapshot_error,
        output,
        managed_agent_state,
        managed_agent_note,
    ) = {
        let inner = state.inner.lock().expect("desktop state poisoned");
        (
            inner.network_snapshot.clone(),
            inner.network_snapshot_note.clone(),
            inner.snapshot_error.clone(),
            inner.output.clone(),
            inner.managed_agent.state.as_str().to_string(),
            inner.managed_agent.note.clone(),
        )
    };
    let status_report = build_status_report(&config, network_snapshot.as_ref(), snapshot_error);
    let (observed_runtime, observed_runtime_note) = runtime_snapshot_for_config(config_path);
    let (raw_config_preview, raw_config_note) = raw_config_preview_for_path(config_path);

    Ok(DesktopSnapshot {
        config_path: config_path.display().to_string(),
        server_url: config.server_url.clone(),
        device_name: if config.device_name.trim().is_empty() {
            default_device_name()
        } else {
            config.device_name.clone()
        },
        device_id: config
            .device_id
            .clone()
            .unwrap_or_else(|| "尚未加入网络".to_string()),
        session_summary: render_session_summary(&config),
        status_report,
        network_snapshot,
        network_snapshot_note,
        managed_agent_state,
        managed_agent_note,
        observed_runtime,
        observed_runtime_note,
        autostart_status: map_autostart_status(config_path),
        service_drafts: config
            .published_services
            .iter()
            .map(PublishedServiceDraft::from_config)
            .collect(),
        forward_drafts: config
            .forward_rules
            .iter()
            .map(ForwardRuleDraft::from_config)
            .collect(),
        raw_config_preview,
        raw_config_note,
        output,
    })
}

fn response(
    state: &SharedDesktopState,
    config_path: &Path,
    notice_level: impl Into<String>,
    notice_title: impl Into<String>,
    notice_message: impl Into<String>,
    clear_join_token: bool,
) -> Result<DesktopActionResponse> {
    Ok(DesktopActionResponse {
        notice_level: notice_level.into(),
        notice_title: notice_title.into(),
        notice_message: notice_message.into(),
        clear_join_token,
        snapshot: build_snapshot(state, config_path)?,
    })
}

#[tauri::command]
pub async fn desktop_snapshot(
    request: DesktopSnapshotRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopSnapshot> {
    reconcile_managed_agent(&app)
        .await
        .map_err(|err| err.to_string())?;
    let config_path = resolve_config_path(&state, request.config_path);
    build_snapshot(&state, &config_path).map_err(|err| err.to_string())
}

#[tauri::command]
pub async fn load_config(
    request: SimpleConfigRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    reconcile_managed_agent(&app)
        .await
        .map_err(|err| err.to_string())?;
    let config_path = resolve_config_path(&state, Some(request.config_path));
    AgentConfig::load_or_default(&config_path).map_err(|err| err.to_string())?;
    {
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        inner.network_snapshot = None;
        inner.snapshot_error = None;
        inner.network_snapshot_note = INITIAL_NETWORK_NOTE.to_string();
        inner.output = format!("已加载配置：{}", config_path.display());
    }
    response(
        &state,
        &config_path,
        "success",
        "配置已加载",
        format!("已读取配置文件 {}", config_path.display()),
        false,
    )
    .map_err(|err| err.to_string())
}

#[tauri::command]
pub async fn save_config(
    request: SaveConfigRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    reconcile_managed_agent(&app)
        .await
        .map_err(|err| err.to_string())?;
    let config_path = resolve_config_path(&state, Some(request.config_path.clone()));
    let config = save_config_from_drafts(&request).map_err(|err| err.to_string())?;
    {
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        inner.output = format!(
            "已保存配置：{}\n已发布服务：{}\n转发规则：{}",
            config_path.display(),
            config.published_services.len(),
            config.forward_rules.len()
        );
    }
    response(
        &state,
        &config_path,
        "success",
        "配置已保存",
        format!("已保存到 {}", config_path.display()),
        false,
    )
    .map_err(|err| err.to_string())
}

#[tauri::command]
pub async fn join_network(
    request: JoinNetworkRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    reconcile_managed_agent(&app)
        .await
        .map_err(|err| err.to_string())?;
    let config_path = resolve_config_path(&state, Some(request.config_path.clone()));
    AgentRuntime::init(
        &config_path,
        request.server_url.trim(),
        request.join_token.trim(),
        request.device_name.trim(),
    )
    .await
    .map_err(|err| err.to_string())?;
    {
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        inner.network_snapshot = None;
        inner.snapshot_error = None;
        inner.network_snapshot_note = "已加入网络，请刷新在线设备快照。".to_string();
        inner.output = format!("设备已加入网络：{}", config_path.display());
    }
    response(
        &state,
        &config_path,
        "success",
        "已加入网络",
        "设备已经接入控制面。",
        true,
    )
    .map_err(|err| err.to_string())
}

#[tauri::command]
pub async fn heartbeat(
    request: SimpleConfigRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    reconcile_managed_agent(&app)
        .await
        .map_err(|err| err.to_string())?;
    let config_path = resolve_config_path(&state, Some(request.config_path));
    let mut runtime = AgentRuntime::load(&config_path)
        .await
        .map_err(|err| err.to_string())?;
    runtime.heartbeat().await.map_err(|err| err.to_string())?;
    {
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        inner.output = "心跳成功。".to_string();
    }
    response(
        &state,
        &config_path,
        "success",
        "心跳已发送",
        "设备在线时间已刷新。",
        false,
    )
    .map_err(|err| err.to_string())
}

#[tauri::command]
pub async fn refresh_network(
    request: SimpleConfigRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    reconcile_managed_agent(&app)
        .await
        .map_err(|err| err.to_string())?;
    let config_path = resolve_config_path(&state, Some(request.config_path));
    let mut runtime = AgentRuntime::load(&config_path)
        .await
        .map_err(|err| err.to_string())?;
    let snapshot = runtime
        .network_snapshot()
        .await
        .map_err(|err| err.to_string())?;
    {
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        inner.network_snapshot_note = format!(
            "在线设备快照已更新：{} 台设备，{} 个服务。",
            snapshot.devices.len(),
            snapshot.services.len()
        );
        inner.snapshot_error = None;
        inner.network_snapshot = Some(snapshot.clone());
        inner.output = serde_json::to_string_pretty(&snapshot).unwrap_or_default();
    }
    response(
        &state,
        &config_path,
        "success",
        "在线设备已刷新",
        format!(
            "已读取 {} 台设备、{} 个服务。",
            snapshot.devices.len(),
            snapshot.services.len()
        ),
        false,
    )
    .map_err(|err| err.to_string())
}

#[tauri::command]
pub async fn refresh_status(
    request: SimpleConfigRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    reconcile_managed_agent(&app)
        .await
        .map_err(|err| err.to_string())?;
    let config_path = resolve_config_path(&state, Some(request.config_path));
    let result = async {
        let mut runtime = AgentRuntime::load(&config_path).await?;
        runtime.network_snapshot().await
    }
    .await;

    match result {
        Ok(snapshot) => {
            {
                let mut inner = state.inner.lock().expect("desktop state poisoned");
                inner.network_snapshot_note = format!(
                    "在线设备快照已更新：{} 台设备，{} 个服务。",
                    snapshot.devices.len(),
                    snapshot.services.len()
                );
                inner.snapshot_error = None;
                inner.network_snapshot = Some(snapshot);
                inner.output = "状态刷新成功。".to_string();
            }
            response(
                &state,
                &config_path,
                "success",
                "状态已刷新",
                "本地配置和在线设备快照都已更新。",
                false,
            )
            .map_err(|err| err.to_string())
        }
        Err(err) => {
            {
                let mut inner = state.inner.lock().expect("desktop state poisoned");
                inner.snapshot_error = Some(err.to_string());
                inner.network_snapshot_note = format!("最近一次读取在线设备失败：{err}");
                inner.output = format!("刷新状态失败：{err}");
            }
            response(
                &state,
                &config_path,
                "error",
                "状态刷新失败",
                format!("控制面读取失败，已保留当前本地视图：{err}"),
                false,
            )
            .map_err(|err| err.to_string())
        }
    }
}

#[tauri::command]
pub async fn publish_services(
    request: SaveConfigRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    reconcile_managed_agent(&app)
        .await
        .map_err(|err| err.to_string())?;
    let config_path = resolve_config_path(&state, Some(request.config_path.clone()));
    let config = save_config_from_drafts(&request).map_err(|err| err.to_string())?;
    let mut runtime = AgentRuntime::load(&config_path)
        .await
        .map_err(|err| err.to_string())?;
    let mut published = Vec::<ServiceDefinition>::new();
    for service in config.published_services.clone() {
        published.push(
            runtime
                .publish_service(service)
                .await
                .map_err(|err| err.to_string())?,
        );
    }
    let network_snapshot = runtime.network_snapshot().await.ok();
    {
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        inner.network_snapshot = network_snapshot.clone();
        if let Some(snapshot) = &network_snapshot {
            inner.network_snapshot_note = format!(
                "在线设备快照已更新：{} 台设备，{} 个服务。",
                snapshot.devices.len(),
                snapshot.services.len()
            );
        }
        inner.snapshot_error = None;
        inner.output = serde_json::to_string_pretty(&published).unwrap_or_default();
    }
    response(
        &state,
        &config_path,
        "success",
        "服务已同步",
        format!("已同步 {} 个已发布服务。", published.len()),
        false,
    )
    .map_err(|err| err.to_string())
}

#[tauri::command]
pub async fn start_managed_agent(
    request: SaveConfigRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    let config_path = resolve_config_path(&state, Some(request.config_path.clone()));
    save_config_from_drafts(&request).map_err(|err| err.to_string())?;
    start_managed_agent_for_path(
        &app,
        config_path.clone(),
        ManagedAgentStartTrigger::Interactive,
    )
    .await
    .map_err(|err| err.to_string())?;
    response(
        &state,
        &config_path,
        "success",
        "已启动本地 Agent",
        "桌面端开始托管 run 模式。",
        false,
    )
    .map_err(|err| err.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn autostart_argument_enables_background_agent_boot() {
        let launch = DesktopLaunchArgs {
            autostart: true,
            background: true,
            start_agent: true,
            config_path: None,
        };
        assert!(launch.uses_boot_managed_agent_mode());
    }

    #[test]
    fn legacy_background_start_agent_launch_still_counts_as_boot_mode() {
        let launch = DesktopLaunchArgs {
            autostart: false,
            background: true,
            start_agent: true,
            config_path: None,
        };
        assert!(launch.uses_boot_managed_agent_mode());
    }

    #[test]
    fn boot_ready_config_requires_joined_identity_material() {
        let mut config = AgentConfig {
            server_url: "http://127.0.0.1:9443".to_string(),
            device_name: "windows-box".to_string(),
            ..AgentConfig::default()
        };
        let err = validate_boot_ready_config(&config).expect_err("config should be incomplete");
        assert!(err.to_string().contains("缺少设备 ID"));

        config.device_id = Some("dev_test".to_string());
        config.private_key_base64 = Some("identity".to_string());
        let err =
            validate_boot_ready_config(&config).expect_err("relay key should still be required");
        assert!(err.to_string().contains("relay 私钥"));
    }

    #[test]
    fn boot_ready_config_allows_missing_session_if_identity_is_present() {
        let config = AgentConfig {
            server_url: "http://127.0.0.1:9443".to_string(),
            device_name: "windows-box".to_string(),
            device_id: Some("dev_test".to_string()),
            private_key_base64: Some("identity".to_string()),
            relay_private_key_base64: Some("relay".to_string()),
            session_token: None,
            session_expires_at: None,
            published_services: Vec::new(),
            forward_rules: Vec::new(),
        };
        validate_boot_ready_config(&config).expect("session should be refreshable at boot");
    }
}

#[tauri::command]
pub async fn stop_managed_agent(
    request: SimpleConfigRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    let config_path = resolve_config_path(&state, Some(request.config_path));
    stop_managed_agent_for_current_path(&app)
        .await
        .map_err(|err| err.to_string())?;
    response(
        &state,
        &config_path,
        "info",
        "正在停止本地 Agent",
        "停止请求已发出，正在等待后台任务退出。",
        false,
    )
    .map_err(|err| err.to_string())
}

#[tauri::command]
pub async fn toggle_autostart(
    request: SimpleConfigRequest,
    state: State<'_, SharedDesktopState>,
    app: AppHandle,
) -> CommandResult<DesktopActionResponse> {
    reconcile_managed_agent(&app)
        .await
        .map_err(|err| err.to_string())?;
    let config_path = resolve_config_path(&state, Some(request.config_path));
    let status = detect_autostart(&config_path).map_err(|err| err.to_string())?;
    let next = if status.enabled {
        disable_autostart(&config_path).map_err(|err| err.to_string())?
    } else {
        enable_autostart(&config_path).map_err(|err| err.to_string())?
    };
    {
        let mut inner = state.inner.lock().expect("desktop state poisoned");
        inner.output = format!(
            "自启动已{}，方式：{}，入口：{}\n{}",
            if next.enabled { "启用" } else { "禁用" },
            next.platform_label,
            next.entry_path.display(),
            next.detail
        );
    }
    response(
        &state,
        &config_path,
        "success",
        if next.enabled {
            "已开启开机自启"
        } else {
            "已关闭开机自启"
        },
        next.detail,
        false,
    )
    .map_err(|err| err.to_string())
}
