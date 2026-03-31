#![cfg_attr(target_os = "windows", windows_subsystem = "windows")]

mod autostart;
mod desktop;

use anyhow::Result;

fn main() {
    if let Err(err) = run() {
        eprintln!("failed to launch MiniPunch desktop: {err}");
        std::process::exit(1);
    }
}

fn run() -> Result<()> {
    let launch_args = desktop::DesktopLaunchArgs::parse();
    let initial_config_path = desktop::default_config_path();
    let state = desktop::SharedDesktopState::new(initial_config_path);

    tauri::Builder::default()
        .manage(state)
        .setup(move |app| Ok(desktop::setup(app, launch_args.clone())?))
        .on_window_event(desktop::handle_window_event)
        .invoke_handler(tauri::generate_handler![
            desktop::desktop_snapshot,
            desktop::load_config,
            desktop::save_config,
            desktop::join_network,
            desktop::heartbeat,
            desktop::refresh_network,
            desktop::refresh_status,
            desktop::publish_services,
            desktop::start_managed_agent,
            desktop::stop_managed_agent,
            desktop::toggle_autostart,
        ])
        .run(tauri::generate_context!())
        .map_err(Into::into)
}
