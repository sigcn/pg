package netlink

var info = Info{}

type Info struct {
	IPv4 string
	IPv6 string
}

func Show() Info {
	return info
}

type Link struct {
	Name  string
	Index int
	Type  uint32
}
