package application_test

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestNewRejectsInvalidKeys(t *testing.T) {
	t.Parallel()
	handler, err := application.New(
		t.Context(),
		application.Config{Auth: application.AuthConfig{SigningKeyFile: "private-path-with-secret"}},
	)
	require.Error(t, err)
	assert.Nil(t, handler)
	assert.NotContains(t, err.Error(), "private-path-with-secret")
}

func TestNewPropagatesStartupCancellation(t *testing.T) {
	t.Parallel()
	id, signing, verifying, _ := testutil.AuthKeys(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	handler, err := application.New(
		ctx,
		application.Config{
			Auth: application.AuthConfig{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying},
		},
	)
	require.Error(t, err)
	assert.Nil(t, handler)
}

func TestHealthzReportsProcessHealthWithoutCredentials(t *testing.T) {
	t.Parallel()
	server := newPublicRoutesServer(t)
	response, err := server.Client().Get(server.URL + "/healthz")
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Empty(t, body)
}

func TestAPIDocumentationServesGeneratedSpecifications(t *testing.T) {
	t.Parallel()
	server := newPublicRoutesServer(t)
	for _, name := range []string{"swagger.yaml", "openapi.yaml"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expected, err := os.ReadFile("../../docs/openapi/" + name)
			require.NoError(t, err)
			response, err := server.Client().Get(server.URL + "/v1/" + name)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			assert.Equal(t, "application/yaml", response.Header.Get("Content-Type"))
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.Equal(t, expected, body)
		})
	}
}

func TestSwaggerUIRedirectsToTrailingSlash(t *testing.T) {
	t.Parallel()
	server := newPublicRoutesServer(t)
	client := *server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Get(server.URL + "/v1/swagger")
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusMovedPermanently, response.StatusCode)
	location, err := response.Location()
	require.NoError(t, err)
	assert.Equal(t, server.URL+"/v1/swagger/", location.String())
}

func TestSwaggerUIUsesTheSameOriginOpenAPIDocument(t *testing.T) {
	t.Parallel()
	server := newPublicRoutesServer(t)
	response, err := server.Client().Get(server.URL + "/v1/swagger/")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, response.Header.Get("Content-Type"), "text/html")
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	for _, expected := range []string{
		`href="./swagger-ui.css"`,
		`src="./swagger-ui-bundle.js"`,
		`url: "/v1/openapi.yaml"`,
		`validatorUrl: null`,
		`persistAuthorization: false`,
	} {
		assert.Contains(t, string(body), expected)
	}
}

func TestSwaggerUIServesItsAssets(t *testing.T) {
	t.Parallel()
	server := newPublicRoutesServer(t)
	for _, asset := range []struct {
		name      string
		mediaType string
		marker    string
	}{
		{"swagger-ui.css", "/css", ".swagger-ui"},
		{"swagger-ui-bundle.js", "/javascript", "SwaggerUIBundle"},
	} {
		t.Run(asset.name, func(t *testing.T) {
			t.Parallel()
			response, err := server.Client().Get(server.URL + "/v1/swagger/" + asset.name)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(mediaType, asset.mediaType), "unexpected content type %q", mediaType)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.Contains(t, string(body), asset.marker)
		})
	}
}

func newPublicRoutesServer(t *testing.T) *httptest.Server {
	t.Helper()
	id, signing, verifying, _ := testutil.AuthKeys(t)
	pool, err := pgxpool.New(t.Context(), "postgres://unused:unused@localhost/unused")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	app, err := application.New(t.Context(), application.Config{
		Auth: application.AuthConfig{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying},
	}, application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(app.Close)
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)
	return server
}
