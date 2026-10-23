// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"gamewe-collector/internal/config"
)

// BuildTLS turns the tls section into a *tls.Config. It returns nil for
// mode disable. The modes mean:
//
//	require      encrypt, do not verify the server certificate
//	verify-ca    encrypt, verify the chain against ca_file (or system CAs)
//	verify-full  like verify-ca, and the certificate must match the host
//	             (or server_name)
func BuildTLS(t config.TLS, host string) (*tls.Config, error) {
	if t.Mode == config.TLSDisable {
		return nil, nil
	}
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	c.ServerName = t.ServerName
	if c.ServerName == "" {
		c.ServerName = host
	}
	if t.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("database.tls client certificate: %w", err)
		}
		c.Certificates = []tls.Certificate{cert}
	}
	var roots *x509.CertPool
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("database.tls.ca_file: %w", err)
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("database.tls.ca_file %s: no PEM certificates found", t.CAFile)
		}
	}
	switch t.Mode {
	case config.TLSRequire:
		c.InsecureSkipVerify = true
	case config.TLSVerifyCA:
		// Go cannot verify the chain without the host name, so verification
		// is done by hand in VerifyConnection.
		c.InsecureSkipVerify = true
		c.VerifyConnection = func(cs tls.ConnectionState) error {
			return verifyChain(cs, roots)
		}
	case config.TLSVerifyFull:
		c.RootCAs = roots
	default:
		return nil, fmt.Errorf("unknown tls mode %q", t.Mode)
	}
	return c, nil
}

func verifyChain(cs tls.ConnectionState, roots *x509.CertPool) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("server sent no certificate")
	}
	opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
	for _, ic := range cs.PeerCertificates[1:] {
		opts.Intermediates.AddCert(ic)
	}
	_, err := cs.PeerCertificates[0].Verify(opts)
	return err
}
