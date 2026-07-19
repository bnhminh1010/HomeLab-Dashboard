package hostagent

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	protocolVersion byte = 1
	headerSize           = 6

	maxInputPayload   = 16 << 10
	maxOutputPayload  = 32 << 10
	maxControlPayload = 4 << 10
	maxFramePayload   = maxOutputPayload
)

type frameType byte

const (
	frameOpen frameType = iota + 1
	frameInput
	frameResize
	frameClose

	frameReady frameType = 0x81 + iota
	frameOutput
	frameExit
	frameError
)

type frame struct {
	typeID  frameType
	payload []byte
}

type readyPayload struct {
	Hostname string `json:"hostname"`
	User     string `json:"user"`
	Shell    string `json:"shell"`
}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func readFrame(reader io.Reader) (frame, error) {
	var header [headerSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return frame{}, err
	}
	if header[0] != protocolVersion {
		return frame{}, fmt.Errorf("%w: unsupported version %d", ErrProtocol, header[0])
	}
	length := binary.BigEndian.Uint32(header[2:])
	if length > maxFramePayload {
		return frame{}, fmt.Errorf("%w: frame payload is too large", ErrProtocol)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return frame{}, err
	}
	return frame{typeID: frameType(header[1]), payload: payload}, nil
}

func writeFrame(writer io.Writer, typeID frameType, payload []byte) error {
	if len(payload) > maxFramePayload {
		return fmt.Errorf("%w: frame payload is too large", ErrProtocol)
	}
	var header [headerSize]byte
	header[0] = protocolVersion
	header[1] = byte(typeID)
	binary.BigEndian.PutUint32(header[2:], uint32(len(payload)))
	if err := writeAll(writer, header[:]); err != nil {
		return err
	}
	return writeAll(writer, payload)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func encodeSize(size Size) ([]byte, error) {
	if !validSize(size) {
		return nil, ErrInvalidSize
	}
	payload := make([]byte, 8)
	binary.BigEndian.PutUint32(payload[0:4], uint32(size.Cols))
	binary.BigEndian.PutUint32(payload[4:8], uint32(size.Rows))
	return payload, nil
}

func decodeSize(payload []byte) (Size, error) {
	if len(payload) != 8 {
		return Size{}, fmt.Errorf("%w: invalid terminal size payload", ErrProtocol)
	}
	size := Size{
		Cols: uint(binary.BigEndian.Uint32(payload[0:4])),
		Rows: uint(binary.BigEndian.Uint32(payload[4:8])),
	}
	if !validSize(size) {
		return Size{}, ErrInvalidSize
	}
	return size, nil
}

func encodeReady(info Info) ([]byte, error) {
	return json.Marshal(readyPayload{Hostname: info.Hostname, User: info.User, Shell: info.Shell})
}

func decodeReady(payload []byte) (Info, error) {
	if len(payload) == 0 || len(payload) > maxControlPayload {
		return Info{}, fmt.Errorf("%w: invalid ready payload", ErrProtocol)
	}
	var ready readyPayload
	if err := decodeJSON(payload, &ready); err != nil {
		return Info{}, fmt.Errorf("%w: invalid ready payload: %v", ErrProtocol, err)
	}
	if ready.Hostname == "" || ready.User == "" || ready.Shell == "" {
		return Info{}, fmt.Errorf("%w: incomplete ready payload", ErrProtocol)
	}
	return Info{Hostname: ready.Hostname, User: ready.User, Shell: ready.Shell}, nil
}

func encodeExit(code int) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(int32(code)))
	return payload
}

func decodeExit(payload []byte) (int, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("%w: invalid exit payload", ErrProtocol)
	}
	return int(int32(binary.BigEndian.Uint32(payload))), nil
}

func encodeRemoteError(code, message string) []byte {
	payload, _ := json.Marshal(errorPayload{Code: code, Message: message})
	return payload
}

func decodeRemoteError(payload []byte) error {
	if len(payload) == 0 || len(payload) > maxControlPayload {
		return fmt.Errorf("%w: invalid error payload", ErrProtocol)
	}
	var remote errorPayload
	if err := decodeJSON(payload, &remote); err != nil {
		return fmt.Errorf("%w: invalid error payload: %v", ErrProtocol, err)
	}
	if remote.Code == "" {
		return fmt.Errorf("%w: error code is missing", ErrProtocol)
	}
	return &RemoteError{Code: remote.Code, Message: remote.Message}
}

func decodeJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
