<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { getCurrentWindow } from "@tauri-apps/api/window";
import {
  desktopSnapshot,
  heartbeat,
  joinNetwork,
  loadConfig,
  publishServices,
  refreshNetwork,
  refreshStatus,
  saveConfig,
  startManagedAgent,
  stopManagedAgent,
  toggleAutostart,
} from "./lib/api";

const tabs = [
  { key: "overview", label: "总览" },
  { key: "control", label: "控制面" },
  { key: "services", label: "已发布服务" },
  { key: "forwards", label: "转发规则" },
  { key: "network", label: "在线设备" },
  { key: "logs", label: "日志" },
  { key: "raw", label: "原始配置" },
];

const currentTab = ref("overview");
const busyLabel = ref("");
const notice = ref(null);
const snapshot = ref(null);
let draftKeySeed = 0;
const form = reactive({
  config_path: "",
  server_url: "",
  join_token: "",
  device_name: "",
  service_drafts: [],
  forward_drafts: [],
});

let pollTimer = null;

const hasSnapshot = computed(() => Boolean(snapshot.value));
const busy = computed(() => busyLabel.value.length > 0);
const publishedCount = computed(() => form.service_drafts.length);
const forwardCount = computed(() => form.forward_drafts.length);
const onlineDevices = computed(() => {
  const devices = snapshot.value?.network_snapshot?.devices ?? [];
  return devices.filter((device) => device.is_online);
});
const hasConfigPath = computed(() => form.config_path.trim().length > 0);
const hasServerUrl = computed(() => form.server_url.trim().length > 0);
const hasJoinToken = computed(() => form.join_token.trim().length > 0);
const hasDeviceName = computed(() => form.device_name.trim().length > 0);
const hasJoinedNetwork = computed(() => Boolean(snapshot.value?.status_report?.device_id));
const isAgentRunning = computed(() => snapshot.value?.managed_agent_state === "running");
const isAgentStopping = computed(() => snapshot.value?.managed_agent_state === "stopping");
const canLoadConfig = computed(() => !busy.value && hasConfigPath.value);
const canSaveConfig = computed(() => !busy.value && hasConfigPath.value);
const canJoinNetwork = computed(
  () =>
    !busy.value &&
    hasConfigPath.value &&
    hasServerUrl.value &&
    hasJoinToken.value &&
    hasDeviceName.value,
);
const canHeartbeat = computed(
  () => !busy.value && hasConfigPath.value && hasJoinedNetwork.value,
);
const canRefreshNetwork = computed(
  () => !busy.value && hasConfigPath.value && hasJoinedNetwork.value,
);
const canRefreshStatus = computed(() => !busy.value && hasConfigPath.value);
const canStartAgent = computed(
  () =>
    !busy.value &&
    hasConfigPath.value &&
    !isAgentRunning.value &&
    !isAgentStopping.value,
);
const canStopAgent = computed(
  () => !busy.value && isAgentRunning.value && !isAgentStopping.value,
);
const canToggleAutostart = computed(() => !busy.value && hasConfigPath.value);
const canPublishServices = computed(
  () => !busy.value && hasConfigPath.value && hasServerUrl.value && publishedCount.value > 0,
);
const canReloadRawConfig = computed(() => !busy.value && hasConfigPath.value);
const sessionSummaryDisplay = computed(() => {
  const session = snapshot.value?.status_report?.session;
  if (!session?.has_token) {
    return "没有已保存设备会话";
  }
  if (session.expires_at) {
    return `${session.is_valid ? "" : "已过期，"}过期时间[${formatTimestamp(session.expires_at)}]`;
  }
  return "已保存设备会话令牌，但没有过期时间。";
});

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function nextDraftKey(prefix) {
  draftKeySeed += 1;
  return `${prefix}-${draftKeySeed}`;
}

function normalizeServiceDraft(draft, fallbackKey = null) {
  return {
    ...draft,
    __ui_key: draft.__ui_key ?? fallbackKey ?? nextDraftKey("service"),
  };
}

function normalizeForwardDraft(draft, fallbackKey = null) {
  return {
    ...draft,
    __ui_key: draft.__ui_key ?? fallbackKey ?? nextDraftKey("forward"),
  };
}

function normalizeServiceDrafts(drafts, previousDrafts = []) {
  return drafts.map((draft, index) =>
    normalizeServiceDraft(draft, previousDrafts[index]?.__ui_key ?? null),
  );
}

function normalizeForwardDrafts(drafts, previousDrafts = []) {
  return drafts.map((draft, index) =>
    normalizeForwardDraft(draft, previousDrafts[index]?.__ui_key ?? null),
  );
}

function serializeServiceDrafts(drafts) {
  return drafts.map(({ __ui_key, ...draft }) => clone(draft));
}

