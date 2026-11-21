package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Listener  string // proxy listener host:port
	LogCopy   string // file Stop copies the instance log to, or empty
	// Container is the Docker container the instance runs in, or empty on
	// the host. Binary and ConfigDir are then paths inside it, and every
	// CLI call runs through docker exec.
	Container string
	// Control is the host:port of the control listener's network address,
	// or empty when the harness reaches the control API through the unix
	// socket. A container's socket is not shared with the host.
	Control string
}

// StartInstance launches a detached service instance in a scratch config
// directory under /tmp, which keeps the control socket path short.
func StartInstance(ctx context.Context, binary, project string) (*Instance, error) {
	configDir, err := os.MkdirTemp("/tmp", "mcf.")
	if err != nil {
		return nil, err
	}
	instance := &Instance{Binary: binary, ConfigDir: configDir, Name: fmt.Sprintf("cf%d", os.Getpid())}
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
	instance.Listener = started.ProxyListener
	return instance, nil
}

// Stop stops the instance, copies its log to LogCopy when set, and removes
// its config directory.
func (i *Instance) Stop() {
	_, _ = i.cli(context.Background(), nil, "service", "stop", "--json")
	if i.Container != "" {
		if i.LogCopy != "" {
			log := filepath.Join(i.ConfigDir, "instances", i.Name+".log")
			if data, err := exec.Command("docker", "exec", i.Container, "cat", log).Output(); err == nil {
				_ = os.WriteFile(i.LogCopy, data, 0o644)
			}
		}
		removeContainer(i.Container)
		return
	}
	if i.LogCopy != "" {
		if data, err := os.ReadFile(filepath.Join(i.ConfigDir, "instances", i.Name+".log")); err == nil {
			_ = os.WriteFile(i.LogCopy, data, 0o644)
		}
	}
	_ = os.RemoveAll(i.ConfigDir)
}

