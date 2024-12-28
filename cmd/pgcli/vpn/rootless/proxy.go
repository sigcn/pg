package rootless

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	N "github.com/sigcn/pg/net"
	"github.com/sigcn/pg/socks5"
	"github.com/sigcn/pg/vpn/nic/gvisor"
)

type peekConn struct {
	net.Conn
	peekBytes []byte
}

func (c *peekConn) Read(b []byte) (n int, err error) {
	if c.peekBytes != nil {
		n = copy(b, c.peekBytes)
		if n < len(c.peekBytes) {
			c.peekBytes = c.peekBytes[n:]
		} else {
			c.peekBytes = nil
		}
		return
	}
	return c.Conn.Read(b)
}

type ProxyConfig struct {
	Listen string
}

type ProxyServer struct {
	Config     ProxyConfig
	GvisorCard *gvisor.GvisorCard

	udpListener *N.UDPListener
}

func (s *ProxyServer) Start(ctx context.Context, wg *sync.WaitGroup) error {
	tcpListener, err := net.Listen("tcp", s.Config.Listen)
	if err != nil {
		return err
	}
	udpPacketConn, err := net.ListenPacket("udp", s.Config.Listen)
	if err != nil {
		tcpListener.Close()
		return err
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		tcpListener.Close()
		udpPacketConn.Close()
	}()
	s.udpListener = &N.UDPListener{PacketConn: udpPacketConn}
	slog.Info("[Proxy] Server started", "listen", fmt.Sprintf("tcp+udp://%s", tcpListener.Addr().String()), "protocols", "socks5,http")
	go s.run(tcpListener)
	return nil
}

func (s *ProxyServer) run(tcp net.Listener) {
	for {
		c, err := tcp.Accept()
		if err != nil {
			return
		}

		b := make([]byte, 1)
		if _, err = io.ReadFull(c, b); err != nil {
			continue
		}

		conn := &peekConn{Conn: c, peekBytes: b}
		go func() {
			if b[0] == 5 {
				if err := s.serveSOCKS5(conn); err != nil {
					slog.Error("[Proxy] Serve socks5", "err", err)
				}
				return
			}
			if err := s.serveHTTP(conn); err != nil {
				slog.Error("[Proxy] Serve http", "err", err)
			}
		}()
	}
}

func (s *ProxyServer) serveSOCKS5(c net.Conn) error {
	defer c.Close()
	addr, cmd, err := socks5.ServerHandshake(c, nil)
	if err != nil {
		return fmt.Errorf("socks5 handshake: %w", err)
	}
	if cmd == socks5.CmdConnect {
		if err := s.proxyTCP(c, addr.String()); err != nil {
			return fmt.Errorf("socks5 proxy tcp: %w", err)
		}
		return nil
	}
	if cmd == socks5.CmdUDPAssociate {
		if err := s.proxyUDP(addr.String()); err != nil {
			return fmt.Errorf("socks5 proxy udp: %w", err)
		}
		return nil
	}
	return nil
}

func (s *ProxyServer) serveHTTP(c net.Conn) error {
	defer c.Close()
	r, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil {
		return fmt.Errorf("http parse request: %w", err)
	}

	if _, _, err := net.SplitHostPort(r.Host); err != nil {
		r.Host = fmt.Sprintf("%s:80", r.Host)
	}

	if r.Method == http.MethodConnect {
		_, err = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		if err != nil {
			return err
		}
		if err = s.proxyTCP(c, r.Host); err != nil {
			s.responseError(c, err)
			return err
		}
		return nil
	}

	r.Header.Del("Proxy-Connection")
	b := &bytes.Buffer{}
	r.Write(b)

	if err = s.proxyTCP(&peekConn{Conn: c, peekBytes: b.Bytes()}, r.Host); err != nil {
		s.responseError(c, err)
		return err
	}
	return nil
}

func (s *ProxyServer) proxyTCP(rw net.Conn, addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := s.GvisorCard.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	relay(rw, c)
	return nil
}

func (s *ProxyServer) proxyUDP(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := s.udpListener.AcceptContext(ctx)
	if err != nil {
		return err
	}
	c1, err := s.GvisorCard.DialContext(context.TODO(), "udp", addr)
	if err != nil {
		c.Close()
		return err
	}
	relay(c, c1)
	return nil
}

func (s *ProxyServer) responseError(w io.Writer, err error) {
	r := &http.Response{
		Status:     "500 InternalServerError",
		StatusCode: http.StatusInternalServerError,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Proto:      "HTTP/1.1",
		Body:       io.NopCloser(strings.NewReader(err.Error() + "\n")),
	}
	r.Write(w)
}
