package fileshare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/rdt"
)

type Downloader struct {
	Network     string
	Server      string
	PrivateKey  string
	UDPPort     int
	ProgressBar func(total int64, desc string) ProgressBar
}

func (d *Downloader) Download(ctx context.Context, shareURL string) error {
	pnet := PublicNetwork{Name: d.Network, Server: d.Server, PrivateKey: d.PrivateKey}
	packetConn, err := pnet.ListenPacket(d.UDPPort)
	if err != nil {
		return fmt.Errorf("listen p2p packet failed: %w", err)
	}

	listener, err := rdt.Listen(packetConn, rdt.EnableStatsServer(fmt.Sprintf(":%d", d.UDPPort+100)))
	if err != nil {
		return fmt.Errorf("listen rdt: %w", err)
	}

	resourceURL, err := url.Parse(shareURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	dir, filename := path.Split(resourceURL.Path)
	index, err := strconv.ParseInt(strings.Trim(dir, "/"), 10, 16)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	fn, err := url.QueryUnescape(filename)
	if err != nil {
		fn = filename
	}

	conn, err := listener.OpenStream(peer.ID(resourceURL.Host))
	if err != nil {
		return fmt.Errorf("dial server failed: %w", err)
	}
	defer conn.Close()
	go func() { // watch exit program event
		<-ctx.Done()
		conn.Write(buildClose())
		conn.Close()
	}()
	return d.download(conn, uint16(index), fn)
}

func (d *Downloader) download(conn net.Conn, index uint16, filename string) error {
	f, err := os.OpenFile(filename, os.O_RDWR, 0666)
	if err != nil {
		f, err = os.Create(filename)
		if err != nil {
			return err
		}
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	partSize := stat.Size()

	sha256Checksum := sha256.New()
	if partSize > 0 {
		fmt.Println("download resuming")
	}

	if _, err = io.CopyN(sha256Checksum, f, partSize); err != nil {
		return err
	}
	_, err = conn.Write(buildGet(uint16(index), uint32(stat.Size()), sha256Checksum.Sum(nil)))
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
	case 20:
	case 1:
		return errors.New("bad request. maybe the version is lower than peer")
	case 2:
		return errors.New("file not found")
	case 4:
		return errors.New("download file size is less than local file")
	default:
		return errors.New("invalid protocol header")
	}

	fileSize := binary.BigEndian.Uint32(header[1:])
	var bar ProgressBar = NopProgress{}
	if d.ProgressBar != nil {
		bar = d.ProgressBar(int64(fileSize), filename)
	}
	bar.Add(int(stat.Size()))
	defer conn.Write(buildClose())

	_, err = io.CopyN(io.MultiWriter(f, bar, sha256Checksum), conn, int64(fileSize-uint32(partSize)))
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("download file falied: %w", err)
	}
	checksum := make([]byte, 32)
	if _, err = io.ReadFull(conn, checksum); err != nil {
		return fmt.Errorf("read checksum failed: %w", err)
	}
	recvSum := sha256Checksum.Sum(nil)
	slog.Debug("Checksum", "recv", recvSum, "send", checksum)
	if !bytes.Equal(checksum, recvSum) {
		return fmt.Errorf("download file failed: checksum mismatched")
	}
	fmt.Printf("sha256: %x\n", checksum)
	return nil
}

func buildGet(index uint16, partSize uint32, checksum []byte) []byte {
	header := []byte{0, 0}
	if partSize > 0 {
		header[1] = 36
	}
	header = append(header, binary.BigEndian.AppendUint16(nil, index)...)
	if partSize > 0 {
		header = append(header, binary.BigEndian.AppendUint32(nil, partSize)...)
		header = append(header, checksum...)
	}
	return header
}

func buildClose() []byte {
	return []byte{1}
}
