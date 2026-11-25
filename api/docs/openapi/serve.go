package apidocs

import (
	"embed"
	"net/http"
	"strings"
	"time"
)

//go:embed swagger.yaml
var swaggerYAML string

//go:embed openapi.yaml
var openapiYAML string

//go:embed swagger/index.html swagger/swagger-ui.css swagger/swagger-ui-bundle.js
//go:embed swagger/LICENSE swagger/NOTICE swagger/swagger-ui-bundle.js.LICENSE.txt
var swaggerUIFiles embed.FS

// ServeSwagger returns the generated Swagger 2 document.
//
// @Summary Download the Swagger 2 specification
// @ID downloadSwagger
// @Tags operational
// @Produce application/yaml
// @Success 200 {string} string "Swagger 2 YAML."
// @Router /v1/swagger.yaml [get]
func ServeSwagger(w http.ResponseWriter, r *http.Request) {
	serveYAML(w, r, "swagger.yaml", swaggerYAML)
}

// ServeOpenAPI returns the generated OpenAPI 3 document.
//
// @Summary Download the OpenAPI 3 specification
// @ID downloadOpenAPI
// @Tags operational
// @Produce application/yaml
// @Success 200 {string} string "OpenAPI 3 YAML."
// @Router /v1/openapi.yaml [get]
func ServeOpenAPI(w http.ResponseWriter, r *http.Request) {
	serveYAML(w, r, "openapi.yaml", openapiYAML)
}

// SwaggerUI serves the interactive API documentation and its assets.
//
// @Summary Open the interactive Swagger UI
// @ID openSwaggerUI
// @Tags operational
// @Produce text/html
// @Success 200 {string} string "Interactive API documentation."
// @Router /v1/swagger/ [get]
func SwaggerUI() http.Handler {
	return http.StripPrefix("/v1", http.FileServerFS(swaggerUIFiles))
}

func serveYAML(w http.ResponseWriter, r *http.Request, name, document string) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(document))
}
