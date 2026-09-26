package service

import (
	"encoding/pem"
	"net/http"
	"time"

	"github.com/tfkr-ae/marasi"
)

func addCertificateRoutes(mux *http.ServeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("GET /certificate", func(w http.ResponseWriter, r *http.Request) {
		certificate := proxy.Cert
		writeJSON(w, r, http.StatusOK, struct {
			PEM       string    `json:"pem"`
			Subject   string    `json:"subject"`
			Issuer    string    `json:"issuer"`
			NotBefore time.Time `json:"not_before"`
			NotAfter  time.Time `json:"not_after"`
			SPKIHash  string    `json:"spki_hash"`
		}{
			PEM:       string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})),
			Subject:   certificate.Subject.String(),
			Issuer:    certificate.Issuer.String(),
			NotBefore: certificate.NotBefore,
			NotAfter:  certificate.NotAfter,
			SPKIHash:  proxy.SPKIHash,
		})
	})
}
