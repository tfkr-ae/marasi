package service

import (
	"net/http"

	"github.com/tfkr-ae/marasi"
)

func NewServer(proxy *marasi.Proxy) http.Handler {
	mux := http.NewServeMux()
	addRoutes(mux)
	return mux
}
