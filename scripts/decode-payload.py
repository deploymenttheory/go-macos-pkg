#!/usr/bin/env python3
"""Decode the pieces of a flat package that the tests and docs pin.

Stdlib only. Given a .pkg (or an expanded directory), it prints JSON with:
- every cpio header of Payload and Scripts (name, ino, mode, nlink, size,
  data offset), so hard-link encoding can be read off directly;
- every AppleDouble sidecar (``._name``) decoded: entries, ATTR header,
  attribute names/offsets/lengths, and the raw bytes (base64);
- the Bom: header, variables, every path record with its raw bytes, and
  the HLIndex tree with each leaf's key/value blocks and the sub-tree
  those point at.

This is a research and fixture tool: the Go code must not depend on it.
"""
import base64
import gzip
import json
import os
import struct
import subprocess
import sys
import tempfile
import zlib


def read_xar_entry(pkg, name):
    """Return the decoded bytes of one xar entry via the TOC (no tools)."""
    with open(pkg, "rb") as f:
        hdr = f.read(28)
        magic, size, version, tocz, tocu, alg = struct.unpack(">IHHQQI", hdr)
        assert magic == 0x78617221, "not a xar"
        f.seek(size)
        toc = zlib.decompress(f.read(tocz))
        heap = size + tocz
        import xml.etree.ElementTree as ET
        root = ET.fromstring(toc)

        def walk(el, prefix):
            for fe in el.findall("file"):
                nm = fe.findall("name")[-1].text
                path = prefix + nm
                yield path, fe
                yield from walk(fe, path + "/")

        for path, fe in walk(root.find("toc"), ""):
            if path != name:
                continue
            data = fe.find("data")
            off = int(data.findtext("offset"))
            length = int(data.findtext("length"))
            enc = data.find("encoding").get("style")
            f.seek(heap + off)
            raw = f.read(length)
            if enc in ("application/x-gzip", "application/zlib"):
                return zlib.decompress(raw)
            return raw
    raise KeyError(name)


def unwrap_payload(raw):
    """gzip or pbz* -> cpio bytes. pbz* chunks are xz/stored only here."""
    if raw[:3] == b"\x1f\x8b\x08":
        return gzip.decompress(raw)
    if raw[:3] == b"pbz":
        import lzma
        out = bytearray()
        pos = 12
        while pos < len(raw):
            inflated, deflated = struct.unpack(">QQ", raw[pos:pos + 16])
            pos += 16
            chunk = raw[pos:pos + deflated]
            pos += deflated
            out += chunk if inflated == deflated else lzma.decompress(chunk)
        return bytes(out)
    return raw


def cpio_entries(data):
    """Walk odc headers; yield dicts with the header fields and data."""
    pos = 0
    out = []
    while pos + 76 <= len(data):
        h = data[pos:pos + 76]
        if h[:6] != b"070707":
            raise ValueError("not odc at %d: %r" % (pos, h[:6]))
        f = [int(h[6:12], 8), int(h[12:18], 8), int(h[18:24], 8), int(h[24:30], 8),
             int(h[30:36], 8), int(h[36:42], 8), int(h[42:48], 8), int(h[48:59], 8),
             int(h[59:65], 8), int(h[65:76], 8)]
        dev, ino, mode, uid, gid, nlink, rdev, mtime, namesize, filesize = f
        name = data[pos + 76:pos + 76 + namesize - 1].decode("utf-8", "replace")
        body_off = pos + 76 + namesize
        body = data[body_off:body_off + filesize]
        pos = body_off + filesize
        if name == "TRAILER!!!":
            break
        out.append({"name": name, "dev": dev, "ino": ino, "mode": "%o" % mode, "uid": uid, "gid": gid,
                    "nlink": nlink, "mtime": mtime, "size": filesize, "dataOffset": body_off,
                    "header": h.decode("ascii"), "_body": body})
    return out


