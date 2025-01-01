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

type Socks5UDPConn struct {
	net.Conn
	Addr socks5.Addr

	readBufMakeOnce sync.Once
	readBuf         []byte
	peekPacket      []byte
}

func (c *Socks5UDPConn) peekTarget() (socks5.Addr, error) {
	c.readBufMakeOnce.Do(func() {
		c.readBuf = make([]byte, 65535)
	})
	n, err := c.Conn.Read(c.readBuf)
	if err != nil {
		return socks5.Addr{}, err
	}
	c.peekPacket = make([]byte, n)
	copy(c.peekPacket, c.readBuf[:n])
	addr, _, err := socks5.DecodeUDPPacket(c.readBuf[:n])
	if err != nil {
		return socks5.Addr{}, err
	}
	c.Addr = addr
	return addr, nil
}

func (c *Socks5UDPConn) Read(p []byte) (int, error) {
	c.readBufMakeOnce.Do(func() {
		c.readBuf = make([]byte, 65535)
	})
	pkt := c.peekPacket
	if pkt == nil {
		n, err := c.Conn.Read(c.readBuf)
		if err != nil {
			return 0, err
		}
		pkt = c.readBuf[:n]
	}
	c.peekPacket = nil
	_, b, err := socks5.DecodeUDPPacket(pkt)
	if err != nil {
		return 0, err
	}
	fmt.Printf("%x\n", b)
	return copy(p, b), nil
}

func (c *Socks5UDPConn) Write(p []byte) (int, error) {
	b, err := socks5.EncodeUDPPacket(c.Addr, p)
	if err != nil {
		return 0, err
	}
	_, err = c.Conn.Write(b)
	return len(p), err
}

type ProxyConfig struct {
	Listen string `yaml:"listen"`
}

type ProxyServer struct {
	Config     ProxyConfig
	GvisorCard *gvisor.GvisorCard
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
	slog.Info("[Proxy] Server started", "listen", fmt.Sprintf("tcp+udp://%s", tcpListener.Addr().String()), "protocols", "socks5,http")
	go s.readTCP(tcpListener)
	go s.readUDP(&N.UDPListener{PacketConn: udpPacketConn})
	return nil
}

func (s *ProxyServer) readTCP(tcp net.Listener) {
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

func (s *ProxyServer) readUDP(udp net.Listener) {
	for {
		c, err := udp.Accept()
		if err != nil {
			return
		}

		cc := &Socks5UDPConn{Conn: c}
		addr, err := cc.peekTarget()
		if err != nil {
			slog.Error("[Proxy] Read udp", "err", err)
			continue
		}
		go s.proxy("udp", cc, addr.String())
	}
}

func (s *ProxyServer) serveSOCKS5(c net.Conn) error {
	defer c.Close()
	addr, cmd, err := socks5.ServerHandshake(c, nil)
	if err != nil {
		return fmt.Errorf("socks5 handshake: %w", err)
	}
	if cmd == socks5.CmdConnect {
		if err := s.proxy("tcp", c, addr.String()); err != nil {
			return fmt.Errorf("socks5 proxy tcp: %w", err)
		}
		return nil
	}
	if cmd == socks5.CmdUDPAssociate {
		// TODO add ip whitelist
		io.Copy(io.Discard, c)
		c.Close()
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
		if err = s.proxy("tcp", c, r.Host); err != nil {
			s.responseError(c, err)
			return err
		}
		return nil
	}

	r.Header.Del("Proxy-Connection")
	b := &bytes.Buffer{}
	r.Write(b)

	if err = s.proxy("tcp", &peekConn{Conn: c, peekBytes: b.Bytes()}, r.Host); err != nil {
		s.responseError(c, err)
		return err
	}
	return nil
}

func (s *ProxyServer) proxy(network string, rw net.Conn, addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := s.GvisorCard.DialContext(ctx, network, addr)
	if err != nil {
		return err
	}
	relay(rw, c)
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