function serializeForwardDrafts(drafts) {
  return drafts.map(({ __ui_key, ...draft }) => clone(draft));
}

function emptyServiceDraft() {
  return normalizeServiceDraft({
    name: "ssh",
    target_host: "127.0.0.1",
    target_port: "22",
    allowed_device_ids: "",
    direct_enabled: false,
    direct_udp_bind_addr: "",
    direct_candidate_type: "local",
    direct_wait_seconds: "5",
  });
}

function emptyForwardDraft() {
  return normalizeForwardDraft({
    name: "office-ssh",
    target_device_id: "",
    service_name: "ssh",
    local_bind_addr: "127.0.0.1:10022",
    enabled: true,
    transport_mode: "relay",
    direct_udp_bind_addr: "",
    direct_candidate_type: "local",
    direct_wait_seconds: "5",
  });
}

function showNotice(level, title, message) {
  notice.value = {
    level,
    title,
    message,
    created_at: Date.now(),
  };
}

function syncFormFromSnapshot(next) {
  const previousServiceDrafts = form.service_drafts;
  const previousForwardDrafts = form.forward_drafts;
  form.config_path = next.config_path ?? "";
  form.server_url = next.server_url ?? "";
  form.device_name = next.device_name ?? "";
  form.service_drafts = normalizeServiceDrafts(
    clone(next.service_drafts ?? []),
    previousServiceDrafts,
  );
  form.forward_drafts = normalizeForwardDrafts(
    clone(next.forward_drafts ?? []),
    previousForwardDrafts,
  );
}

function applySnapshot(next, options = {}) {
  snapshot.value = next;
  if (options.syncConfig) {
    syncFormFromSnapshot(next);
  } else if (!form.config_path && next.config_path) {
    form.config_path = next.config_path;
  }
}

function buildSavePayload() {
  return {
    config_path: form.config_path,
    server_url: form.server_url,
    device_name: form.device_name,
    service_drafts: serializeServiceDrafts(form.service_drafts),
    forward_drafts: serializeForwardDrafts(form.forward_drafts),
  };
}

function buildSimplePayload() {
  return {
    config_path: form.config_path,
  };
}

async function runAction(label, action, options = {}) {
  if (busy.value) {
    return;
  }
  busyLabel.value = label;
  showNotice("info", `正在${label}`, "操作已经开始，完成后会在这里给出结果。");
  try {
    const response = await action();
    applySnapshot(response.snapshot, { syncConfig: options.syncConfig ?? false });
    if (response.clear_join_token || options.clearJoinToken) {
      form.join_token = "";
    }
    showNotice(
      response.notice_level ?? "success",
      response.notice_title ?? `${label}成功`,
      response.notice_message ?? `${label}已完成`,
    );
  } catch (error) {
    showNotice("error", `${label}失败`, String(error));
  } finally {
    busyLabel.value = "";
  }
}

async function refreshPassive() {
  if (busy.value) {
    return;
  }
  try {
    const next = await desktopSnapshot(form.config_path);
    applySnapshot(next, { syncConfig: false });
  } catch {
    // 被动刷新失败时保持静默，不打断当前编辑。
  }
}

async function bootstrap() {
  try {
    const response = await loadConfig(buildSimplePayload());
    applySnapshot(response.snapshot, { syncConfig: true });
    showNotice("success", "配置已加载", response.notice_message);
  } catch {
    const next = await desktopSnapshot(form.config_path);
    applySnapshot(next, { syncConfig: true });
  }
}

function startPolling() {
  pollTimer = window.setInterval(refreshPassive, 1500);
}

function stopPolling() {
  if (pollTimer) {
    window.clearInterval(pollTimer);
    pollTimer = null;
  }
}

function addServiceDraft() {
  form.service_drafts.push(emptyServiceDraft());
}

function removeServiceDraft(index) {
  form.service_drafts.splice(index, 1);
}

function addForwardDraft() {
  form.forward_drafts.push(emptyForwardDraft());
}

function removeForwardDraft(index) {
  form.forward_drafts.splice(index, 1);
}

function slugify(raw) {
  const normalized = raw
    .toLowerCase()
    .replace(/[^a-z0-9\s_-]/g, "")
    .replace(/[\s_]+/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "");
  return normalized || "forward";
}

function parsePort(bindAddr) {
  const parts = String(bindAddr ?? "").split(":");
  const value = Number(parts[parts.length - 1]);
  return Number.isInteger(value) ? value : null;
}

function suggestForwardName(deviceName, serviceName) {
  const base = `${slugify(deviceName)}-${slugify(serviceName)}`;
  let candidate = base;
  let suffix = 2;
  while (form.forward_drafts.some((draft) => draft.name === candidate)) {
    candidate = `${base}-${suffix}`;
    suffix += 1;
  }
  return candidate;
}

