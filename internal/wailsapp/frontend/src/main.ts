import "./style.css";

import { backend } from "./backend";
import type {
  ActionResult,
  AppState,
  BindConfig,
  DiscoveredService,
  EditableConfig,
  NetworkDevice,
  PublishConfig,
} from "./types";

type PageName = "dashboard" | "services" | "devices" | "settings" | "diagnostics";

type PageMeta = {
  eyebrow: string;
  title: string;
  intro: string;
};

type TableCell = string | Node;

const pageMeta: Record<PageName, PageMeta> = {
  dashboard: {
    eyebrow: "状态与拓扑",
    title: "总览",
    intro: "查看当前连接、服务发布和风险信号。",
  },
  services: {
    eyebrow: "服务编排",
    title: "服务",
    intro: "管理 publish / bind，并根据发现到的远端服务快速建图。",
  },
  devices: {
    eyebrow: "在线设备",
    title: "网络",
    intro: "查看在线设备与服务分布，并在必要时执行踢出。",
  },
  settings: {
    eyebrow: "连接参数",
    title: "连接",
    intro: "维护服务端、凭据、日志级别和开机启动。",
  },
  diagnostics: {
    eyebrow: "深度诊断",
    title: "诊断",
    intro: "查看 overview、runtime/status、network 和运行日志。",
  },
};

let appState: AppState | null = null;
let workingConfig: EditableConfig | null = null;
let activePage: PageName = "dashboard";
const dateTimeFormatter = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

function must<T extends HTMLElement>(id: string): T {
  const node = document.getElementById(id);
  if (!node) {
    throw new Error(`missing element: ${id}`);
  }
  return node as T;
}

function button(id: string): HTMLButtonElement {
  return must<HTMLButtonElement>(id);
}

function input(id: string): HTMLInputElement {
  return must<HTMLInputElement>(id);
}

function select(id: string): HTMLSelectElement {
  return must<HTMLSelectElement>(id);
}

function text(value: unknown, fallback = "-"): string {
  if (value === null || value === undefined) {
    return fallback;
  }
  const normalized = String(value).trim();
  return normalized === "" ? fallback : normalized;
}

function lines(values: Array<string | undefined | null | false>): string {
  return values.filter(Boolean).join("\n");
}

function normalizeLogs(values: string[] | null | undefined): string[] {
  return Array.isArray(values) ? values : [];
}

