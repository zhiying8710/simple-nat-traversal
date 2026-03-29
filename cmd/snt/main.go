package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"simple-nat-traversal/internal/autostart"
	"simple-nat-traversal/internal/buildinfo"
	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/control"
	"simple-nat-traversal/internal/proto"
)

func main() {
	configPath := flag.String("config", "client.json", "path to client config")
	versionMode := flag.Bool("version", false, "print version information and exit")
	statusMode := flag.Bool("status", false, "query local client admin status and exit")
	peersMode := flag.Bool("peers", false, "print a human-friendly peer status view and exit")
	routesMode := flag.Bool("routes", false, "print a human-friendly route/bind status view and exit")
	traceMode := flag.Bool("trace", false, "print detailed peer candidate diagnostics and recent events")
	overviewMode := flag.Bool("overview", false, "show a GUI-friendly combined overview of config/runtime/network/autostart")
	overviewJSONMode := flag.Bool("overview-json", false, "show the combined overview as JSON")
	networkMode := flag.Bool("network", false, "query server online devices with the config password and exit")
	networkJSONMode := flag.Bool("network-json", false, "query server online devices as JSON and exit")
	autostartStatusMode := flag.Bool("autostart-status", false, "show login/startup auto-launch status for the current config")
	installAutostartMode := flag.Bool("install-autostart", false, "install login/startup auto-launch for the current config")
	uninstallAutostartMode := flag.Bool("uninstall-autostart", false, "remove login/startup auto-launch entry")
	kickDeviceName := flag.String("kick-device-name", "", "ask the server to kick a device by device_name")
	kickDeviceID := flag.String("kick-device-id", "", "ask the server to kick a device by device_id")
	initConfigMode := flag.Bool("init-config", false, "interactively create a client config file")
	editConfigMode := flag.Bool("edit-config", false, "interactively edit an existing client config file")
	showConfigMode := flag.Bool("show-config", false, "print the normalized client config with secrets redacted and exit")
	showConfigUnsafeMode := flag.Bool("show-config-unsafe", false, "print the normalized client config including secrets and exit")
	setServerURL := flag.String("set-server-url", "", "update server_url in client config")
	setAllowInsecureHTTP := flag.String("set-allow-insecure-http", "", "update allow_insecure_http in client config with true/false")
	setPasswordEnv := flag.String("set-password-env", "", "read password from environment variable and update client config")
	setPasswordFile := flag.String("set-password-file", "", "read password from file and update client config")
	setAdminPasswordEnv := flag.String("set-admin-password-env", "", "read admin_password from environment variable and update client config")
	setAdminPasswordFile := flag.String("set-admin-password-file", "", "read admin_password from file and update client config")
	setDeviceName := flag.String("set-device-name", "", "update device_name in client config")
	setAutoConnect := flag.String("set-auto-connect", "", "update auto_connect in client config with true/false")
	setUDPListen := flag.String("set-udp-listen", "", "update udp_listen in client config")
	setAdminListen := flag.String("set-admin-listen", "", "update admin_listen in client config")
	var upsertPublish multiFlag
	var deletePublish multiFlag
	var upsertBind multiFlag
	var deleteBind multiFlag
	flag.Var(&upsertPublish, "upsert-publish", "upsert publish entry as name=host:port")
	flag.Var(&deletePublish, "delete-publish", "delete publish entry by name")
	flag.Var(&upsertBind, "upsert-bind", "upsert bind entry as name=peer,service,host:port")
	flag.Var(&deleteBind, "delete-bind", "delete bind entry by name")
	flag.Parse()

	if *kickDeviceName != "" && *kickDeviceID != "" {
		log.Fatal("choose only one of -kick-device-name or -kick-device-id")
	}
	if *showConfigMode && *showConfigUnsafeMode {
		log.Fatal("choose only one of -show-config or -show-config-unsafe")
	}

	modeCount := 0
	for _, enabled := range []bool{*versionMode, *statusMode, *peersMode, *routesMode, *traceMode, *overviewMode, *overviewJSONMode, *networkMode, *networkJSONMode, *autostartStatusMode, *installAutostartMode, *uninstallAutostartMode, *initConfigMode, *editConfigMode, *showConfigMode || *showConfigUnsafeMode, *kickDeviceName != "", *kickDeviceID != ""} {
		if enabled {
			modeCount++
		}
	}
	if modeCount > 1 {
		log.Fatal("choose only one of -version, -status, -peers, -routes, -trace, -overview, -overview-json, -network, -network-json, -autostart-status, -install-autostart, -uninstall-autostart, -kick-device-name, -kick-device-id, -init-config, -edit-config, -show-config, -show-config-unsafe")
	}

	patch := config.ClientConfigPatch{
		UpsertPublish: upsertPublish,
		DeletePublish: deletePublish,
		UpsertBind:    upsertBind,
		DeleteBind:    deleteBind,
	}
	assignIfSet := func(dst **string, value string) {
		if value == "" {
			return
		}
		copied := value
		*dst = &copied
	}
	assignIfSet(&patch.ServerURL, *setServerURL)
	if *setAllowInsecureHTTP != "" {
		parsed, err := strconv.ParseBool(*setAllowInsecureHTTP)
		if err != nil {
			log.Fatalf("parse -set-allow-insecure-http: %v", err)
		}
		patch.AllowInsecureHTTP = &parsed
	}
	passwordValue, err := resolveOptionalSecret(*setPasswordEnv, *setPasswordFile, "password")
	if err != nil {
		log.Fatalf("resolve password update: %v", err)
	}
	patch.Password = passwordValue
	adminPasswordValue, err := resolveOptionalSecret(*setAdminPasswordEnv, *setAdminPasswordFile, "admin-password")
	if err != nil {
		log.Fatalf("resolve admin_password update: %v", err)
	}
	patch.AdminPassword = adminPasswordValue
	assignIfSet(&patch.DeviceName, *setDeviceName)
	if *setAutoConnect != "" {
		parsed, err := strconv.ParseBool(*setAutoConnect)
		if err != nil {
			log.Fatalf("parse -set-auto-connect: %v", err)
		}
		patch.AutoConnect = &parsed
	}
	assignIfSet(&patch.UDPListen, *setUDPListen)
	assignIfSet(&patch.AdminListen, *setAdminListen)
	if modeCount > 0 && patch.HasChanges() {
		log.Fatal("config patch flags cannot be combined with status/overview/network/autostart/kick modes or config wizard modes")
	}

	switch {
	case *versionMode:
		if _, err := os.Stdout.WriteString(buildinfo.String("snt") + "\n"); err != nil {
			log.Fatalf("print version: %v", err)
		}
		return
	case *initConfigMode:
		if _, err := config.InitClientConfigInteractive(*configPath, os.Stdin, os.Stdout); err != nil {
			log.Fatalf("init client config: %v", err)
		}
		return
	case *editConfigMode:
		if _, err := config.EditClientConfigInteractive(*configPath, os.Stdin, os.Stdout); err != nil {
			log.Fatalf("edit client config: %v", err)
		}
		return
	case *showConfigMode || *showConfigUnsafeMode:
		if err := config.ShowClientConfig(*configPath, os.Stdout, *showConfigUnsafeMode); err != nil {
			log.Fatalf("show client config: %v", err)
		}
		return
	case patch.HasChanges():
		if _, err := config.ApplyClientConfigPatch(*configPath, patch); err != nil {
			log.Fatalf("update client config: %v", err)
		}
		if err := config.ShowClientConfig(*configPath, os.Stdout, false); err != nil {
			log.Fatalf("show updated client config: %v", err)
		}
		return
	}

	if *overviewMode || *overviewJSONMode {
		executablePath, err := os.Executable()
		if err != nil {
			log.Fatalf("resolve executable path: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()

		overview, err := control.LoadOverview(ctx, executablePath, *configPath, control.OverviewOptions{
			IncludeNetwork: true,
		})
		if err != nil {
			log.Fatalf("load overview: %v", err)
		}
		if *overviewMode {
			if _, err := os.Stdout.WriteString(control.RenderOverview(overview)); err != nil {
				log.Fatalf("print overview: %v", err)
			}
			return
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(overview); err != nil {
			log.Fatalf("encode overview: %v", err)
		}
		return
	}

	if *autostartStatusMode || *installAutostartMode || *uninstallAutostartMode {
		executablePath, err := os.Executable()
		if err != nil {
			log.Fatalf("resolve executable path: %v", err)
		}

		switch {
		case *autostartStatusMode:
			status, err := autostart.StatusFor(executablePath, *configPath)
			if err != nil {
				log.Fatalf("query autostart status: %v", err)
			}
			if _, err := os.Stdout.WriteString(autostart.RenderStatus(status)); err != nil {
				log.Fatalf("print autostart status: %v", err)
			}
		case *installAutostartMode:
			if _, err := config.LoadClientConfig(*configPath); err != nil {
				log.Fatalf("load client config for autostart: %v", err)
			}
			status, err := autostart.Install(executablePath, *configPath)
			if err != nil {
				log.Fatalf("install autostart: %v", err)
			}
			if _, err := os.Stdout.WriteString(autostart.RenderStatus(status)); err != nil {
				log.Fatalf("print autostart status: %v", err)
			}
		default:
			status, err := autostart.Uninstall()
			if err != nil {
				log.Fatalf("uninstall autostart: %v", err)
			}
			if _, err := os.Stdout.WriteString(autostart.RenderStatus(status)); err != nil {
				log.Fatalf("print autostart status: %v", err)
			}
		}
		return
	}

	cfg, err := config.LoadClientConfig(*configPath)
	if err != nil {
		log.Fatalf("load client config: %v", err)
	}
	if _, changed, err := config.EnsureClientIdentity(&cfg); err != nil {
		log.Fatalf("ensure client identity: %v", err)
	} else if changed {
		if err := config.SaveClientConfig(*configPath, cfg); err != nil {
			log.Fatalf("persist client identity: %v", err)
		}
	}

	if *statusMode || *peersMode || *routesMode || *traceMode {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		snapshot, err := client.FetchStatus(ctx, cfg)
		if err != nil {
			log.Fatalf("fetch client status: %v", err)
		}
		switch {
		case *peersMode:
			if _, err := os.Stdout.WriteString(client.RenderPeersStatus(snapshot)); err != nil {
				log.Fatalf("print peer status: %v", err)
			}
		case *routesMode:
			if _, err := os.Stdout.WriteString(client.RenderRoutesStatus(snapshot)); err != nil {
				log.Fatalf("print route status: %v", err)
			}
		case *traceMode:
			if _, err := os.Stdout.WriteString(client.RenderTraceStatus(snapshot)); err != nil {
				log.Fatalf("print trace status: %v", err)
			}
		default:
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(snapshot); err != nil {
				log.Fatalf("encode client status: %v", err)
			}
		}
		return
	}

	if *networkMode || *networkJSONMode {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		snapshot, err := client.FetchNetworkDevices(ctx, cfg)
		if err != nil {
			log.Fatalf("fetch network devices: %v", err)
		}
		if *networkMode {
			if _, err := os.Stdout.WriteString(client.RenderNetworkDevicesStatus(snapshot)); err != nil {
				log.Fatalf("print network devices: %v", err)
			}
			return
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(snapshot); err != nil {
			log.Fatalf("encode network devices: %v", err)
		}
		return
	}

	if *kickDeviceName != "" || *kickDeviceID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		resp, err := client.KickNetworkDevice(ctx, cfg, proto.KickDeviceRequest{
			DeviceID:   *kickDeviceID,
			DeviceName: *kickDeviceName,
		})
		if err != nil {
			log.Fatalf("kick network device: %v", err)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(resp); err != nil {
			log.Fatalf("encode kick response: %v", err)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := client.Run(ctx, cfg); err != nil {
		log.Fatalf("client exited: %v", err)
	}
}

type multiFlag []string

func (m *multiFlag) String() string {
	return ""
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
