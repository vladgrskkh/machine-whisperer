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
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/mobiletrace"
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

const (
	authStartupTimeout   = 10 * time.Second
	instrumentationScope = "github.com/vladgrskkh/machine-whisperer/api/internal/"
)

type Application struct {
	pool             *pgxpool.Pool
	databaseConfig   *DatabaseConfig
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
	deleteMachine    *machine.Delete
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
	mobileTrace      *mobiletrace.Handler
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

	logger := NewLogger(os.Stderr, os.Getenv("APP_ENV"), slog.LevelDebug)
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
	if err = app.setupDatabase(ctx); err != nil {
		return nil, err
	}
	transactions := transactionpostgres.New(app.pool)
	app.setupAuth(transactions, hasher, now, dummy)
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

func (a *Application) setupAuth(
	transactions *transactionpostgres.Manager,
	hasher password.Argon2id,
	now func() time.Time,
	dummy string,
) {
	repository := postgres.New(a.pool)
	sessions := sessionpostgres.New(a.pool)
	authTracer := a.tracerProvider.Tracer(instrumentationScope + "usecase/auth")
	a.signup = auth.NewSignup(repository, hasher, authTracer)
	a.login = auth.NewLogin(
		repository,
		hasher,
		sessions,
		transactions,
		a.verifier,
		token.Refresh{},
		token.IDs{},
		now,
		dummy,
		authTracer,
	)
	a.refresh = auth.NewRefresh(
		sessions,
		repository,
		transactions,
		a.verifier,
		token.Refresh{},
		token.IDs{},
		now,
		authTracer,
	)
	a.logout = auth.NewLogout(sessions, transactions, now, authTracer)
	a.me = auth.NewMe(repository, authTracer)
}

func (a *Application) setupEnrollment(
	transactions *transactionpostgres.Manager,
	claimTTL time.Duration,
	history telemetry.HistoryClient,
) {
	machines := machinepostgres.New(a.pool)
	devices := devicepostgres.New(a.pool)
	deviceTracer := a.tracerProvider.Tracer(instrumentationScope + "usecase/device")
	machineTracer := a.tracerProvider.Tracer(instrumentationScope + "usecase/machine")
	telemetryTracer := a.tracerProvider.Tracer(instrumentationScope + "usecase/telemetry")
	a.deviceAuth = device.NewAuthenticator(devices, deviceTracer)
	a.provision = device.NewProvision(
		devices,
		token.IDs{},
		credential.Generator{},
		credential.Generator{},
		claimTTL,
		deviceTracer,
	)
	a.claim = device.NewClaim(devices, transactions, a.logger, deviceTracer)
	a.createMachine = machine.NewCreate(machines, token.IDs{}, machineTracer)
	a.deleteMachine = machine.NewDelete(machines, machineTracer)
	a.listMachines = machine.NewList(machines, machineTracer)
	a.machineDetail = machine.NewDetail(machines, machineTracer)
	a.telemetryHistory = telemetry.NewListHistory(machines, history, telemetryTracer)
	a.getBaseline = machine.NewGetBaseline(machines, machineTracer)
	a.setBaseline = machine.NewSetBaseline(machines, transactions, a.logger, machineTracer)
}

func (a *Application) setupTelemetry(
	transactions *transactionpostgres.Manager,
	detection DetectorConfig,
	relay RelayConfig,
) {
	outbox := telemetrypostgres.NewOutbox(a.pool)
	a.metrics = metrics.New(telemetry.NewMeasureOutbox(outbox))
	repository := telemetrypostgres.New()
	telemetryTracer := a.tracerProvider.Tracer(instrumentationScope + "usecase/telemetry")
	detector := telemetry.NewDetector(repository, telemetry.DetectorSettings(detection), telemetryTracer)
	a.ingestTelemetry = telemetry.NewIngest(repository, transactions, detector, a.metrics, a.logger, telemetryTracer)
	alerts := alertpostgres.New(a.pool)
	alertTracer := a.tracerProvider.Tracer(instrumentationScope + "usecase/alert")
	a.listAlerts = alert.NewList(alerts, alertTracer)
	a.acknowledgeAlert = alert.NewAcknowledge(alerts, a.metrics, alertTracer)
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
		a.tracerProvider.Tracer(instrumentationScope+"usecase/telemetry"),
	)
	a.telemetryWorker = telemetryworker.New(export, config.BatchSize, config.PollInterval, a.logger,
		a.tracerProvider.Tracer(instrumentationScope+"worker/telemetry"))
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
