package wailsapp

import (
	"context"
	"embed"

	"simple-nat-traversal/internal/desktopapp"
	"simple-nat-traversal/internal/logx"
)

//go:embed all:frontend/dist
var Assets embed.FS

type App struct {
	service *desktopapp.Service
}

func New(deps desktopapp.Dependencies) *App {
	return &App{
		service: desktopapp.New(deps),
	}
}

func (a *App) Startup(context.Context) {
	if err := a.service.AutoStart(); err != nil {
		logx.Warnf("wails gui auto-connect failed: %v", err)
	}
}

func (a *App) Shutdown(context.Context) {
	a.service.Shutdown()
}

func (a *App) State() (desktopapp.AppState, error) {
	return a.service.State()
}

func (a *App) Refresh(input desktopapp.EditableConfig) (desktopapp.AppState, error) {
	return a.service.Refresh(input)
}

func (a *App) SaveConfig(input desktopapp.EditableConfig) (desktopapp.ActionResult, error) {
	return a.service.SaveConfig(input)
}

func (a *App) StartClient(input desktopapp.EditableConfig) (desktopapp.ActionResult, error) {
	return a.service.StartClient(input)
}

func (a *App) StopClient() (desktopapp.ActionResult, error) {
	return a.service.StopClient()
}

func (a *App) ApplyLogLevel(input desktopapp.EditableConfig) (desktopapp.ActionResult, error) {
	return a.service.ApplyLogLevel(input)
}

func (a *App) InstallAutostart(input desktopapp.EditableConfig) (desktopapp.ActionResult, error) {
	return a.service.InstallAutostart(input)
}

func (a *App) UninstallAutostart() (desktopapp.ActionResult, error) {
	return a.service.UninstallAutostart()
}

func (a *App) UpsertPublish(input desktopapp.EditableConfig, name, protocol, local string) (desktopapp.ActionResult, error) {
	return a.service.UpsertPublish(input, name, protocol, local)
}

func (a *App) DeletePublish(input desktopapp.EditableConfig, name string) (desktopapp.ActionResult, error) {
	return a.service.DeletePublish(input, name)
}

func (a *App) UpsertBind(input desktopapp.EditableConfig, name, protocol, peer, serviceName, local string) (desktopapp.ActionResult, error) {
	return a.service.UpsertBind(input, name, protocol, peer, serviceName, local)
}

func (a *App) DeleteBind(input desktopapp.EditableConfig, name string) (desktopapp.ActionResult, error) {
	return a.service.DeleteBind(input, name)
}

func (a *App) QuickBindDiscovered(input desktopapp.EditableConfig, service desktopapp.DiscoveredService) (desktopapp.ActionResult, error) {
	return a.service.QuickBindDiscovered(input, service)
}

func (a *App) KickDevice(input desktopapp.EditableConfig, deviceName, deviceID string) (desktopapp.ActionResult, error) {
	return a.service.KickDevice(input, deviceName, deviceID)
}
