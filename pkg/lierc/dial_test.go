package lierc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDialerRefusesPrivate(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	called := false
	d := dialer(false, false, func(net.Conn) { called = true })
	conn, err := d(context.Background(), "tcp", ln.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("dial to 127.0.0.1 should fail")
	}
	if !strings.Contains(err.Error(), "private address") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("onConnected called for a refused dial")
	}
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestDialerTLS(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("x"))
		time.Sleep(500 * time.Millisecond)
	}()

	var got []net.Conn
	d := dialer(true, true, func(c net.Conn) { got = append(got, c) })
	conn, err := d(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if len(got) != 1 {
		t.Fatalf("onConnected called %d times", len(got))
	}
	if _, ok := got[0].(*tls.Conn); !ok {
		t.Fatalf("onConnected got %T, want *tls.Conn", got[0])
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil || buf[0] != 'x' {
		t.Fatalf("read = %q, %v", buf, err)
	}
}

// With TLS on, the private-address guard still runs first: the dial is
// refused before any connection is made, so there's no handshake.
func TestDialerTLSRefusesPrivate(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	called := false
	d := dialer(false, true, func(net.Conn) { called = true })
	conn, err := d(context.Background(), "tcp", ln.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("TLS dial to 127.0.0.1 should fail")
	}
	if !strings.Contains(err.Error(), "private address") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("onConnected called for a refused dial")
	}
	ln.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond))
	if c, err := ln.Accept(); err == nil {
		c.Close()
		t.Fatal("the guard let a connection through")
	}
}

// A failed TLS handshake closes the TCP connection under it, and reports
// the failure without calling onConnected.
func TestDialerTLSHandshakeFailureCloses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	closed := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer c.Close()
		c.Write([]byte("not TLS\r\n"))
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4096)
		for {
			if _, err := c.Read(buf); err != nil {
				closed <- err
				return
			}
		}
	}()

	called := false
	d := dialer(true, true, func(net.Conn) { called = true })
	if conn, err := d(context.Background(), "tcp", ln.Addr().String()); err == nil {
		conn.Close()
		t.Fatal("handshake with a non-TLS server should fail")
	}
	if called {
		t.Fatal("onConnected called for a failed handshake")
	}
	if err := <-closed; !errors.Is(err, io.EOF) {
		t.Fatalf("server's read ended with %v, want EOF (client closed)", err)
	}
}
