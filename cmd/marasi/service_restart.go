package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/internal/filelock"
	"github.com/tfkr-ae/marasi/internal/update"
	"github.com/tfkr-ae/marasi/service"
)

// restartReadyTimeout bounds how long a restart waits for its successor.
const restartReadyTimeout = 30 * time.Second

// restartWarning names what a restart ends (ADR-0051).
const restartWarning = "warning: a restart ends Armory runs, held Checkpoint items, live WebSocket and HTTP connections, and launches\n"

var restartForce bool
var noProxyListener bool

func init() {
	restartCmd.Flags().BoolVar(&restartForce, "force", false, "Restart even when Checkpoint items are held, dropping them")
	startCmd.Flags().BoolVar(&noProxyListener, "no-proxy-listener", false, "")
	_ = startCmd.Flags().MarkHidden("no-proxy-listener")
	serviceCmd.AddCommand(restartCmd)
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart a service instance on the installed build",
	Long: "Restart a service instance on the installed build. The successor keeps the instance name, open project, and proxy listener address. " +
		"If it does not become ready, the instance keeps running on its current build. " +
		"A restart ends Armory runs, held Checkpoint items, live WebSocket and HTTP connections, and launches, and is refused while Checkpoint items are held unless --force is given.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return restartService(ctx, jsonOutput, restartForce, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

// restartService asks the selected instance to restart and prints the result.
func restartService(ctx context.Context, asJSON, force bool, stdout, stderr io.Writer) error {
	const operation = "restarting service"
	if !asJSON {
		if _, err := io.WriteString(stderr, restartWarning); err != nil {
			return fmt.Errorf("writing restart warning: %w", err)
		}
	}
	body, err := json.Marshal(struct {
		Force bool `json:"force"`
	}{Force: force})
	if err != nil {
		return fmt.Errorf("encoding restart request: %w", err)
	}
	response, err := callInstance(ctx, http.MethodPost, "/service/restart", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return withStartHint(err)
	}
	if response.StatusCode != http.StatusOK {
		apiErr := controlAPIError(operation, response.Status, response.Body)
		if asJSON {
			return apiErr
		}
		var refusal struct {
			Error string `json:"error"`
			Items []struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Summary string `json:"summary"`
			} `json:"items"`
		}
		if json.Unmarshal(response.Body, &refusal) == nil && refusal.Error == "checkpoint_items_held" {
			var message strings.Builder
			fmt.Fprintf(&message, "%s: %d Checkpoint items are held:", operation, len(refusal.Items))
			for _, item := range refusal.Items {
				fmt.Fprintf(&message, "\n  %s %s %s", item.ID, item.Type, item.Summary)
			}
			message.WriteString("\nforward or drop them, or rerun with --force to drop them")
			return errors.New(message.String())
		}
		return apiErr
	}
	if asJSON {
		_, err := stdout.Write(response.Body)
		return wrapError("writing service restart result", err)
	}
	var result struct {
		Instance      string  `json:"instance"`
		Version       string  `json:"version"`
		ProxyListener *string `json:"proxy_listener"`
	}
	if err := json.Unmarshal(response.Body, &result); err != nil {
		return fmt.Errorf("decoding service restart result: %w", err)
	}
	proxyListener := "inactive"
	if result.ProxyListener != nil {
		proxyListener = *result.ProxyListener
	}
	_, err = fmt.Fprintf(stderr, "instance %s restarted on version %s\nproxy listener: %s\n", result.Instance, result.Version, proxyListener)
	return wrapError("writing service restart result", err)
}

// handoverListener is the control socket listener of an instance that can
// hand its name to a successor. While handed over, Accept waits instead of
// failing, so the control server keeps serving the connections it has.
type handoverListener struct {
	mu      sync.Mutex
	current net.Listener
	changed chan struct{}
	closed  bool
	addr    net.Addr
}

func newHandoverListener(listener net.Listener) *handoverListener {
	return &handoverListener{current: listener, changed: make(chan struct{}), addr: listener.Addr()}
}

func (l *handoverListener) signal() {
	close(l.changed)
	l.changed = make(chan struct{})
}

// Accept accepts on the current socket, or waits while handed over.
func (l *handoverListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		current, closed, changed := l.current, l.closed, l.changed
		l.mu.Unlock()
		if closed {
			return nil, net.ErrClosed
		}
		if current == nil {
			<-changed
			continue
		}
		conn, err := current.Accept()
		if err == nil {
			return conn, nil
		}
		l.mu.Lock()
		replaced := l.current != current || l.closed
		l.mu.Unlock()
		if !replaced {
			return nil, err
		}
	}
}

