package ui

import (
	"embed"
	"net/http"
	"path"
	"strings"
)

//go:embed dist/*
var staticFiles embed.FS

func HandleStaticFiles(w http.ResponseWriter, r *http.Request) {
	filePath := path.Join("dist", strings.TrimPrefix(r.URL.Path, "/"))
	f, err := staticFiles.Open(filePath)
	if err != nil {
		http.ServeFileFS(w, r, staticFiles, "dist/index.html")
		return
	}
	f.Close()
	http.ServeFileFS(w, r, staticFiles, filePath)
}