function suggestLocalBindAddr(serviceName) {
  const key = String(serviceName ?? "").trim().toLowerCase();
  let port = {
    ssh: 10022,
    rdp: 13389,
    "remote-desktop": 13389,
    http: 18080,
    https: 18443,
    mysql: 13306,
    postgres: 15432,
    postgresql: 15432,
  }[key] ?? 12000;
  const used = form.forward_drafts
    .map((draft) => parsePort(draft.local_bind_addr))
    .filter((value) => value !== null);
  while (used.includes(port)) {
    port += 1;
  }
  return `127.0.0.1:${port}`;
}

function servicesForDevice(deviceId) {
  return (snapshot.value?.network_snapshot?.services ?? []).filter(
    (service) => service.owner_device_id === deviceId,
  );
}

function isServiceAlreadyForwarded(targetDeviceId, serviceName) {
  return form.forward_drafts.some(
    (draft) =>
      draft.target_device_id === targetDeviceId &&
      draft.service_name === serviceName,
  );
}

function addForwardFromService(device, service) {
  form.forward_drafts.push(normalizeForwardDraft({
    name: suggestForwardName(device.device_name, service.name),
    target_device_id: device.device_id,
    service_name: service.name,
    local_bind_addr: suggestLocalBindAddr(service.name),
    enabled: true,
    transport_mode: "relay",
    direct_udp_bind_addr: "",
    direct_candidate_type: "local",
    direct_wait_seconds: "5",
  }));
  currentTab.value = "forwards";
  showNotice(
    "success",
    "已添加转发草稿",
    `已为设备“${device.device_name}”的服务“${service.name}”生成本地转发。`,
  );
}

async function hideToTray() {
  await getCurrentWindow().hide();
}

function formatTimestamp(timestamp) {
  if (!timestamp) {
    return "-";
  }
  const date = new Date(Number(timestamp) * 1000);
  if (Number.isNaN(date.getTime())) {
    return String(timestamp);
  }
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  const hour = String(date.getHours()).padStart(2, "0");
  const minute = String(date.getMinutes()).padStart(2, "0");
  const second = String(date.getSeconds()).padStart(2, "0");
  return `${year}-${month}-${day} ${hour}:${minute}:${second}`;
}

function displayState(state) {
  return {
    ready: "就绪",
    online: "在线",
    fresh: "最新",
    running: "运行中",
    starting: "启动中",
    unknown: "未知",
    disabled: "已禁用",
    stopped: "已停止",
    offline: "离线",
    target_offline: "目标离线",
    registered_offline: "已注册但离线",
    degraded: "降级",
    stopping: "停止中",
    retrying: "重试中",
    restarting: "重启中",
    stale: "过期",
    service_missing: "服务缺失",
    target_missing: "目标缺失",
    not_synced: "未同步",
    not_joined: "未入网",
    failed: "失败",
    missing: "缺失",
    enabled: "已启用",
    unavailable: "不可用",
    pending: "待定",
    relay: "中继",
    auto: "自动",
    direct: "直连",
    relay_active: "中继中",
    direct_active: "直连中",
    relay_fallback: "回落中继",
    direct_retry: "重试直连",
    direct_retry_deferred: "延后重试直连",
    direct_ready: "直连就绪",
    direct_handoff_fallback: "直连切换回落",
    starting_relay: "启动中继",
    waiting: "等待中",
  }[state] ?? state;
}

function displayEventLevel(level) {
  return {
    info: "信息",
    warn: "警告",
    error: "错误",
  }[level] ?? level;
}

function noticeClass(level) {
  return {
    info: "notice-info",
    success: "notice-success",
    error: "notice-error",
  }[level] ?? "notice-info";
}

function stateClass(state) {
  if (["ready", "online", "fresh", "running", "direct_active", "relay_active"].includes(state)) {
    return "state-good";
  }
  if (
    [
      "degraded",
      "stale",
      "retrying",
      "restarting",
      "direct_retry",
      "direct_retry_deferred",
      "relay_fallback",
      "stopping",
    ].includes(state)
  ) {
    return "state-warn";
  }
  if (
    [
      "failed",
      "missing",
      "service_missing",
      "target_missing",
      "offline",
      "target_offline",
      "registered_offline",
      "not_synced",
      "not_joined",
    ].includes(state)
  ) {
    return "state-bad";
  }
  return "state-neutral";
}

const runtime = computed(() => snapshot.value?.observed_runtime ?? null);
const autostartStatus = computed(() => snapshot.value?.autostart_status ?? null);

onMounted(async () => {
  await bootstrap();
  startPolling();
});

onBeforeUnmount(() => {
  stopPolling();
});
</script>