function formatTimestamp(value: unknown, fallback = "-"): string {
  const normalized = text(value, "");
  if (!normalized || normalized.startsWith("0001-01-01T00:00:00")) {
    return fallback;
  }

  const parsed = new Date(normalized);
  if (Number.isNaN(parsed.getTime())) {
    return normalized;
  }
  return dateTimeFormatter.format(parsed).replace(/\//g, "-");
}

function formatError(error: unknown): string {
  if (error instanceof Error) {
    return text(error.message, "发生未知错误");
  }
  return text(error, "发生未知错误");
}

function tail(values: string[] | null | undefined, limit: number): string {
  const logLines = normalizeLogs(values);
  if (logLines.length === 0) {
    return "暂无日志";
  }
  return logLines.slice(-Math.max(limit, 1)).join("\n");
}

function issueList(state: AppState): string[] {
  const overview = state.overview;
  const runtimeStatus = state.runtimeStatus;
  return [
    text(overview.config_error, ""),
    text(runtimeStatus.last_error, ""),
    text(overview.status_error, ""),
    text(overview.network_error, ""),
  ].filter(Boolean);
}

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function setMessage(message: string, isError = false): void {
  const el = must<HTMLElement>("message-bar");
  const normalized = text(message, "准备就绪");
  el.textContent = normalized;
  el.title = normalized;
  el.parentElement?.classList.toggle("is-error", isError);
}

function syncConfigFromForms(): void {
  if (!workingConfig) {
    return;
  }

  workingConfig.serverURL = input("server-url").value.trim();
  workingConfig.allowInsecureHTTP = input("allow-insecure").checked;
  workingConfig.password = input("password").value;
  workingConfig.clearPassword = input("clear-password").checked;
  workingConfig.adminPassword = input("admin-password").value;
  workingConfig.clearAdminPassword = input("clear-admin-password").checked;
  workingConfig.deviceName = input("device-name").value.trim();
  workingConfig.autoConnect = input("auto-connect").checked;
  workingConfig.udpListen = input("udp-listen").value.trim();
  workingConfig.adminListen = input("admin-listen").value.trim();
  workingConfig.logLevel = select("log-level").value;
}

function currentConfig(): EditableConfig | null {
  if (!workingConfig) {
    return null;
  }
  syncConfigFromForms();
  return clone(workingConfig);
}

function fillForms(config: EditableConfig): void {
  const configPath = text(appState?.config.configPath);
  const configPathNode = must<HTMLElement>("sidebar-config-path");
  configPathNode.textContent = configPath;
  configPathNode.title = configPath;
  input("server-url").value = config.serverURL || "";
  input("allow-insecure").checked = !!config.allowInsecureHTTP;
  input("password").value = "";
  input("clear-password").checked = !!config.clearPassword;
  input("admin-password").value = "";
  input("clear-admin-password").checked = !!config.clearAdminPassword;
  input("device-name").value = config.deviceName || "";
  input("auto-connect").checked = !!config.autoConnect;
  input("udp-listen").value = config.udpListen || "";
  input("admin-listen").value = config.adminListen || "";
  select("log-level").value = config.logLevel || "info";
  must<HTMLElement>("password-hint").textContent = config.passwordConfigured
    ? "组网密码已保存，留空表示不修改。"
    : "组网密码未保存。";
  must<HTMLElement>("admin-password-hint").textContent = config.adminPasswordConfigured
    ? "管理密码已保存，留空表示不修改。"
    : "管理密码未保存。";
}

function renderSelectOptions(id: string, names: string[]): void {
  const element = select(id);
  const previous = element.value;
  element.replaceChildren();

  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = names.length ? "请选择" : "当前为空";
  element.appendChild(placeholder);

  names.forEach((name) => {
    const option = document.createElement("option");
    option.value = name;
    option.textContent = name;
    element.appendChild(option);
  });

  if (names.includes(previous)) {
    element.value = previous;
  }
}

function createTable(headers: string[], rows: TableCell[][]): HTMLElement {
  if (rows.length === 0) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "当前没有可展示的数据。";
    return empty;
  }

  const table = document.createElement("table");
  const thead = document.createElement("thead");
  const headerRow = document.createElement("tr");
  headers.forEach((header) => {
    const th = document.createElement("th");
    th.textContent = header;
    headerRow.appendChild(th);
  });
  thead.appendChild(headerRow);

  const tbody = document.createElement("tbody");
  rows.forEach((cells) => {
    const tr = document.createElement("tr");
    cells.forEach((cell) => {
      const td = document.createElement("td");
      if (typeof cell === "string") {
        td.textContent = cell;
      } else {
        td.appendChild(cell);
      }
      tr.appendChild(td);
    });
    tbody.appendChild(tr);
  });

  table.appendChild(thead);
  table.appendChild(tbody);
  return table;
}

