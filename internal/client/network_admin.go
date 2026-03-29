package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/proto"
)

func FetchNetworkDevices(ctx context.Context, cfg config.ClientConfig) (proto.NetworkDevicesResponse, error) {
	serverURL, err := config.ValidateServerURL(cfg.ServerURL, cfg.AllowInsecureHTTP)
	if err != nil {
		return proto.NetworkDevicesResponse{}, err
	}
	if strings.TrimSpace(cfg.AdminPassword) == "" {
		return proto.NetworkDevicesResponse{}, errors.New("client config missing admin_password")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/v1/network/devices", nil)
	if err != nil {
		return proto.NetworkDevicesResponse{}, err
	}
	req.Header.Set("X-SNT-Admin-Password", cfg.AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return proto.NetworkDevicesResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return proto.NetworkDevicesResponse{}, readAdminResponseError(resp)
	}

	var out proto.NetworkDevicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return proto.NetworkDevicesResponse{}, err
	}
	return out, nil
}

func KickNetworkDevice(ctx context.Context, cfg config.ClientConfig, payload proto.KickDeviceRequest) (proto.KickDeviceResponse, error) {
	serverURL, err := config.ValidateServerURL(cfg.ServerURL, cfg.AllowInsecureHTTP)
	if err != nil {
		return proto.KickDeviceResponse{}, err
	}
	if strings.TrimSpace(cfg.AdminPassword) == "" {
		return proto.KickDeviceResponse{}, errors.New("client config missing admin_password")
	}
	if strings.TrimSpace(payload.DeviceID) == "" && strings.TrimSpace(payload.DeviceName) == "" {
		return proto.KickDeviceResponse{}, errors.New("device_id or device_name is required")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return proto.KickDeviceResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/v1/network/devices/kick", bytes.NewReader(raw))
	if err != nil {
		return proto.KickDeviceResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SNT-Admin-Password", cfg.AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return proto.KickDeviceResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return proto.KickDeviceResponse{}, readAdminResponseError(resp)
	}

	var out proto.KickDeviceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return proto.KickDeviceResponse{}, err
	}
	return out, nil
}

func LeaveNetworkSession(ctx context.Context, serverURL string, payload proto.LeaveNetworkRequest) (proto.LeaveNetworkResponse, error) {
	if strings.TrimSpace(serverURL) == "" {
		return proto.LeaveNetworkResponse{}, errors.New("server_url is required")
	}
	if strings.TrimSpace(payload.DeviceID) == "" || strings.TrimSpace(payload.SessionToken) == "" {
		return proto.LeaveNetworkResponse{}, errors.New("device_id and session_token are required")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return proto.LeaveNetworkResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/v1/network/leave", bytes.NewReader(raw))
	if err != nil {
		return proto.LeaveNetworkResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return proto.LeaveNetworkResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return proto.LeaveNetworkResponse{}, readAPIResponseError(resp)
	}

	var out proto.LeaveNetworkResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return proto.LeaveNetworkResponse{}, err
	}
	return out, nil
}

func readAPIResponseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return readHTTPErrorBody(resp.StatusCode, body)
}

func readAdminResponseError(resp *http.Response) error {
	return fmt.Errorf("server admin request failed: %w", readAPIResponseError(resp))
}

func readHTTPErrorBody(statusCode int, body []byte) error {
	var apiErr proto.APIErrorResponse
	if err := json.Unmarshal(body, &apiErr); err == nil && strings.TrimSpace(apiErr.Message) != "" {
		if strings.TrimSpace(apiErr.Detail) != "" {
			return fmt.Errorf("%s (%s): %s", apiErr.Message, apiErr.Code, apiErr.Detail)
		}
		if strings.TrimSpace(apiErr.Code) != "" {
			return fmt.Errorf("%s (%s)", apiErr.Message, apiErr.Code)
		}
		return errors.New(apiErr.Message)
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = fmt.Sprintf("http %d", statusCode)
	}
	return errors.New(message)
}
