export type PublishConfig = {
  protocol?: string;
  local: string;
};

export type BindConfig = {
  protocol?: string;
  peer: string;
  service: string;
  local: string;
};

export type EditableConfig = {
  serverURL: string;
  allowInsecureHTTP: boolean;
  password?: string;
  clearPassword?: boolean;
  passwordConfigured: boolean;
  adminPassword?: string;
  clearAdminPassword?: boolean;
  adminPasswordConfigured: boolean;
  deviceName: string;
  autoConnect: boolean;
  udpListen: string;
  adminListen: string;
  logLevel: string;
  publish: Record<string, PublishConfig>;
  binds: Record<string, BindConfig>;
};

export type LoadedConfig = {
  exists: boolean;
  defaultDeviceName: string;
  configPath: string;
  config: EditableConfig;
};

export type DiscoveredService = {
  deviceID: string;
  deviceName: string;
  serviceName: string;
  protocol: string;
};

export type RuntimeStatus = {
  state?: string;
  config_path?: string;
  started_at?: string;
  stopped_at?: string;
  last_error?: string;
};

export type OverviewConfig = {
  server_url?: string;
  allow_insecure_http?: boolean;
  device_name?: string;
  auto_connect?: boolean;
  udp_listen?: string;
  admin_listen?: string;
  log_level?: string;
  password_configured?: boolean;
  admin_password_configured?: boolean;
  identity_configured?: boolean;
  publish?: Record<string, PublishConfig>;
  binds?: Record<string, BindConfig>;
};

export type StatusSnapshot = {
  device_id?: string;
  device_name?: string;
  network_state?: string;
  observed_addr?: string;
  active_service_proxies?: number;
  peers?: Array<unknown>;
};

export type NetworkDevice = {
  device_id?: string;
  device_name?: string;
  state?: string;
  observed_addr?: string;
  last_seen?: string;
  services?: string[];
  service_details?: Array<{ name: string; protocol?: string }>;
};

export type Overview = {
  generated_at?: string;
  config_exists?: boolean;
  config_valid?: boolean;
  config_error?: string;
  client_running?: boolean;
  status_error?: string;
  network_error?: string;
  autostart?: {
    installed?: boolean;
    file_path?: string;
  };
  config?: OverviewConfig;
  status?: StatusSnapshot;
  network?: {
    generated_at?: string;
    devices?: NetworkDevice[];
  };
};

export type AppState = {
  generatedAt: string;
  config: LoadedConfig;
  overview: Overview;
  runtimeStatus: RuntimeStatus;
  logs: string[] | null;
  discovered: DiscoveredService[];
  statusJSON: string;
  overviewJSON: string;
  networkJSON: string;
};

export type ActionResult = {
  message: string;
  state: AppState;
};
