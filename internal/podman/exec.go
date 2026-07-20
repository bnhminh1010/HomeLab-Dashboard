package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

func (c *Client) CreateShellExec(ctx context.Context, containerID string, shell Shell, size TerminalSize) (string, error) {
	if err := validateIdentifier(containerID); err != nil {
		return "", err
	}
	if !shell.valid() {
		return "", ErrInvalidShell
	}
	details, err := c.InspectContainer(ctx, containerID)
	if err != nil {
		return "", err
	}
	if details.Protected || IsHidden(details.Labels) {
		return "", ErrProtectedContainer
	}
	if !details.Running {
		return "", ErrContainerNotRunning
	}

	input := struct {
		AttachStdin  bool     `json:"AttachStdin"`
		AttachStdout bool     `json:"AttachStdout"`
		AttachStderr bool     `json:"AttachStderr"`
		TTY          bool     `json:"Tty"`
		Command      []string `json:"Cmd"`
		Privileged   bool     `json:"Privileged"`
	}{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		TTY:          true,
		Command:      []string{string(shell)},
		Privileged:   false,
	}
	var output struct {
		ID string `json:"Id"`
	}
	endpoint := "/containers/" + url.PathEscape(containerID) + "/exec"
	if err := c.doJSON(ctx, http.MethodPost, endpoint, nil, input, &output); err != nil {
		return "", err
	}
	if output.ID == "" {
		return "", errors.New("podman: create exec returned an empty ID")
	}
	return output.ID, nil
}

// StartExec starts an interactive TTY and returns the upgraded full-duplex
// stream. Callers must close the stream to unblock Podman on disconnect.
func (c *Client) StartExec(ctx context.Context, execID string, size TerminalSize) (io.ReadWriteCloser, error) {
	if err := validateIdentifier(execID); err != nil {
		return nil, err
	}
	input := struct {
		Detach bool `json:"Detach"`
		TTY    bool `json:"Tty"`
		Rows   uint `json:"h,omitempty"`
		Cols   uint `json:"w,omitempty"`
	}{Detach: false, TTY: true, Rows: size.Rows, Cols: size.Cols}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("podman: encode exec start: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/exec/"+url.PathEscape(execID)+"/start", nil), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("podman: create exec start request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "tcp")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("podman: start exec: %w", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		err := decodeAPIError(response)
		response.Body.Close()
		return nil, err
	}
	stream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		response.Body.Close()
		return nil, errors.New("podman: upgraded exec response is not writable")
	}
	return stream, nil
}

func (c *Client) ResizeExec(ctx context.Context, execID string, size TerminalSize) error {
	if err := validateIdentifier(execID); err != nil {
		return err
	}
	if size.Cols == 0 || size.Rows == 0 {
		return errors.New("podman: terminal dimensions must be positive")
	}
	query := url.Values{
		"h": {strconv.FormatUint(uint64(size.Rows), 10)},
		"w": {strconv.FormatUint(uint64(size.Cols), 10)},
	}
	return c.doJSON(ctx, http.MethodPost, "/exec/"+url.PathEscape(execID)+"/resize", query, struct{}{}, nil)
}

func (c *Client) RemoveExec(ctx context.Context, execID string, force bool) error {
	if err := validateIdentifier(execID); err != nil {
		return err
	}
	input := struct {
		Force bool `json:"Force"`
	}{Force: force}
	return c.doJSON(ctx, http.MethodPost, "/exec/"+url.PathEscape(execID)+"/remove", nil, input, nil)
}
