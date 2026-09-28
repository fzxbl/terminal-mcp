package terminal

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed assets/*
var terminalAssetFiles embed.FS

var terminalAssets fs.FS

func init() {
	var err error
	terminalAssets, err = fs.Sub(terminalAssetFiles, "assets")
	if err != nil {
		panic(err)
	}
}

func serveTerminalAsset(w http.ResponseWriter, req *http.Request, name string) {
	if name == "" || path.Base(name) != name || strings.Contains(name, "..") {
		http.NotFound(w, nil)
		return
	}
	switch name {
	case "xterm-6.0.0.css", "xterm-6.0.0.js", "addon-fit-0.11.0.js", "addon-webgl-0.19.0.js":
	default:
		http.NotFound(w, nil)
		return
	}
	data, err := fs.ReadFile(terminalAssets, name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, req, name, time.Time{}, bytes.NewReader(data))
}
