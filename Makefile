version ?= unknown
git_hash := $(shell git rev-parse --short HEAD)

GOBUILD := CGO_ENABLED=0 go build -ldflags "-s -w -X 'main.Version=${version}' -X 'main.Commit=${git_hash}'"

all: linux windows darwin

linuxamd64:
	GOOS=linux GOARCH=amd64 ${GOBUILD} -o pgcli-${version}-linux-amd64 ./cmd/pgcli
	GOOS=linux GOARCH=amd64 ${GOBUILD} -o pgmap-${version}-linux-amd64 ./cmd/pgmap
linuxarm64:
	GOOS=linux GOARCH=arm64 ${GOBUILD} -o pgcli-${version}-linux-arm64 ./cmd/pgcli
	GOOS=linux GOARCH=arm64 ${GOBUILD} -o pgmap-${version}-linux-arm64 ./cmd/pgmap
linuxmips:
	GOOS=linux GOARCH=mips ${GOBUILD} -o pgcli-${version}-linux-mips ./cmd/pgcli
	GOOS=linux GOARCH=mipsle ${GOBUILD} -o pgcli-${version}-linux-mipsle ./cmd/pgcli
linuxmips64:
	GOOS=linux GOARCH=mips64 ${GOBUILD} -o pgcli-${version}-linux-mips64 ./cmd/pgcli
	GOOS=linux GOARCH=mips64le ${GOBUILD} -o pgcli-${version}-linux-mips64le ./cmd/pgcli
linux: linuxamd64 linuxarm64 linuxmips linuxmips64

wintun:
	curl -OL https://www.wintun.net/builds/wintun-0.14.1.zip
	unzip wintun-0.14.1.zip
	rm wintun-0.14.1.zip
winamd64: wintun
	GOOS=windows GOARCH=amd64 ${GOBUILD} -o pgcli-${version}-windows-amd64.exe ./cmd/pgcli
	cp wintun/bin/amd64/wintun.dll .
	zip -r pgcli-${version}-windows-amd64.zip pgcli-${version}-windows-amd64.exe wintun.dll 
	rm wintun.dll
winarm64: wintun
	GOOS=windows GOARCH=arm64 ${GOBUILD} -o pgcli-${version}-windows-arm64.exe ./cmd/pgcli
	cp wintun/bin/arm64/wintun.dll .
	zip -r pgcli-${version}-windows-arm64.zip pgcli-${version}-windows-arm64.exe wintun.dll 
	rm wintun.dll
windows: winamd64 winarm64

darwinamd64:
	GOOS=darwin GOARCH=amd64 ${GOBUILD} -o pgcli-${version}-darwin-amd64 ./cmd/pgcli
darwinarm64:
	GOOS=darwin GOARCH=arm64 ${GOBUILD} -o pgcli-${version}-darwin-arm64 ./cmd/pgcli
darwin: darwinamd64 darwinarm64

github: clean all
	gzip pgcli-${version}-linux*
	gzip pgcli-${version}-darwin*
	gzip pgmap-${version}-linux*
	git tag -d ${version} 2>/dev/null || true
	gh release delete ${version} -y --cleanup-tag 2>/dev/null || true
	gh release create ${version} --generate-notes --title "peerguard ${version}" pgcli-${version}*.gz pgcli-${version}*.zip pgmap-${version}*.gz

image:
	docker build . -t rkonfj/peerguard:${version} --build-arg version=${version} --build-arg githash=${git_hash}

dockerhub: image
	docker push rkonfj/peerguard:${version}

dist: github dockerhub

clean:
	rm pgcli* 2>/dev/null || true
	rm pgmap* 2>/dev/null || true
	rm *.zip 2>/dev/null || true
	rm *.dll 2>/dev/null || true
