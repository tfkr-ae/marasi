package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
	"rsc.io/qr"
)

// qrQuietZone is the light border, in modules, around a QR code. The QR
// specification asks for four so a scanner can find the code's edges.
const qrQuietZone = 4

// errHostnameWithoutPassword refuses --host with a hostname on an instance
// with no control password, whose network address refuses a Host that is
// a hostname, so the URL in the QR code could never connect.
var errHostnameWithoutPassword = errors.New("the instance has no control password, so its network address accepts only an IP address as the host; pass an IP address to --host or set a control password")

func init() {
	var host string
	command := &cobra.Command{
		Use:   "pair",
		Short: "Print a QR code that another device scans to connect to the control listener's network address",
		Long: `Print a QR code for Pairing: another device scans it to connect to the
control listener's network address. The QR code holds a JSON payload with
the network address URL, the control password when one is set, and the
instance name. The URL is printed below it as text. --json prints only the
payload.

On the instance machine the payload is read through the socket. With
--remote it is built from the remote URL and the password the CLI holds.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPairing(cmd, host)
		},
	}
	command.Flags().StringVar(&host, "host", "", "Host to put in the URL instead of the network address's own, such as a VPN IP; the port is kept")
	serviceControlCmd.AddCommand(command)
}

// runPairing prints the Pairing payload of this process's instance, as a QR
// code or, with --json, as JSON.
func runPairing(cmd *cobra.Command, host string) error {
	read := readLocalPairing
	if endpoint.remote() {
		read = readRemotePairing
	}
	pairing, err := read(cmd.Context())
	if err != nil {
		return err
	}
	if host != "" {
		if pairing.URL, err = replaceURLHost(pairing.URL, host, pairing.Password != ""); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(pairing)
	if err != nil {
		return fmt.Errorf("encoding pairing payload: %w", err)
	}
	if jsonOutput {
		return writeJSONBody(cmd, append(payload, '\n'))
	}
	code, err := qr.Encode(string(payload), qr.M)
	if err != nil {
		return fmt.Errorf("encoding pairing QR code: %w", err)
	}
	if err := writeQRCode(cmd.OutOrStdout(), code); err != nil {
		return fmt.Errorf("writing pairing QR code: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", pairing.URL); err != nil {
		return fmt.Errorf("writing pairing URL: %w", err)
	}
	return nil
}

// readLocalPairing reads the Pairing payload through the socket, the only
// place the instance hands out its control password.
func readLocalPairing(ctx context.Context) (service.Pairing, error) {
	const operation = "reading pairing"
	response, err := callInstance(ctx, http.MethodGet, "/service/control-listener/pairing", "", nil)
	if err != nil {
		return service.Pairing{}, withStartHint(err)
	}
	if code, _ := controlRefusal(response); response.StatusCode == http.StatusConflict && code == "listener_inactive" {
		return service.Pairing{}, fmt.Errorf("%s: the instance has no control listener network address; start one with marasi service control start --address HOST --port PORT", operation)
	}
	if err := rejectInstanceStatus(operation, response, true); err != nil {
		return service.Pairing{}, err
	}
	var pairing service.Pairing
	if err := json.Unmarshal(response.Body, &pairing); err != nil || pairing.URL == "" {
		return service.Pairing{}, fmt.Errorf("decoding pairing payload: %s", response.Body)
	}
	return pairing, nil
}

// readRemotePairing builds the Pairing payload for the instance at
// --remote: the URL and control password this CLI already reaches it with,
// and the instance's name from its status. The password is included only
// when the instance has one, since a client of an instance without one
// needs none.
func readRemotePairing(ctx context.Context) (service.Pairing, error) {
	var status struct {
		Instance string `json:"instance"`
	}
	if err := readInstanceJSON(ctx, "/service/status", "getting service status", &status); err != nil {
		return service.Pairing{}, err
	}
	var control service.ControlListenerStatus
	if err := readInstanceJSON(ctx, "/service/control-listener/status", "getting control listener status", &control); err != nil {
		return service.Pairing{}, err
	}
	pairing := service.Pairing{URL: endpoint.control.String(), Instance: status.Instance}
	if control.PasswordSet {
		pairing.Password = controlPassword
	}
	return pairing, nil
}

// readInstanceJSON GETs path from this process's instance and decodes the
// JSON response into value.
func readInstanceJSON(ctx context.Context, path, operation string, value any) error {
	response, err := callInstance(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return err
	}
	if err := rejectInstanceStatus(operation, response, true); err != nil {
		return err
	}
	if err := json.Unmarshal(response.Body, value); err != nil {
		return fmt.Errorf("%s: decoding response: %w", operation, err)
	}
	return nil
}

// replaceURLHost returns rawURL with its host replaced by host and its port
// kept. A hostname is refused unless the instance has a control password.
func replaceURLHost(rawURL, host string, passwordSet bool) (string, error) {
	isIP := net.ParseIP(host) != nil
	if !isIP && (strings.ContainsAny(host, ":/?#@[]% \t") || strings.TrimSpace(host) == "") {
		return "", fmt.Errorf("invalid --host %q: want a hostname or IP address without a port", host)
	}
	if !isIP && !passwordSet {
		return "", fmt.Errorf("--host %s: %w", host, errHostnameWithoutPassword)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing pairing URL: %w", err)
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	return parsed.String(), nil
}

// writeQRCode draws code with Unicode half blocks, two modules per line,
// inside its quiet zone. Light modules are drawn and dark ones left blank,
// for the dark background most terminals have.
func writeQRCode(w io.Writer, code *qr.Code) error {
	var b strings.Builder
	for y := -qrQuietZone; y < code.Size+qrQuietZone; y += 2 {
		for x := -qrQuietZone; x < code.Size+qrQuietZone; x++ {
			// Black reports false outside the code, which draws the quiet zone.
			top, bottom := !code.Black(x, y), !code.Black(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