function renderDashboard(state: AppState): void {
  const overview = state.overview;
  const cfg = overview.config ?? {};
  const status = overview.status ?? {};
  const runtimeStatus = state.runtimeStatus;
  const issues = issueList(state);

  const heroTitle = issues.length > 0
    ? "当前有需要关注的问题"
    : overview.config_valid && runtimeStatus.state === "running" && overview.status
      ? "组网已上线"
      : overview.config_valid
        ? "配置已就位，等待连接"
        : "先完成配置，再开始接入";

  const heroDetail = issues.length > 0
    ? issues[0]
    : overview.config_valid && runtimeStatus.state === "running" && overview.status
      ? lines([
          `设备名称: ${text(status.device_name)}`,
          `公网 UDP: ${text(status.observed_addr)}`,
          `网络状态: ${text(status.network_state)}`,
        ])
      : lines([
          `服务端地址: ${text(cfg.server_url)}`,
          `设备名称: ${text(cfg.device_name)}`,
        ]);

  must<HTMLElement>("hero-title").textContent = heroTitle;
  must<HTMLElement>("hero-detail").textContent = heroDetail;

  must<HTMLElement>("runtime-summary").textContent = lines([
    `客户端状态: ${text(runtimeStatus.state)}`,
    `启动时间: ${formatTimestamp(runtimeStatus.started_at)}`,
    `公网 UDP: ${text(status.observed_addr)}`,
    `网络状态: ${text(status.network_state)}`,
    `Peer 数量: ${Array.isArray(status.peers) ? status.peers.length : 0}`,
  ]);

  must<HTMLElement>("service-summary").textContent = lines([
    `发布数量: ${Object.keys(workingConfig?.publish ?? {}).length}`,
    `绑定数量: ${Object.keys(workingConfig?.binds ?? {}).length}`,
    `发现服务: ${state.discovered.length}`,
    `活跃服务代理: ${status.active_service_proxies ?? 0}`,
  ]);

  must<HTMLElement>("alert-summary").textContent = issues.length > 0
    ? issues.slice(0, 4).join("\n")
    : "当前没有阻断性问题";

  must<HTMLElement>("topology-summary").textContent = lines([
    `设备名称: ${text(status.device_name ?? cfg.device_name)}`,
    `设备 ID: ${text(status.device_id)}`,
    `在线设备: ${overview.network?.devices?.length ?? 0}`,
    `Peer 数量: ${Array.isArray(status.peers) ? status.peers.length : 0}`,
    `发布 / 绑定: ${Object.keys(workingConfig?.publish ?? {}).length} / ${Object.keys(workingConfig?.binds ?? {}).length}`,
  ]);

  must<HTMLElement>("log-tail").textContent = tail(state.logs, 12);
}

function renderServices(state: AppState): void {
  const publish = workingConfig?.publish ?? {};
  const binds = workingConfig?.binds ?? {};

  renderSelectOptions("publish-select", Object.keys(publish).sort());
  renderSelectOptions("bind-select", Object.keys(binds).sort());

  const publishRows = Object.entries(publish)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([name, item]) => [
      text(name),
      text(item.protocol, "udp"),
      text(item.local),
    ]);

  const bindRows = Object.entries(binds)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([name, item]) => [
      text(name),
      text(item.protocol, "udp"),
      text(item.peer),
      text(item.service),
      text(item.local),
    ]);

  const servicesCurrent = must<HTMLElement>("services-current");
  const currentWrap = document.createElement("div");
  currentWrap.className = "table-wrap-inner";

  const publishTitle = document.createElement("h4");
  publishTitle.textContent = "当前发布";
  currentWrap.appendChild(publishTitle);
  currentWrap.appendChild(createTable(["名称", "协议", "本地地址"], publishRows));

  const bindTitle = document.createElement("h4");
  bindTitle.textContent = "当前绑定";
  currentWrap.appendChild(bindTitle);
  currentWrap.appendChild(createTable(["名称", "协议", "远端设备", "远端服务", "本地地址"], bindRows));

  servicesCurrent.replaceChildren(currentWrap);

  const discoveredRows = state.discovered.map((service) => {
    const action = document.createElement("button");
    action.className = "primary";
    action.textContent = "一键绑定";
    action.addEventListener("click", async () => {
      await runAction(() => backend()?.QuickBindDiscovered(requireConfig(), service));
    });

    return [
      text(service.deviceName),
      text(service.serviceName),
      text(service.protocol, "udp"),
      text(service.deviceID),
      action,
    ];
  });
  must<HTMLElement>("discovered-table").replaceChildren(createTable(
    ["设备", "服务", "协议", "设备 ID", "操作"],
    discoveredRows,
  ));
}

