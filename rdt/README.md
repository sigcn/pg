# RDT

a Reliable Data Transfer protocol.

### Example
**Server**
```go
packetConn, err := net.ListenPacket("udp", "192.168.3.99:22334")
if err != nil {
    panic(err)
}
listener, err := rdt.Listen(packetConn)
if err != nil {
    panic(err)
}

for {
    conn, err := listener.Accept()
    if err != nil {
        panic(err)
    }
    handle(conn)
}
```
**Client**
```go
packetConn, err := net.ListenPacket("udp", "192.168.3.98:22335")
if err != nil {
    panic(err)
}
listener, err := rdt.Listen(packetConn)
if err != nil {
    panic(err)
}

conn, err := listener.OpenStream(&net.UDPAddr{
    IP:   net.ParseIP("192.168.3.99"),
    Port: 22334,
})
if err != nil {
    panic(err)
}
...
```