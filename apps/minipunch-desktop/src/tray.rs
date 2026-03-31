use std::sync::mpsc::{self, Receiver};
use std::thread;
use std::time::Duration;

use anyhow::{Context, Result};
use eframe::egui;
use tray_icon::menu::{CheckMenuItem, Menu, MenuEvent, MenuItem, PredefinedMenuItem};
use tray_icon::{Icon, MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent};

const TRAY_SHOW_ID: &str = "tray-show";
const TRAY_HIDE_ID: &str = "tray-hide";
const TRAY_AGENT_TOGGLE_ID: &str = "tray-agent-toggle";
const TRAY_AUTOSTART_ID: &str = "tray-autostart";
const TRAY_QUIT_ID: &str = "tray-quit";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TrayCommand {
    ToggleWindow,
    ShowWindow,
    HideWindow,
    ToggleManagedAgent,
    ToggleAutostart,
    Quit,
}

pub struct DesktopTray {
    _tray_icon: TrayIcon,
    _menu: Menu,
    show_item: MenuItem,
    hide_item: MenuItem,
    toggle_agent_item: MenuItem,
    autostart_item: CheckMenuItem,
    command_rx: Receiver<TrayCommand>,
}

impl DesktopTray {
    pub fn create(ctx: egui::Context) -> Result<Self> {
        let menu = Menu::new();
        let show_item = MenuItem::with_id(TRAY_SHOW_ID, "Show MiniPunch", true, None);
        let hide_item = MenuItem::with_id(TRAY_HIDE_ID, "Hide Window", false, None);
        let toggle_agent_item = MenuItem::with_id(TRAY_AGENT_TOGGLE_ID, "Start Agent", true, None);
        let autostart_item =
            CheckMenuItem::with_id(TRAY_AUTOSTART_ID, "Launch At Login", true, false, None);
        let quit_item = MenuItem::with_id(TRAY_QUIT_ID, "Quit MiniPunch", true, None);
        let separator = PredefinedMenuItem::separator();
        menu.append_items(&[
            &show_item,
            &hide_item,
            &toggle_agent_item,
            &autostart_item,
            &separator,
            &quit_item,
        ])
        .context("failed to build tray menu")?;

        let tray_icon = TrayIconBuilder::new()
            .with_icon(build_tray_icon()?)
            .with_tooltip("MiniPunch Desktop")
            .with_menu(Box::new(menu.clone()))
            .build()
            .context("failed to create tray icon")?;

        let (command_tx, command_rx) = mpsc::channel();
        thread::spawn(move || tray_event_loop(command_tx, ctx));

        Ok(Self {
            _tray_icon: tray_icon,
            _menu: menu,
            show_item,
            hide_item,
            toggle_agent_item,
            autostart_item,
            command_rx,
        })
    }

    pub fn drain_commands(&self) -> Vec<TrayCommand> {
        let mut commands = Vec::new();
        while let Ok(command) = self.command_rx.try_recv() {
            commands.push(command);
        }
        commands
    }

    pub fn sync_state(
        &self,
        window_visible: bool,
        managed_agent_running: bool,
        managed_agent_stopping: bool,
        autostart_enabled: bool,
    ) {
        self.show_item.set_enabled(!window_visible);
        self.hide_item.set_enabled(window_visible);
        if managed_agent_stopping {
            self.toggle_agent_item.set_text("Stopping Agent...");
            self.toggle_agent_item.set_enabled(false);
        } else if managed_agent_running {
            self.toggle_agent_item.set_text("Stop Agent");
            self.toggle_agent_item.set_enabled(true);
        } else {
            self.toggle_agent_item.set_text("Start Agent");
            self.toggle_agent_item.set_enabled(true);
        }
        self.autostart_item.set_checked(autostart_enabled);
    }
}

fn tray_event_loop(command_tx: mpsc::Sender<TrayCommand>, ctx: egui::Context) {
    loop {
        while let Ok(event) = TrayIconEvent::receiver().try_recv() {
            if let Some(command) = tray_command_from_tray_event(&event) {
                let _ = command_tx.send(command);
                ctx.request_repaint();
            }
        }

        match MenuEvent::receiver().recv_timeout(Duration::from_millis(200)) {
            Ok(event) => {
                if let Some(command) = tray_command_from_menu_event(&event) {
                    let _ = command_tx.send(command);
                    ctx.request_repaint();
                }
                while let Ok(next_event) = MenuEvent::receiver().try_recv() {
                    if let Some(command) = tray_command_from_menu_event(&next_event) {
                        let _ = command_tx.send(command);
                        ctx.request_repaint();
                    }
                }
            }
            Err(_) => {}
        }
    }
}

fn tray_command_from_tray_event(event: &TrayIconEvent) -> Option<TrayCommand> {
    match event {
        TrayIconEvent::Click {
            button: MouseButton::Left,
            button_state: MouseButtonState::Up,
            ..
        }
        | TrayIconEvent::DoubleClick {
            button: MouseButton::Left,
            ..
        } => Some(TrayCommand::ToggleWindow),
        _ => None,
    }
}

fn tray_command_from_menu_event(event: &MenuEvent) -> Option<TrayCommand> {
    match event.id().0.as_str() {
        TRAY_SHOW_ID => Some(TrayCommand::ShowWindow),
        TRAY_HIDE_ID => Some(TrayCommand::HideWindow),
        TRAY_AGENT_TOGGLE_ID => Some(TrayCommand::ToggleManagedAgent),
        TRAY_AUTOSTART_ID => Some(TrayCommand::ToggleAutostart),
        TRAY_QUIT_ID => Some(TrayCommand::Quit),
        _ => None,
    }
}

fn build_tray_icon() -> Result<Icon> {
    const SIZE: u32 = 32;
    let mut rgba = vec![0u8; (SIZE * SIZE * 4) as usize];
    for y in 0..SIZE {
        for x in 0..SIZE {
            let index = ((y * SIZE + x) * 4) as usize;
            let edge = x < 3 || y < 3 || x >= SIZE - 3 || y >= SIZE - 3;
            let highlight = (8..24).contains(&x) && (8..24).contains(&y);
            let (r, g, b) = if edge {
                (22, 36, 32)
            } else if highlight {
                (70, 150, 118)
            } else {
                (36, 88, 72)
            };
            rgba[index] = r;
            rgba[index + 1] = g;
            rgba[index + 2] = b;
            rgba[index + 3] = 255;
        }
    }

    for y in 11..21 {
        for x in 11..21 {
            let index = ((y * SIZE + x) * 4) as usize;
            rgba[index] = 240;
            rgba[index + 1] = 245;
            rgba[index + 2] = 239;
            rgba[index + 3] = 255;
        }
    }

    Icon::from_rgba(rgba, SIZE, SIZE).context("failed to create tray icon image")
}