func (i *Instance) cli(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	argv := append([]string{i.Binary, "--config-dir", i.ConfigDir, "--instance", i.Name}, args...)
	if i.Container != "" {
		argv = append([]string{"docker", "exec", "-i", i.Container}, argv...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
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

// Address is the proxy listener host:port.
func (i *Instance) Address() string {
	return i.Listener
}

// Newest returns the ID of the newest item of kind the instance recorded,
// or "" when there is none.
func (i *Instance) Newest(ctx context.Context, kind TrafficKind) (string, error) {
	command, ok := kind.command()
	if !ok {
		return "", fmt.Errorf("no newest-item check for traffic kind %q", kind)
	}
	out, err := i.cli(ctx, nil, command, "list", "--limit", "1", "--json")
	if err != nil {
		return "", err
	}
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return "", fmt.Errorf("decoding %s list: %w", command, err)
	}
	if len(list.Items) == 0 {
		return "", nil
	}
	return list.Items[0].ID, nil
}

// Certificate returns the instance's loaded CA certificate, PEM-encoded,
// through certificate get.
func (i *Instance) Certificate(ctx context.Context) ([]byte, error) {
	return i.cli(ctx, nil, "certificate", "get")
}

var _ Certifier = (*Instance)(nil)

// EnableCheckpoint turns on global Checkpoint for kind, so every item of
// that kind is held until something forwards or drops it. HTTP intercept
// holds each request and, while it stays on, the request's response.
func (i *Instance) EnableCheckpoint(ctx context.Context, kind CheckpointKind) error {
	flag, ok := kind.flag()
	if !ok {
		return fmt.Errorf("no Checkpoint setup for kind %q", kind)
	}
	_, err := i.cli(ctx, nil, "checkpoint", flag, "on", "--json")
	return err
}

// client returns a control API client: over the control listener's network
// address when the instance has one, else over the unix socket.
func (i *Instance) client() (*service.Client, error) {
	if i.Control == "" {
		return service.NewClient(filepath.Join(i.ConfigDir, "instances", i.Name+".sock")), nil
	}
	endpoint, err := service.RemoteEndpoint("http://"+i.Control, "")
	if err != nil {
		return nil, err
	}
	return service.NewEndpointClient(endpoint), nil
}

// InstallExtension appends lua to the workshop extension.
func (i *Instance) InstallExtension(ctx context.Context, lua string) error {
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
	// The Lua goes in on stdin, which reaches an instance in a container
	// as well as one on the host.
	_, err = i.cli(ctx, []byte(extension.LuaContent+lua), "extension", "update", id, "--json")
	return err
}

// MarasiLauncher starts a Marasi service instance on the host for each
// proxied conformance run and sets it up for the run: Checkpoint with a
// resolver for the Checkpoint runs, the suite's Lua for every run and its
// extension Lua for the extension run.
// Its log is copied to the run's report directory when it stops.
func MarasiLauncher(binary string, log io.Writer) Launcher {
	return func(ctx context.Context, suite Suite, run RunName, dir string) (Proxy, error) {
		instance, err := StartInstance(ctx, binary, "conformance-"+suite.Name()+"-"+string(run))
		if err != nil {
			return nil, err
		}
		return prepare(ctx, instance, suite, run, dir, log)
	}
}

// prepare sets a started instance up for run and keeps its log in dir.
func prepare(ctx context.Context, instance *Instance, suite Suite, run RunName, dir string, log io.Writer) (_ Proxy, err error) {
	launched := &launchedInstance{Instance: instance}
	defer func() {
		if err != nil {
			launched.Stop()
		}
	}()
	// The instance log goes into the reports, so a failed run keeps it.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	instance.LogCopy = filepath.Join(dir, "marasi.log")
	setup := suite.Setup()
	if setup.Lua != "" {
		if err := instance.InstallExtension(ctx, setup.Lua); err != nil {
			return nil, err
		}
	}
	switch run {
	case RunProxy:
	case RunCheckpointForward, RunCheckpointEdit:
		for _, kind := range setup.Checkpoint {
			if err := instance.EnableCheckpoint(ctx, kind); err != nil {
				return nil, err
			}
		}
		client, err := instance.client()
		if err != nil {
			return nil, err
		}
		resolver := &Resolver{Edit: run == RunCheckpointEdit}
		launched.resolver = resolver
		resolveCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			resolver.Run(resolveCtx, client)
			close(done)
		}()
		launched.stops = append(launched.stops, func() {
			cancel()
			<-done
			fmt.Fprintf(log, "resolver forwarded %d items, %d rejected\n", resolver.Forwarded.Load(), resolver.Failed.Load())
		})
	case RunExtension:
		if err := instance.InstallExtension(ctx, setup.Extension); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w %q", ErrUnknownRun, run)
	}
	return launched, nil
}

// launchedInstance is an instance with the helpers its run started, which
// stop before it does.
type launchedInstance struct {
	*Instance
	stops    []func()
	resolver *Resolver // in the Checkpoint runs
}

// Held counts the items the resolver forwarded.
func (l *launchedInstance) Held(context.Context) (int64, error) {
	if l.resolver == nil {
		return 0, nil
	}
	return l.resolver.Forwarded.Load(), nil
}

func (l *launchedInstance) Stop() {
	for i := len(l.stops) - 1; i >= 0; i-- {
		l.stops[i]()
	}
	l.Instance.Stop()
}

var (
	_ Proxy  = (*launchedInstance)(nil)
	_ Holder = (*launchedInstance)(nil)
)

// Resolver forwards every held Checkpoint item through the control API.
// With Edit set, each forward carries the original content as an edit,
// taking the same path as a real edit: the raw bytes of an HTTP request or
// response or of a trailer block, the payload and opcode of a WebSocket
// message, the payload of a stream message (which keeps its length, so a
// declared length allows it).
type Resolver struct {
	Edit      bool
	Forwarded atomic.Int64 // items forwarded
	Failed    atomic.Int64 // forwards the service rejected
}

// resolverPoll is how often the resolver lists held items when no
// checkpoint.held event woke it. Events wake it at once; the poll only
// covers an event stream that dropped or has not connected yet.
const resolverPoll = 50 * time.Millisecond

// subscribeWait is how long the resolver waits for its event subscription
// before it starts listing; past it, the poll alone finds held items.
const subscribeWait = 5 * time.Second

// Run resolves items until ctx ends, then closes client. Every hold in a
// suite waits for the resolver, so it lists again as soon as a
// checkpoint.held event arrives instead of sleeping between polls.
func (r *Resolver) Run(ctx context.Context, client *service.Client) {
	defer client.Close()
	wake := make(chan struct{}, 1)
	subscribed := make(chan struct{})
	go watchHolds(ctx, client, wake, subscribed)
	select {
	case <-subscribed:
	case <-ctx.Done():
	case <-time.After(subscribeWait):
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

// heldItem is a held Checkpoint item: Raw for an HTTP request or response
// or a trailer block, Payload and Opcode for a WebSocket message, Payload
// for a stream message.
type heldItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Raw     []byte `json:"raw"`
	Payload []byte `json:"payload"`
	Opcode  int    `json:"opcode"`
}

func (r *Resolver) pending(ctx context.Context, client *service.Client) ([]heldItem, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://marasi/checkpoint", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var list struct {
		Items []heldItem `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (r *Resolver) forward(ctx context.Context, client *service.Client, item heldItem) {
	body := []byte("{}")
	if r.Edit {
		body = editBody(item)
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

// editBody is a forward that carries item's original content as an edit.
func editBody(item heldItem) []byte {
	var body []byte
	if item.Type == "stream" {
		payload := item.Payload
		if payload == nil {
			payload = []byte{}
		}
		body, _ = json.Marshal(struct {
			Payload []byte `json:"payload"`
		}{payload})
		return body
	}
	if item.Type == "websocket" {
		payload := item.Payload
		if payload == nil {
			payload = []byte{}
		}
		body, _ = json.Marshal(struct {
			Payload []byte `json:"payload"`
			Opcode  int    `json:"opcode"`
		}{payload, item.Opcode})
		return body
	}
	raw := item.Raw
	if raw == nil {
		raw = []byte{}
	}
	body, _ = json.Marshal(struct {
		Raw []byte `json:"raw"`
	}{raw})
	return body
}
