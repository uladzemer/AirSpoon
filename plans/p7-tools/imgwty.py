"""Список и извлечение файлов из незашифрованного Allwinner IMAGEWTY v3.
imgwty.py <img> [имя_файла_для_извлечения> <куда>]"""
import struct, sys

path = sys.argv[1]
with open(path, "rb") as f:
    hdr = f.read(1024)
    assert hdr[:8] == b"IMAGEWTY", "не IMAGEWTY"
    ver = struct.unpack_from("<I", hdr, 8)[0]
    # v3: num_files лежит после pid/vid/hwid/fwid/val1/val1024
    num = struct.unpack_from("<I", hdr, 0x3C)[0] if ver == 0x300 else struct.unpack_from("<I", hdr, 0x38)[0]
    files = []
    for i in range(num):
        f.seek(1024 + i * 1024)
        fh = f.read(1024)
        maintype = fh[8:16].decode("latin1").strip("\0 ")
        subtype = fh[16:32].decode("latin1").strip("\0 ")
        name = fh[36:36 + 256].split(b"\0")[0].decode("latin1")
        stored, _, orig, _, off = struct.unpack_from("<IIIII", fh, 36 + 256)
        files.append((name, maintype, subtype, stored, orig, off))
    if len(sys.argv) == 2:
        print(f"версия 0x{ver:x}, файлов {num}")
        for n, m, s, st, o, off in files:
            print(f"{o:>12} {off:>12} {m:8} {s:16} {n}")
    else:
        want, out = sys.argv[2], sys.argv[3]
        for n, m, s, st, o, off in files:
            if n == want or s == want:
                f.seek(off)
                left = o
                with open(out, "wb") as w:
                    while left:
                        chunk = f.read(min(left, 1 << 22))
                        w.write(chunk)
                        left -= len(chunk)
                print("извлечено", n, o)
                break
        else:
            sys.exit("не найдено")
