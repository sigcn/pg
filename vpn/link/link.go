package link

var info = Info{}

type Info struct {
	IPv4 string
	IPv6 string
}

func Show() Info {
	return info
}
