import { check, fail } from 'k6';
import exec from 'k6/execution';
import http from 'k6/http';

const minutes = __ENV.LOAD_DURATION_MINUTES ?? '5';
if (!/^(?:[1-9]|1[0-4])$/.test(minutes)) {
    throw new Error('LOAD_DURATION_MINUTES must be a whole number from 1 to 14.');
}

const durationSeconds = Number(minutes) * 60;
const telemetryPath = '/v1/devices/me/telemetry';
const loginPath = '/v1/auth/login';
const telemetryRates = { telemetry_1: 2, telemetry_2: 2, telemetry_3: 1 };
const fixture = loadFixture();

function loadFixture() {
    let data;
    try {
        data = JSON.parse(open(__ENV.FIXTURES_FILE ?? './load-fixtures.json'));
    } catch {
        throw new Error('Could not read the private load fixture file.');
    }
    const origin = /^https:\/\/[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?(?::[0-9]{1,5})?$/;
    if (typeof data?.api_base_url !== 'string' || !origin.test(data.api_base_url) ||
        typeof data.owner_email !== 'string' || !data.owner_email ||
        typeof data.owner_password !== 'string' || !data.owner_password) {
        throw new Error('The load fixture must provide an HTTPS origin and owner login.');
    }
    const devices = data.scenarios?.healthy;
    if (!Array.isArray(devices) || devices.length < 3) {
        throw new Error('The load fixture must contain three healthy devices. Seed with SIMULATIONS=3.');
    }

    const uuid = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/;
    const machineIDs = new Set();
    const deviceIDs = new Set();
    for (const device of devices.slice(0, 3)) {
        if (typeof device?.machine_id !== 'string' || !uuid.test(device.machine_id) ||
            typeof device.device_id !== 'string' || !uuid.test(device.device_id) ||
            typeof device.device_api_key !== 'string' ||
            !device.device_api_key.startsWith(device.device_id + '.') ||
            !/^[A-Za-z0-9_-]{43}$/.test(device.device_api_key.slice(37)) ||
            !Number.isFinite(device.mean_rms_g) || device.mean_rms_g < 0 ||
            !Number.isFinite(device.stddev_rms_g) || device.stddev_rms_g <= 0 ||
            machineIDs.has(device.machine_id) || deviceIDs.has(device.device_id)) {
            throw new Error('The first three healthy fixtures must have distinct machines and devices, valid keys and valid baselines.');
        }
        machineIDs.add(device.machine_id);
        deviceIDs.add(device.device_id);
    }
    return data;
}

function arrivalRate(handler, rate, timeUnit, startTime = '0s') {
    return {
        executor: 'constant-arrival-rate',
        exec: handler,
        rate,
        timeUnit,
        startTime,
        // Exclude the tick at the right edge of the requested window.
        duration: `${durationSeconds * 1000 - 50}ms`,
        // Each device keeps a single request in flight to preserve observation order.
        preAllocatedVUs: 1,
        maxVUs: 1,
        gracefulStop: '5s',
    };
}

const scenarios = {};
const thresholds = {
    checks: ['rate==1'],
    dropped_iterations: ['count==0'],
    'http_reqs{scenario:owner_reads}': [`count==${durationSeconds / 2}`],
};
for (const [index, [name, rate]] of Object.entries(telemetryRates).entries()) {
    scenarios[name] = arrivalRate('submitTelemetry', rate, '1s', `${index * 200}ms`);
    thresholds[`http_reqs{scenario:${name}}`] = [`count==${durationSeconds * rate}`];
}
scenarios.owner_reads = arrivalRate('readOwnerAPI', 1, '2s');

export const options = {
    scenarios,
    thresholds,
    discardResponseBodies: true,
    summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
    systemTags: ['scenario', 'method', 'status', 'name', 'check'],
};

function requestParams(name, token) {
    return {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        tags: { name },
        redirects: 0,
        timeout: '5s',
    };
}

export function setup() {
    const params = requestParams(`POST ${loginPath}`);
    params.headers['Content-Type'] = 'application/json';
    const response = http.post(fixture.api_base_url + loginPath, JSON.stringify({
        email: fixture.owner_email,
        password: fixture.owner_password,
    }), { ...params, responseType: 'text' });

    if (response.status !== 200) {
        fail('Demo owner login did not return HTTP 200.');
    }
    let token;
    try {
        token = response.json().access_token;
    } catch {
        fail('Demo owner login returned invalid JSON.');
    }
    if (typeof token !== 'string' || !token) {
        fail('Demo owner login did not return an access token.');
    }
    return { token };
}

const deviceIndexes = Object.keys(telemetryRates);
let lastObservedMs = 0;

export function submitTelemetry() {
    const device = fixture.scenarios.healthy[deviceIndexes.indexOf(exec.scenario.name)];
    lastObservedMs = Math.max(Date.now(), lastObservedMs);
    const event = {
        schema_version: 1,
        event_id: crypto.randomUUID(),
        observed_at: new Date(lastObservedMs).toISOString(),
        operating_state: 'running',
        vibration_rms_g: device.mean_rms_g,
    };
    const params = requestParams(`POST ${telemetryPath}`, device.device_api_key);
    params.headers['Content-Type'] = 'application/json';
    const response = http.post(fixture.api_base_url + telemetryPath, JSON.stringify(event), params);
    check(response, { 'device telemetry accepted': result => result.status === 202 });
}

export function readOwnerAPI({ token }) {
    const machine = `/v1/machines/${fixture.scenarios.healthy[0].machine_id}`;
    const reads = [
        ['/v1/users/me', '/v1/users/me'],
        ['/v1/machines', '/v1/machines'],
        [machine + '/baseline', '/v1/machines/:id/baseline'],
        [machine + '/alerts', '/v1/machines/:id/alerts'],
        [machine + '/telemetry?limit=20', '/v1/machines/:id/telemetry'],
    ];
    const [path, name] = reads[exec.scenario.iterationInTest % reads.length];
    const response = http.get(fixture.api_base_url + path, requestParams(`GET ${name}`, token));
    check(response, { 'owner read succeeded': result => result.status === 200 });
}
