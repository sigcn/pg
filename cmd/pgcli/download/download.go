package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rkonfj/peerguard/fileshare"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "download",
		Short: "Download shared file from peer",
		Args:  cobra.ExactArgs(1),
		RunE:  execute,
	}
	Cmd.Flags().StringP("server", "s", "", "peermap server")
	Cmd.Flags().StringP("pubnet", "n", "public", "peermap public network")
	Cmd.Flags().IntP("verbose", "V", int(slog.LevelError), "log level")
}

func execute(cmd *cobra.Command, args []string) error {
	verbose, err := cmd.Flags().GetInt("verbose")
	if err != nil {
		return err
	}
	slog.SetLogLoggerLevel(slog.Level(verbose))

	downloader := fileshare.Downloader{UDPPort: 29879, ProgressBar: createBar}

	downloader.Server, err = cmd.Flags().GetString("server")
	if err != nil {
		return err
	}
	if len(downloader.Server) == 0 {
		downloader.Server = os.Getenv("PG_SERVER")
		if len(downloader.Server) == 0 {
			return errors.New("unknown peermap server")
		}
	}

	downloader.Network, err = cmd.Flags().GetString("pubnet")
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return downloader.Download(ctx, args[0])
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
