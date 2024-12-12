package share

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/jedib0t/go-pretty/v6/progress"
	"github.com/sigcn/pg/fileshare"
)

func Run() error {
	flagSet := flag.NewFlagSet("share", flag.ExitOnError)
	flagSet.Usage = func() {
		fmt.Printf("Usage: %s [flags] [files...]\n\n", flagSet.Name())
		fmt.Printf("Flags:\n")
		flagSet.PrintDefaults()
	}
	trackerManager := TrackerManager{}
	fileManager := fileshare.FileManager{ListenUDPPort: 28878, ProgressBar: trackerManager.CreateBar}

	flagSet.StringVar(&fileManager.Server, "s", "", "peermap server")
	flagSet.StringVar(&fileManager.Network, "pubnet", "public", "peermap public network")
	flagSet.StringVar(&fileManager.PrivateKey, "key", "", "curve25519 private key in base58 format (default generate a new one)")

	var logLevel int
	flagSet.IntVar(&logLevel, "loglevel", 1, "log level")
	flagSet.Parse(flag.Args()[1:])
	slog.SetLogLoggerLevel(slog.Level(logLevel))

	if len(fileManager.Server) == 0 {
		fileManager.Server = os.Getenv("PG_SERVER")
		if len(fileManager.Server) == 0 {
			return errors.New("unknown peermap server")
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	for _, file := range flagSet.Args() {
		if err := fileManager.Add(file); err != nil {
			slog.Warn("AddFile", "path", file, "err", err)
		}
	}

	listener, err := fileManager.ListenNetwork()
	if err != nil {
		return err
	}

	sharedURLs, err := fileManager.SharedURLs()
	if err != nil {
		return err
	}
	for _, url := range sharedURLs {
		fmt.Println("ShareURL:", url)
	}

	return fileManager.Serve(ctx, listener)
}

type ProgressBar struct {
	tracker *progress.Tracker
}

func (bar *ProgressBar) Write(p []byte) (int, error) {
	bar.tracker.Increment(int64(len(p)))
	return len(p), nil
}

func (bar *ProgressBar) Add(progress int) error {
	bar.tracker.Increment(int64(progress))
	return nil
}

type TrackerManager struct {
	AutoStop   bool
	pw         progress.Progress
	renderOnce sync.Once
}

func (tm *TrackerManager) CreateBar(total int64, desc string) fileshare.ProgressBar {
	tm.renderOnce.Do(func() {
		tm.pw.SetAutoStop(tm.AutoStop)
		go tm.pw.Render()
	})
	tracker := progress.Tracker{Message: desc, Total: total, Units: progress.UnitsBytes}
	tm.pw.AppendTracker(&tracker)
	return &ProgressBar{tracker: &tracker}
}
