package httpapi

import (
	"bytes"
	_ "embed"
	"net/http"

	"github.com/sweeney/identity/common/spec"
)

//go:embed openapi.yaml
var openapiYAML []byte

func buildSpecConverter(publicURL string) *spec.Converter {
	data := openapiYAML
	if publicURL != "" {
		data = bytes.ReplaceAll(data, []byte("__PUBLIC_URL__"), []byte(publicURL))
	}
	return spec.NewConverter(data)
}

func (s *Server) handleOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	data, err := s.specConverter.JSON()
	if err != nil {
		http.Error(w, "spec unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
