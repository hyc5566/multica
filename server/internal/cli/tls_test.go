package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestExplicitCA(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("MULTICA_CA_CERT_FILE", "")
	if err := ConfigureTLSFromEnv(); err != nil {
		t.Fatal(err)
	}
	if http.DefaultTransport != original {
		t.Fatal("unset CA must preserve default transport")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			u := websocket.Upgrader{}
			c, err := u.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			kind, data, err := c.ReadMessage()
			if err == nil {
				_ = c.WriteMessage(kind, data)
			}
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	untrusted := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{}}, Timeout: time.Second}
	if r, err := untrusted.Get(server.URL); err == nil {
		r.Body.Close()
		t.Fatal("untrusted server accepted")
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_CA_CERT_FILE", path)
	if err := ConfigureTLSFromEnv(); err != nil {
		t.Fatal(err)
	}
	for _, client := range []*http.Client{{Timeout: time.Second}, NewStallAwareHTTPClient()} {
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "ok" {
			t.Fatalf("body=%q err=%v", body, err)
		}
		client.CloseIdleConnections()
	}
	dialer := websocket.Dialer{TLSClientConfig: DefaultTLSConfig()}
	ws, _, err := dialer.Dial("wss"+strings.TrimPrefix(server.URL, "https")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := ws.ReadMessage()
	if err != nil || string(msg) != "ping" {
		t.Fatalf("WS response=%q err=%v", msg, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(42), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	other.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	other.StartTLS()
	defer other.Close()
	for _, client := range []*http.Client{{Timeout: time.Second}, NewStallAwareHTTPClient()} {
		if r, err := client.Get(other.URL); err == nil {
			r.Body.Close()
			t.Fatal("unrelated CA accepted after configuring trust")
		}
		client.CloseIdleConnections()
	}
	if c, _, err := dialer.Dial("wss"+strings.TrimPrefix(other.URL, "https")+"/ws", nil); err == nil {
		c.Close()
		t.Fatal("unrelated WSS CA accepted")
	}
	wrongHost := DefaultTLSConfig()
	wrongHost.ServerName = "wrong.invalid"
	bad := &http.Client{Transport: &http.Transport{TLSClientConfig: wrongHost}, Timeout: time.Second}
	if r, err := bad.Get(server.URL); err == nil {
		r.Body.Close()
		t.Fatal("hostname mismatch accepted")
	}
	dialer.TLSClientConfig = wrongHost
	if c, _, err := dialer.Dial("wss"+strings.TrimPrefix(server.URL, "https")+"/ws", nil); err == nil {
		c.Close()
		t.Fatal("WSS hostname mismatch accepted")
	}
	for _, contents := range []string{"", "not a certificate"} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		before := http.DefaultTransport
		if err := ConfigureTLSFromEnv(); err == nil {
			t.Fatal("invalid CA accepted")
		}
		if http.DefaultTransport != before {
			t.Fatal("failed CA load changed transport")
		}
	}
	t.Setenv("MULTICA_CA_CERT_FILE", filepath.Join(t.TempDir(), "missing"))
	if err := ConfigureTLSFromEnv(); err == nil {
		t.Fatal("missing CA accepted")
	}
}
