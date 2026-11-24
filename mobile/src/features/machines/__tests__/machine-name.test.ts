import { parseMachineName } from '../machine-name';

it('trims surrounding space and counts Unicode code points', () => {
  expect(parseMachineName('  ' + '𐐀'.repeat(100) + '  ')).toBe('𐐀'.repeat(100));
});

it.each([' ', '𐐀'.repeat(101), 'Fan\n1', 'Fan\u0085B', 'Fan\u0000', 'Fan\uD800'])(
  'rejects an invalid machine name',
  (name) => expect(parseMachineName(name)).toBeNull(),
);
