package provider

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPClientSeparatesConnectionAndResponseHeaderTimeout(t *testing.T) {
	client := newHTTPClient(3 * time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	if transport.TLSHandshakeTimeout != 3*time.Second || transport.ResponseHeaderTimeout != 3*time.Second {
		t.Fatalf("TLS=%s headers=%s", transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout)
	}
}