function networkServiceLabel(device: NetworkDevice): string {
  if (device.service_details && device.service_details.length > 0) {
    return device.service_details
      .map((item) => (item.protocol ? `${item.name}/${item.protocol}` : item.name))
      .join(", ");
  }
  return Array.isArray(device.services) && device.services.length > 0
    ? device.services.join(", ")
    : "-";
}

function renderDevices(state: AppState): void {
  const rows = (state.overview.network?.devices ?? []).map((device) => [
    text(device.device_name),
    text(device.device_id),
    text(device.state),
    text(device.observed_addr),
    text(networkServiceLabel(device)),
    formatTimestamp(device.last_seen),
  ]);
  must<HTMLElement>("devices-table").replaceChildren(createTable(
    ["设备", "设备 ID", "状态", "公网 UDP", "服务", "最后看到"],
    rows,
  ));
}

function renderDiagnostics(state: AppState): void {
  const logs = normalizeLogs(state.logs);
  must<HTMLElement>("overview-json").textContent = state.overviewJSON || "{}";
  must<HTMLElement>("status-json").textContent = state.statusJSON || "{}";
  must<HTMLElement>("network-json").textContent = state.networkJSON || "{}";
  must<HTMLElement>("logs-json").textContent = tail(logs, logs.length || 1);
}

function renderChrome(state: AppState): void {
  const status = state.overview.status ?? {};
  const runtimeStatus = state.runtimeStatus;
  const sidebarDevice = must<HTMLElement>("sidebar-device");
  const sidebarRuntime = must<HTMLElement>("sidebar-runtime");
  const runtimeLabel = text(runtimeStatus.state);
  const deviceLabel = text(status.device_name ?? state.overview.config?.device_name);

  sidebarRuntime.textContent = runtimeLabel;
  sidebarRuntime.title = runtimeLabel;
  sidebarDevice.textContent = deviceLabel;
  sidebarDevice.title = deviceLabel;
  must<HTMLElement>("runtime-state").textContent = runtimeLabel;
  must<HTMLElement>("network-state").textContent = text(status.network_state);
  must<HTMLElement>("last-refresh").textContent = formatTimestamp(state.generatedAt);
  must<HTMLElement>("last-refresh").title = text(state.generatedAt);
}

function applyState(state: AppState, message = ""): void {
  appState = state;
  workingConfig = clone(state.config.config);
  fillForms(workingConfig);
  renderChrome(state);
  renderDashboard(state);
  renderServices(state);
  renderDevices(state);
  renderDiagnostics(state);
  if (message) {
    setMessage(message);
  }
}

function showPage(page: PageName): void {
  activePage = page;
  document.querySelectorAll<HTMLElement>(".page").forEach((node) => {
    node.classList.toggle("active", node.id === `page-${page}`);
  });
  document.querySelectorAll<HTMLButtonElement>(".nav-button").forEach((node) => {
    node.classList.toggle("active", node.dataset.page === page);
  });

  const meta = pageMeta[page];
  must<HTMLElement>("page-eyebrow").textContent = meta.eyebrow;
  must<HTMLElement>("page-title").textContent = meta.title;
  must<HTMLElement>("page-intro").textContent = meta.intro;
}

function loadSelectedPublish(): void {
  const name = select("publish-select").value;
  const item = workingConfig?.publish?.[name];
  if (!item) {
    return;
  }
  select("publish-protocol").value = item.protocol || "udp";
  input("publish-name").value = name;
  input("publish-local").value = item.local || "";
}

