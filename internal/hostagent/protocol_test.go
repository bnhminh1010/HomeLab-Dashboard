package hostagent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	payload := []byte("hello")
	if err := writeFrame(&wire, frameInput, payload); err != nil {
		t.Fatal(err)
	}
	message, err := readFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if message.typeID != frameInput || !bytes.Equal(message.payload, payload) {
		t.Fatalf("unexpected frame: %#v", message)
	}
}

func TestReadFrameRejectsVersionAndOversize(t *testing.T) {
	tests := []struct {
		name   string
		header [headerSize]byte
	}{
		{name: "version", header: [headerSize]byte{protocolVersion + 1, byte(frameOpen)}},
		{name: "oversize", header: [headerSize]byte{protocolVersion, byte(frameInput)}},
	}
	binary.BigEndian.PutUint32(tests[1].header[2:], maxFramePayload+1)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := readFrame(bytes.NewReader(test.header[:]))
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("expected protocol error, got %v", err)
			}
		})
	}
}

func TestSizeValidation(t *testing.T) {
	for _, size := range []Size{{Cols: 19, Rows: 30}, {Cols: 120, Rows: 4}, {Cols: 301, Rows: 30}, {Cols: 120, Rows: 101}} {
		if _, err := encodeSize(size); !errors.Is(err, ErrInvalidSize) {
			t.Fatalf("expected invalid size for %#v, got %v", size, err)
		}
	}
	payload, err := encodeSize(Size{Cols: 120, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeSize(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Size{Cols: 120, Rows: 30}) {
		t.Fatalf("unexpected decoded size: %#v", got)
	}
}

func TestRemoteErrorMatchesPublicErrors(t *testing.T) {
	tests := []struct {
		code string
		want error
	}{
		{code: "session_limit", want: ErrSessionLimit},
		{code: "idle_timeout", want: ErrIdleTimeout},
		{code: "maximum_duration", want: ErrHardTimeout},
	}
	for _, test := range tests {
		if !errors.Is(&RemoteError{Code: test.code}, test.want) {
			t.Errorf("%s must match %v", test.code, test.want)
		}
	}
	if errors.Is(&RemoteError{Code: "idle_timeout"}, ErrSessionLimit) {
		t.Fatal("unrelated remote error matched ErrSessionLimit")
	}
}
