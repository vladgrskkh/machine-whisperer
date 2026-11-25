import { check, fail, sleep } from 'k6';
import exec from 'k6/execution';
import http from 'k6/http';

const scenarioNames = ['healthy', 'alert', 'recovery', 'unknown'];
const scenario = __ENV.SCENARIO ?? 'all';
const simulationCount = __ENV.SIMULATIONS ?? '1';
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const keySuffixPattern = /^[A-Za-z0-9_-]{43}$/;
const originPattern = /^https:\/\/[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?$/;

if (scenario !== 'all' && !scenarioNames.includes(scenario)) {
    throw new Error('SCENARIO must be healthy, alert, recovery, unknown, or all.');
}
if (!/^[1-3]$/.test(simulationCount)) {
    throw new Error('SIMULATIONS must be 1, 2, or 3.');
}

const selected = scenario === 'all' ? scenarioNames : [scenario];
const simulations = Number(simulationCount);
const fixtureFile = loadFixtures();

function loadFixtures() {
    let file;
    try {
        file = JSON.parse(open('./demo-fixtures.json'));
    } catch {
        throw new Error('The private demo fixture file could not be read.');
    }

    if (!file || !originPattern.test(file.api_base_url) ||
        typeof file.owner_email !== 'string' || !file.owner_email ||
        typeof file.owner_password !== 'string' || !file.owner_password) {
        throw new Error('The private demo fixture file has an invalid origin or owner.');
    }

    const machines = new Set();
    const devices = new Set();
    for (const name of selected) {
        const group = file.scenarios?.[name];
        if (!Array.isArray(group) || group.length < simulations) {
            throw new Error(`The fixture file needs at least ${simulations} entries for ${name}.`);
        }
        for (const entry of group.slice(0, simulations)) {
            if (!entry || !uuidPattern.test(entry.machine_id) || !uuidPattern.test(entry.device_id) ||
                typeof entry.device_api_key !== 'string' || !entry.device_api_key.startsWith(entry.device_id + '.') ||
                !keySuffixPattern.test(entry.device_api_key.slice(37)) ||
                !Number.isFinite(entry.mean_rms_g) || entry.mean_rms_g < 0 ||
                !Number.isFinite(entry.stddev_rms_g) || entry.stddev_rms_g <= 0 ||
                machines.has(entry.machine_id) || devices.has(entry.device_id)) {
                throw new Error(`The ${name} fixtures must have distinct machines and devices with valid keys and baselines.`);
            }
            machines.add(entry.machine_id);
            devices.add(entry.device_id);
        }
    }
    return file;
}

const scenarios = {};
for (const name of selected) {
    // Let the whole timestamp window fit after a preceding run before starting the VUs.
    scenarios[name] = {
        executor: 'per-vu-iterations',
        vus: simulations,
        iterations: 1,
        startTime: name === 'recovery' ? '6s' : '3s',
        maxDuration: '2m',
    };
}

export const options = {
    scenarios,
    thresholds: {
        checks: ['rate==1'],
        dropped_iterations: ['count==0'],
    },
    systemTags: ['scenario', 'method', 'status', 'name', 'check'],
};

function requestParams(route, token) {
    const headers = { 'Content-Type': 'application/json' };
    if (token) {
        headers.Authorization = `Bearer ${token}`;
    }
    return { headers, tags: { name: route }, redirects: 0, timeout: '5s' };
}

function expect(name, value) {
    if (!check(value, { [name]: result => result === true })) {
        fail(name);
    }
}

function json(response) {
    try {
        return response.json();
    } catch {
        return null;
    }
}

export function setup() {
    const route = '/v1/auth/login';
    const response = http.post(fixtureFile.api_base_url + route, JSON.stringify({
        email: fixtureFile.owner_email,
        password: fixtureFile.owner_password,
    }), requestParams(route));
    expect('Owner login returns 200', response.status === 200);
    const ownerToken = json(response)?.access_token;
    expect('Owner login returns an access token', typeof ownerToken === 'string' && ownerToken.length > 0);
    return { ownerToken };
}

function readAlerts(machineURL, ownerToken) {
    const response = http.get(machineURL + '/alerts', requestParams('GET /v1/machines/:id/alerts', ownerToken));
    expect('Alert list returns 200', response.status === 200);
    const alerts = json(response);
    expect('Alert list contains valid alert states', Array.isArray(alerts) &&
        alerts.every(alert => alert && (alert.recovered_at === null || typeof alert.recovered_at === 'string')));
    return alerts;
}

function verifyActiveAlert(alerts, fixture, trigger, previousAlert) {
    const active = alerts.filter(alert => alert.recovered_at === null);
    expect('Machine has one active alert with the expected trigger', active.length === 1 &&
        active[0].trigger_device_id === fixture.device_id &&
        active[0].trigger_z_score >= 3 && active[0].last_z_score >= 3 &&
        (previousAlert ? active[0].id === previousAlert.id : active[0].trigger_event_id === trigger.event_id));
    return active[0];
}

function verifyHistory(machineURL, fixture, events, ownerToken) {
    const params = requestParams('GET /v1/machines/:id/telemetry', ownerToken);
    const url = machineURL + `/telemetry?limit=${events.length}`;
    const deadline = Date.now() + 30_000;
    do {
        const response = http.get(url, params);
        const items = response.status === 200 ? json(response)?.items : null;
        if (Array.isArray(items)) {
            const ids = new Set(items.filter(item => item?.device_id === fixture.device_id).map(item => item.event_id));
            if (events.every(event => ids.has(event.event_id))) {
                expect('All submitted events are visible in telemetry history', true);
                return;
            }
        }
        sleep(0.5);
    } while (Date.now() < deadline);
    expect('All submitted events are visible in telemetry history', false);
}

export default function ({ ownerToken }) {
    const startedAt = Date.now();
    const { name, iterationInTest } = exec.scenario;
    const fixture = fixtureFile.scenarios[name][iterationInTest];
    const machineURL = `${fixtureFile.api_base_url}/v1/machines/${fixture.machine_id}`;
    const telemetryURL = `${fixtureFile.api_base_url}/v1/devices/me/telemetry`;
    const params = {
        ...requestParams('POST /v1/devices/me/telemetry', fixture.device_api_key),
        responseType: 'none',
    };

    const recovery = name === 'recovery';
    const shouldAlert = name === 'alert' || recovery;
    const previousAlert = shouldAlert ?
        readAlerts(machineURL, ownerToken).find(alert => alert.recovered_at === null) : null;
    const { mean_rms_g: mean, stddev_rms_g: stddev } = fixture;
    const readings = Array(3).fill(mean + (name === 'healthy' ? 0.5 : 4) * stddev);
    if (recovery) {
        readings.push(mean, mean, mean);
    }

    const firstAt = startedAt - (readings.length - 1) * 1000;
    const events = readings.map((reading, index) => ({
        schema_version: 1,
        event_id: crypto.randomUUID(),
        observed_at: new Date(firstAt + 1000 * index).toISOString(),
        operating_state: name === 'unknown' ? 'unknown' : 'running',
        vibration_rms_g: reading,
    }));

    let activeAlert;
    for (const [index, event] of events.entries()) {
        const response = http.post(telemetryURL, JSON.stringify(event), params);
        expect('Telemetry ingest returns 202', response.status === 202);
        if (shouldAlert && index === 2) {
            activeAlert = verifyActiveAlert(readAlerts(machineURL, ownerToken), fixture, event, previousAlert);
        }
    }

    if (recovery) {
        const alerts = readAlerts(machineURL, ownerToken);
        const recovered = alerts.find(alert => alert.id === activeAlert.id);
        expect('Active alert recovers on the final baseline observation',
            !alerts.some(alert => alert.recovered_at === null) && typeof recovered?.recovered_at === 'string' &&
            recovered.recovery_event_id === events[events.length - 1].event_id &&
            recovered.recovery_device_id === fixture.device_id && recovered.last_z_score <= 2);
    } else if (!shouldAlert) {
        expect('Machine has no alerts', readAlerts(machineURL, ownerToken).length === 0);
    }

    verifyHistory(machineURL, fixture, events, ownerToken);
}
