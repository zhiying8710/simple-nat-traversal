import { invoke } from "@tauri-apps/api/core";

export function desktopSnapshot(configPath) {
  return invoke("desktop_snapshot", {
    request: {
      config_path: configPath || null,
    },
  });
}

export function loadConfig(request) {
  return invoke("load_config", { request });
}

export function saveConfig(request) {
  return invoke("save_config", { request });
}

export function joinNetwork(request) {
  return invoke("join_network", { request });
}

export function heartbeat(request) {
  return invoke("heartbeat", { request });
}

export function refreshNetwork(request) {
  return invoke("refresh_network", { request });
}

export function refreshStatus(request) {
  return invoke("refresh_status", { request });
}

export function publishServices(request) {
  return invoke("publish_services", { request });
}

export function startManagedAgent(request) {
  return invoke("start_managed_agent", { request });
}

export function stopManagedAgent(request) {
  return invoke("stop_managed_agent", { request });
}

export function toggleAutostart(request) {
  return invoke("toggle_autostart", { request });
}
