import { ServerOff, WifiOff } from 'lucide-react';
import { useT } from '../lib/i18n';
import { useNetState } from '../lib/netStatus';

/** A strip on the screens while the server has no internet (or cannot be reached). */
export default function NetBanner() {
  const t = useT();
  const net = useNetState();
  if (net === 'online') return null;
  return (
    <div className="net-banner" role="status">
      {net === 'offline' ? <WifiOff className="net-banner-icon" /> : <ServerOff className="net-banner-icon" />}
      {t(net === 'offline' ? 'net.bannerOffline' : 'net.bannerUnreachable')}
    </div>
  );
}