def decode_appledouble(b):
    """Decode an AppleDouble file with an ATTR block, as macOS writes it."""
    if len(b) < 26 or b[:4] != b"\x00\x05\x16\x07":
        return {"error": "not AppleDouble"}
    magic, version, filler, n = struct.unpack(">II16sH", b[:26])
    d = {"version": "%08x" % version, "filler": filler.decode("latin1"), "numEntries": n, "entries": [], "size": len(b)}
    pos = 26
    for _ in range(n):
        eid, off, ln = struct.unpack(">III", b[pos:pos + 12])
        pos += 12
        d["entries"].append({"id": eid, "offset": off, "length": ln})
    for e in d["entries"]:
        if e["id"] == 9:
            fi = b[e["offset"]:e["offset"] + 32]
            d["finderInfo"] = fi.hex()
            rest = b[e["offset"] + 32:e["offset"] + e["length"]]
            # macOS pads with 2 bytes before the ATTR header.
            i = rest.find(b"ATTR")
            d["padBeforeATTR"] = i
            if i >= 0:
                a = rest[i:]
                (amagic, debug, total, dstart, dlen, r0, r1, r2, flags, nattrs) = struct.unpack(">4sIIIIIIIHH", a[:36])
                d["attr"] = {"headerOffset": e["offset"] + 32 + i, "debugTag": debug, "totalSize": total, "dataStart": dstart,
                             "dataLength": dlen, "reserved": [r0, r1, r2], "flags": flags, "numAttrs": nattrs, "attrs": []}
                p = 36
                for _ in range(nattrs):
                    aoff, alen, aflags, namelen = struct.unpack(">IIHB", a[p:p + 11])
                    name = a[p + 11:p + 11 + namelen].rstrip(b"\x00").decode()
                    entry_len = (11 + namelen + 3) & ~3
                    d["attr"]["attrs"].append({"name": name, "offset": aoff, "length": alen, "flags": aflags,
                                               "nameLen": namelen, "entryOffset": e["offset"] + 32 + i + p, "entryLen": entry_len,
                                               "value": base64.b64encode(b[aoff:aoff + alen]).decode()})
                    p += entry_len
        elif e["id"] == 2:
            d["resourceFork"] = {"offset": e["offset"], "length": e["length"]}
    d["raw"] = base64.b64encode(b).decode()
    return d


