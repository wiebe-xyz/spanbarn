package api

import (
	"encoding/json"
	"net/http"
)

// themeManifest is the schema served at /.well-known/iambarn-theme.json.
// IAMBarn fetches this file when a user lands on its login page via an OAuth
// authorize redirect originating from a SpanBarn host, so the login screen can
// adopt SpanBarn's brand colours, name, and logo. Field names and shape match
// the contract in iambarn/internal/theme.
type themeManifest struct {
	Name            string `json:"name"`
	LogoURL         string `json:"logo_url"`
	PrimaryColor    string `json:"primary_color"`
	BackgroundColor string `json:"background_color"`
	CardColor       string `json:"card_color"`
	BodyTextColor   string `json:"body_text_color"`
	SupportURL      string `json:"support_url"`
	Locale          string `json:"locale"`
}

// spanBarnThemeManifest holds the values served at /.well-known/iambarn-theme.json.
// Colours mirror the CSS variables in web/src/index.css so the IAMBarn login
// page stays visually consistent with the SpanBarn UI. URLs are absolute and
// point at the canonical production host.
var spanBarnThemeManifest = themeManifest{
	Name:            "SpanBarn",
	LogoURL:         "https://spanbarn.wiebe.xyz/favicon.svg",
	PrimaryColor:    "#3b82f6", // --accent
	BackgroundColor: "#0f1117", // --bg
	CardColor:       "#1a1d27", // --surface
	BodyTextColor:   "#e2e8f0", // --text
	SupportURL:      "https://spanbarn.wiebe.xyz",
	Locale:          "en",
}

// handleThemeManifest serves the public IAMBarn theme manifest. The endpoint
// is unauthenticated, returns application/json, and must not redirect — the
// IAMBarn cache uses CheckRedirect=ErrUseLastResponse and treats any 3xx as a
// failure.
func (s *Server) handleThemeManifest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(spanBarnThemeManifest)
}
