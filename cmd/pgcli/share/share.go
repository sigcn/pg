package share

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/rkonfj/peerguard/cmd/pgcli/share/pubnet"
	"github.com/spf13/cobra"
	"github.com/xtaci/kcp-go/v5"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "share",
		Short: "Share files to peers",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			verbose, err := cmd.Flags().GetInt("verbose")
			if err != nil {
				return err
			}
			slog.SetLogLoggerLevel(slog.Level(verbose))

			var pubnet pubnet.PublicNetwork

			if pubnet.Server, err = cmd.Flags().GetString("server"); err != nil {
				return err
			}
			if len(pubnet.Server) == 0 {
				pubnet.Server = os.Getenv("PEERMAP_SERVER")
				if len(pubnet.Server) == 0 {
					return errors.New("unknown peermap server")
				}
			}

			pubnet.PrivateKey, err = cmd.Flags().GetString("key")
			if err != nil {
				return err
			}

			if pubnet.Name, err = cmd.Flags().GetString("pubnet"); err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return serve(ctx, pubnet, args)
		},
	}
	Cmd.Flags().StringP("server", "s", "", "peermap server")
	Cmd.Flags().StringP("pubnet", "n", "public", "peermap public network")
	Cmd.Flags().String("key", "", "curve25519 private key in base58 format (default generate a new one)")
	Cmd.Flags().IntP("verbose", "V", int(slog.LevelError), "log level")
}

func serve(ctx context.Context, pubnet pubnet.PublicNetwork, files []string) error {
	packetConn, err := pubnet.ListenPacket(29878)
	if err != nil {
		return fmt.Errorf("listen p2p packet failed: %w", err)
	}

	fm := FileManager{files: map[int]string{}}
	for _, file := range files {
		if index, err := fm.Add(file); err != nil {
			slog.Warn("AddFile", "path", file, "err", err)
		} else {
			fmt.Printf("ShareURL: pg://%s/%d/%s\n", packetConn.LocalAddr(), index, filepath.Base(file))
		}
	}

	listener, err := kcp.ServeConn(nil, 10, 3, packetConn)
	if err != nil {
		return fmt.Errorf("listen kcp failed: %w", err)
	}

	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		conn, err := listener.AcceptKCP()
		if err != nil {
			slog.Debug("Accept failed", "err", err)
			continue
		}
		conn.SetStreamMode(true)
		conn.SetNoDelay(1, 10, 2, 1)
		conn.SetWindowSize(1024, 1024)
		go func() {
			<-ctx.Done()
			conn.Close()
		}()
		fm.HandleRequest(conn.RemoteAddr().String(), conn)
	}
}
