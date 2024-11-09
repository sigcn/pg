package share

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/sigcn/pg/fileshare"
)

func Run() error {
	flagSet := flag.NewFlagSet("share", flag.ExitOnError)
	flagSet.Usage = func() {
		fmt.Printf("Usage: %s [flags] [files...]\n\n", flagSet.Name())
		fmt.Printf("Flags:\n")
		flagSet.PrintDefaults()
	}
	fileManager := fileshare.FileManager{ListenUDPPort: 28878, ProgressBar: createBar}

	flagSet.StringVar(&fileManager.Server, "s", "", "peermap server")
	flagSet.StringVar(&fileManager.Network, "pubnet", "public", "peermap public network")
	flagSet.StringVar(&fileManager.PrivateKey, "key", "", "curve25519 private key in base58 format (default generate a new one)")

	var logLevel int
	flagSet.IntVar(&logLevel, "loglevel", 0, "log level")
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

func createBar(total int64, desc string) fileshare.ProgressBar {
	return progressbar.NewOptions64(
		total,
		progressbar.OptionSetDescription(desc),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(10),
		progressbar.OptionThrottle(200*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionShowElapsedTimeOnFinish(),
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
}
