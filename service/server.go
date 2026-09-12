package service

import (
	"net/http"

	"github.com/tfkr-ae/marasi"
)

func NewServer(proxy *marasi.Proxy, stop func()) http.Handler {
	mux := http.NewServeMux()
	addRoutes(mux, proxy, stop)
	return mux
}
