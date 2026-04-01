#![cfg_attr(target_os = "windows", windows_subsystem = "windows")]

mod autostart;
mod desktop;
mod single_instance;

use anyhow::Result;
use std::sync::Mutex;

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
    let single_instance_guard = single_instance::acquire_or_activate_existing()?;
    if single_instance_guard.is_none() {
        return Ok(());
    }
    let single_instance_guard = Mutex::new(single_instance_guard);

    let app = tauri::Builder::default()
        .manage(state)
        .setup(move |app| {
            if let Some(guard) = single_instance_guard
                .lock()
                .expect("single-instance state poisoned")
                .take()
            {
                single_instance::spawn_listener(guard, app.handle().clone());
            }
            Ok(desktop::setup(app, launch_args.clone())?)
        })
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
        .build(tauri::generate_context!())?;
    app.run(|app_handle, event| {
        #[cfg(target_os = "macos")]
        if let tauri::RunEvent::Reopen { .. } = event {
            let _ = desktop::reveal_main_window(app_handle);
        }
    });
    Ok(())
}
