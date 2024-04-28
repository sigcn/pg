package share

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
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
	"time"

	"github.com/schollz/progressbar/v3"
)

type FileManager struct {
	mutex sync.RWMutex
	index int
	files map[int]string
}

func (fm *FileManager) Add(file string) (int, error) {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()
	absPath, err := filepath.Abs(file)
	if err != nil {
		return -1, err
	}
	if relPath, ok := strings.CutPrefix(file, "~"); ok {
		curUser, err := user.Current()
		if err != nil {
			return -1, err
		}
		absPath = filepath.Join(curUser.HomeDir, relPath)
	}
	fm.files[fm.index] = absPath
	fm.index++
	return fm.index - 1, nil
}

func (fm *FileManager) Open(index int) (*os.File, error) {
	fm.mutex.RLock()
	defer fm.mutex.RUnlock()
	if absPath, ok := fm.files[index]; ok {
		return os.Open(absPath)
	}
	return nil, os.ErrNotExist
}

func (fm *FileManager) HandleRequest(peerID string, conn net.Conn) {
	header := make([]byte, 4)
	_, err := io.ReadFull(conn, header)
	if err != nil || !bytes.Equal(header[:2], []byte{0, 0}) {
		conn.Write(buildErr(1))
		slog.Error("Magic not verified, closing connection...")
		return
	}

	index := binary.BigEndian.Uint16(header[2:])
	f, err := fm.Open(int(index))
	if err != nil {
		conn.Write(buildErr(2))
		slog.Error("Open file failed", "err", err)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return
	}

	conn.Write(buildOK(stat.Size()))

	go func() {
		header := make([]byte, 1)
		io.ReadFull(conn, header)
		switch header[0] {
		case 1:
			conn.Close()
		}
	}()

	bar := progressbar.NewOptions64(
		stat.Size(),
		progressbar.OptionSetDescription(fmt.Sprintf("%s:%s", peerID, url.QueryEscape(stat.Name()))),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(10),
		progressbar.OptionThrottle(200*time.Millisecond),
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

	sha256Checksum := sha256.New()
	_, err = io.Copy(io.MultiWriter(&sender{conn}, bar, sha256Checksum), f)
	if err != nil {
		slog.Info("Copy file failed", "err", err)
	}
	conn.Write(sha256Checksum.Sum(nil))
}

type sender struct {
	net.Conn
}

func (s *sender) Write(b []byte) (n int, err error) {
	s.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return s.Conn.Write(b)
}

func buildOK(fileSize int64) []byte {
	pkt := make([]byte, 5)
	copy(pkt[1:], binary.BigEndian.AppendUint32(nil, uint32(fileSize)))
	return pkt
}

func buildErr(code byte) []byte {
	pkt := make([]byte, 5)
	pkt[0] = code
	return pkt
}