class Bom:
    def __init__(self, data):
        self.d = data
        (magic, self.version, self.nblocks, io, il, vo, vl) = struct.unpack(">8sIIIIII", data[:32])
        assert magic == b"BOMStore"
        n = struct.unpack(">I", data[io:io + 4])[0]
        self.blocks = [struct.unpack(">II", data[io + 4 + i * 8:io + 12 + i * 8]) for i in range(n)]
        self.vars = {}
        p = vo + 4
        for _ in range(struct.unpack(">I", data[vo:vo + 4])[0]):
            idx = struct.unpack(">I", data[p:p + 4])[0]
            ln = data[p + 4]
            self.vars[data[p + 5:p + 5 + ln].decode()] = idx
            p += 5 + ln

    def block(self, i):
        a, l = self.blocks[i]
        return self.d[a:a + l]

    def tree(self, i):
        b = self.block(i)
        assert b[:4] == b"tree", b[:4]
        return {"child": struct.unpack(">I", b[8:12])[0], "blockSize": struct.unpack(">I", b[12:16])[0],
                "pathCount": struct.unpack(">I", b[16:20])[0]}

    def paths(self, i):
        b = self.block(i)
        leaf, count, fwd, back = struct.unpack(">HHII", b[:12])
        ents = [struct.unpack(">II", b[12 + k * 8:20 + k * 8]) for k in range(count)]
        return {"isLeaf": leaf, "count": count, "forward": fwd, "backward": back, "entries": ents}

    def leaves(self, t):
        idx = t["child"]
        if idx == 0:
            return []
        p = self.paths(idx)
        while not p["isLeaf"]:
            if not p["entries"]:
                return []
            idx = p["entries"][0][0]
            p = self.paths(idx)
        out = []
        while idx:
            p = self.paths(idx)
            out += p["entries"]
            idx = p["forward"]
        return out

    def path_records(self):
        t = self.tree(self.vars["Paths"])
        recs = []
        byid = {}
        for info_idx, file_idx in self.leaves(t):
            info = self.block(info_idx)
            pid, rec_idx = struct.unpack(">II", info[:8])
            fb = self.block(file_idx)
            parent = struct.unpack(">I", fb[:4])[0]
            name = fb[4:].split(b"\x00")[0].decode("utf-8", "replace")
            rec = self.block(rec_idx)
            r = {"id": pid, "parent": parent, "name": name, "infoBlock": info_idx, "fileBlock": file_idx,
                 "recordBlock": rec_idx, "recordLen": len(rec), "record": rec.hex(),
                 "type": rec[0], "arch": struct.unpack(">H", rec[2:4])[0] if len(rec) >= 4 else None,
                 "mode": "%o" % struct.unpack(">H", rec[4:6])[0] if len(rec) >= 6 else None,
                 "size": struct.unpack(">I", rec[18:22])[0] if len(rec) >= 22 else None,
                 "checksum": struct.unpack(">I", rec[23:27])[0] if len(rec) >= 27 else None}
            recs.append(r)
            byid[pid] = r
        for r in recs:
            parts = []
            cur = r
            while cur:
                parts.append(cur["name"])
                cur = byid.get(cur["parent"])
            r["path"] = "/".join(reversed(parts))
        return recs

    def hlindex(self, recs):
        if "HLIndex" not in self.vars:
            return None
        t = self.tree(self.vars["HLIndex"])
        byrec = {r["recordBlock"]: r for r in recs}
        out = {"tree": t, "leaves": []}
        for val_idx, key_idx in self.leaves(t):
            key = self.block(key_idx)
            val = self.block(val_idx)
            leaf = {"keyBlock": key_idx, "key": key.hex(), "valueBlock": val_idx, "value": val.hex()}
            if len(key) >= 4:
                kb = struct.unpack(">I", key[:4])[0]
                leaf["keyRecordBlock"] = kb
                leaf["path"] = byrec.get(kb, {}).get("path")
            if len(val) >= 4:
                sub = struct.unpack(">I", val[:4])[0]
                try:
                    st = self.tree(sub)
                    sub_leaves = self.leaves(st)
                    leaf["subTree"] = {"block": sub, "tree": st,
                                       "leaves": [{"index0": a, "index1": b, "block0": self.block(a).hex(), "block1": self.block(b).hex()}
                                                  for a, b in sub_leaves]}
                except Exception as e:  # noqa: BLE001
                    leaf["subTree"] = {"block": sub, "error": str(e)}
            out["leaves"].append(leaf)
        return out


def main():
    pkg = sys.argv[1]
    comp = sys.argv[2] if len(sys.argv) > 2 else ""
    prefix = comp + "/" if comp else ""
    result = {"package": os.path.basename(pkg)}
    for entry in ("Payload", "LargeSegmentedPayload", "Scripts"):
        try:
            raw = read_xar_entry(pkg, prefix + entry)
        except KeyError:
            continue
        ents = cpio_entries(unwrap_payload(raw))
        for e in ents:
            base = e["name"].rsplit("/", 1)[-1]
            if base.startswith("._"):
                e["appleDouble"] = decode_appledouble(e["_body"])
            del e["_body"]
        result[entry] = ents
    bom = Bom(read_xar_entry(pkg, prefix + "Bom"))
    recs = bom.path_records()
    result["bom"] = {"version": bom.version, "numberOfBlocks": bom.nblocks, "blockTableEntries": len(bom.blocks),
                     "vars": bom.vars, "records": recs, "hlindex": bom.hlindex(recs)}
    json.dump(result, sys.stdout, indent=1, sort_keys=True)
    print()


if __name__ == "__main__":
    main()
