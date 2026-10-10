import { NextResponse, type NextRequest } from 'next/server';
import { hostAllowed } from '@/shared/lib/lanHost';

// Extra host names the web app may be reached by (deploy/config/web.env), like the server's ALLOWED_HOSTS
const extraHosts = (process.env.ALLOWED_HOSTS || '').split(',').map(h => h.trim()).filter(Boolean);

// DNS rebinding guard: a page on a public domain that resolves to this machine would otherwise
// read /api through the rewrites as same-origin (the server only sees Host "server").
export function proxy(request: NextRequest) {
  if (hostAllowed(request.headers.get('host'), extraHosts)) return NextResponse.next();
  return NextResponse.json({ error: 'host not allowed' }, { status: 403 });
}
