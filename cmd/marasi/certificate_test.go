package main

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCertificateCommandLifecycle(t *testing.T) {
	binary := buildMarasi(t)
	configDir := serviceConfigDir(t)
	t.Cleanup(func() {
		runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop")
		runMarasi(binary, "--config-dir", configDir, "--instance", "other", "service", "stop")
	})

	stdout, stderr, err := runMarasi(binary, "--config-dir", configDir, "certificate")
	if err == nil || stdout != "" || !strings.Contains(stderr, "certificate requires a subcommand") {
		t.Fatalf("certificate without a subcommand: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--json", "--config-dir", configDir, "certificate")
	assertJSONCommandError(t, stdout, stderr, err, "certificate requires a subcommand")
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "certificate", "--format", "der")
	if err == nil || stdout != "" || !strings.Contains(stderr, "unknown flag: --format") {
		t.Fatalf("certificate group format flag: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	for _, format := range []string{"nope", "PEM", "cer"} {
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "certificate", "get", "--format", format)
		if err == nil || stdout != "" || !strings.Contains(stderr, "invalid certificate format: "+format) || strings.Contains(stderr, "not running") {
			t.Fatalf("invalid certificate format %q: stdout %q, stderr %q, error %v", format, stdout, stderr, err)
		}
	}
	for _, format := range []string{"der", "pem", "nope"} {
		stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "certificate", "get", "--format", format, "--json")
		assertJSONCommandError(t, stdout, stderr, err, "certificate get does not support --format with --json")
	}

	startNamedInstance(t, binary, configDir, "work", "--project-name", "certificate-one")
	certificatePEM, err := os.ReadFile(filepath.Join(configDir, "marasi_cert.pem"))
	if err != nil {
		t.Fatalf("reading published CA certificate: %v", err)
	}
	certificateBlock, _ := pem.Decode(certificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" {
		t.Fatalf("published CA certificate is not one CERTIFICATE block")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		t.Fatalf("parsing published CA certificate: %v", err)
	}
	wantPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "listener", "stop"); err != nil {
		t.Fatalf("stopping proxy listener: %v", err)
	}

	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating events stderr pipe: %v", err)
	}
	var eventsOutput bytes.Buffer
	eventsCommand := exec.Command(binary, "--config-dir", configDir, "--instance", "work", "events")
	eventsCommand.Stdout = &eventsOutput
	eventsCommand.Stderr = stderrWriter
	if err := eventsCommand.Start(); err != nil {
		stderrReader.Close()
		stderrWriter.Close()
		t.Fatalf("starting events command: %v", err)
	}
	stderrWriter.Close()
	t.Cleanup(func() {
		stderrReader.Close()
		if eventsCommand.ProcessState == nil {
			eventsCommand.Process.Kill()
			eventsCommand.Wait()
		}
	})
	connected := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stderrReader).ReadString('\n')
		connected <- line
	}()
	select {
	case line := <-connected:
		if line != ": connected\n" {
			t.Fatalf("events connection: got %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("events command did not connect")
	}

	for _, args := range [][]string{
		{"certificate", "get"},
		{"certificate", "get", "--format", "pem"},
	} {
		stdout, stderr, err = runMarasi(binary, append([]string{"--config-dir", configDir, "--instance", "work"}, args...)...)
		if err != nil || stdout != string(wantPEM) || stderr != "" {
			t.Fatalf("%v: stdout %q, stderr %q, error %v", args, stdout, stderr, err)
		}
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "certificate", "get", "--format", "der")
	if err != nil || stdout != string(certificate.Raw) || stderr != "" {
		t.Fatalf("DER certificate output: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	var jsonOutput string
	for _, args := range [][]string{
		{"--json", "--config-dir", configDir, "--instance", "work", "certificate", "get"},
		{"--config-dir", configDir, "--instance", "work", "certificate", "get", "--json"},
	} {
		stdout, stderr, err = runMarasi(binary, args...)
		if err != nil || stderr != "" || !strings.HasSuffix(stdout, "\n") || strings.Count(stdout, "\n") != 1 {
			t.Fatalf("JSON certificate output: stdout %q, stderr %q, error %v", stdout, stderr, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stdout), &fields); err != nil {
			t.Fatalf("decoding certificate JSON fields: %v", err)
		}
		if len(fields) != 6 {
			t.Fatalf("certificate JSON has %d fields, want exactly six: %s", len(fields), stdout)
		}
		for _, key := range []string{"pem", "subject", "issuer", "not_before", "not_after", "spki_hash"} {
			if _, ok := fields[key]; !ok {
				t.Fatalf("certificate JSON is missing %q: %s", key, stdout)
			}
		}
		var got struct {
			PEM       string    `json:"pem"`
			Subject   string    `json:"subject"`
			Issuer    string    `json:"issuer"`
			NotBefore time.Time `json:"not_before"`
			NotAfter  time.Time `json:"not_after"`
			SPKIHash  string    `json:"spki_hash"`
		}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("decoding certificate JSON: %v", err)
		}
		spkiHash := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
		if got.PEM != string(wantPEM) || got.Subject != certificate.Subject.String() || got.Issuer != certificate.Issuer.String() ||
			!got.NotBefore.Equal(certificate.NotBefore) || !got.NotAfter.Equal(certificate.NotAfter) ||
			got.SPKIHash != base64.StdEncoding.EncodeToString(spkiHash[:]) {
			t.Fatalf("certificate JSON does not describe the published CA: %#v", got)
		}
		if jsonOutput != "" && stdout != jsonOutput {
			t.Fatalf("JSON flag position changed the control response: first %q, second %q", jsonOutput, stdout)
		}
		jsonOutput = stdout
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "certificate", "get")
	if err == nil || stdout != "" || !strings.Contains(stderr, "instance missing is not running") {
		t.Fatalf("missing service instance: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "missing", "certificate", "get", "--json")
	assertJSONCommandError(t, stdout, stderr, err, "instance missing is not running")

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "certificate", "get")
	if err != nil || stdout != string(wantPEM) || stderr != "" {
		t.Fatalf("getting certificate with proxy listener stopped: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	replacementKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating replacement CA key: %v", err)
	}
	replacementTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Marasi Replacement Authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	replacementDER, err := x509.CreateCertificate(rand.Reader, replacementTemplate, replacementTemplate, &replacementKey.PublicKey, replacementKey)
	if err != nil {
		t.Fatalf("creating replacement CA certificate: %v", err)
	}
	replacementCertificate, err := x509.ParseCertificate(replacementDER)
	if err != nil {
		t.Fatalf("parsing replacement CA certificate: %v", err)
	}
	replacementKeyBytes, err := x509.MarshalPKCS8PrivateKey(replacementKey)
	if err != nil {
		t.Fatalf("encoding replacement CA key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "marasi_cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: replacementCertificate.Raw}), 0o600); err != nil {
		t.Fatalf("replacing published CA certificate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "marasi_key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: replacementKeyBytes}), 0o600); err != nil {
		t.Fatalf("replacing published CA key: %v", err)
	}
	replacementPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: replacementCertificate.Raw})

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "certificate", "get")
	if err != nil || stdout != string(wantPEM) || stderr != "" {
		t.Fatalf("first instance after certificate files changed: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	startNamedInstance(t, binary, configDir, "other", "--project-name", "certificate-two")
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "other", "certificate", "get")
	if err != nil || stdout != string(replacementPEM) || stderr != "" {
		t.Fatalf("second instance after certificate files changed: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "certificate", "get")
	if err != nil || stdout != string(wantPEM) || stderr != "" {
		t.Fatalf("first instance after second start: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}

	if _, _, err := runMarasi(binary, "--config-dir", configDir, "--instance", "work", "service", "stop"); err != nil {
		t.Fatalf("stopping first instance: %v", err)
	}
	if err := eventsCommand.Wait(); err != nil {
		t.Fatalf("waiting for events command after service stop: %v", err)
	}
	if eventsOutput.Len() != 0 {
		t.Fatalf("certificate reads emitted events: %s", eventsOutput.String())
	}

	stdout, stderr, err = runMarasi(binary, "--config-dir", configDir, "--instance", "work", "certificate", "get")
	if err == nil || stdout != "" || !strings.Contains(stderr, "instance work is not running") {
		t.Fatalf("stopped service instance with CA files present: stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}
