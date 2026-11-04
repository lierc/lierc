package lierc

import (
	"context"
	"crypto/tls"
	"net"
)

// dialer returns an ircevent DialContext: the guarded TCP dial, then TLS
// (InsecureSkipVerify) when ssl, then onConnected(conn) once both are up.
// irc-go's UseTLS stays false; we do TLS so "Connected" means TLS is up.
func dialer(exempt, ssl bool, onConnected func(net.Conn)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := guardedDialer(exempt).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if ssl {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			tc := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: host})
			if err := tc.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			conn = tc
		}
		if onConnected != nil {
			onConnected(conn)
		}
		return conn, nil
	}
}
