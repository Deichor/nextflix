"""Announces nextflix.local on the LAN via mDNS, so phones and TVs can open http://nextflix.local."""
import os
import socket
import time

from zeroconf import IPVersion, ServiceInfo, Zeroconf

HOST = os.environ.get("MDNS_HOST", "nextflix")
PORT = int(os.environ.get("MDNS_PORT", "80"))
# The TV app's names; some resolvers only handle single-label .local names, hence both
TV_HOSTS = os.environ.get("MDNS_TV_HOSTS", "tv.nextflix,nextflix-tv").split(",")


def lan_ip() -> str:
    # The address of the interface with the default route (UDP connect sends no packet);
    # set MDNS_IP if that is not the LAN interface (e.g. with a VPN)
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
        s.connect(("1.1.1.1", 9))
        return s.getsockname()[0]


def announce(ip: str) -> Zeroconf:
    zc = Zeroconf(ip_version=IPVersion.V4Only, interfaces=[ip])
    try:
        for i, host in enumerate([HOST] + [h.strip() for h in TV_HOSTS if h.strip()]):
            name = "Nextflix" if i == 0 else f"Nextflix TV {i}"
            zc.register_service(ServiceInfo(
                "_http._tcp.local.",
                f"{name}._http._tcp.local.",
                addresses=[socket.inet_aton(ip)],
                port=PORT,
                server=f"{host}.local.",
            ))
            print(f"announcing {host}.local -> {ip}:{PORT}", flush=True)
    except BaseException:
        stop(zc)
        raise
    return zc


def stop(zc: Zeroconf) -> None:
    try:
        zc.unregister_all_services()
        zc.close()
    except OSError:
        pass


ip, zc = None, None
while True:
    try:
        # Follow a new DHCP address
        now = os.environ.get("MDNS_IP") or lan_ip()
        if now != ip or zc is None:
            if zc is not None:
                stop(zc)
                zc = None
            zc = announce(now)
            ip = now
        time.sleep(60)
    except OSError as e:
        # No network yet or Wi-Fi reconnecting: retry instead of crash-looping, and announce
        # again afterwards in case the interface lost its multicast membership
        print(f"network error, retrying: {e}", flush=True)
        ip = None
        time.sleep(10)
