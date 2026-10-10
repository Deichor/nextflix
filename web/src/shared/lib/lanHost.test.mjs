// Run in a node:24 container: node --test src/shared/lib/lanHost.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { hostAllowed, hostName, isLANHost } from './lanHost.ts';

test('host names lose port, brackets and trailing dot', () => {
  assert.equal(hostName('Nextflix.Local.:80'), 'nextflix.local');
  assert.equal(hostName('192.168.1.5:3008'), '192.168.1.5');
  assert.equal(hostName('[::1]:3008'), '::1');
  assert.equal(hostName('fe80::1'), 'fe80::1');
  assert.equal(hostName('localhost'), 'localhost');
});

test('LAN names and private IPs match the Go server', () => {
  const want = {
    localhost: true,
    'app.localhost': true,
    'nextflix.local': true,
    'NEXTFLIX.LOCAL.': true,
    'tv.nextflix.local': true,
    '127.0.0.1': true,
    '192.168.1.5': true,
    '10.0.0.2': true,
    '172.20.0.3': true,
    '169.254.1.1': true,
    '::1': true,
    'fd00::1': true,
    'fe80::1%eth0': true,
    '::ffff:192.168.1.5': true,
    '': false,
    '8.8.8.8': false,
    '100.64.0.1': false,
    '172.32.0.1': false,
    '192.168.1.256': false,
    'evil.com': false,
    'local.evil.com': false,
    'localhost.evil.com': false,
    '2001:db8::1': false,
    server: false,
  };
  for (const [host, ok] of Object.entries(want)) assert.equal(isLANHost(host), ok, host);
});

test('a rebinding domain is refused, LAN and configured hosts pass', () => {
  const extra = ['mypc', 'NAS.lan.'];
  for (const h of ['nextflix.local', 'nextflix.local:80', 'tv.nextflix.local', '192.168.1.5:3008', '127.0.0.1:3008', 'localhost:3008', 'mypc:3008', 'nas.lan']) {
    assert.ok(hostAllowed(h, extra), h);
  }
  for (const h of ['rebind.evil.com', 'rebind.evil.com:3008', '8.8.8.8', ':80', '', null, undefined]) {
    assert.ok(!hostAllowed(h, extra), String(h));
  }
  assert.ok(!hostAllowed(':80', ['']));
});
