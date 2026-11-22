package auth

import (
	"net/http"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

type signupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

type refreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func writeTokenPair(w http.ResponseWriter, pair auth.TokenPair) {
	handler.JSON(w, http.StatusOK, tokenResponse{
		AccessToken:  pair.AccessToken,
		TokenType:    "Bearer",
		ExpiresIn:    pair.ExpiresIn,
		RefreshToken: pair.RefreshToken,
	})
}
