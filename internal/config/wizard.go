package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const redactedSecretValue = "<redacted>"

func InitClientConfigInteractive(path string, in io.Reader, out io.Writer) (ClientConfig, error) {
	if _, err := os.Stat(path); err == nil {
		return ClientConfig{}, fmt.Errorf("config %s already exists; use edit mode instead", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ClientConfig{}, err
	}
	cfg := ClientDefaults()
	return runClientConfigWizard(path, cfg, in, out, true)
}

func EditClientConfigInteractive(path string, in io.Reader, out io.Writer) (ClientConfig, error) {
	cfg, err := LoadClientConfig(path)
	if err != nil {
		return ClientConfig{}, err
	}
	return runClientConfigWizard(path, cfg, in, out, false)
}

func ShowClientConfig(path string, out io.Writer, revealSecrets bool) error {
	cfg, err := LoadClientConfig(path)
	if err != nil {
		return err
	}
	if !revealSecrets {
		cfg = redactClientConfigSecrets(cfg)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", raw)
	return err
}

func runClientConfigWizard(path string, cfg ClientConfig, in io.Reader, out io.Writer, create bool) (ClientConfig, error) {
	reader := bufio.NewReader(in)

	modeLabel := "Edit"
	if create {
		modeLabel = "Init"
	}
	fmt.Fprintf(out, "%s client config: %s\n", modeLabel, path)

	var err error
	if cfg.ServerURL, err = promptString(reader, out, "server_url", cfg.ServerURL); err != nil {
		return ClientConfig{}, err
	}
	if cfg.AllowInsecureHTTP, err = promptBool(reader, out, "allow_insecure_http", cfg.AllowInsecureHTTP); err != nil {
		return ClientConfig{}, err
	}
	if cfg.Password, err = promptSecretString(reader, out, "password", cfg.Password); err != nil {
		return ClientConfig{}, err
	}
	if cfg.AdminPassword, err = promptSecretString(reader, out, "admin_password", cfg.AdminPassword); err != nil {
		return ClientConfig{}, err
	}
	if cfg.DeviceName, err = promptString(reader, out, "device_name", cfg.DeviceName); err != nil {
		return ClientConfig{}, err
	}
	if cfg.AutoConnect, err = promptBool(reader, out, "auto_connect", cfg.AutoConnect); err != nil {
		return ClientConfig{}, err
	}
	if cfg.UDPListen, err = promptString(reader, out, "udp_listen", cfg.UDPListen); err != nil {
		return ClientConfig{}, err
	}
	if cfg.AdminListen, err = promptString(reader, out, "admin_listen", cfg.AdminListen); err != nil {
		return ClientConfig{}, err
	}

	rewritePublish, err := promptBool(reader, out, "rewrite publish entries", len(cfg.Publish) == 0)
	if err != nil {
		return ClientConfig{}, err
	}
	if rewritePublish {
		cfg.Publish, err = promptPublishEntries(reader, out)
		if err != nil {
			return ClientConfig{}, err
		}
	}

	rewriteBinds, err := promptBool(reader, out, "rewrite bind entries", len(cfg.Binds) == 0)
	if err != nil {
		return ClientConfig{}, err
	}
	if rewriteBinds {
		cfg.Binds, err = promptBindEntries(reader, out)
		if err != nil {
			return ClientConfig{}, err
		}
	}

	if err := SaveClientConfig(path, cfg); err != nil {
		return ClientConfig{}, err
	}
	fmt.Fprintf(out, "saved %s\n", path)
	return cfg, nil
}

func promptPublishEntries(reader *bufio.Reader, out io.Writer) (map[string]PublishConfig, error) {
	entries := map[string]PublishConfig{}
	fmt.Fprintln(out, "Publish entries. Leave name empty to finish.")
	for {
		name, err := promptString(reader, out, "  publish name", "")
		if err != nil {
			return nil, err
		}
		if name == "" {
			break
		}
		protocol, err := promptString(reader, out, "  protocol (udp/tcp)", ServiceProtocolUDP)
		if err != nil {
			return nil, err
		}
		protocol, err = NormalizeServiceProtocol(protocol)
		if err != nil {
			return nil, err
		}
		defaultLocal := "127.0.0.1:19132"
		if protocol == ServiceProtocolTCP {
			defaultLocal = "127.0.0.1:3389"
		}
		local, err := promptString(reader, out, "  local service addr", defaultLocal)
		if err != nil {
			return nil, err
		}
		entries[name] = PublishConfig{Protocol: protocol, Local: local}
	}
	return entries, nil
}

func promptBindEntries(reader *bufio.Reader, out io.Writer) (map[string]BindConfig, error) {
	entries := map[string]BindConfig{}
	fmt.Fprintln(out, "Bind entries. Leave name empty to finish.")
	for {
		name, err := promptString(reader, out, "  bind name", "")
		if err != nil {
			return nil, err
		}
		if name == "" {
			break
		}
		protocol, err := promptString(reader, out, "  protocol (udp/tcp)", ServiceProtocolUDP)
		if err != nil {
			return nil, err
		}
		protocol, err = NormalizeServiceProtocol(protocol)
		if err != nil {
			return nil, err
		}
		peer, err := promptString(reader, out, "  peer device_name", "")
		if err != nil {
			return nil, err
		}
		service, err := promptString(reader, out, "  peer service name", "")
		if err != nil {
			return nil, err
		}
		defaultLocal := "127.0.0.1:29132"
		if protocol == ServiceProtocolTCP {
			defaultLocal = "127.0.0.1:13389"
		}
		local, err := promptString(reader, out, "  local listen addr", defaultLocal)
		if err != nil {
			return nil, err
		}
		entries[name] = BindConfig{
			Protocol: protocol,
			Peer:     peer,
			Service:  service,
			Local:    local,
		}
	}
	return entries, nil
}

func promptString(reader *bufio.Reader, out io.Writer, label, current string) (string, error) {
	if current == "" {
		fmt.Fprintf(out, "%s: ", label)
	} else {
		fmt.Fprintf(out, "%s [%s]: ", label, current)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return current, nil
	}
	return value, nil
}

func promptSecretString(reader *bufio.Reader, out io.Writer, label, current string) (string, error) {
	if current == "" {
		fmt.Fprintf(out, "%s: ", label)
	} else {
		fmt.Fprintf(out, "%s [configured]: ", label)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return current, nil
	}
	return value, nil
}

func redactClientConfigSecrets(cfg ClientConfig) ClientConfig {
	if strings.TrimSpace(cfg.Password) != "" {
		cfg.Password = redactedSecretValue
	}
	if strings.TrimSpace(cfg.AdminPassword) != "" {
		cfg.AdminPassword = redactedSecretValue
	}
	if strings.TrimSpace(cfg.IdentityPrivate) != "" {
		cfg.IdentityPrivate = redactedSecretValue
	}
	return cfg
}

func promptBool(reader *bufio.Reader, out io.Writer, label string, current bool) (bool, error) {
	defaultLabel := "y/N"
	if current {
		defaultLabel = "Y/n"
	}
	fmt.Fprintf(out, "%s [%s]: ", label, defaultLabel)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	value := strings.TrimSpace(strings.ToLower(line))
	switch value {
	case "":
		return current, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid yes/no answer %q", value)
	}
}
