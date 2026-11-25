#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT

NODE_NO_WARNINGS=1 node --experimental-vm-modules - "$root" "$scratch" <<'JS'
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const vm = require('node:vm');
const [root, scratch] = process.argv.slice(2);
const source = root + '/deploy/k6/telemetry-scenarios.js';
const sourceText = fs.existsSync(source) ? fs.readFileSync(source, 'utf8') : null;

const groups = ['healthy', 'alert', 'recovery', 'unknown'];
const fixture = {
    api_base_url: 'https://api.example.invalid',
    owner_email: 'demo@example.invalid',
    owner_password: 'dummy-password',
    simulations: 2,
    scenarios: Object.fromEntries(groups.map(group => [group, Array.from({length: 2}, () => {
        const deviceId = crypto.randomUUID();
        return {
            machine_id: crypto.randomUUID(),
            device_id: deviceId,
            device_api_key: deviceId + '.' + 'x'.repeat(43),
            mean_rms_g: 0.04,
            stddev_rms_g: 0.005,
        };
    })])),
};
fs.writeFileSync(scratch + '/fixtures.json', JSON.stringify(fixture), {mode: 0o600});

const expected = {
    healthy: [0.0425, 0.0425, 0.0425],
    alert: [0.06, 0.06, 0.06],
    recovery: [0.06, 0.06, 0.06, 0.04, 0.04, 0.04],
    unknown: [0.06, 0.06, 0.06],
};

async function load(env, mode, data = fixture) {
    assert.ok(sourceText, 'telemetry scenario script is missing');
    const start = Date.parse('2026-11-25T12:00:00Z');
    let now = start;
    const execution = {scenario: {name: '', iterationInTest: 0}};
    const machines = new Map(Object.values(fixture.scenarios).flat().map(machine => [
        machine.machine_id, {...machine, events: [], alerts: [], historyReads: 0},
    ]));
    const checks = [];
    const logins = [];
    const reply = (status, body) => ({status, json: () => body});
    const request = (method, url, payload, params) => {
        const {origin, pathname} = new URL(url);
        assert.equal(origin, fixture.api_base_url);
        assert.equal(params.redirects, 0);
        if (pathname === '/v1/auth/login') {
            assert.equal(method, 'POST');
            logins.push(JSON.parse(payload));
            return reply(200, {access_token: 'dummy.owner.token'});
        }

        const matched = /^\/v1\/machines\/([^/]+)\/(telemetry|alerts)$/.exec(pathname);
        assert.ok(matched, 'unexpected API route');
        const machine = machines.get(matched[1]);
        assert.ok(machine, 'unexpected machine');
        if (method === 'POST') {
            assert.equal(matched[2], 'telemetry');
            assert.equal(params.headers.Authorization, 'Bearer ' + machine.device_api_key);
            const event = JSON.parse(payload);
            if (mode === 'rejected-post') return reply(200);
            if (machine.events.length > 0) {
                assert.ok(Date.parse(event.observed_at) >= Date.parse(machine.events.at(-1).observed_at),
                    'observations must satisfy the API machine cursor on reruns');
            }
            machine.events.push(event);
            const {name} = execution.scenario;
            const position = machine.events.length % expected[name].length;
            if ((name === 'alert' && machine.events.length === 3) || (name === 'recovery' && position === 3)) {
                machine.alerts.push({
                    id: event.event_id, trigger_event_id: event.event_id, trigger_device_id: machine.device_id,
                    trigger_z_score: 4, last_z_score: 4, recovered_at: null,
                });
            }
            if (name === 'recovery' && position === 0 && mode !== 'missing-recovery') {
                Object.assign(machine.alerts.at(-1), {
                    recovered_at: event.observed_at, last_z_score: 0,
                    recovery_event_id: event.event_id, recovery_device_id: machine.device_id,
                });
            }
            return reply(202);
        }
        assert.equal(method, 'GET');
        assert.equal(params.headers.Authorization, 'Bearer dummy.owner.token');
        if (matched[2] === 'alerts') {
            if (mode === 'missing-alert') return reply(200, []);
            if (mode === 'unexpected-alert') return reply(200, [{recovered_at: null}]);
            if (mode === 'malformed-alert') return reply(200, [null]);
            return reply(200, structuredClone(machine.alerts));
        }
        const items = machine.events.map(event => ({...event, device_id: machine.device_id}));
        if (mode === 'missing-history') items.pop();
        return reply(200, {items: ++machine.historyReads === 1 ? [] : items});
    };
    const context = vm.createContext({
        __ENV: env,
        Date: class extends Date { static now() { return now; } },
        crypto,
        open: () => JSON.stringify(data),
    });
    const modules = {
        'k6': {
            check: (value, conditions) => {
                const results = Object.entries(conditions).map(([name, condition]) => [name, condition(value)]);
                checks.push(...results);
                return results.every(([, ok]) => ok);
            },
            fail: message => { throw Error(message); },
            sleep: seconds => { now += seconds * 1000; },
        },
        'k6/http': {default: {
            get: (url, params) => request('GET', url, null, params),
            post: (url, body, params) => request('POST', url, body, params),
        }},
        'k6/execution': {default: execution},
    };
    const module = new vm.SourceTextModule(sourceText, {context});
    await module.link(name => new vm.SyntheticModule(Object.keys(modules[name]), function() {
        for (const [key, value] of Object.entries(modules[name])) this.setExport(key, value);
    }, {context}));
    await module.evaluate();
    return {script: module.namespace, execution, machines, checks, logins, now: () => now, advance: ms => { now += ms; }};
}

