package autobahn

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tfkr-ae/marasi/service"
)

// Instance is a Marasi service instance started by the harness through the CLI.
type Instance struct {
	Binary    string // path to the marasi binary
	ConfigDir string // scratch config directory owned by the harness
	Name      string // instance name
	Proxy     string // proxy listener host:port
	LogCopy   string // file Stop copies the instance log to, or empty
}

// StartInstance launches a detached service instance in a scratch config
// directory under /tmp, which keeps the control socket path short.
func StartInstance(ctx context.Context, binary, project string) (*Instance, error) {
	configDir, err := os.MkdirTemp("/tmp", "mab.")
	if err != nil {
		return nil, err
	}
	instance := &Instance{Binary: binary, ConfigDir: configDir, Name: fmt.Sprintf("ab%d", os.Getpid())}
	out, err := instance.cli(ctx, nil, "service", "start", "--project-name", project, "--address", "127.0.0.1", "--port", "0", "--json")
	if err != nil {
		_ = os.RemoveAll(configDir)
		return nil, err
	}
	var started struct {
		ProxyListener string `json:"proxy_listener"`
	}
	if err := json.Unmarshal(out, &started); err != nil || started.ProxyListener == "" {
		instance.Stop()
		return nil, fmt.Errorf("reading service start output %q: %w", out, err)
	}
	instance.Proxy = started.ProxyListener
	return instance, nil
}

// Stop stops the instance, copies its log to LogCopy when set, and removes
// its config directory.
func (i *Instance) Stop() {
	_, _ = i.cli(context.Background(), nil, "service", "stop", "--json")
	if i.LogCopy != "" {
		if data, err := os.ReadFile(filepath.Join(i.ConfigDir, "instances", i.Name+".log")); err == nil {
			_ = os.WriteFile(i.LogCopy, data, 0o644)
		}
	}
	_ = os.RemoveAll(i.ConfigDir)
}

