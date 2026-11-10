package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/attempts"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth/authmock"
)

const loginPassword = "exact password material"
const loginEmail = "alice@example.com"
const userID = "e1f068c6-82a9-4e59-903f-639b11c1861b"
const sessionID = "c50b9b42-a335-4b77-82f1-c19cdf3ca8d9"
const tokenID = "4c435583-a1ee-4438-8cfc-09e3787c75a6"

type loginMocks struct {
	repository   *authmock.Repository
	hasher       *authmock.PasswordHasher
	sessions     *authmock.SessionRepository
	transactions *authmock.TransactionManager
	signer       *authmock.AccessSigner
	refresh      *authmock.RefreshGenerator
	ids          *authmock.IDGenerator
	clock        *authmock.Clock
	dependencies auth.LoginDependencies
}

func newLoginMocks(t *testing.T) *loginMocks {
	t.Helper()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	clock := authmock.NewClock(t)

	value := &loginMocks{
		repository: authmock.NewRepository(t), hasher: authmock.NewPasswordHasher(t),
		sessions: authmock.NewSessionRepository(t), transactions: authmock.NewTransactionManager(t),
		signer: authmock.NewAccessSigner(t), refresh: authmock.NewRefreshGenerator(t), ids: authmock.NewIDGenerator(t),
	}
	value.clock = clock
	value.dependencies = auth.LoginDependencies{
		Sessions:     value.sessions,
		Transactions: value.transactions,
		Signer:       value.signer,
		Refresh:      value.refresh,
		IDs:          value.ids,
		Clock:        clock,
		Attempts:     attempts.New(attempts.Config{Now: func() time.Time { return now }}),
		DummyHash:    "dummy PHC",
	}
	return value
}
func (m *loginMocks) usecase() *auth.Usecase {
	return auth.NewWithLogin(m.repository, m.hasher, m.dependencies)
}
func (m *loginMocks) validCredentials() {
	m.repository.EXPECT().
		FindCredentialsByEmail(mock.Anything, loginEmail).
		Return(user.User{ID: userID, Email: loginEmail}, "real PHC", nil).
		Once()
	m.hasher.EXPECT().Verify(mock.Anything, loginPassword, "real PHC").Return(true, nil).Once()
}

func TestLoginCredentialOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, hash           string
		lookupErr, verifyErr error
		matches              bool
		want                 error
	}{
		{name: "wrong password", hash: "real PHC", want: auth.ErrAuthenticationFailed},
		{name: "unknown user", lookupErr: user.ErrNotFound, hash: "dummy PHC", want: auth.ErrAuthenticationFailed},
		{name: "dummy matches unknown", lookupErr: user.ErrNotFound, hash: "dummy PHC", matches: true, want: auth.ErrAuthenticationFailed},
		{name: "malformed real hash", hash: "invalid PHC", verifyErr: errors.New("invalid hash")},
		{name: "malformed dummy hash", lookupErr: user.ErrNotFound, hash: "dummy PHC", verifyErr: errors.New("invalid hash")},
		{name: "database failure", lookupErr: errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newLoginMocks(t)
			m.repository.EXPECT().
				FindCredentialsByEmail(mock.Anything, loginEmail).
				Return(user.User{ID: userID, Email: loginEmail}, tc.hash, tc.lookupErr).
				Once()
			if tc.lookupErr == nil || errors.Is(tc.lookupErr, user.ErrNotFound) {
				m.hasher.EXPECT().Verify(mock.Anything, loginPassword, tc.hash).Return(tc.matches, tc.verifyErr).Once()
			}
			pair, err := m.usecase().Login(t.Context(), "  Alice@Example.COM  ", loginPassword)
			assert.Empty(t, pair)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			} else {
				assert.Error(t, err)
				assert.NotErrorIs(t, err, auth.ErrAuthenticationFailed)
			}
		})
	}
}

func TestLoginValidation(t *testing.T) {
	t.Parallel()
	for _, input := range []struct{ email, password string }{{"bad", loginPassword}, {loginEmail, "short"}, {loginEmail, "invalid \xff password"}} {
		t.Run(input.email+input.password, func(t *testing.T) {
			t.Parallel()
			m := newLoginMocks(t)
			pair, err := m.usecase().Login(t.Context(), input.email, input.password)
			assert.ErrorIs(t, err, user.ErrInvalidCredentials)
			assert.Empty(t, pair)
		})
	}
}

func TestLoginFailuresNeverReturnTokens(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"session ID", "token ID", "refresh entropy", "signing", "begin", "session insert", "refresh insert", "commit"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			m := newLoginMocks(t)
			m.validCredentials()
			failure := errors.New("injected dependency failure")
			expectLoginPipeline(m, stage, failure, nil)
			pair, err := m.usecase().Login(t.Context(), loginEmail, loginPassword)
			assert.ErrorIs(t, err, failure)
			assert.Empty(t, pair)
		})
	}
}

