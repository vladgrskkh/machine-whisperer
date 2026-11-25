package auth

import (
	"net/http"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

type signupRequest struct {
	// Trimmed and lowercased before validation. The normalized address is at most 254 bytes.
	Email string `json:"email" validate:"required"`
	// Preserved verbatim. Requires 15–128 Unicode code points and at most 512 bytes.
	Password string `json:"password" validate:"required" minLength:"15" maxLength:"128"`
} // @name Credentials

type userResponse struct {
	ID        string    `json:"id"         validate:"required" format:"uuid"`
	Email     string    `json:"email"      validate:"required" format:"email"     maxLength:"254"`
	CreatedAt time.Time `json:"created_at" validate:"required" format:"date-time"`
} // @name User

type tokenResponse struct {
	// User access JWT.
	AccessToken string `json:"access_token" validate:"required"`
	TokenType   string `json:"token_type"   validate:"required" enums:"Bearer"`
	// Access token lifetime in seconds. The current API issues tokens for 900 seconds.
	ExpiresIn int `json:"expires_in" validate:"required" example:"900"`
	// Canonical unpadded base64url encoding of a 32-byte secret.
	RefreshToken string `json:"refresh_token" validate:"required" minLength:"43" maxLength:"43"`
} // @name TokenPair

type refreshTokenRequest struct {
	// Canonical unpadded base64url encoding of a 32-byte secret.
	RefreshToken string `json:"refresh_token" validate:"required" minLength:"43" maxLength:"43"`
} // @name RefreshToken

func writeTokenPair(w http.ResponseWriter, pair auth.TokenPair) {
	handler.JSON(w, http.StatusOK, tokenResponse{
		AccessToken:  pair.AccessToken,
		TokenType:    "Bearer",
		ExpiresIn:    pair.ExpiresIn,
		RefreshToken: pair.RefreshToken,
	})
}
