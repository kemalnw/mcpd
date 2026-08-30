package oauth

import (
	_ "embed"
	"net/http"
)

//go:embed assets/mcpd-signal-core.webp
var signalCoreWebP []byte

func (s *Server) Brand(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(signalCoreWebP)
}
