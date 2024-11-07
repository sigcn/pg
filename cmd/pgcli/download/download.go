package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/sigcn/pg/fileshare"
)

func Run() error {
	flagSet := flag.NewFlagSet("download", flag.ExitOnError)
	flagSet.Usage = func() {
		fmt.Printf("Usage: %s [flags] [url]\n\n", flagSet.Name())
		fmt.Printf("Flags:\n")
		flagSet.PrintDefaults()
	}
	downloader := fileshare.Downloader{ListenUDPPort: 28879}

	flagSet.StringVar(&downloader.Server, "server", "", "peermap server")
	flagSet.StringVar(&downloader.Network, "pubnet", "public", "peermap public network")

	var logLevel int
	flagSet.IntVar(&logLevel, "loglevel", 0, "log level")
	flagSet.Parse(flag.Args()[1:])
	slog.SetLogLoggerLevel(slog.Level(logLevel))

	if len(downloader.Server) == 0 {
		downloader.Server = os.Getenv("PG_SERVER")
		if len(downloader.Server) == 0 {
			return errors.New("unknown peermap server")
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return downloader.Request(ctx, flagSet.Arg(0), readFile)
}

func readFile(fh *fileshare.FileHandle) error {
	f, err := os.OpenFile(fh.Filename, os.O_RDWR, 0666)
	if err != nil {
		f, err = os.Create(fh.Filename)
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
	if err := fh.Handshake(uint32(partSize), sha256Checksum.Sum(nil)); err != nil {
		return err
	}
	r, fileSize, _ := fh.File()
	bar := createBar(int64(fileSize), fh.Filename)
	bar.Add(int(partSize))
	if _, err = io.Copy(io.MultiWriter(f, bar, sha256Checksum), r); err != nil {
		return fmt.Errorf("download file falied: %w", err)
	}
	checksum, err := fh.Sha256()
	if err != nil {
		return err
	}
	recvSum := sha256Checksum.Sum(nil)
	slog.Debug("Checksum", "recv", recvSum, "send", checksum)
	if !bytes.Equal(checksum, recvSum) {
		return fmt.Errorf("download file failed: checksum mismatched")
	}
	fmt.Printf("sha256: %x\n", checksum)
	return nil
}

func createBar(total int64, desc string) fileshare.ProgressBar {
	return progressbar.NewOptions64(
		total,
		progressbar.OptionSetDescription(desc),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionThrottle(500*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionShowElapsedTimeOnFinish(),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetRenderBlankState(true),
	)
}
