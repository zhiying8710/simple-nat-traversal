package config

import (
	"fmt"
	"strings"
)

type ClientConfigPatch struct {
	ServerURL         *string
	AllowInsecureHTTP *bool
	Password          *string
	AdminPassword     *string
	DeviceName        *string
	AutoConnect       *bool
	UDPListen         *string
	AdminListen       *string
	UpsertPublish     []string
	DeletePublish     []string
	UpsertBind        []string
	DeleteBind        []string
}

func (p ClientConfigPatch) HasChanges() bool {
	return p.ServerURL != nil ||
		p.AllowInsecureHTTP != nil ||
		p.Password != nil ||
		p.AdminPassword != nil ||
		p.DeviceName != nil ||
		p.AutoConnect != nil ||
		p.UDPListen != nil ||
		p.AdminListen != nil ||
		len(p.UpsertPublish) > 0 ||
		len(p.DeletePublish) > 0 ||
		len(p.UpsertBind) > 0 ||
		len(p.DeleteBind) > 0
}

func ApplyClientConfigPatch(path string, patch ClientConfigPatch) (ClientConfig, error) {
	cfg, err := LoadClientConfig(path)
	if err != nil {
		return ClientConfig{}, err
	}
	if err := patch.Apply(&cfg); err != nil {
		return ClientConfig{}, err
	}
	if err := SaveClientConfig(path, cfg); err != nil {
		return ClientConfig{}, err
	}
	return cfg, nil
}

func (p ClientConfigPatch) Apply(cfg *ClientConfig) error {
	if p.ServerURL != nil {
		cfg.ServerURL = *p.ServerURL
	}
	if p.AllowInsecureHTTP != nil {
		cfg.AllowInsecureHTTP = *p.AllowInsecureHTTP
	}
	if p.Password != nil {
		cfg.Password = *p.Password
	}
	if p.AdminPassword != nil {
		cfg.AdminPassword = *p.AdminPassword
	}
	if p.DeviceName != nil {
		cfg.DeviceName = *p.DeviceName
	}
	if p.AutoConnect != nil {
		cfg.AutoConnect = *p.AutoConnect
	}
	if p.UDPListen != nil {
		cfg.UDPListen = *p.UDPListen
	}
	if p.AdminListen != nil {
		cfg.AdminListen = *p.AdminListen
	}
	if cfg.Publish == nil {
		cfg.Publish = map[string]PublishConfig{}
	}
	if cfg.Binds == nil {
		cfg.Binds = map[string]BindConfig{}
	}

	for _, raw := range p.UpsertPublish {
		name, publish, err := parsePublishPatch(raw)
		if err != nil {
			return err
		}
		cfg.Publish[name] = publish
	}
	for _, name := range p.DeletePublish {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("delete-publish requires a non-empty name")
		}
		delete(cfg.Publish, name)
	}
	for _, raw := range p.UpsertBind {
		name, bind, err := parseBindPatch(raw)
		if err != nil {
			return err
		}
		cfg.Binds[name] = bind
	}
	for _, name := range p.DeleteBind {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("delete-bind requires a non-empty name")
		}
		delete(cfg.Binds, name)
	}
	return normalizeClientConfig(cfg)
}

func parsePublishPatch(raw string) (string, PublishConfig, error) {
	name, local, ok := strings.Cut(strings.TrimSpace(raw), "=")
	if !ok {
		return "", PublishConfig{}, fmt.Errorf("upsert-publish must look like name=host:port")
	}
	name = strings.TrimSpace(name)
	local = strings.TrimSpace(local)
	if name == "" || local == "" {
		return "", PublishConfig{}, fmt.Errorf("upsert-publish must look like name=host:port")
	}
	return name, PublishConfig{Local: local}, nil
}

func parseBindPatch(raw string) (string, BindConfig, error) {
	name, rest, ok := strings.Cut(strings.TrimSpace(raw), "=")
	if !ok {
		return "", BindConfig{}, fmt.Errorf("upsert-bind must look like name=peer,service,host:port")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", BindConfig{}, fmt.Errorf("upsert-bind must look like name=peer,service,host:port")
	}

	parts := strings.Split(rest, ",")
	if len(parts) != 3 {
		return "", BindConfig{}, fmt.Errorf("upsert-bind must look like name=peer,service,host:port")
	}
	peer := strings.TrimSpace(parts[0])
	service := strings.TrimSpace(parts[1])
	local := strings.TrimSpace(parts[2])
	if peer == "" || service == "" || local == "" {
		return "", BindConfig{}, fmt.Errorf("upsert-bind must look like name=peer,service,host:port")
	}
	return name, BindConfig{
		Peer:    peer,
		Service: service,
		Local:   local,
	}, nil
}