func expectLoginPipeline(m *loginMocks, stage string, failure error, cancel context.CancelFunc) {
	if stage == "session ID" {
		m.ids.EXPECT().Generate(mock.Anything).Return("", failure).Once()
		return
	}
	m.ids.EXPECT().Generate(mock.Anything).Return(sessionID, nil).Once()
	if stage == "token ID" {
		m.ids.EXPECT().Generate(mock.Anything).Return("", failure).Once()
		return
	}
	m.ids.EXPECT().Generate(mock.Anything).Return(tokenID, nil).Once()
	if stage == "refresh entropy" {
		m.refresh.EXPECT().Generate(mock.Anything).Return("", [32]byte{}, failure).Once()
		return
	}
	m.refresh.EXPECT().Generate(mock.Anything).Return("refresh secret", [32]byte{1}, nil).Once()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m.clock.EXPECT().Now().Return(now).Once()
	claims := session.AccessClaims{
		UserID:    userID,
		SessionID: sessionID,
		JTI:       tokenID,
		IssuedAt:  now,
		ExpiresAt: time.Date(2026, 11, 10, 12, 15, 0, 0, time.UTC),
	}
	if stage == "signing" {
		m.signer.EXPECT().Sign(mock.Anything, claims).Return("", failure).Once()
		return
	}
	m.signer.EXPECT().Sign(mock.Anything, claims).Return("signed access", nil).Once()
	if stage == "begin" {
		m.transactions.EXPECT().WithinTransaction(mock.Anything, mock.Anything).Return(failure).Once()
		return
	}
	m.transactions.EXPECT().
		WithinTransaction(mock.Anything, mock.Anything).
		RunAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
			if err := fn(ctx); err != nil {
				return err
			}
			if cancel != nil {
				cancel()
			}
			if stage == "commit" {
				return failure
			}
			return nil
		}).
		Once()
	if stage == "session insert" {
		m.sessions.EXPECT().InsertSession(mock.Anything, mock.Anything).Return(failure).Once()
		return
	}
	m.sessions.EXPECT().
		InsertSession(mock.Anything, session.Session{ID: sessionID, UserID: userID, CreatedAt: now, ExpiresAt: time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)}).
		Return(nil).
		Once()
	if stage == "refresh insert" {
		m.sessions.EXPECT().InsertRefresh(mock.Anything, mock.Anything).Return(failure).Once()
		return
	}
	m.sessions.EXPECT().
		InsertRefresh(mock.Anything, session.RefreshToken{Hash: [32]byte{1}, SessionID: sessionID, CreatedAt: now}).
		Return(nil).
		Once()
}

func TestLoginCancellation(t *testing.T) {
	t.Parallel()
	t.Run("already canceled", func(t *testing.T) {
		t.Parallel()
		m := newLoginMocks(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		pair, err := m.usecase().Login(ctx, loginEmail, loginPassword)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, pair)
	})
	t.Run("verification canceled with matching password", func(t *testing.T) {
		t.Parallel()
		m := newLoginMocks(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m.repository.EXPECT().
			FindCredentialsByEmail(mock.Anything, loginEmail).
			Return(user.User{ID: userID}, "real PHC", nil).
			Once()
		m.hasher.EXPECT().
			Verify(mock.Anything, loginPassword, "real PHC").
			RunAndReturn(func(context.Context, string, string) (bool, error) { cancel(); return true, nil }).
			Once()
		pair, err := m.usecase().Login(ctx, loginEmail, loginPassword)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, pair)
	})
	t.Run("transaction cancellation", func(t *testing.T) {
		t.Parallel()
		m := newLoginMocks(t)
		m.validCredentials()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		expectLoginPipeline(m, "", nil, cancel)
		pair, err := m.usecase().Login(ctx, loginEmail, loginPassword)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, pair)
	})
}

func TestLoginEmailBudget(t *testing.T) {
	t.Parallel()
	m := newLoginMocks(t)
	m.repository.EXPECT().
		FindCredentialsByEmail(mock.Anything, loginEmail).
		Return(user.User{ID: userID}, "real PHC", nil).
		Times(5)
	m.hasher.EXPECT().Verify(mock.Anything, loginPassword, "real PHC").Return(false, nil).Times(5)
	uc := m.usecase()
	for range 5 {
		pair, err := uc.Login(t.Context(), "  Alice@Example.COM  ", loginPassword)
		require.ErrorIs(t, err, auth.ErrAuthenticationFailed)
		assert.Empty(t, pair)
	}
	pair, err := uc.Login(t.Context(), loginEmail, loginPassword)
	assert.Empty(t, pair)
	var limit *auth.RateLimitError
	require.ErrorAs(t, err, &limit)
	assert.ErrorIs(t, err, auth.ErrRateLimited)
	assert.Equal(t, time.Minute, limit.RetryAfter)
}

