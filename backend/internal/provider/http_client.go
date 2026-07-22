package provider

import (
	"net"
	"net/http"
	"time"
)

const defaultConnectTimeout = 10 * time.Second

func newHTTPClient(connectTimeout time.Duration) *http.Client {
	if connectTimeout <= 0 {
		connectTimeout = defaultConnectTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   connectTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = connectTimeout
	transport.ResponseHeaderTimeout = connectTimeout
	return &http.Client{Transport: transport, Timeout: 10 * time.Minute}
}
