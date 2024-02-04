version ?= unknown
git_hash := $(shell git rev-parse --short HEAD)

GOBUILD := CGO_ENABLED=0 go build -ldflags "-s -w -X 'main.Version=${version}' -X 'main.Commit=${git_hash}'"

all: linux windows

linuxamd64:
	GOOS=linux GOARCH=amd64 ${GOBUILD} -o peerguard-${version}-linux-amd64
linuxarm64:
	GOOS=linux GOARCH=arm64 ${GOBUILD} -o peerguard-${version}-linux-arm64
linux: linuxamd64 linuxarm64

wintun:
	curl -OL https://www.wintun.net/builds/wintun-0.14.1.zip
	unzip wintun-0.14.1.zip
	rm wintun-0.14.1.zip
winamd64: wintun
	GOOS=windows GOARCH=amd64 ${GOBUILD} -o peerguard-${version}-windows-amd64.exe
	cp wintun/bin/amd64/wintun.dll .
	zip -r peerguard-${version}-windows-amd64.zip peerguard-${version}-windows-amd64.exe wintun.dll 
	rm wintun.dll
winarm64: wintun
	GOOS=windows GOARCH=arm64 ${GOBUILD} -o peerguard-${version}-windows-arm64.exe
	cp wintun/bin/arm64/wintun.dll .
	zip -r peerguard-${version}-windows-arm64.zip peerguard-${version}-windows-arm64.exe wintun.dll 
	rm wintun.dll
windows: winamd64 winarm64

github: clean all
	gzip peerguard-${version}-linux*
	git tag -d ${version} 2>/dev/null || true
	gh release delete ${version} -y --cleanup-tag 2>/dev/null || true
	gh release create ${version} --generate-notes --title "peerguard ${version}" peerguard-${version}*.gz peerguard-${version}*.zip

dist: github

clean:
	rm peerguard* 2>/dev/null || true
	rm *.zip 2>/dev/null || true