func TestLoginReleaseAfterInternalFailure(t *testing.T) {
	t.Parallel()
	m := newLoginMocks(t)
	m.repository.EXPECT().
		FindCredentialsByEmail(mock.Anything, loginEmail).
		Return(user.User{ID: userID}, "real PHC", nil).
		Times(10)
	m.hasher.EXPECT().Verify(mock.Anything, loginPassword, "real PHC").Return(false, nil).Times(4)
	m.hasher.EXPECT().Verify(mock.Anything, loginPassword, "real PHC").Return(false, errors.New("internal")).Times(5)
	m.hasher.EXPECT().Verify(mock.Anything, loginPassword, "real PHC").Return(false, nil).Once()
	uc := m.usecase()
	for range 10 {
		_, err := uc.Login(t.Context(), loginEmail, loginPassword)
		assert.Error(t, err)
		assert.NotErrorIs(t, err, auth.ErrRateLimited)
	}
	_, err := uc.Login(t.Context(), loginEmail, loginPassword)
	assert.ErrorIs(t, err, auth.ErrRateLimited)
}

func TestMe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		lookupErr error
	}{{"existing", nil}, {"missing", user.ErrNotFound}, {"database", errors.New("database")}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newLoginMocks(t)
			expected := user.User{
				ID:        userID,
				Email:     loginEmail,
				CreatedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
			}
			m.repository.EXPECT().FindByID(mock.Anything, userID).Return(expected, tc.lookupErr).Once()
			actual, err := m.usecase().Me(t.Context(), userID)
			switch {
			case tc.lookupErr == nil:
				assert.NoError(t, err)
				assert.Equal(t, expected, actual)
			case errors.Is(tc.lookupErr, user.ErrNotFound):
				assert.ErrorIs(t, err, auth.ErrAuthenticationFailed)
				assert.Empty(t, actual)
			default:
				assert.ErrorIs(t, err, tc.lookupErr)
				assert.NotErrorIs(t, err, auth.ErrAuthenticationFailed)
				assert.Empty(t, actual)
			}
		})
	}
}

func TestLoginSuccessReleasesOnlyItsReservation(t *testing.T) {
	t.Parallel()
	m := newLoginMocks(t)
	m.repository.EXPECT().
		FindCredentialsByEmail(mock.Anything, loginEmail).
		Return(user.User{ID: userID}, "real PHC", nil).
		Times(6)
	m.hasher.EXPECT().Verify(mock.Anything, loginPassword, "real PHC").Return(false, nil).Times(4)
	m.hasher.EXPECT().Verify(mock.Anything, loginPassword, "real PHC").Return(true, nil).Once()
	m.hasher.EXPECT().Verify(mock.Anything, loginPassword, "real PHC").Return(false, nil).Once()
	expectLoginPipeline(m, "", nil, nil)
	uc := m.usecase()
	for range 4 {
		_, err := uc.Login(t.Context(), loginEmail, loginPassword)
		require.ErrorIs(t, err, auth.ErrAuthenticationFailed)
	}
	pair, err := uc.Login(t.Context(), loginEmail, loginPassword)
	require.NoError(t, err)
	assert.Equal(t, auth.TokenPair{AccessToken: "signed access", RefreshToken: "refresh secret", ExpiresIn: 900}, pair)
	_, err = uc.Login(t.Context(), loginEmail, loginPassword)
	require.ErrorIs(t, err, auth.ErrAuthenticationFailed)
	_, err = uc.Login(t.Context(), loginEmail, loginPassword)
	assert.ErrorIs(t, err, auth.ErrRateLimited)
}

func TestLoginConcurrentUnknownEmailBudget(t *testing.T) {
	t.Parallel()
	m := newLoginMocks(t)
	m.repository.EXPECT().
		FindCredentialsByEmail(mock.Anything, loginEmail).
		Return(user.User{}, "", user.ErrNotFound).
		Times(5)
	ready := make(chan struct{}, 5)
	release := make(chan struct{})
	m.hasher.EXPECT().
		Verify(mock.Anything, loginPassword, "dummy PHC").
		RunAndReturn(func(ctx context.Context, _, _ string) (bool, error) {
			ready <- struct{}{}
			select {
			case <-release:
				return true, nil
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}).
		Times(5)
	uc := m.usecase()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	results := make(chan error, 5)
	for range 5 {
		go func() {
			pair, err := uc.Login(ctx, "  Alice@Example.COM  ", loginPassword)
			assert.Empty(t, pair)
			results <- err
		}()
	}
	for range 5 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("verification barrier was not reached")
		}
	}
	pair, err := uc.Login(ctx, loginEmail, loginPassword)
	assert.Empty(t, pair)
	assert.ErrorIs(t, err, auth.ErrRateLimited)
	close(release)
	for range 5 {
		assert.ErrorIs(t, <-results, auth.ErrAuthenticationFailed)
	}
}
