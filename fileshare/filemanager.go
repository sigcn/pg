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
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/rdt"
)

type FileManager struct {
	Network       string
	Server        string
	PrivateKey    string
	ListenUDPPort int
	ProgressBar   func(total int64, desc string) ProgressBar

	mutex     sync.RWMutex
	index     int
	files     map[int]string
	filesInit sync.Once
	peerID    disco.PeerID
}

func (m *FileManager) ListenNetwork() (net.Listener, error) {
	pnet := PublicNetwork{Name: m.Network, Server: m.Server, PrivateKey: m.PrivateKey}
	packetConn, err := pnet.ListenPacket(m.ListenUDPPort)
	if err != nil {
		return nil, fmt.Errorf("listen p2p packet failed: %w", err)
	}

	listener, err := rdt.Listen(packetConn, rdt.EnableStatsServer(fmt.Sprintf(":%d", m.ListenUDPPort+100)))
	if err != nil {
		return nil, fmt.Errorf("listen rdt: %w", err)
	}
	m.peerID = disco.PeerID(listener.Addr().String())
	return listener, nil
}

func (m *FileManager) SharedURLs() ([]string, error) {
	var ret []string
	for k, v := range m.files {
		ret = append(ret, fmt.Sprintf("pg://%s/%d/%s", m.peerID, k, url.QueryEscape(filepath.Base(v))))
	}
	if ret == nil {
		return nil, errors.New("no file to share")
	}
	return ret, nil
}

func (m *FileManager) Serve(ctx context.Context, listener net.Listener) error {
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
		conn, err := listener.Accept()
		if err != nil {
			slog.Debug("Accept failed", "err", err)
			continue
		}
		go func() {
			<-ctx.Done()
			conn.Close()
		}()
		m.handleRequest(conn.RemoteAddr().String(), conn)
	}
}

func (fm *FileManager) addAbs(absPath string) error {
	fileStat, err := os.Lstat(absPath)
	if err != nil {
		return fmt.Errorf("fileinfo: %w", err)
	}
	if fileStat.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if fileStat.IsDir() {
		files, err := os.ReadDir(absPath)
		if err != nil {
			return fmt.Errorf("readdir: %s: %w", absPath, err)
		}
		for _, f := range files {
			fm.addAbs(filepath.Join(absPath, f.Name()))
		}
		return nil
	}
	fm.filesInit.Do(func() { fm.files = make(map[int]string) })
	fm.files[fm.index] = absPath
	fm.index++
	return nil
}

func (fm *FileManager) Add(file string) error {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()
	absPath, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	if relPath, ok := strings.CutPrefix(file, "~"); ok {
		curUser, err := user.Current()
		if err != nil {
			return err
		}
		absPath = filepath.Join(curUser.HomeDir, relPath)
	}
	return fm.addAbs(absPath)
}

func (fm *FileManager) openFile(index uint16) (*os.File, error) {
	fm.mutex.RLock()
	defer fm.mutex.RUnlock()
	if absPath, ok := fm.files[int(index)]; ok {
		return os.Open(absPath)
	}
	return nil, os.ErrNotExist
}

func (m *FileManager) handleRequest(peerID string, conn net.Conn) {
	defer conn.Close()
	header := make([]byte, 4)
	_, err := io.ReadFull(conn, header)
	if err != nil || header[0] != 0 {
		conn.Write(buildErr(1)) // invalid magic
		slog.Error("Magic not verified", "err", err)
		return
	}

	index := binary.BigEndian.Uint16(header[2:])
	f, err := m.openFile(index)
	if err != nil {
		conn.Write(buildErr(2)) // not found
		slog.Error("Open file failed", "err", err)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return
	}

	length := header[1]
	info := make([]byte, length)
	if _, err = io.ReadFull(conn, info); err != nil {
		conn.Write(buildErr(3)) // invalid protocol
		slog.Error("Read info", "err", err)
		return
	}

	sha256Checksum := sha256.New()

	if len(info) > 0 {
		partSize := binary.BigEndian.Uint32(info[:4])
		partChecksum := info[4:36]
		if partSize > uint32(stat.Size()) {
			conn.Write(buildErr(4)) // part size greater than total file size
			slog.Error("Request file part size greater than total file size")
			return
		}
		io.CopyN(sha256Checksum, f, int64(partSize))
		if !bytes.Equal(sha256Checksum.Sum(nil), partChecksum) {
			conn.Write(buildErr(5)) // not part of file
			slog.Error("Request not part of file", "file", f.Name())
			return
		}
	}

	pos, err := f.Seek(0, io.SeekCurrent)
	resume := err == nil && pos > 0
	conn.Write(buildOK(stat.Size(), resume))
	go func() {
		header := make([]byte, 1)
		io.ReadFull(conn, header)
		switch header[0] {
		case 1:
			conn.Close()
		}
	}()
	var bar ProgressBar = NopProgress{}
	if m.ProgressBar != nil {
		bar = m.ProgressBar(stat.Size(), fmt.Sprintf("%s:%s", peerID, url.QueryEscape(stat.Name())))
		if resume {
			bar.Add(int(pos))
		}
	}
	if _, err = io.Copy(io.MultiWriter(conn, bar, sha256Checksum), f); err != nil {
		slog.Info("Copy file failed", "err", err)
	}
	checksum := sha256Checksum.Sum(nil)
	slog.Debug("Checksum", "sum", checksum)
	conn.Write(checksum)
}

func buildOK(fileSize int64, resume bool) []byte {
	pkt := []byte{0}
	if resume {
		pkt[0] = 20
	}
	pkt = append(pkt, binary.BigEndian.AppendUint32(nil, uint32(fileSize))...)
	return pkt
}

func buildErr(code byte) []byte {
	pkt := make([]byte, 5)
	pkt[0] = code
	return pkt
}
