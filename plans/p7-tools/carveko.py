"""Вырезает модули ядра .ko по имени из образа: carveko.py <img> <outdir> name1 [name2...]"""
import mmap, os, re, struct, sys

img, outdir, names = sys.argv[1], sys.argv[2], sys.argv[3:]
os.makedirs(outdir, exist_ok=True)
with open(img, "rb") as f:
    m = mmap.mmap(f.fileno(), 0, access=mmap.ACCESS_READ)
    for name in names:
        pat = re.compile(rb"name=" + re.escape(name.encode()) + rb"\x00")
        for k, hit in enumerate(pat.finditer(m)):
            pos = hit.start()
            start = m.rfind(b"\x7fELF\x02\x01\x01", max(0, pos - 8 * 1024 * 1024), pos)
            if start < 0:
                print(name, "ELF не найден у", pos)
                continue
            e_shoff, = struct.unpack_from("<Q", m, start + 0x28)
            e_shentsize, e_shnum = struct.unpack_from("<HH", m, start + 0x3A)
            size = e_shoff + e_shentsize * e_shnum
            if size <= 0 or size > 16 * 1024 * 1024 or pos - start > size:
                print(name, "странный размер", size)
                continue
            data = m[start:start + size]
            vm = re.search(rb"vermagic=[^\x00]*", data)
            ver = re.search(rb"2\.0\.2\d{5}\.[0-9a-f]{7}", data)
            out = os.path.join(outdir, f"{name}_{k}.ko")
            open(out, "wb").write(data)
            print(out, size, vm.group().decode() if vm else "-", ver.group().decode() if ver else "-")
