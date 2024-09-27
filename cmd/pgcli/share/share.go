package share

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/sigcn/pg/fileshare"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "share <path> ...",
		Short: "Share files to peers",
		Args:  cobra.MinimumNArgs(1),
		RunE:  execute,
	}
	Cmd.Flags().StringP("server", "s", "", "peermap server")
	Cmd.Flags().StringP("pubnet", "n", "public", "peermap public network")
	Cmd.Flags().String("key", "", "curve25519 private key in base58 format (default generate a new one)")
	Cmd.Flags().IntP("verbose", "V", int(slog.LevelError), "log level")
}

func execute(cmd *cobra.Command, args []string) error {
	verbose, err := cmd.Flags().GetInt("verbose")
	if err != nil {
		return err
	}
	slog.SetLogLoggerLevel(slog.Level(verbose))

	fileManager := fileshare.FileManager{ListenUDPPort: 29878, ProgressBar: createBar}

	if fileManager.Server, err = cmd.Flags().GetString("server"); err != nil {
		return err
	}
	if len(fileManager.Server) == 0 {
		fileManager.Server = os.Getenv("PG_SERVER")
		if len(fileManager.Server) == 0 {
			return errors.New("unknown peermap server")
		}
	}

	fileManager.PrivateKey, err = cmd.Flags().GetString("key")
	if err != nil {
		return err
	}

	if fileManager.Network, err = cmd.Flags().GetString("pubnet"); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	for _, file := range args {
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
