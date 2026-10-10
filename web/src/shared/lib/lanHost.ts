// DNS rebinding guard, same rules as the Go server (server/internal/platform/httpserver):
// a public domain that resolves to this machine still sends its own name as Host.

/** Bare lowercase host name from a Host header: "[::1]:3008" -> "::1", "Nextflix.local.:80" -> "nextflix.local". */
export function hostName(host: string): string {
  let h = host.trim().toLowerCase();
  if (h.startsWith('[')) {
    const end = h.indexOf(']');
    h = end < 0 ? h.slice(1) : h.slice(1, end);
  } else if (h.indexOf(':') === h.lastIndexOf(':')) {
    h = h.replace(/:\d*$/, '');
  }
  return h.replace(/\.$/, '');
}

function privateIPv4(ip: string): boolean {
  const m = ip.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  if (m.slice(1).some(p => Number(p) > 255)) return false;
  return a === 10 || a === 127 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254);
}

function privateIPv6(ip: string): boolean {
  const addr = ip.split('%')[0]; // zone, e.g. fe80::1%eth0
  if (!/^[0-9a-f:.]+$/.test(addr) || !addr.includes(':')) return false;
  if (addr === '::1') return true;
  const mapped = addr.match(/^::ffff:(\d+\.\d+\.\d+\.\d+)$/);
  if (mapped) return privateIPv4(mapped[1]);
  const first = addr.startsWith('::') ? 0 : parseInt(addr.split(':')[0], 16);
  // fc00::/7 (unique local) and fe80::/10 (link-local)
  return (first & 0xfe00) === 0xfc00 || (first & 0xffc0) === 0xfe80;
}

/** True when a bare host name can only point at this machine or the LAN. */
export function isLANHost(host: string): boolean {
  const h = host.toLowerCase().replace(/\.$/, '');
  if (!h) return false;
  if (h === 'localhost' || h.endsWith('.localhost') || h.endsWith('.local')) return true;
  return privateIPv4(h) || privateIPv6(h);
}

/** Whether a request's Host header is a LAN name or one of the configured extra hosts. */
export function hostAllowed(hostHeader: string | null | undefined, extraHosts: readonly string[]): boolean {
  if (!hostHeader) return false;
  const h = hostName(hostHeader);
  return isLANHost(h) || (h !== '' && extraHosts.some(e => hostName(e) === h));
}
