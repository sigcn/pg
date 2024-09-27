package connmux_test

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"

	"github.com/sigcn/pg/connmux"
)

type rwc struct {
	io.ReadCloser
	io.WriteCloser
}

func (c rwc) Close() error {
	c.ReadCloser.Close()
	c.WriteCloser.Close()
	return nil
}

func TestMuxConnOpenAndAccept(t *testing.T) {
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()

	underConn := rwc{ReadCloser: r1, WriteCloser: w2}
	s1 := connmux.Mux(underConn, connmux.SeqEven)
	s2 := connmux.Mux(rwc{ReadCloser: r2, WriteCloser: w1}, connmux.SeqOdd)

	dataMap := map[string]any{}
	dataMapMutex := sync.Mutex{}
	var wg sync.WaitGroup

	go func() {
		for {
			c, err := s1.Accept()
			if err != nil {
				slog.Error("Accept", "err", err)
				return
			}
			go func() {
				count := 0
				bytes := 0
				for {
					r := bufio.NewReader(c)
					l, err := r.ReadString('\n')
					if errors.Is(err, io.EOF) {
						wg.Done()
						slog.Info("AcceptedConnClosed", "seq", c.(*connmux.MuxConn).Seq(), "count", count, "bytes", bytes)
						break
					}
					if err != nil {
						panic(err)
					}
					dataMapMutex.Lock()
					delete(dataMap, l)
					dataMapMutex.Unlock()
					count++
					bytes += len(l)
				}
			}()
		}
	}()

	send := func(c net.Conn) {
		randData := make([]byte, 1024)
		rand.Read(randData)
		data := fmt.Sprintf("%x\n", randData)
		dataMapMutex.Lock()
		dataMap[data] = nil
		dataMapMutex.Unlock()
		if _, err := c.Write([]byte(data)); err != nil {
			panic(err)
		}
	}

	for range 200 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			c, err := s2.OpenStream()
			if err != nil {
				panic(err)
			}
			defer c.Close()
			for range 10000 {
				send(c)
			}
		}()
	}
	wg.Wait()
	if len(dataMap) != 0 {
		t.Fatalf("send and recv failed: %d", len(dataMap))
	}
}
