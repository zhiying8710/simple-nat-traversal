package fyneapp

import (
	"strings"
	"testing"
	"time"

	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/control"
	"simple-nat-traversal/internal/proto"
)

func TestDetectLocalePrefersOverride(t *testing.T) {
	original := detectSystemLocales
	detectSystemLocales = func() ([]string, error) {
		return []string{"en-US"}, nil
	}
	t.Cleanup(func() {
		detectSystemLocales = original
	})
	t.Setenv("SNT_GUI_LOCALE", "zh_CN.UTF-8")

	if got := detectLocale(); got != localeChinese {
		t.Fatalf("detectLocale() = %q, want %q", got, localeChinese)
	}
}

func TestDetectLocaleUsesSystemLocales(t *testing.T) {
	original := detectSystemLocales
	detectSystemLocales = func() ([]string, error) {
		return []string{"zh-Hans-CN"}, nil
	}
	t.Cleanup(func() {
		detectSystemLocales = original
	})
	t.Setenv("SNT_GUI_LOCALE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "")
	t.Setenv("LANGUAGE", "")

	if got := detectLocale(); got != localeChinese {
		t.Fatalf("detectLocale() = %q, want %q", got, localeChinese)
	}
}

func TestRenderOverviewLocalized(t *testing.T) {
	app := newTestApp(t, Config{})
	app.locale = localeChinese

	rendered := app.renderOverview(control.Overview{
		GeneratedAt:    time.Unix(1700000000, 0).UTC(),
		ExecutablePath: "/tmp/snt-gui",
		ConfigPath:     "/tmp/client.json",
		ConfigExists:   true,
		ConfigValid:    true,
		ClientRunning:  true,
		Config: &control.OverviewConfig{
			DeviceName:        "win-laptop",
			ServerURL:         "https://example.com",
			AllowInsecureHTTP: false,
			UDPListen:         ":0",
			AdminListen:       "127.0.0.1:19080",
			LogLevel:          config.LogLevelInfo,
		},
	})

	for _, want := range []string{
		"生成时间",
		"客户端运行中\t是",
		"设备名称\twin-laptop",
		"服务端地址\thttps://example.com",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("renderOverview missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderServiceTablesLocalized(t *testing.T) {
	app := newTestApp(t, Config{})
	app.locale = localeChinese

	publishText := app.renderPublishConfigs(map[string]config.PublishConfig{
		"rdp": {Protocol: config.ServiceProtocolTCP, Local: "127.0.0.1:3389"},
	}, app.t("publish_none"))
	for _, want := range []string{"名称", "协议", "本地地址"} {
		if strings.Contains(publishText, want) {
			continue
		}
		t.Fatalf("publish table header not localized, missing %q:\n%s", want, publishText)
	}

	discoveredText := app.renderDiscoveredServices([]discoveredService{{
		DeviceID:    "peer-1",
		DeviceName:  "win-b",
		ServiceName: "rdp",
		Protocol:    config.ServiceProtocolTCP,
	}}, app.t("discovered_none"))
	for _, want := range []string{"设备", "服务", "协议", "设备 ID"} {
		if strings.Contains(discoveredText, want) {
			continue
		}
		t.Fatalf("discovered table header not localized, missing %q:\n%s", want, discoveredText)
	}
}

func TestRenderStatusAndNetworkViewsLocalized(t *testing.T) {
	app := newTestApp(t, Config{})
	app.locale = localeChinese

	snapshot := client.StatusSnapshot{
		GeneratedAt:          time.Unix(1700000000, 0).UTC(),
		DeviceID:             "dev-mac",
		DeviceName:           "macbook-air",
		NetworkState:         "joined",
		LocalUDPAddr:         "0.0.0.0:50000",
		ObservedAddr:         "1.2.3.4:50000",
		ServerUDPAddr:        "2.3.4.5:3479",
		ActiveServiceProxies: 1,
		RejoinCount:          2,
		Peers: []client.PeerStatus{{
			DeviceID:    "dev-win",
			DeviceName:  "winpc",
			State:       "connected",
			ChosenAddr:  "10.0.0.2:40000",
			RouteReason: "punch",
			ServiceDetails: []proto.ServiceInfo{{
				Name:     "rdp",
				Protocol: config.ServiceProtocolTCP,
			}},
			LastSeen: time.Unix(1700000100, 0).UTC(),
		}},
		Publish: []client.PublishStatus{{
			Name:     "rdp",
			Protocol: config.ServiceProtocolTCP,
			Local:    "127.0.0.1:3389",
		}},
		Binds: []client.BindStatus{{
			Name:           "win-rdp",
			Protocol:       config.ServiceProtocolTCP,
			ListenAddr:     "127.0.0.1:13389",
			Peer:           "winpc",
			Service:        "rdp",
			ActiveSessions: 1,
		}},
		TCPBindStreams: []client.TCPBindStreamStatus{{
			BindName:        "win-rdp",
			PeerName:        "winpc",
			Service:         "rdp",
			SessionID:       "bind-1",
			State:           "open",
			StartedAt:       time.Unix(1700000200, 0).UTC(),
			LastSeen:        time.Unix(1700000210, 0).UTC(),
			BufferedInbound: 12,
			UnackedOutbound: 2,
		}},
		TCPProxies: []client.TCPProxyStatus{{
			PeerName:        "winpc",
			BindName:        "win-rdp",
			Service:         "rdp",
			SessionID:       "proxy-1",
			State:           "open",
			Target:          "127.0.0.1:3389",
			StartedAt:       time.Unix(1700000220, 0).UTC(),
			LastSeen:        time.Unix(1700000230, 0).UTC(),
			BufferedInbound: 3,
			UnackedOutbound: 1,
		}},
		RecentEvents: []client.TraceEvent{{
			At:       time.Unix(1700000240, 0).UTC(),
			Scope:    "peer",
			PeerName: "winpc",
			Event:    "connected",
			Detail:   "punch succeeded",
		}},
	}

	for _, tc := range []struct {
		name  string
		text  string
		wants []string
	}{
		{
			name:  "peers",
			text:  app.renderPeersStatus(snapshot),
			wants: []string{"对端设备", "状态", "服务列表", "打洞次数"},
		},
		{
			name:  "routes",
			text:  app.renderRoutesStatus(snapshot),
			wants: []string{"监听地址", "会话数", "TCP 绑定流", "TCP 发布代理"},
		},
		{
			name:  "trace",
			text:  app.renderTraceStatus(snapshot),
			wants: []string{"Peer 候选地址", "成功来源", "TCP 运行态", "最近事件"},
		},
	} {
		for _, want := range tc.wants {
			if strings.Contains(tc.text, want) {
				continue
			}
			t.Fatalf("%s text not localized, missing %q:\n%s", tc.name, want, tc.text)
		}
	}

	networkText := app.renderNetworkDevicesStatus(proto.NetworkDevicesResponse{
		GeneratedAt: time.Unix(1700000000, 0).UTC(),
		Devices: []proto.NetworkDeviceStatus{{
			DeviceID:     "dev-win",
			DeviceName:   "winpc",
			State:        "online",
			ObservedAddr: "1.2.3.4:40000",
			LastSeen:     time.Unix(1700000300, 0).UTC(),
			ServiceDetails: []proto.ServiceInfo{{
				Name:     "rdp",
				Protocol: config.ServiceProtocolTCP,
			}},
			Candidates: []string{"10.0.0.2:40000"},
		}},
	})
	for _, want := range []string{"设备", "设备 ID", "服务列表", "候选地址"} {
		if strings.Contains(networkText, want) {
			continue
		}
		t.Fatalf("network text not localized, missing %q:\n%s", want, networkText)
	}
}

func TestLocalizeErrorTextChinese(t *testing.T) {
	app := newTestApp(t, Config{})
	app.locale = localeChinese

	if got := app.localizeErrorText("server_url is required"); got != "服务端地址不能为空" {
		t.Fatalf("localizeErrorText exact = %q", got)
	}
	if got := app.localizeErrorText("status endpoint returned 503 Service Unavailable"); !strings.Contains(got, "状态接口返回 503 Service Unavailable") {
		t.Fatalf("localizeErrorText prefix = %q", got)
	}
}
