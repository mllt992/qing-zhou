package sshctl

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestPinnedHostKeyIsSelectedAmongOtherServerKeys(t *testing.T) {
	ecdsaPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaSigner, err := ssh.NewSignerFromKey(ecdsaPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, edPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edSigner, err := ssh.NewSignerFromKey(edPrivate)
	if err != nil {
		t.Fatal(err)
	}
	rsaPrivate, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(rsaPrivate)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &ssh.Certificate{Key: rsaSigner.PublicKey(), CertType: ssh.HostCert, ValidPrincipals: []string{"fixture"}, ValidAfter: 0, ValidBefore: ssh.CertTimeInfinity}
	if err := certificate.SignCert(rand.Reader, edSigner); err != nil {
		t.Fatal(err)
	}
	certSigner, err := ssh.NewCertSigner(certificate, rsaSigner)
	if err != nil {
		t.Fatal(err)
	}
	legacyRSA, err := ssh.NewSignerWithAlgorithms(rsaSigner.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSA})
	if err != nil {
		t.Fatal(err)
	}
	legacyCert, err := ssh.NewSignerWithAlgorithms(certSigner.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSA})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		pin    ssh.Signer
		others []ssh.Signer
	}{
		{"ed25519", edSigner, []ssh.Signer{ecdsaSigner, rsaSigner}},
		{"rsa-sha2", rsaSigner, []ssh.Signer{ecdsaSigner, edSigner}},
		{"rsa-legacy-compatible", legacyRSA, []ssh.Signer{ecdsaSigner, edSigner}},
		{"rsa-certificate-sha2", certSigner, []ssh.Signer{ecdsaSigner, edSigner}},
		{"rsa-certificate-legacy-compatible", legacyCert, []ssh.Signer{ecdsaSigner, edSigner}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, port := startHostKeyNegotiationServer(t, append(tc.others, tc.pin)...)
			cfg := ServerConfig{Host: host, Port: port, SSHUser: "fixture", SSHPassword: "fixture", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(tc.pin.PublicKey())))}
			manager := New(WithTimeout(2 * time.Second))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := manager.dial(ctx, &cfg)
			if err != nil {
				t.Fatalf("pinned key rejected with other server keys present: %v", err)
			}
			client.Close()
		})
	}
}

func TestPinnedHostKeyDoesNotTrustAnotherKeyOfSameType(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := ssh.NewSignerFromKey(other)
	if err != nil {
		t.Fatal(err)
	}
	host, port := startHostKeyNegotiationServer(t, signer)
	cfg := ServerConfig{Host: host, Port: port, SSHUser: "fixture", SSHPassword: "fixture", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey())))}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := New(WithTimeout(2*time.Second)).dial(ctx, &cfg)
	if err == nil {
		client.Close()
		t.Fatal("wrong pinned key accepted")
	}
	if !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("unexpected failure: %v", err)
	}
}

func startHostKeyNegotiationServer(t *testing.T, signers ...ssh.Signer) (string, int) {
	t.Helper()
	server := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	for _, signer := range signers {
		server.AddHostKey(signer)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer raw.Close()
				conn, channels, requests, err := ssh.NewServerConn(raw, server)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					channel.Reject(ssh.Prohibited, "handshake fixture only")
				}
			}()
		}
	}()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}
