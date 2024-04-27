package download

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rkonfj/peerguard/cmd/pgcli/share/pubnet"
	"github.com/rkonfj/peerguard/peer"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
	"github.com/xtaci/kcp-go/v5"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "download",
		Short: "Download shared file from peer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			verbose, err := cmd.Flags().GetInt("verbose")
			if err != nil {
				return err
			}
			slog.SetLogLoggerLevel(slog.Level(verbose))
			var pubnet pubnet.PublicNetwork

			pubnet.Server, err = cmd.Flags().GetString("server")
			if err != nil {
				return err
			}
			if len(pubnet.Server) == 0 {
				pubnet.Server = os.Getenv("PEERMAP_SERVER")
				if len(pubnet.Server) == 0 {
					return errors.New("unknown peermap server")
				}
			}

			pubnet.Name, err = cmd.Flags().GetString("pubnet")
			if err != nil {
				return err
			}

			resourceURL, err := url.Parse(args[0])
			if err != nil {
				return fmt.Errorf("invalid URL: %w", err)
			}

			dir, filename := path.Split(resourceURL.Path)
			index, err := strconv.ParseInt(strings.Trim(dir, "/"), 10, 16)
			if err != nil {
				return fmt.Errorf("invalid URL: %w", err)
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return requestFile(ctx, pubnet, resourceURL.Host, uint16(index), filename)
		},
	}
	Cmd.Flags().StringP("server", "s", "", "peermap server")
	Cmd.Flags().StringP("pubnet", "n", "public", "peermap public network")
	Cmd.Flags().IntP("verbose", "V", int(slog.LevelError), "log level")
}

func requestFile(ctx context.Context, pubnet pubnet.PublicNetwork, peerID string, index uint16, filename string) error {
	packetConn, err := pubnet.ListenPacket(29879)
	if err != nil {
		return fmt.Errorf("listen p2p packet failed: %w", err)
	}

	var convid uint32
	binary.Read(rand.Reader, binary.LittleEndian, &convid)
	conn, err := kcp.NewConn3(convid, peer.ID(peerID), nil, 10, 3, packetConn)
	if err != nil {
		return fmt.Errorf("dial kcp server failed: %w", err)
	}
	defer conn.Close()
	conn.SetStreamMode(true)
	conn.SetNoDelay(1, 10, 2, 1)
	conn.SetWindowSize(1024, 1024)

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = conn.Write(buildGet(uint16(index)))
	if err != nil {
		return err
	}

	header := make([]byte, 5)
	_, err = io.ReadFull(conn, header)
	if err != nil {
		return err
	}
	switch header[0] {
	case 0:
	case 1:
		return errors.New("bad request")
	case 2:
		return errors.New("file not found")
	default:
		return errors.New("invalid protocol header")
	}

	fileSize := binary.BigEndian.Uint32(header[1:])
	bar := progressbar.NewOptions64(
		int64(fileSize),
		progressbar.OptionSetDescription("downloading"),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(10),
		progressbar.OptionThrottle(10*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "=",
			SaucerHead:    ">",
			SaucerPadding: " ",
			BarStart:      "[",
			BarEnd:        "]",
		}),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetRenderBlankState(true),
	)
	go func() { // watch exit program event
		<-ctx.Done()
		conn.Write(buildClose())
		conn.Close()
	}()
	defer conn.Write(buildClose())

	sha256Checksum := sha256.New()

	_, err = io.Copy(io.MultiWriter(f, bar, sha256Checksum), &downloader{r: conn, finished: bar.IsFinished})
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("download file falied: %w", err)
	}
	conn.Write(buildChecksum())
	checksum := make([]byte, 32)
	if _, err = io.ReadFull(conn, checksum); err != nil {
		return fmt.Errorf("read checksum failed: %w", err)
	}
	if !bytes.Equal(checksum, sha256Checksum.Sum(nil)) {
		return fmt.Errorf("download file failed: checksum mismatched")
	}
	fmt.Printf("sha256: %x\n", checksum)
	return nil
}

type downloader struct {
	r        io.Reader
	finished func() bool
}

func (d *downloader) Read(p []byte) (n int, err error) {
	if d.finished() {
		return 0, io.EOF
	}
	return d.r.Read(p)
}

func buildGet(index uint16) []byte {
	var header []byte
	header = append(header, 0, 0)
	header = append(header, binary.BigEndian.AppendUint16(nil, index)...)
	return header
}

func buildClose() []byte {
	return []byte{1}
}

func buildChecksum() []byte {
	return []byte{2}
}
