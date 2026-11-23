// Package protocol defines the wire protocol between a CVMS client and server.
//
// The client keeps one outbound WebSocket connection to the server.
// Text frames carry JSON control messages (Envelope), binary frames carry
// media (video access units, snapshots) with a fixed 13-byte header.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

// ProtocolVersion is bumped when the wire format changes incompatibly.
const ProtocolVersion = 1

// WebSocket path on the server that clients connect to.
const ClientWSPath = "/client/ws"

// Message types.
const (
	TypeHello        = "hello"         // client -> server
	TypeStatus       = "status"        // server -> client
	TypeCameras      = "cameras"       // client -> server
	TypeCameraStatus = "camera_status" // client -> server
	TypeStreamStart  = "stream_start"  // server -> client
	TypeStreamStop   = "stream_stop"   // server -> client
	TypeStreamInfo   = "stream_info"   // client -> server
	TypeStreamError  = "stream_error"  // client -> server
	TypeSnapshotReq  = "snapshot_req"  // server -> client
	TypeSnapshotErr  = "snapshot_err"  // client -> server
)

// Client approval states.
const (
	StatusPending      = "pending"
	StatusApproved     = "approved"
	StatusBlocked      = "blocked"
	StatusUnauthorized = "unauthorized"
)

// Stream profiles.
const (
	ProfileMain = "main"
	ProfileSub  = "sub"
)

// Codecs.
const (
	CodecH264 = "h264"
	CodecH265 = "h265"
)

// Envelope wraps every JSON control message.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Encode builds a JSON envelope.
func Encode(typ string, data any) ([]byte, error) {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(Envelope{Type: typ, Data: raw})
}

// Hello is the first message a client sends after connecting.
type Hello struct {
	ProtocolVersion int    `json:"protocol_version"`
	ClientUID       string `json:"client_uid"`
	Token           string `json:"token,omitempty"`
	Name            string `json:"name"`
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Version         string `json:"version"`
}

// Status tells the client its approval state.
type Status struct {
	Status  string `json:"status"`
	Token   string `json:"token,omitempty"`
	Message string `json:"message,omitempty"`
}

// StreamInfo describes one stream (main or sub) of a camera.
type StreamInfo struct {
	Available bool   `json:"available"`
	Codec     string `json:"codec,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

// CameraInfo describes one camera of a client.
type CameraInfo struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Host   string     `json:"host"`
	Model  string     `json:"model,omitempty"`
	Online bool       `json:"online"`
	Main   StreamInfo `json:"main"`
	Sub    StreamInfo `json:"sub"`
}

// Cameras is the full camera list of a client.
type Cameras struct {
	List []CameraInfo `json:"list"`
}

// CameraStatus is an online/offline change of one camera.
type CameraStatus struct {
	ID     string `json:"id"`
	Online bool   `json:"online"`
}

// StreamStart asks the client to start pushing a stream.
type StreamStart struct {
	StreamID uint32 `json:"stream_id"`
	CameraID string `json:"camera_id"`
	Profile  string `json:"profile"`
}

// StreamStop asks the client to stop pushing a stream.
type StreamStop struct {
	StreamID uint32 `json:"stream_id"`
}

// StreamInfoMsg tells the server which codec is actually being pushed
// and whether the client transcoded it.
type StreamInfoMsg struct {
	StreamID    uint32 `json:"stream_id"`
	Codec       string `json:"codec"`
	SourceCodec string `json:"source_codec"`
	Transcoded  bool   `json:"transcoded"`
}

// StreamError reports a failed stream.
type StreamError struct {
	StreamID uint32 `json:"stream_id"`
	Error    string `json:"error"`
}

// SnapshotReq asks the client for one JPEG frame.
type SnapshotReq struct {
	ReqID    uint32 `json:"req_id"`
	CameraID string `json:"camera_id"`
	Profile  string `json:"profile"`
}

// SnapshotErr reports a failed snapshot request.
type SnapshotErr struct {
	ReqID uint32 `json:"req_id"`
	Error string `json:"error"`
}

// Binary frame types.
const (
	BinVideo    byte = 1 // payload: Annex-B access unit
	BinSnapshot byte = 2 // payload: JPEG
)

// BinHeaderSize is the size of the binary frame header.
const BinHeaderSize = 13

// BinFrame is a decoded binary frame.
type BinFrame struct {
	Type    byte
	ID      uint32 // stream id or snapshot request id
	TS      int64  // capture time, unix microseconds
	Payload []byte
}

// EncodeBin builds a binary frame.
func EncodeBin(typ byte, id uint32, ts int64, payload []byte) []byte {
	b := make([]byte, BinHeaderSize+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], id)
	binary.BigEndian.PutUint64(b[5:13], uint64(ts))
	copy(b[BinHeaderSize:], payload)
	return b
}

// DecodeBin parses a binary frame. Payload aliases b.
func DecodeBin(b []byte) (BinFrame, error) {
	if len(b) < BinHeaderSize {
		return BinFrame{}, errors.New("short binary frame")
	}
	return BinFrame{
		Type:    b[0],
		ID:      binary.BigEndian.Uint32(b[1:5]),
		TS:      int64(binary.BigEndian.Uint64(b[5:13])),
		Payload: b[BinHeaderSize:],
	}, nil
}
