# fileshare

A p2p file sharing library

### Example

#### download
```go
downloader := &fileshare.Downloader{
    Server: "wss://synf.in/pg",
    ListenUDPPort: 29999,
}

read := func(fh *fileshare.FileHandle) error {
    // handshake (can set the offset to facilitate breakpoint resuming)
    if err := fh.Handshake(0, nil); err != nil {
        return err
    }
    reader, fileSize, err := fh.File()
    if err != nil {
        return err
    }
    f, err := os.Create(fh.Filename)
    if err != nil {
        return err
    }
    
    if err = io.Copy(io.MultiWriter(f, sum), reader); err != nil {
        return err
    }

    peerSum, err := fh.Sha256() // file checksum from peer
    if err != nil {
        return err
    }

    if !bytes.Equal(sum.Sum(nil), peerSum) { // assert that local and remote are consistent
        return errors.New("transfer error")
    }
    return nil
}

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
defer cancel()
err := downloader.Request(ctx, "pg://DJX2csRurJ3DvKeh63JebVHFDqVhnFjckdVhToAAiPYf/0/my-show.pptx", read)
if err != nil {
    panic(err)
}
```