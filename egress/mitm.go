package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

type caAuthority struct {
	cert  *x509.Certificate
	key   crypto.Signer
	pool  *x509.CertPool
	cache sync.Map
}

func loadCA(path string) (*caAuthority, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cert *x509.Certificate
	var key crypto.Signer
	pool := x509.NewCertPool()
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			c, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, err
			}
			pool.AddCert(c)
			if cert == nil && c.IsCA {
				cert = c
			}
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			signer, ok := k.(crypto.Signer)
			if !ok {
				return nil, fmt.Errorf("unsupported CA key type")
			}
			key = signer
		case "RSA PRIVATE KEY":
			k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			key = k
		case "EC PRIVATE KEY":
			k, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			key = k
		}
	}
	if cert == nil {
		return nil, fmt.Errorf("no CA certificate found")
	}
	if key == nil {
		return nil, fmt.Errorf("no CA private key found")
	}
	return &caAuthority{cert: cert, key: key, pool: pool}, nil
}

func (a *caAuthority) leaf(host string) (*tls.Certificate, error) {
	if v, ok := a.cache.Load(host); ok {
		return v.(*tls.Certificate), nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		template.IPAddresses = []net.IP{net.ParseIP(addr.String())}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{Certificate: [][]byte{der, a.cert.Raw}, PrivateKey: key, Leaf: leaf}
	actual, _ := a.cache.LoadOrStore(host, c)
	return actual.(*tls.Certificate), nil
}