<template>
  <div class="shell">
    <header class="hero">
      <div>
        <p class="eyebrow">Tauri + Vue 桌面控制台</p>
        <h1>MiniPunch</h1>
        <p class="subtitle">
          面向 5 台以内设备的轻量 TCP 互联工作台。把控制面、转发、在线设备和运行观测拆成独立页签，保持清爽。
        </p>
      </div>
      <div class="hero-meta" v-if="hasSnapshot">
        <div class="summary-pill">
          <span>设备</span>
          <strong>{{ snapshot.device_id }}</strong>
        </div>
        <div class="summary-pill">
          <span>设备会话</span>
          <strong>{{ sessionSummaryDisplay }}</strong>
        </div>
      </div>
    </header>

    <section class="tabs">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        class="tab"
        :class="{ active: currentTab === tab.key }"
        @click="currentTab = tab.key"
      >
        {{ tab.label }}
      </button>
    </section>

    <div v-if="busy" class="loading-mask" role="status" aria-live="polite">
      <div class="loading-dialog">
        <div class="spinner spinner-lg"></div>
        <strong>正在{{ busyLabel }}</strong>
        <p>请稍候，完成后会自动更新当前页面状态。</p>
      </div>
    </div>

    <section v-if="notice" class="notice" :class="noticeClass(notice.level)">
      <div>
        <strong>{{ notice.title }}</strong>
        <p>{{ notice.message }}</p>
      </div>
      <small>{{ formatTimestamp(Math.floor(notice.created_at / 1000)) }}</small>
    </section>

    <main class="content">
      <template v-if="currentTab === 'overview'">
        <section class="panel">
          <div class="panel-header">
            <div>
              <h2>总览</h2>
              <p>这里集中展示设备、会话、本地运行和配置规模摘要。</p>
            </div>
          </div>
          <div class="summary-grid">
            <article class="summary-card soft-blue">
              <span>设备</span>
              <strong>{{ snapshot?.device_id ?? "尚未加入网络" }}</strong>
            </article>
            <article class="summary-card soft-green">
              <span>设备会话</span>
              <strong>{{ sessionSummaryDisplay }}</strong>
            </article>
            <article class="summary-card soft-mint">
              <span>已发布</span>
              <strong>{{ publishedCount }}</strong>
            </article>
            <article class="summary-card soft-indigo">
              <span>转发</span>
              <strong>{{ forwardCount }}</strong>
            </article>
            <article class="summary-card soft-lilac">
              <span>受管运行</span>
              <strong>{{ displayState(snapshot?.managed_agent_state ?? "stopped") }}</strong>
            </article>
            <article class="summary-card soft-gray">
              <span>运行观测</span>
              <strong>{{ displayState(runtime?.status ?? "missing") }}</strong>
            </article>
          </div>
        </section>

        <section class="panel">
          <div class="panel-header">
            <div>
              <h2>运行控制</h2>
              <p>启动或停止本地受管 Agent，切换开机自启，并快速查看当前观测摘要。</p>
            </div>
            <div class="toolbar toolbar-grid toolbar-grid-wide">
              <button class="action-button" :disabled="!canStartAgent" @click="runAction('启动本地 Agent', () => startManagedAgent(buildSavePayload()), { syncConfig: false })">启动 Agent</button>
              <button class="action-button secondary" :disabled="!canStopAgent" @click="runAction('停止本地 Agent', () => stopManagedAgent(buildSimplePayload()), { syncConfig: false })">停止 Agent</button>
              <button class="action-button secondary" :disabled="!canRefreshStatus" @click="runAction('刷新状态', () => refreshStatus(buildSimplePayload()), { syncConfig: false })">刷新状态</button>
              <button class="action-button ghost" :disabled="busy" @click="hideToTray">隐藏到托盘</button>
              <button class="action-button ghost" :disabled="!canToggleAutostart" @click="runAction('切换开机自启', () => toggleAutostart(buildSimplePayload()), { syncConfig: false })">
                {{ autostartStatus?.enabled ? "关闭开机自启" : "开启开机自启" }}
              </button>
            </div>
          </div>
          <div class="info-list">
            <div class="info-row">
              <span>受管运行说明</span>
              <strong>{{ snapshot?.managed_agent_note ?? "桌面端尚未启动受管 Agent。" }}</strong>
            </div>
            <div class="info-row">
              <span>运行观测说明</span>
              <strong>{{ snapshot?.observed_runtime_note ?? "还没有本地运行态观测。" }}</strong>
            </div>
            <div class="info-row" v-if="autostartStatus">
              <span>开机自启</span>
              <strong>
                {{ autostartStatus.detail }}
                <em>[{{ autostartStatus.platform_label }}]</em>
              </strong>
            </div>
          </div>
        </section>

        <section class="two-column">
          <article class="panel compact-panel">
            <div class="panel-header">
              <div>
                <h2>已发布服务状态</h2>
                <p>根据当前配置和最近一次网络快照推导。</p>
              </div>
            </div>
            <div class="status-list" v-if="snapshot?.status_report?.published_services?.length">
              <div
                v-for="service in snapshot.status_report.published_services"
                :key="service.name"
                class="status-item"
                :class="stateClass(service.state)"
              >
                <div class="status-title">
                  <strong>{{ service.name }}</strong>
                  <span>{{ displayState(service.state) }}</span>
                </div>
                <p>{{ service.target_host }}:{{ service.target_port }} | {{ service.detail }}</p>
              </div>
            </div>
            <p v-else class="empty">当前没有已发布服务。</p>
          </article>

          <article class="panel compact-panel">
            <div class="panel-header">
              <div>
                <h2>转发规则状态</h2>
                <p>这里展示本机回环入口和目标服务是否可解析。</p>
              </div>
            </div>
            <div class="status-list" v-if="snapshot?.status_report?.forward_rules?.length">
              <div
                v-for="rule in snapshot.status_report.forward_rules"
                :key="rule.name"
                class="status-item"
                :class="stateClass(rule.state)"
              >
                <div class="status-title">
                  <strong>{{ rule.name }}</strong>
                  <span>{{ displayState(rule.state) }}</span>
                </div>
                <p>{{ rule.local_bind_addr }} -> {{ rule.service_name }} | {{ rule.detail }}</p>
              </div>
            </div>
            <p v-else class="empty">当前没有转发规则。</p>
          </article>
        </section>
      </template>

      <template v-else-if="currentTab === 'control'">
        <section class="panel">
          <div class="panel-header">
            <div>
              <h2>控制面</h2>
              <p>配置本机的控制面地址、入网令牌与设备名，并执行最常用的控制面动作。入网令牌只在首次加入网络时使用，配置里的 <code>session_expires_at</code> 表示设备会话过期时间，不是入网令牌过期时间。</p>
            </div>
          </div>
          <div class="form-grid">
            <label class="field">
              <span>配置路径</span>
              <input v-model="form.config_path" type="text" placeholder="agent.toml 绝对路径" />
            </label>
            <label class="field">
              <span>服务器地址</span>
              <input v-model="form.server_url" type="text" placeholder="http://your-vps:9443" />
            </label>
            <label class="field">
              <span>入网令牌</span>
              <input v-model="form.join_token" type="text" placeholder="join_xxx" />
            </label>
            <label class="field">
              <span>设备名称</span>
              <input v-model="form.device_name" type="text" placeholder="我的设备" />
            </label>
          </div>
          <div class="toolbar toolbar-grid" style="margin-top: 10px;">
            <button class="action-button" :disabled="!canLoadConfig" @click="runAction('加载配置', () => loadConfig(buildSimplePayload()), { syncConfig: true })">加载配置</button>
            <button class="action-button secondary" :disabled="!canSaveConfig" @click="runAction('保存配置', () => saveConfig(buildSavePayload()), { syncConfig: true })">保存配置</button>
            <button class="action-button" :disabled="!canJoinNetwork" @click="runAction('加入网络', () => joinNetwork({ ...buildSimplePayload(), server_url: form.server_url, join_token: form.join_token, device_name: form.device_name }), { syncConfig: true, clearJoinToken: true })">加入网络</button>
            <button class="action-button secondary" :disabled="!canHeartbeat" @click="runAction('发送心跳', () => heartbeat(buildSimplePayload()), { syncConfig: false })">发送心跳</button>
            <button class="action-button secondary" :disabled="!canRefreshNetwork" @click="runAction('读取网络快照', () => refreshNetwork(buildSimplePayload()), { syncConfig: false })">读取网络</button>
            <button class="action-button secondary" :disabled="!canRefreshStatus" @click="runAction('刷新状态', () => refreshStatus(buildSimplePayload()), { syncConfig: false })">刷新状态</button>
          </div>
        </section>
      </template>

      <template v-else-if="currentTab === 'services'">
        <section class="panel">
          <div class="panel-header">
            <div>
              <h2>已发布服务</h2>
              <p>在这里定义其他设备可访问的本机 TCP 服务，可选开启直连 responder。</p>
            </div>
            <div class="toolbar">
              <button class="action-button" :disabled="busy" @click="addServiceDraft">添加服务</button>
              <button class="action-button secondary" :disabled="!canSaveConfig" @click="runAction('保存配置', () => saveConfig(buildSavePayload()), { syncConfig: true })">保存配置</button>
              <button class="action-button" :disabled="!canPublishServices" @click="runAction('同步服务到服务器', () => publishServices(buildSavePayload()), { syncConfig: true })">同步服务到服务器</button>
            </div>
          </div>

          <div v-if="form.service_drafts.length" class="card-stack">
            <article
              v-for="(service, index) in form.service_drafts"
              :key="service.__ui_key"
              class="editor-card"
            >
              <div class="editor-header">
                <strong>{{ service.name || `服务 ${index + 1}` }}</strong>
                <button class="mini-button danger" :disabled="busy" @click="removeServiceDraft(index)">删除</button>
              </div>
              <div class="form-grid">
                <label class="field">
                  <span>名称</span>
                  <input v-model="service.name" type="text" />
                </label>
                <label class="field">
                  <span>主机</span>
                  <input v-model="service.target_host" type="text" />
                </label>
                <label class="field">
                  <span>端口</span>
                  <input v-model="service.target_port" type="text" />
                </label>
                <label class="field">
                  <span>允许设备 ID（逗号分隔）</span>
                  <input v-model="service.allowed_device_ids" type="text" />
                </label>
              </div>
              <div class="transport-block" :class="{ 'transport-block--disabled': !service.direct_enabled }">
                <div class="transport-block__header">
                  <h3>直连参数</h3>
                  <label class="toggle">
                    <input v-model="service.direct_enabled" type="checkbox" />
                    <span>启用直连</span>
                  </label>
                </div>
                <div class="transport-grid">
                  <label class="field small">
                    <span>UDP 绑定</span>
                    <input v-model="service.direct_udp_bind_addr" :disabled="!service.direct_enabled" type="text" />
                  </label>
                  <label class="field tiny">
                    <span>候选类型</span>
                    <input v-model="service.direct_candidate_type" :disabled="!service.direct_enabled" type="text" />
                  </label>
                  <label class="field tiny">
                    <span>等待秒数</span>
                    <input v-model="service.direct_wait_seconds" :disabled="!service.direct_enabled" type="text" />
                  </label>
                </div>
                <p class="transport-note">关闭直连时，这些参数会自动失效，服务将只通过 relay 暴露。</p>
              </div>
            </article>
          </div>
          <p v-else class="empty">还没有已发布服务，点击“添加服务”开始配置。</p>
        </section>
      </template>

      <template v-else-if="currentTab === 'forwards'">
        <section class="panel">
          <div class="panel-header">
            <div>
              <h2>转发规则</h2>
              <p>在本机回环地址上创建入口，把远端设备的服务映射回来。</p>
            </div>
            <div class="toolbar">
              <button class="action-button" :disabled="busy" @click="addForwardDraft">添加转发</button>
              <button class="action-button secondary" :disabled="!canSaveConfig" @click="runAction('保存配置', () => saveConfig(buildSavePayload()), { syncConfig: true })">保存配置</button>
              <button class="action-button" :disabled="!canStartAgent" @click="runAction('启动本地 Agent', () => startManagedAgent(buildSavePayload()), { syncConfig: false })">保存并启动</button>
            </div>
          </div>

          <div v-if="form.forward_drafts.length" class="card-stack">
            <article
              v-for="(rule, index) in form.forward_drafts"
              :key="rule.__ui_key"
              class="editor-card"
            >
              <div class="editor-header">
                <div class="inline-group">
                  <strong>{{ rule.name || `转发 ${index + 1}` }}</strong>
                  <label class="toggle subtle-toggle">
                    <input v-model="rule.enabled" type="checkbox" />
                    <span>启用</span>
                  </label>
                </div>
                <button class="mini-button danger" :disabled="busy" @click="removeForwardDraft(index)">删除</button>
              </div>
              <div class="form-grid">
                <label class="field">
                  <span>名称</span>
                  <input v-model="rule.name" type="text" />
                </label>
                <label class="field">
                  <span>目标设备 ID</span>
                  <input v-model="rule.target_device_id" type="text" />
                </label>
                <label class="field">
                  <span>服务名</span>
                  <input v-model="rule.service_name" type="text" />
                </label>
                <label class="field">
                  <span>本地绑定</span>
                  <input v-model="rule.local_bind_addr" type="text" />
                </label>
              </div>
              <div class="transport-block" :class="{ 'transport-block--disabled': rule.transport_mode !== 'auto' }">
                <div class="transport-block__header">
                  <h3>链路参数</h3>
                  <p>选择 `auto` 后，桌面端会先试直连，失败再自动回落 relay。</p>
                </div>
                <div class="transport-grid transport-grid--four">
                  <label class="field tiny">
                    <span>传输模式</span>
                    <select v-model="rule.transport_mode">
                      <option value="relay">relay</option>
                      <option value="auto">auto</option>
                    </select>
                  </label>
                  <label class="field small">
                    <span>UDP 绑定</span>
                    <input v-model="rule.direct_udp_bind_addr" :disabled="rule.transport_mode !== 'auto'" type="text" />
                  </label>
                  <label class="field tiny">
                    <span>候选类型</span>
                    <input v-model="rule.direct_candidate_type" :disabled="rule.transport_mode !== 'auto'" type="text" />
                  </label>
                  <label class="field tiny">
                    <span>等待秒数</span>
                    <input v-model="rule.direct_wait_seconds" :disabled="rule.transport_mode !== 'auto'" type="text" />
                  </label>
                </div>
              </div>
            </article>
          </div>
          <p v-else class="empty">还没有转发规则，点击“添加转发”开始配置。</p>
        </section>
      </template>

      <template v-else-if="currentTab === 'network'">
        <section class="panel">
          <div class="panel-header">
            <div>
              <h2>在线设备</h2>
              <p>查看当前在线设备及其已发布服务，并一键生成本地转发草稿。</p>
            </div>
            <div class="toolbar">
              <button class="action-button" :disabled="!canRefreshNetwork" @click="runAction('刷新在线设备', () => refreshNetwork(buildSimplePayload()), { syncConfig: false })">刷新在线设备</button>
            </div>
          </div>
          <p class="soft-tip">{{ snapshot?.network_snapshot_note ?? "尚未读取在线设备快照。" }}</p>
          <div v-if="onlineDevices.length" class="card-stack">
            <article v-for="device in onlineDevices" :key="device.device_id" class="editor-card">
              <div class="editor-header">
                <div class="stacked">
                  <strong>{{ device.device_name }}</strong>
                  <span class="muted">设备 ID：{{ device.device_id }}</span>
                </div>
                <span class="status-pill" :class="stateClass(device.is_online ? 'online' : 'offline')">
                  {{ device.is_online ? "在线" : "离线" }}
                </span>
              </div>
              <p class="device-meta">
                系统：{{ device.os }} · 最近心跳：{{ formatTimestamp(device.last_seen_at) }}
              </p>
              <div v-if="servicesForDevice(device.device_id).length" class="service-grid">
                <div
                  v-for="service in servicesForDevice(device.device_id)"
                  :key="service.service_id"
                  class="service-tile"
                >
                  <div>
                    <strong>{{ service.name }}</strong>
                    <p>{{ service.protocol }} · {{ service.service_id }}</p>
                  </div>
                  <button
                    v-if="!isServiceAlreadyForwarded(device.device_id, service.name)"
                    class="mini-button"
                    :disabled="busy"
                    @click="addForwardFromService(device, service)"
                  >
                    添加转发
                  </button>
                  <span v-else class="mini-pill">已存在转发</span>
                </div>
              </div>
              <p v-else class="empty inline-empty">这个在线设备当前没有可见服务。</p>
            </article>
          </div>
          <p v-else class="empty">还没有在线设备快照，或者当前没有在线设备。</p>
        </section>
      </template>

      <template v-else-if="currentTab === 'logs'">
        <section class="panel">
          <div class="panel-header">
            <div>
              <h2>日志</h2>
              <p>集中展示本地运行观测、链路统计、最近事件和最近一次动作输出。</p>
            </div>
          </div>

          <div v-if="runtime" class="logs-section">
            <h3>本地运行观测</h3>
            <div class="summary-grid small-grid">
              <article class="summary-card soft-lilac">
                <span>状态</span>
                <strong>{{ displayState(runtime.status) }}</strong>
              </article>
              <article class="summary-card soft-indigo">
                <span>PID</span>
                <strong>{{ runtime.pid }}</strong>
              </article>
              <article class="summary-card soft-gray">
                <span>重启次数</span>
                <strong>{{ runtime.restart_count }}</strong>
              </article>
              <article class="summary-card soft-mint">
                <span>最近心跳</span>
                <strong>{{ formatTimestamp(runtime.last_heartbeat_ok_at) }}</strong>
              </article>
            </div>
            <div class="info-list">
              <div class="info-row">
                <span>启动时间</span>
                <strong>{{ formatTimestamp(runtime.started_at) }}</strong>
              </div>
              <div class="info-row">
                <span>更新时间</span>
                <strong>{{ formatTimestamp(runtime.updated_at) }}</strong>
              </div>
              <div class="info-row">
                <span>详情</span>
                <strong>{{ runtime.status_detail }}</strong>
              </div>
              <div class="info-row" v-if="runtime.last_error">
                <span>最近错误</span>
                <strong>{{ runtime.last_error }}</strong>
              </div>
            </div>

            <div v-if="runtime.forward_observations?.length" class="logs-section">
              <h3>转发链路观测</h3>
              <div
                v-for="observation in runtime.forward_observations"
                :key="observation.name"
                class="status-item"
                :class="stateClass(observation.state)"
              >
                <div class="status-title">
                  <strong>{{ observation.name }}</strong>
                  <span>{{ displayState(observation.state) }}</span>
                </div>
                <p>{{ observation.detail }}</p>
                <small>
                  配置={{ displayState(observation.configured_transport) }} · 当前={{ displayState(observation.active_transport || 'pending') }}
                  · 直连尝试={{ observation.direct_attempt_count }}
                  · 中继回落={{ observation.relay_fallback_count }}
                  · 活跃连接={{ observation.active_connection_count }}
                </small>
                <small v-if="observation.last_transport_switch_at">最近切换：{{ formatTimestamp(observation.last_transport_switch_at) }}</small>
                <small v-if="observation.last_failure_stage">
                  最近失败：{{ displayState(observation.last_failure_transport) }} / {{ displayState(observation.last_failure_stage) }}
                  @ {{ formatTimestamp(observation.last_failure_at) }}
                </small>
                <small v-if="observation.last_failure_error">{{ observation.last_failure_error }}</small>
                <small v-if="observation.direct_metrics">
                  直连指标：cwnd={{ observation.direct_metrics.window_packets }} · ssthresh={{ observation.direct_metrics.ssthresh_packets }}
                  · rto={{ observation.direct_metrics.rto_ms }}ms · srtt={{ observation.direct_metrics.smoothed_rtt_ms ?? "-" }}ms
                  · 保活={{ observation.direct_metrics.keepalive_sent_count }}/{{ observation.direct_metrics.keepalive_ack_count }}
                </small>
              </div>
            </div>

            <div v-if="runtime.published_service_observations?.length" class="logs-section">
              <h3>服务链路观测</h3>
              <div
                v-for="observation in runtime.published_service_observations"
                :key="observation.name"
                class="status-item"
                :class="stateClass(observation.state)"
              >
                <div class="status-title">
                  <strong>{{ observation.name }}</strong>
                  <span>{{ displayState(observation.state) }}</span>
                </div>
                <p>{{ observation.detail }}</p>
                <small>
                  当前={{ displayState(observation.active_transport || 'pending') }}
                  · 直连会话={{ observation.direct_session_count }}
                  · 活跃会话={{ observation.active_session_count }}
                  · 直连连接={{ observation.direct_connection_count }}
                  · 中继连接={{ observation.relay_connection_count }}
                </small>
                <small v-if="observation.direct_metrics">
                  直连指标：cwnd={{ observation.direct_metrics.window_packets }} · ssthresh={{ observation.direct_metrics.ssthresh_packets }}
                  · rto={{ observation.direct_metrics.rto_ms }}ms · srtt={{ observation.direct_metrics.smoothed_rtt_ms ?? "-" }}ms
                  · 保活={{ observation.direct_metrics.keepalive_sent_count }}/{{ observation.direct_metrics.keepalive_ack_count }}
                </small>
              </div>
            </div>

            <div v-if="runtime.recent_events?.length" class="logs-section">
              <h3>最近运行事件</h3>
              <div
                v-for="event in [...runtime.recent_events].reverse().slice(0, 12)"
                :key="`${event.timestamp}-${event.message}`"
                class="status-item"
                :class="stateClass(event.level === 'error' ? 'failed' : event.level === 'warn' ? 'degraded' : 'running')"
              >
                <div class="status-title">
                  <strong>{{ displayEventLevel(event.level) }}</strong>
                  <span>{{ formatTimestamp(event.timestamp) }}</span>
                </div>
                <p>{{ event.message }}</p>
              </div>
            </div>
          </div>
          <p v-else class="empty">{{ snapshot?.observed_runtime_note ?? "还没有本地运行态观测。" }}</p>

          <div class="logs-section">
            <h3>最近一次操作输出</h3>
            <textarea class="raw-output" :value="snapshot?.output ?? ''" readonly></textarea>
          </div>
        </section>
      </template>

      <template v-else-if="currentTab === 'raw'">
        <section class="panel">
          <div class="panel-header">
            <div>
              <h2>原始配置</h2>
              <p>直接查看磁盘上的 agent.toml 当前内容，便于排查图形界面与实际文件是否一致。</p>
            </div>
            <div class="toolbar">
              <button class="action-button secondary" :disabled="!canReloadRawConfig" @click="refreshPassive">重新读取</button>
            </div>
          </div>
          <p class="soft-tip">{{ snapshot?.raw_config_note ?? "还没有读取原始配置。" }}</p>
          <textarea class="raw-output tall" :value="snapshot?.raw_config_preview ?? ''" readonly></textarea>
        </section>
      </template>
    </main>
  </div>
</template>
