package cli

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
)

var loadedCASHA256 string

// LoadedCASHA256 identifies the bundle actually loaded at CLI startup, not
// whatever may have replaced that file while the daemon was running.
func LoadedCASHA256() string { return loadedCASHA256 }

// ConfigureTLSFromEnv runs before CLI networking starts. Go's macOS system
// verifier does not read SSL_CERT_FILE, so Desktop supplies an explicit bundle.
// This only changes this process; it never modifies the OS trust store.
func ConfigureTLSFromEnv() error {
	path := os.Getenv("MULTICA_CA_CERT_FILE")
	if path == "" {
		return nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read MULTICA_CA_CERT_FILE: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return fmt.Errorf("load system certificates: %w", err)
	}
	if !roots.AppendCertsFromPEM(pem) {
		return fmt.Errorf("MULTICA_CA_CERT_FILE contains no valid certificates")
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return fmt.Errorf("cannot configure CA: unsupported default HTTP transport")
	}
	transport := base.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.RootCAs = roots
	http.DefaultTransport = transport
	loadedCASHA256 = fmt.Sprintf("%x", sha256.Sum256(pem))
	return nil
}

// DefaultTLSConfig shares the CLI's explicit trust with independent transports.
func DefaultTLSConfig() *tls.Config {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport.TLSClientConfig != nil {
		config := transport.TLSClientConfig.Clone()
		// net/http adds h2 to its config after use. Each independent transport
		// must choose its own ALPN protocols; gorilla WebSocket only speaks HTTP/1.1.
		config.NextProtos = nil
		return config
	}
	return nil
}
