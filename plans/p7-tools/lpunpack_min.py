"""Минимальный lpunpack: распаковывает разделы из raw super.img (метаданные LP v10).
lpunpack_min.py <super.raw> <outdir> [раздел...]"""
import os, struct, sys

src, outdir, want = sys.argv[1], sys.argv[2], set(sys.argv[3:])
os.makedirs(outdir, exist_ok=True)
with open(src, "rb") as f:
    f.seek(4096)
    geo = f.read(4096)
    assert struct.unpack_from("<I", geo, 0)[0] == 0x616C4467, "нет LP geometry"
    meta_max, = struct.unpack_from("<I", geo, 40)
    f.seek(4096 * 3)  # первая копия метаданных: 4096 (резерв) + 2*4096 (geometry)
    mh = f.read(meta_max)
    assert struct.unpack_from("<I", mh, 0)[0] == 0x414C5030, "нет LP metadata"
    hsize, = struct.unpack_from("<I", mh, 8)
    # таблицы: partitions, extents, groups, block_devices — описатели (offset,num,entry_size)
    tabs = [struct.unpack_from("<III", mh, 80 + 12 * i) for i in range(4)]
    base = hsize
    def table(i):
        off, num, esz = tabs[i]
        return [mh[base + off + k * esz: base + off + (k + 1) * esz] for k in range(num)]
    parts, exts = table(0), table(1)
    for p in parts:
        name = p[:36].split(b"\0")[0].decode()
        first, num = struct.unpack_from("<II", p, 40)
        size = sum(struct.unpack_from("<Q", exts[first + j], 0)[0] for j in range(num)) * 512
        print(f"{name:24} {size:>12}")
        if want and name not in want:
            continue
        with open(os.path.join(outdir, name + ".img"), "wb") as w:
            for j in range(num):
                nsec, ttype, tdata, _ = struct.unpack_from("<QIQI", exts[first + j], 0)
                if ttype != 0:
                    w.write(b"\0" * nsec * 512)
                    continue
                f.seek(tdata * 512)
                left = nsec * 512
                while left:
                    c = f.read(min(left, 1 << 22)); w.write(c); left -= len(c)
