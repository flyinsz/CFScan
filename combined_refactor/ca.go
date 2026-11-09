package main

import (
	"crypto/tls"
	"crypto/x509"
	"embed"
	"sync"
)

//go:embed ca-certificates.crt
var embeddedCAFS embed.FS

var rootCAPoolState = struct {
	sync.Once
	pool *x509.CertPool
}{}

func rootCAPool() *x509.CertPool {
	rootCAPoolState.Do(func() {
		pool, err := x509.SystemCertPool()
		if err == nil && pool != nil && len(pool.Subjects()) > 0 {
			rootCAPoolState.pool = pool
			return
		}

		data, err := embeddedCAFS.ReadFile("ca-certificates.crt")
		if err != nil || len(data) == 0 {
			return
		}
		fallback := x509.NewCertPool()
		if fallback.AppendCertsFromPEM(data) {
			rootCAPoolState.pool = fallback
		}
	})
	return rootCAPoolState.pool
}

func tlsConfigWithRootCAs(serverName string) *tls.Config {
	return &tls.Config{ServerName: serverName, RootCAs: rootCAPool()}
}