function iteration(run, name, index, setupData) {
    Object.assign(run.execution.scenario, {name, iterationInTest: index});
    const delay = run.script.options.scenarios[name].startTime;
    assert.equal(delay, name === 'recovery' ? '6s' : '3s');
    run.advance(parseInt(delay, 10) * 1000);
    const started = run.now();
    const machine = run.machines.get(fixture.scenarios[name][index].machine_id);
    const oldCount = machine.events.length;
    run.script.default(setupData);
    return {started, events: machine.events.slice(oldCount)};
}

(async () => {
    const all = await load({SCENARIO: 'all', SIMULATIONS: '2'});
    assert.deepEqual(Object.keys(all.script.options.scenarios).sort(), groups.slice().sort());
    assert.equal(all.script.options.thresholds.checks[0], 'rate==1');
    const tokens = all.script.setup();
    assert.deepEqual(all.logins, [{email: fixture.owner_email, password: fixture.owner_password}]);

    const eventIds = new Set();
    for (const name of groups) {
        const options = all.script.options.scenarios[name];
        assert.equal(options.executor, 'per-vu-iterations');
        assert.equal(options.vus, 2);
        assert.equal(options.iterations, 1);
        for (let index = 0; index < 2; index++) {
            const {events, started} = iteration(all, name, index, tokens);
            assert.equal(events.length, expected[name].length);
            for (const [i, event] of events.entries()) {
                assert.equal(event.schema_version, 1);
                assert.match(event.event_id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
                assert.ok(!eventIds.has(event.event_id));
                eventIds.add(event.event_id);
                assert.match(event.observed_at, /Z$/);
                assert.ok(Date.parse(event.observed_at) <= started);
                if (i) assert.equal(Date.parse(event.observed_at) - Date.parse(events[i - 1].observed_at), 1000);
                assert.equal(event.operating_state, name === 'unknown' ? 'unknown' : 'running');
                assert.ok(Math.abs(event.vibration_rms_g - expected[name][i]) < 1e-12);
            }
        }
    }
    assert.ok(all.checks.length > 0);
    assert.ok(all.checks.every(([, ok]) => ok), 'a successful run must pass all checks');

    const single = await load({SCENARIO: 'recovery'});
    assert.deepEqual(Object.keys(single.script.options.scenarios), ['recovery']);
    assert.equal(single.script.options.scenarios.recovery.vus, 1);
    iteration(single, 'recovery', 0, single.script.setup());
    iteration(single, 'recovery', 0, single.script.setup());
    assert.equal(single.machines.get(fixture.scenarios.recovery[1].machine_id).events.length, 0);

    const alert = await load({SCENARIO: 'alert'});
    iteration(alert, 'alert', 0, alert.script.setup());
    iteration(alert, 'alert', 0, alert.script.setup());

    await assert.rejects(() => load({SCENARIO: 'invalid'}));
    await assert.rejects(() => load({SIMULATIONS: '0'}));
    await assert.rejects(() => load({SIMULATIONS: '3'}));
    const duplicate = structuredClone(fixture);
    duplicate.scenarios.alert[0].device_id = duplicate.scenarios.healthy[0].device_id;
    duplicate.scenarios.alert[0].device_api_key = duplicate.scenarios.healthy[0].device_api_key;
    await assert.rejects(() => load({}, null, duplicate));

    for (const [name, failure] of [
        ['healthy', 'rejected-post'], ['alert', 'missing-alert'], ['recovery', 'missing-recovery'],
        ['unknown', 'unexpected-alert'], ['alert', 'malformed-alert'], ['healthy', 'missing-history'],
    ]) {
        const run = await load({SCENARIO: name}, failure);
        assert.throws(() => iteration(run, name, 0, run.script.setup()), failure);
        assert.ok(run.checks.some(([, ok]) => !ok), failure + ' must fail a k6 check');
    }
    console.log('Telemetry scenario contracts passed.');
})().catch(error => {
    console.error(error);
    process.exitCode = 1;
});
JS

runner="$root/deploy/scripts/run-k6-demo.sh"
[[ -x "$runner" ]] || { echo 'Docker runner is missing or is not executable.' >&2; exit 1; }
mkdir "$scratch/bin"
cat > "$scratch/bin/docker" <<'DOCKER'
#!/usr/bin/env bash
printf '%s\n' "$@"
DOCKER
chmod +x "$scratch/bin/docker"

args=$(PATH="$scratch/bin:$PATH" SCENARIO=alert SIMULATIONS=2 FIXTURES_FILE="$scratch/fixtures.json" "$runner")
grep -Fxq 'grafana/k6:1.8.2' <<<"$args"
grep -Fxq "type=bind,src=$scratch/fixtures.json,dst=/demo-fixtures.json,readonly" <<<"$args"
grep -Fxq 'SCENARIO=alert' <<<"$args"
grep -Fxq 'SIMULATIONS=2' <<<"$args"
grep -Fxq -- '--log-output=none' <<<"$args"
grep -Fxq "$(id -u):$(id -g)" <<<"$args"
if grep -Eq 'dummy-password|demo@example.invalid' <<<"$args"; then
    echo 'Docker runner exposed fixture credentials.' >&2
    exit 1
fi
if PATH="$scratch/bin:$PATH" FIXTURES_FILE="$scratch/missing.json" "$runner" >/dev/null 2>&1; then
    echo 'Docker runner accepted a missing fixture.' >&2
    exit 1
fi
echo 'Docker runner contracts passed.'

if [[ ${1:-} == --archive ]]; then
    docker run --rm --network none --user "$(id -u):$(id -g)" \
        --mount "type=bind,src=$root/deploy/k6/telemetry-scenarios.js,dst=/telemetry-scenarios.js,readonly" \
        --mount "type=bind,src=$scratch/fixtures.json,dst=/demo-fixtures.json,readonly" \
        grafana/k6:1.8.2 archive -e SCENARIO=all -e SIMULATIONS=2 \
        --archive-out /tmp/telemetry-scenarios.tar /telemetry-scenarios.js
    echo 'Pinned k6 offline archive check passed.'
fi
