package nodes

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProtocolRoundTripAndStrictDecode(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	message, err := NewMessage("node_one", MessageHeartbeat, 1, now, "", Heartbeat{AgentVersion: "1.0.0", Hostname: "compute-1"})
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte(`{"version":1,"type":"agent.heartbeat","nodeId":"node_one","seq":1,"timestamp":"2026-07-19T12:00:00Z","payload":{"agentVersion":"1.0.0","hostname":"compute-1"}}`)
	decoded, err := DecodeMessage(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := DecodePayload[Heartbeat](decoded)
	if err != nil || heartbeat.Hostname != "compute-1" || message.Timestamp != now {
		t.Fatalf("heartbeat = %#v, err = %v", heartbeat, err)
	}
	if _, err := DecodeMessage(strings.NewReader(`{"version":1,"type":"agent.heartbeat","nodeId":"node_one","seq":1,"timestamp":"2026-07-19T12:00:00Z","extra":true}`)); err == nil {
		t.Fatal("unknown envelope field was accepted")
	}
	if _, err := DecodePayload[Heartbeat](Message{Type: MessageHeartbeat, Payload: []byte(`{"hostname":"ok","unknown":true}`)}); err == nil {
		t.Fatal("unknown payload field was accepted")
	}
}

func TestProtocolRejectsUnknownTypesAndOversizeFrames(t *testing.T) {
	if _, err := NewMessage("node", "shell.command", 1, time.Now(), "", nil); !errors.Is(err, ErrProtocolType) {
		t.Fatalf("unknown type error = %v", err)
	}
	oversize := bytes.NewReader(bytes.Repeat([]byte("x"), MaxMessageBytes+1))
	if _, err := DecodeMessage(oversize); !errors.Is(err, ErrProtocolSize) {
		t.Fatalf("oversize error = %v", err)
	}
}
