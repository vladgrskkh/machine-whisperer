import { parseUUID } from '../../../api/uuid';
import { parseClaimCode, parseClaimQR } from '../claim-input';

const id = '93a68d42-c83f-4221-aeae-d951a995b262';
const code = 'A'.repeat(43);

it('canonicalizes a hyphenated UUID', () => {
  expect(parseUUID(id.toUpperCase())).toBe(id);
});

it.each([id.replaceAll('-', ''), ' ' + id, id + '/other', id + '\n', 'not-a-uuid'])(
  'rejects a non-canonical UUID representation',
  (invalid) => expect(parseUUID(invalid)).toBeNull(),
);

it('accepts a 32-byte unpadded base64url claim code', () => {
  expect(parseClaimCode('a-_' + 'A'.repeat(40))).toBe('a-_' + 'A'.repeat(40));
});

it.each([code.slice(1), code + '=', code + '\n', 'A'.repeat(42) + 'B', code.replace('A', '/')])(
  'rejects a noncanonical claim code',
  (invalid) => expect(parseClaimCode(invalid)).toBeNull(),
);

it('parses a versioned device QR without a machine ID', () => {
  expect(parseClaimQR(JSON.stringify({ v: 1, device_id: id.toUpperCase(), claim_code: code })))
    .toEqual({ device_id: id, claim_code: code });
});

it.each([
  'not JSON',
  `{"v":1.0,"device_id":"${id}","claim_code":"${code}"}`,
  `{"v":1e0,"device_id":"${id}","claim_code":"${code}"}`,
  'null',
  JSON.stringify([{ v: 1, device_id: id, claim_code: code }]),
  JSON.stringify({ device_id: id, claim_code: code }),
  JSON.stringify({ v: 2, device_id: id, claim_code: code }),
  JSON.stringify({ v: 1, device_id: 9, claim_code: code }),
  JSON.stringify({ v: 1, device_id: id, claim_code: 'short' }),
  JSON.stringify({ v: 1, device_id: id, claim_code: code, machine_id: id }),
  `{"v":1,"device_id":"${id}","claim_code":"${code}","claim_code":"${code}"}`,
  `{"v":1,"device_id":"${id}","claim_code":"${code}"}${' '.repeat(1024)}`,
])('rejects an invalid device QR', (raw) => expect(parseClaimQR(raw)).toBeNull());
