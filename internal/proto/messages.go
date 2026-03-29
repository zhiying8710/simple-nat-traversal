package proto

import (
	"encoding/json"
	"time"
)

const (
	TypeRegister    = "register"
	TypeRegisterAck = "register_ack"
	TypePeerSync    = "peer_sync"
	TypePunchHello  = "punch_hello"
	TypeData        = "data"
	TypeError       = "error"
)

const (
	DataKindRequest     = "service_request"
	DataKindResponse    = "service_response"
	DataKindKeepalive   = "keepalive"
	DataKindTCPOpen     = "tcp_open"
	DataKindTCPOk       = "tcp_open_result"
	DataKindTCPData     = "tcp_data"
	DataKindTCPAck      = "tcp_ack"
	DataKindTCPClose    = "tcp_close"
	DataKindTCPCloseAck = "tcp_close_ack"
)

type JoinNetworkRequest struct {
	Password       string `json:"password"`
	DeviceName     string `json:"device_name"`
	IdentityPublic []byte `json:"identity_public,omitempty"`
}

type LeaveNetworkRequest struct {
	DeviceID     string `json:"device_id"`
	SessionToken string `json:"session_token"`
}

type JoinNetworkResponse struct {
	DeviceID         string `json:"device_id"`
	SessionToken     string `json:"session_token"`
	UDPAddr          string `json:"udp_addr"`
	HeartbeatSeconds int    `json:"heartbeat_seconds"`
	PunchIntervalMS  int    `json:"punch_interval_ms"`
}

type LeaveNetworkResponse struct {
	Removed    bool   `json:"removed"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

type ServiceInfo struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol,omitempty"`
}

type PeerInfo struct {
	DeviceID       string        `json:"device_id"`
	DeviceName     string        `json:"device_name"`
	ObservedAddr   string        `json:"observed_addr"`
	Candidates     []string      `json:"candidates,omitempty"`
	Services       []ServiceInfo `json:"services,omitempty"`
	IdentityPublic []byte        `json:"identity_public,omitempty"`
}

type Envelope struct {
	Type        string              `json:"type"`
	Register    *RegisterMessage    `json:"register,omitempty"`
	RegisterAck *RegisterAckMessage `json:"register_ack,omitempty"`
	PeerSync    *PeerSyncMessage    `json:"peer_sync,omitempty"`
	PunchHello  *PunchHelloMessage  `json:"punch_hello,omitempty"`
	Data        *DataMessage        `json:"data,omitempty"`
	Error       *ErrorMessage       `json:"error,omitempty"`
}

type RegisterMessage struct {
	DeviceID   string        `json:"device_id"`
	DeviceName string        `json:"device_name"`
	Token      string        `json:"token"`
	Candidates []string      `json:"candidates,omitempty"`
	Services   []ServiceInfo `json:"services,omitempty"`
}

type RegisterAckMessage struct {
	ObservedAddr string `json:"observed_addr"`
	ServerTime   int64  `json:"server_time"`
}

type PeerSyncMessage struct {
	Peers []PeerInfo `json:"peers"`
}

type PunchHelloMessage struct {
	FromID    string `json:"from_id"`
	FromName  string `json:"from_name"`
	Nonce     []byte `json:"nonce"`
	Public    []byte `json:"public"`
	MAC       []byte `json:"mac"`
	Signature []byte `json:"signature,omitempty"`
}

type DataMessage struct {
	FromID     string `json:"from_id"`
	Seq        uint64 `json:"seq"`
	Ciphertext []byte `json:"ciphertext"`
}

type ServicePayload struct {
	Kind      string `json:"kind"`
	Protocol  string `json:"protocol,omitempty"`
	BindName  string `json:"bind_name,omitempty"`
	Service   string `json:"service,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	StreamSeq uint64 `json:"stream_seq,omitempty"`
	Ack       uint64 `json:"ack,omitempty"`
	Error     string `json:"error,omitempty"`
	Payload   []byte `json:"payload,omitempty"`
}

type ErrorMessage struct {
	Message string `json:"message"`
}

type NetworkDevicesResponse struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Devices     []NetworkDeviceStatus `json:"devices"`
}

type NetworkDeviceStatus struct {
	DeviceID       string        `json:"device_id"`
	DeviceName     string        `json:"device_name"`
	State          string        `json:"state"`
	ObservedAddr   string        `json:"observed_addr,omitempty"`
	Candidates     []string      `json:"candidates,omitempty"`
	Services       []string      `json:"services,omitempty"`
	ServiceDetails []ServiceInfo `json:"service_details,omitempty"`
	LastSeen       time.Time     `json:"last_seen"`
}

type KickDeviceRequest struct {
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

type KickDeviceResponse struct {
	Removed    bool   `json:"removed"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

type LogLevelUpdateRequest struct {
	LogLevel string `json:"log_level"`
}

type LogLevelResponse struct {
	LogLevel string `json:"log_level"`
}

type APIErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func MarshalEnvelope(env Envelope) ([]byte, error) {
	return json.Marshal(env)
}

func UnmarshalEnvelope(data []byte) (Envelope, error) {
	var env Envelope
	err := json.Unmarshal(data, &env)
	return env, err
}

func MarshalServicePayload(payload ServicePayload) ([]byte, error) {
	return json.Marshal(payload)
}

func UnmarshalServicePayload(data []byte) (ServicePayload, error) {
	var payload ServicePayload
	err := json.Unmarshal(data, &payload)
	return payload, err
}
