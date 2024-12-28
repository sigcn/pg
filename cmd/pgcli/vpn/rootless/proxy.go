package rootless

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	N "github.com/sigcn/pg/net"
	"github.com/sigcn/pg/socks5"
	"github.com/sigcn/pg/vpn/nic/gvisor"
)

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
	slog.Info("[Proxy] Server started", "listen", fmt.Sprintf("tcp+udp://%s", tcpListener.Addr().String()))
	go s.run(tcpListener)
	return nil
}

func (s *ProxyServer) run(tcp net.Listener) {
	for {
		c, err := tcp.Accept()
		if err != nil {
			return
		}
		addr, cmd, err := socks5.ServerHandshake(c, nil)
		if err != nil {
			slog.Error("[Proxy] SOCKS5 handshake", "err", err)
			continue
		}
		if cmd == socks5.CmdConnect {
			if err := s.proxyTCP(c, addr); err != nil {
				slog.Error("[Proxy] SOCKS5 tcp", "err", err)
			}
			continue
		}
		if cmd == socks5.CmdUDPAssociate {
			go func() {
				if err := s.proxyUDP(addr); err != nil {
					slog.Error("[Proxy] SOCKS5 udp", "err", err)
				}
			}()
			continue
		}
	}
}

func (s *ProxyServer) proxyTCP(rw net.Conn, addr socks5.Addr) error {
	c, err := s.GvisorCard.DialContext(context.TODO(), "tcp", addr.String())
	if err != nil {
		rw.Close()
		return err
	}
	go relay(rw, c)
	return nil
}

func (s *ProxyServer) proxyUDP(addr socks5.Addr) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := s.udpListener.AcceptContext(ctx)
	if err != nil {
		return err
	}
	c1, err := s.GvisorCard.DialContext(context.TODO(), "udp", addr.String())
	if err != nil {
		c.Close()
		return err
	}
	go relay(c, c1)
	return nil
}
