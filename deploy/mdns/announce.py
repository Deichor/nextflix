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
    # The address of the interface that routes to the LAN gateway (no packet is sent)
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
        s.connect(("192.168.1.1", 9))
        return s.getsockname()[0]


def announce(ip: str) -> Zeroconf:
    zc = Zeroconf(ip_version=IPVersion.V4Only, interfaces=[ip])
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
    return zc


ip = os.environ.get("MDNS_IP") or lan_ip()
zc = announce(ip)
while True:
    time.sleep(60)
    # Follow a new DHCP address
    now = os.environ.get("MDNS_IP") or lan_ip()
    if now != ip:
        zc.unregister_all_services()
        zc.close()
        ip = now
        zc = announce(ip)
