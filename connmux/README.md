# connmux
A connection multiplexing library

### Example
#### client
```
c, err := net.Dial("tcp", "192.168.3.99:7676")
if err != nil {
    panic(err)
}

session := connmux.Mux(c, connmux.SeqOdd)
defer session.Close()
for {
    muxC, err := session.Accept()
    if err != nil {
        panic(err)
    }
    go handleConn(muxC)
}
```
#### server
```
l, err := net.Listen("tcp", ":7676")
if err != nil {
    panic(err)
}
c, err := l.Accept()
if err != nil {
    panic(err)
}
session := connmux.Mux(c, connmux.SeqEven)
defer session.Close()

muxConn, err := session.OpenStream()
if err != nil {
    panic(err)
}

// ...
```