func (i *Instance) cli(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, i.Binary, append([]string{"--config-dir", i.ConfigDir, "--instance", i.Name}, args...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	} else {
		devNull, err := os.Open(os.DevNull)
		if err != nil {
			return nil, err
		}
		defer devNull.Close()
		cmd.Stdin = devNull
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("marasi %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// NewestWebSocket returns the ID of the newest WebSocket connection the
// instance recorded, or "" when there is none.
func (i *Instance) NewestWebSocket(ctx context.Context) (string, error) {
	out, err := i.cli(ctx, nil, "websocket", "list", "--limit", "1", "--json")
	if err != nil {
		return "", err
	}
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return "", fmt.Errorf("decoding websocket list: %w", err)
	}
	if len(list.Items) == 0 {
		return "", nil
	}
	return list.Items[0].ID, nil
}

// EnableWebSocketCheckpoint turns on global WebSocket Checkpoint, so every
// message is held until something forwards or drops it.
func (i *Instance) EnableWebSocketCheckpoint(ctx context.Context) error {
	_, err := i.cli(ctx, nil, "checkpoint", "websocket-intercept", "on", "--json")
	return err
}

// echoExtension sets every message payload to itself in processWebSocketMessage.
const echoExtension = `

-- Autobahn harness: a no-op edit through the extension path.
function processWebSocketMessage(message)
	message:set_payload(message:payload())
end
`

// InstallEchoExtension appends the payload-to-itself hook to the workshop extension.
func (i *Instance) InstallEchoExtension(ctx context.Context) error {
	out, err := i.cli(ctx, nil, "extension", "list", "--json")
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return fmt.Errorf("decoding extension list: %w", err)
	}
	id := ""
	for _, item := range list.Items {
		if item.Name == "workshop" {
			id = item.ID
		}
	}
	if id == "" {
		return errors.New("no workshop extension in the project")
	}
	out, err = i.cli(ctx, nil, "extension", "get", id, "--json")
	if err != nil {
		return err
	}
	var extension struct {
		LuaContent string `json:"lua_content"`
	}
	if err := json.Unmarshal(out, &extension); err != nil {
		return fmt.Errorf("decoding workshop extension: %w", err)
	}
	file := filepath.Join(i.ConfigDir, "autobahn-workshop.lua")
	if err := os.WriteFile(file, []byte(extension.LuaContent+echoExtension), 0o600); err != nil {
		return err
	}
	_, err = i.cli(ctx, nil, "extension", "update", id, "--file", file, "--json")
	return err
}

// Resolver forwards every held WebSocket Checkpoint item through the
// control API. With Edit set, each forward carries the original payload and
// opcode as an edit, taking the same path as a real edit.
type Resolver struct {
	Edit      bool
	Forwarded atomic.Int64 // items forwarded
	Failed    atomic.Int64 // forwards the service rejected
}

// resolverPoll is how often the resolver lists held items when no
// checkpoint.held event woke it. Events wake it at once; the poll only
// covers an event stream that dropped or has not connected yet.
const resolverPoll = 50 * time.Millisecond

// Run resolves items until ctx ends. Every hold in a suite waits for the
// resolver, so it lists again as soon as a checkpoint.held event arrives
// instead of sleeping between polls.
func (r *Resolver) Run(ctx context.Context, instance *Instance) {
	client := service.NewClient(filepath.Join(instance.ConfigDir, "instances", instance.Name+".sock"))
	defer client.Close()
	wake := make(chan struct{}, 1)
	subscribed := make(chan struct{})
	go watchHolds(ctx, client, wake, subscribed)
	select {
	case <-subscribed:
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
	for ctx.Err() == nil {
		items, err := r.pending(ctx, client)
		if err != nil || len(items) == 0 {
			select {
			case <-ctx.Done():
			case <-wake:
			case <-time.After(resolverPoll):
			}
			continue
		}
		for _, item := range items {
			r.forward(ctx, client, item)
		}
	}
}

// watchHolds signals wake on every checkpoint.held event until ctx ends,
// subscribing again when the service drops the stream. subscribed is closed
// once the first subscription is live.
func watchHolds(ctx context.Context, client *service.Client, wake chan<- struct{}, subscribed chan<- struct{}) {
	var once sync.Once
	for ctx.Err() == nil {
		_ = streamHolds(ctx, client, wake, func() { once.Do(func() { close(subscribed) }) })
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func streamHolds(ctx context.Context, client *service.Client, wake chan<- struct{}, onSubscribed func()) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/events", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("subscribing to events: %s", response.Status)
	}
	// The service subscribes before it sends the response headers.
	onSubscribed()

	held := []byte("event: checkpoint.held")
	reader := bufio.NewReaderSize(response.Body, 64<<10)
	midLine := false
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// A long data line; only event lines matter.
			midLine = true
			continue
		}
		if err != nil {
			return err
		}
		if !midLine && bytes.HasPrefix(line, held) {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		midLine = false
	}
}

type heldMessage struct {
	ID      string `json:"id"`
	Payload []byte `json:"payload"`
	Opcode  int    `json:"opcode"`
}

func (r *Resolver) pending(ctx context.Context, client *service.Client) ([]heldMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/checkpoint?kind=websocket", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var list struct {
		Items []heldMessage `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (r *Resolver) forward(ctx context.Context, client *service.Client, item heldMessage) {
	body := []byte("{}")
	if r.Edit {
		payload := item.Payload
		if payload == nil {
			payload = []byte{}
		}
		body, _ = json.Marshal(struct {
			Payload []byte `json:"payload"`
			Opcode  int    `json:"opcode"`
		}{payload, item.Opcode})
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://marasi/checkpoint/"+item.ID+"/forward", bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		r.Failed.Add(1)
		return
	}
	_ = response.Body.Close()
	// 404 means the connection went away while the item was listed.
	switch response.StatusCode {
	case http.StatusOK:
		r.Forwarded.Add(1)
	case http.StatusNotFound:
	default:
		r.Failed.Add(1)
	}
}
