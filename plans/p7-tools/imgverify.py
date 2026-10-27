"""Сверяет контрольные суммы V*.fex в Allwinner IMAGEWTY (сумма 32-битных слов)."""
import mmap, struct, sys

path = sys.argv[1]
with open(path, "rb") as f:
    m = mmap.mmap(f.fileno(), 0, access=mmap.ACCESS_READ)
    num = struct.unpack_from("<I", m, 0x3C)[0]
    files = {}
    for i in range(num):
        fh = 1024 + i * 1024
        name = m[fh + 36: fh + 36 + 256].split(b"\0")[0].decode("latin1").split("\\")[-1]
        _, _, orig, _, off = struct.unpack_from("<IIIII", m, fh + 36 + 256)
        files[name] = (off, orig)
    bad = 0
    for name, (off, size) in files.items():
        v = files.get("V" + name)
        if not v:
            continue
        want = struct.unpack_from("<I", m, v[0])[0]
        total = 0
        step = 1 << 24
        for p in range(off, off + size - size % 4, step):
            n = min(step, off + size - size % 4 - p) // 4
            total = (total + sum(struct.unpack_from(f"<{n}I", m, p))) & 0xFFFFFFFF
        if size % 4:
            tail = m[off + size - size % 4: off + size] + b"\0" * (4 - size % 4)
            total = (total + struct.unpack("<I", tail)[0]) & 0xFFFFFFFF
        ok = total == want
        bad += not ok
        print(f"{'OK ' if ok else 'BAD'} {name:22} {size:>12} 0x{total:08x} / 0x{want:08x}")
    print("ошибок:", bad)
