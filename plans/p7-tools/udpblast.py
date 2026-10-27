"""Шлёт UDP-пакеты с равным шагом: udpblast.py <ip> <port> <мбит/с> <секунды> [размер]."""
import socket, sys, time

ip, port, mbps, secs = sys.argv[1], int(sys.argv[2]), float(sys.argv[3]), float(sys.argv[4])
size = int(sys.argv[5]) if len(sys.argv) > 5 else 1400
pps = mbps * 1e6 / 8 / size
step = 1.0 / pps
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
payload = b"x" * size
sent = 0
t0 = time.perf_counter()
end = t0 + secs
nxt = t0
while True:
    now = time.perf_counter()
    if now >= end:
        break
    if now < nxt:
        if nxt - now > 0.002:
            time.sleep(0.001)
        continue
    s.sendto(payload, (ip, port))
    sent += 1
    nxt += step
print(f"sent={sent} pps={pps:.0f} size={size}")
