import type {
  ActionResult,
  AppState,
  DiscoveredService,
  EditableConfig,
} from "./types";

type BackendApp = {
  State(): Promise<AppState>;
  Refresh(input: EditableConfig): Promise<AppState>;
  SaveConfig(input: EditableConfig): Promise<ActionResult>;
  StartClient(input: EditableConfig): Promise<ActionResult>;
  StopClient(): Promise<ActionResult>;
  ApplyLogLevel(input: EditableConfig): Promise<ActionResult>;
  InstallAutostart(input: EditableConfig): Promise<ActionResult>;
  UninstallAutostart(): Promise<ActionResult>;
  UpsertPublish(
    input: EditableConfig,
    name: string,
    protocol: string,
    local: string,
  ): Promise<ActionResult>;
  DeletePublish(input: EditableConfig, name: string): Promise<ActionResult>;
  UpsertBind(
    input: EditableConfig,
    name: string,
    protocol: string,
    peer: string,
    serviceName: string,
    local: string,
  ): Promise<ActionResult>;
  DeleteBind(input: EditableConfig, name: string): Promise<ActionResult>;
  QuickBindDiscovered(
    input: EditableConfig,
    service: DiscoveredService,
  ): Promise<ActionResult>;
  KickDevice(
    input: EditableConfig,
    deviceName: string,
    deviceID: string,
  ): Promise<ActionResult>;
};

declare global {
  interface Window {
    go?: {
      wailsapp?: {
        App?: BackendApp;
      };
      main?: {
        App?: BackendApp;
      };
    };
  }
}

export function backend(): BackendApp | null {
  return window.go?.wailsapp?.App ?? window.go?.main?.App ?? null;
}
