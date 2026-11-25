package application

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	alertpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/alert/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/password"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/credential"
	devicepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/postgres"
	machinepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/machine/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/metrics"
	sessionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/session/postgres"
	analyticshttp "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/analytics"
	telemetrypostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/tracing"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/alert"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/device"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry"
	telemetryworker "github.com/vladgrskkh/machine-whisperer/api/internal/worker/telemetry"
)

const authStartupTimeout = 10 * time.Second

type Application struct {
	pool             *pgxpool.Pool
	ownsPool         bool
	signup           *auth.Signup
	login            *auth.Login
	refresh          *auth.Refresh
	logout           *auth.Logout
	me               *auth.Me
	provision        *device.Provision
	claim            *device.Claim
	deviceAuth       *device.Authenticator
	ingestTelemetry  *telemetry.Ingest
	telemetryHistory *telemetry.ListHistory
	telemetryWorker  *telemetryworker.Worker
	metrics          *metrics.Metrics
	createMachine    *machine.Create
	listMachines     *machine.List
	machineDetail    *machine.Detail
	getBaseline      *machine.GetBaseline
	setBaseline      *machine.SetBaseline
	listAlerts       *alert.List
	acknowledgeAlert *alert.Acknowledge
	verifier         *token.JWT
	logger           *slog.Logger
	rateLimitConfig  middleware.RateLimitConfig
	tracerProvider   trace.TracerProvider
	propagator       propagation.TextMapPropagator
}

type Option func(context.Context, *Application) error

func New(ctx context.Context, config Config, options ...Option) (*Application, error) {
	// Route-only callers can omit relay settings. LoadConfig always requires them.
	if config.Relay != (RelayConfig{}) {
		if err := config.Relay.validate(); err != nil {
			return nil, err
		}
	}
	detectorConfig := config.Detector.withDefaults()
	if err := detectorConfig.validate(); err != nil {
		return nil, err
	}

	claimTTL := config.Device.ClaimTTL
	if claimTTL < 0 {
		return nil, errInvalidClaimTTL
	}
	if claimTTL == 0 {
		claimTTL = defaultClaimTTL
	}
	startupCtx, cancel := context.WithTimeout(ctx, authStartupTimeout)
	defer cancel()
	if startupCtx.Err() != nil {
		return nil, errors.New("auth initialization failed")
	}
	keys, err := token.LoadKeys(config.Auth.SigningKeyID, config.Auth.SigningKeyFile, config.Auth.VerifyKeysFile)
	if err != nil {
		return nil, errors.New("invalid auth key configuration")
	}
	now := config.Auth.Now
	if now == nil {
		now = time.Now
	}
	signer, err := token.NewJWT(keys, now)
	if err != nil {
		return nil, errors.New("invalid auth key configuration")
	}
	hasher := password.Argon2id{}
	dummy, err := hasher.Hash(startupCtx, "fixed non-user dummy password material")
	if err != nil {
		return nil, errors.New("auth initialization failed")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app := &Application{
		verifier: signer, logger: logger, rateLimitConfig: config.RateLimits.IP,
		tracerProvider: noop.NewTracerProvider(), propagator: propagation.TraceContext{},
	}
	for _, option := range options {
		if err = option(ctx, app); err != nil {
			app.Close()
			return nil, err
		}
	}
	if app.pool == nil {
		return nil, errors.New("database pool is required")
	}
	repository := postgres.New(app.pool)
	sessions := sessionpostgres.New(app.pool)
	transactions := transactionpostgres.New(app.pool)
	app.signup = auth.NewSignup(repository, hasher)
	app.login = auth.NewLogin(
		repository,
		hasher,
		sessions,
		transactions,
		signer,
		token.Refresh{},
		token.IDs{},
		now,
		dummy,
	)
	app.refresh = auth.NewRefresh(sessions, repository, transactions, signer, token.Refresh{}, token.IDs{}, now)
	app.logout = auth.NewLogout(sessions, transactions, now)
	app.me = auth.NewMe(repository)
	app.setupEnrollment(
		transactions,
		claimTTL,
		analyticshttp.NewHistoryClient(
			config.Relay.AnalyticsURL,
			config.Relay.AnalyticsAPIKey,
			tracing.HTTPTransport(app.tracerProvider, app.propagator),
		),
	)
	app.setupTelemetry(transactions, detectorConfig, config.Relay)

	return app, nil
}

func (a *Application) setupEnrollment(
	transactions *transactionpostgres.Manager,
	claimTTL time.Duration,
	history telemetry.HistoryClient,
) {
	machines := machinepostgres.New(a.pool)
	devices := devicepostgres.New(a.pool)
	a.deviceAuth = device.NewAuthenticator(devices)
	a.provision = device.NewProvision(
		devices,
		token.IDs{},
		credential.Generator{},
		credential.Generator{},
		claimTTL,
	)
	a.claim = device.NewClaim(devices, transactions)
	a.createMachine = machine.NewCreate(machines, token.IDs{})
	a.listMachines = machine.NewList(machines)
	a.machineDetail = machine.NewDetail(machines)
	a.telemetryHistory = telemetry.NewListHistory(machines, history)
	a.getBaseline = machine.NewGetBaseline(machines)
	a.setBaseline = machine.NewSetBaseline(machines, transactions)
}

func (a *Application) setupTelemetry(
	transactions *transactionpostgres.Manager,
	detection DetectorConfig,
	relay RelayConfig,
) {
	outbox := telemetrypostgres.NewOutbox(a.pool)
	a.metrics = metrics.New(telemetry.NewMeasureOutbox(outbox))
	repository := telemetrypostgres.New()
	detector := telemetry.NewDetector(repository, telemetry.DetectorSettings(detection))
	a.ingestTelemetry = telemetry.NewIngest(repository, transactions, detector, a.metrics)
	alerts := alertpostgres.New(a.pool)
	a.listAlerts = alert.NewList(alerts)
	a.acknowledgeAlert = alert.NewAcknowledge(alerts, a.metrics)
	if relay != (RelayConfig{}) {
		a.setupRelay(relay, outbox)
	}
}

func (a *Application) setupRelay(config RelayConfig, outbox *telemetrypostgres.Outbox) {
	export := telemetry.NewExport(
		outbox,
		analyticshttp.NewHTTPClient(
			config.AnalyticsURL, config.AnalyticsAPIKey, config.HTTPTimeout, a.metrics,
			tracing.HTTPTransport(a.tracerProvider, a.propagator),
		),
		telemetry.ExportSettings{
			BatchSize: config.BatchSize,
			Lease:     config.Lease,
			RetryBase: config.RetryBase,
			RetryMax:  config.RetryMax,
			Retention: config.Retention,
		},
		a.logger,
		a.metrics,
	)
	a.telemetryWorker = telemetryworker.New(export, config.BatchSize, config.PollInterval, a.logger)
}

func (a *Application) RunWorkers(ctx context.Context) {
	if a.telemetryWorker != nil {
		a.telemetryWorker.Run(ctx)
	}
}

func (a *Application) MetricsHandler() http.Handler {
	return a.metrics.Handler()
}

func (a *Application) Close() {
	if a.ownsPool {
		a.pool.Close()
	}
}
