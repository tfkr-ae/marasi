// Package nginx runs the reference target of the client-leg suites: nginx
// pinned by digest, in a container on the host's network, with fixed
// locations and, for TLS, a certificate from the harness CA. A suite
// chooses the listeners: plaintext h1, TLS h1 and TLS with h2 by ALPN.
package nginx

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tfkr-ae/marasi/internal/conformance"
)

// Image is nginx 1.29.3, pinned by digest. It ships the njs module the
// locations use.
const Image = "nginx:1.29.3@sha256:2f4e10193b0c4f61472245e9d26cea78e7df7af266c7e7cf5e67353ee2731b51"

// configDir is where the configuration, handlers and certificate go in the
// container.
const configDir = "/etc/nginx/conformance"

// echoJS holds the handlers of the nginx server Http11Probe's own results
// are measured on, so its verdicts mean what they mean upstream.
//
//go:embed echo.js
var echoJS []byte

// config serves the same fixed locations on every listener:
//   - /echo: 200 with the request's header fields, one per line
//   - /cookie: 200 with the Cookie pairs, one per line
//   - /: 200 "OK", or the request body for POST
var config = template.Must(template.New("nginx.conf").Parse(`load_module modules/ngx_http_js_module.so;

worker_processes 1;
error_log /dev/stderr info;

events {
    worker_connections 256;
}

http {
    access_log /dev/stdout;
    absolute_redirect off;
    js_import echo from ` + configDir + `/echo.js;
{{range .Listeners}}
    server {
        listen {{.Address}}{{if .TLS}} ssl{{end}};
{{- if .HTTP2}}
        http2 on;
{{- end}}
{{- if .TLS}}
        ssl_certificate     ` + configDir + `/cert.pem;
        ssl_certificate_key ` + configDir + `/key.pem;
{{- end}}

        location /echo {
            js_content echo.echo;
        }

        location /cookie {
            js_content echo.cookie;
        }

        location / {
            js_content echo.handler;
        }
    }
{{end}}}
`))

// Listener is one address nginx listens on.
type Listener struct {
	Address string // host:port; one on 127.0.0.1 is reachable from the harness
	TLS     bool   // serve TLS with the harness CA's certificate
	HTTP2   bool   // offer h2 by ALPN; needs TLS
}

// Config says how to start nginx.
type Config struct {
	Name      string // container name
	Image     string // empty means Image
	Listeners []Listener
	// CA issues the certificate for Names, which the TLS listeners serve.
	CA    *conformance.CA
	Names []string
}

// Server is a running nginx.
type Server struct {
	name string
}

// Start starts nginx on the host's network and waits until every
// listener on 127.0.0.1 takes connections. A listener on another loopback
// address, such as 127.0.0.2, is up when those are: nginx binds every
// listener before it serves any.
func Start(ctx context.Context, c Config) (*Server, error) {
	if c.Image == "" {
		c.Image = Image
	}
	dir, err := os.MkdirTemp("", "conformance-nginx-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var conf bytes.Buffer
	if err := config.Execute(&conf, c); err != nil {
		return nil, err
	}
	files := map[string][]byte{"nginx.conf": conf.Bytes(), "echo.js": echoJS}
	for _, l := range c.Listeners {
		if l.TLS {
			if c.CA == nil {
				return nil, errors.New("a TLS listener needs the harness CA")
			}
			cert, key, err := c.CA.Issue(c.Names...)
			if err != nil {
				return nil, err
			}
			files["cert.pem"], files["key.pem"] = cert, key
			break
		}
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return nil, err
		}
	}

	_ = exec.Command("docker", "rm", "--force", c.Name).Run()
	if err := docker(ctx, "create", "--name", c.Name, "--network", "host", c.Image,
		"nginx", "-c", configDir+"/nginx.conf", "-g", "daemon off;"); err != nil {
		return nil, err
	}
	s := &Server{name: c.Name}
	if err := docker(ctx, "cp", dir, c.Name+":"+configDir); err != nil {
		s.Stop("")
		return nil, err
	}
	if err := docker(ctx, "start", c.Name); err != nil {
		s.Stop("")
		return nil, err
	}
	for _, l := range c.Listeners {
		if host, _, _ := net.SplitHostPort(l.Address); host != "127.0.0.1" {
			continue
		}
		if err := conformance.AwaitListener(ctx, l.Address); err != nil {
			logs, _ := exec.Command("docker", "logs", c.Name).CombinedOutput()
			s.Stop("")
			return nil, fmt.Errorf("nginx: %w\n%s", err, logs)
		}
	}
	return s, nil
}

// Stop writes nginx's log to logFile, unless it is empty, and removes the
// container.
func (s *Server) Stop(logFile string) {
	if logFile != "" {
		if logs, err := exec.Command("docker", "logs", s.name).CombinedOutput(); err == nil {
			_ = os.WriteFile(logFile, logs, 0o644)
		}
	}
	_ = exec.Command("docker", "rm", "--force", s.name).Run()
}

// FreePort returns a port nothing listens on at 127.0.0.1 right now.
func FreePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	return port, err
}

func docker(ctx context.Context, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