function loadSelectedBind(): void {
  const name = select("bind-select").value;
  const item = workingConfig?.binds?.[name];
  if (!item) {
    return;
  }
  select("bind-protocol").value = item.protocol || "udp";
  input("bind-name").value = name;
  input("bind-peer").value = item.peer || "";
  input("bind-service").value = item.service || "";
  input("bind-local").value = item.local || "";
}

function requireConfig(): EditableConfig {
  const value = currentConfig();
  if (!value) {
    throw new Error("frontend config state is not ready");
  }
  return value;
}

async function runAction<T extends ActionResult | AppState | null | undefined>(
  action: () => Promise<T> | T,
): Promise<void> {
  try {
    const result = await action();
    if (!result) {
      setMessage("操作已完成");
      return;
    }
    if ("state" in result) {
      applyState(result.state, result.message);
      return;
    }
    if ("config" in result) {
      applyState(result, "已刷新");
      return;
    }
    setMessage("操作已完成");
  } catch (error) {
    setMessage(formatError(error), true);
  }
}

function bindEvents(): void {
  document.querySelectorAll<HTMLButtonElement>(".nav-button").forEach((node) => {
    const page = node.dataset.page as PageName | undefined;
    if (!page) {
      return;
    }
    node.addEventListener("click", () => showPage(page));
  });

  button("dashboard-services").addEventListener("click", () => showPage("services"));
  button("dashboard-devices").addEventListener("click", () => showPage("devices"));
  button("dashboard-diagnostics").addEventListener("click", () => showPage("diagnostics"));

  button("refresh-button").addEventListener("click", async () => {
    await runAction(() => backend()?.Refresh(requireConfig()));
  });
  button("save-button").addEventListener("click", async () => {
    await runAction(() => backend()?.SaveConfig(requireConfig()));
  });
  button("start-button").addEventListener("click", async () => {
    await runAction(() => backend()?.StartClient(requireConfig()));
  });
  button("stop-button").addEventListener("click", async () => {
    await runAction(() => backend()?.StopClient());
  });
  button("apply-log-level").addEventListener("click", async () => {
    await runAction(() => backend()?.ApplyLogLevel(requireConfig()));
  });
  button("install-autostart").addEventListener("click", async () => {
    await runAction(() => backend()?.InstallAutostart(requireConfig()));
  });
  button("uninstall-autostart").addEventListener("click", async () => {
    await runAction(() => backend()?.UninstallAutostart());
  });
  button("publish-load").addEventListener("click", loadSelectedPublish);
  button("publish-save").addEventListener("click", async () => {
    await runAction(() =>
      backend()?.UpsertPublish(
        requireConfig(),
        input("publish-name").value,
        select("publish-protocol").value,
        input("publish-local").value,
      ),
    );
  });
  button("publish-delete").addEventListener("click", async () => {
    await runAction(() => backend()?.DeletePublish(requireConfig(), select("publish-select").value));
  });
  button("bind-load").addEventListener("click", loadSelectedBind);
  button("bind-save").addEventListener("click", async () => {
    await runAction(() =>
      backend()?.UpsertBind(
        requireConfig(),
        input("bind-name").value,
        select("bind-protocol").value,
        input("bind-peer").value,
        input("bind-service").value,
        input("bind-local").value,
      ),
    );
  });
  button("bind-delete").addEventListener("click", async () => {
    await runAction(() => backend()?.DeleteBind(requireConfig(), select("bind-select").value));
  });
  button("kick-button").addEventListener("click", async () => {
    await runAction(() =>
      backend()?.KickDevice(
        requireConfig(),
        input("kick-name").value,
        input("kick-id").value,
      ),
    );
  });
}

async function bootstrap(): Promise<void> {
  const api = backend();
  if (!api) {
    setMessage("Wails backend 未就绪。", true);
    return;
  }

  bindEvents();

  try {
    const state = await api.State();
    applyState(state, "准备就绪");
    showPage(activePage);
  } catch (error) {
    setMessage(formatError(error), true);
  }
}

void bootstrap();
