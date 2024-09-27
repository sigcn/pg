package fileshare

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/rdt"
)

type FileHandle struct {
	Filename string

	c     net.Conn
	index uint16
	fSize uint32
	f     io.Reader
}

func (h *FileHandle) Handshake(offset uint32, sha256Checksum []byte) error {
	_, err := h.c.Write(buildGet(h.index, offset, sha256Checksum))
	if err != nil {
		return err
	}
	header := make([]byte, 5)
	h.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.ReadFull(h.c, header)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	h.c.SetReadDeadline(time.Time{})
	switch header[0] {
	case 0:
	case 20:
	case 1:
		return errors.New("bad request. maybe the version is lower than peer")
	case 2:
		return errors.New("file not found")
	case 4:
		return errors.New("download file size is less than local file")
	case 5:
		return errors.New("local file is not part of the file to be downloaded")
	default:
		return errors.New("invalid protocol header")
	}
	if offset > 0 && header[0] != 20 {
		return errors.New("sha256 checksum non matched for [0, offset)")
	}
	h.fSize = binary.BigEndian.Uint32(header[1:])
	h.f = io.LimitReader(h.c, int64(h.fSize-offset))
	return nil
}

func (h *FileHandle) File() (io.Reader, uint32, error) {
	if h.f == nil {
		return nil, 0, errors.New("handshake first")
	}
	return h.f, h.fSize, nil
}

func (h *FileHandle) Sha256() ([]byte, error) {
	checksum := make([]byte, 32)
	if _, err := io.ReadFull(h.c, checksum); err != nil {
		return nil, fmt.Errorf("read checksum failed: %w", err)
	}
	return checksum, nil
}

type Read func(f *FileHandle) error

type Downloader struct {
	Network       string
	Server        string
	PrivateKey    string
	ListenUDPPort int
}

func (d *Downloader) Request(ctx context.Context, shareURL string, read Read) error {
	pnet := PublicNetwork{Name: d.Network, Server: d.Server, PrivateKey: d.PrivateKey}
	packetConn, err := pnet.ListenPacket(d.ListenUDPPort)
	if err != nil {
		return fmt.Errorf("listen p2p packet failed: %w", err)
	}

	listener, err := rdt.Listen(packetConn, rdt.EnableStatsServer(fmt.Sprintf(":%d", d.ListenUDPPort+100)))
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

	conn, err := listener.OpenStream(disco.PeerID(resourceURL.Host))
	if err != nil {
		return fmt.Errorf("dial server failed: %w", err)
	}
	defer conn.Close()
	go func() { // watch exit program event
		<-ctx.Done()
		conn.Write(buildClose())
		conn.Close()
	}()
	defer conn.Write(buildClose())
	return read(&FileHandle{
		Filename: fn,
		c:        conn,
		index:    uint16(index),
	})
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