// suspend closes the current socket listener and makes Accept wait.
func (l *handoverListener) suspend() error {
	l.mu.Lock()
	current := l.current
	l.current = nil
	l.signal()
	l.mu.Unlock()
	if current == nil {
		return nil
	}
	if err := current.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// resume accepts on listener again.
func (l *handoverListener) resume(listener net.Listener) {
	l.mu.Lock()
	l.current = listener
	l.signal()
	l.mu.Unlock()
}

// Close ends Accept for good.
func (l *handoverListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	l.closed = true
	current := l.current
	l.current = nil
	l.signal()
	l.mu.Unlock()
	if current == nil {
		return nil
	}
	return current.Close()
}

func (l *handoverListener) Addr() net.Addr { return l.addr }

// instanceClaim is an instance's name: its lock and its control socket.
type instanceClaim struct {
	mu         sync.Mutex
	socketPath string
	lockPath   string
	listener   *handoverListener
	lock       *os.File
	handedOver bool
}

// claimInstanceName locks lockPath and listens on socketPath.
func claimInstanceName(socketPath, lockPath string) (*instanceClaim, error) {
	listener, lock, err := claimInstance(socketPath, lockPath)
	if err != nil {
		return nil, err
	}
	return &instanceClaim{socketPath: socketPath, lockPath: lockPath, listener: newHandoverListener(listener), lock: lock}, nil
}

// release gives the name up at shutdown. A name already handed over belongs
// to the successor, so only the listener closes.
func (c *instanceClaim) release() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handedOver {
		if err := c.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return wrapError("closing instance listener", err)
		}
		return nil
	}
	return releaseInstance(c.listener, c.socketPath, c.lock)
}

// handOver stops listening, removes the socket, and unlocks the name so a
// successor can claim it. Connections already accepted keep being served.
func (c *instanceClaim) handOver() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handedOver {
		return nil
	}
	suspendErr := c.listener.suspend()
	removeErr := os.Remove(c.socketPath)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	unlockErr := filelock.Unlock(c.lock)
	closeErr := c.lock.Close()
	c.lock = nil
	c.handedOver = true
	return errors.Join(
		wrapError("closing instance listener", suspendErr),
		wrapError("removing instance socket", removeErr),
		wrapError("unlocking instance", unlockErr),
		wrapError("closing instance lock", closeErr),
	)
}

// reclaim takes the name back after a successor failed.
func (c *instanceClaim) reclaim() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.handedOver {
		return nil
	}
	lock, err := acquireInstanceLock(c.lockPath)
	if err != nil {
		return err
	}
	listener, err := listenOnInstanceSocket(c.socketPath)
	if err != nil {
		return errors.Join(err, filelock.Unlock(lock), lock.Close())
	}
	c.lock = lock
	c.listener.resume(listener)
	c.handedOver = false
	return nil
}

// successorStarter starts successors for one instance.
type successorStarter struct {
	claim        *instanceClaim
	configDir    string
	instancePath string
	readyTimeout time.Duration
}

// start hands the instance name to a successor from the installed build and
// waits for it to report ready. On failure it takes the name back. The
// Server never cancels ctx: a restart finishes once begun, so only
// readyTimeout bounds the wait.
func (s successorStarter) start(ctx context.Context, project string, proxyListener *string) (service.Successor, error) {
	logPath := s.instancePath + ".log"
	fail := func(err error) (service.Successor, error) {
		return service.Successor{}, &service.RestartError{Log: logPath, Err: errors.Join(err, wrapError("reclaiming instance", s.claim.reclaim()))}
	}
	executable, err := update.Executable()
	if err != nil {
		return service.Successor{}, &service.RestartError{Log: logPath, Err: fmt.Errorf("locating installed build: %w", err)}
	}
	instanceName := filepath.Base(s.instancePath)
	args := []string{
		"--config-dir", s.configDir,
		"--instance", instanceName,
		"service", "start",
		"--project", project,
		"--service-child",
	}
	if proxyListener != nil {
		host, port, err := net.SplitHostPort(*proxyListener)
		if err != nil {
			return service.Successor{}, &service.RestartError{Log: logPath, Err: fmt.Errorf("reading proxy listener %q: %w", *proxyListener, err)}
		}
		args = append(args, "--address", host, "--port", port)
	} else {
		args = append(args, "--no-proxy-listener")
	}
	logFile, err := openInstanceLog(logPath)
	if err != nil {
		return service.Successor{}, &service.RestartError{Log: logPath, Err: err}
	}
	defer logFile.Close()

	if err := s.claim.handOver(); err != nil {
		return fail(err)
	}
	command := exec.Command(executable, args...)
	configureDetachedProcess(command)
	// The successor's own failures before it logs anything still reach its log.
	command.Stderr = logFile
	childInput, startupResult, processResult, err := launchServiceChild(command)
	if err != nil {
		return fail(err)
	}
	timer := time.NewTimer(s.readyTimeout)
	defer timer.Stop()
	var startup childStartup
	select {
	case startup = <-startupResult:
	case <-timer.C:
		_ = command.Process.Kill()
		stopChild(childInput, processResult)
		return fail(fmt.Errorf("successor did not become ready within %s", s.readyTimeout))
	}
	if startup.err != nil {
		stopChild(childInput, processResult)
		return fail(fmt.Errorf("successor failed to start: %w", startup.err))
	}
	if _, err := childInput.Write([]byte{detachChild}); err != nil {
		stopChild(childInput, processResult)
		return fail(fmt.Errorf("detaching successor: %w", err))
	}
	_ = childInput.Close()

	successor := service.Successor{}
	if startup.proxyListener != "" {
		address := startup.proxyListener
		successor.ProxyListener = &address
	}
	statusCtx, cancel := context.WithTimeout(ctx, s.readyTimeout)
	defer cancel()
	response, err := dialInstance(statusCtx, s.claim.socketPath, instanceName, http.MethodGet, "/service/status", "", nil)
	if err == nil && response.StatusCode == http.StatusOK {
		var status struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(response.Body, &status) == nil {
			successor.Version = status.Version
		}
	}
	return successor, nil
}
