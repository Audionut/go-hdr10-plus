# MIT licensed with this repository. Offline syntax fixture generator; Go tests
# never invoke Python or a video tool. VCL entropy bytes are arbitrary.
from pathlib import Path

root = Path(__file__).resolve().parent

def ue(value):
    bits = bin(value + 1)[2:]
    return '0' * (len(bits) - 1) + bits

def pack(bits):
    bits += '1'
    bits += '0' * (-len(bits) % 8)
    return int(bits, 2).to_bytes(len(bits) // 8, 'big')

def nal(kind, rbsp):
    out, zeros = bytearray([kind << 1, 1]), 0
    for value in rbsp:
        if zeros == 2 and value <= 3:
            out.append(3)
            zeros = 0
        out.append(value)
        zeros = zeros + 1 if value == 0 else 0
    return b'\x00\x00\x01' + out

def sei(name):
    payload = root.joinpath(name + '.t35').read_bytes()
    return nal(39, bytes([4, len(payload)]) + payload + b'\x80')

ptl = '000' + format(2, '05b') + format(1 << 29, '032b') + '1001' + '0' * 44 + format(120, '08b')
vps = format(2, '04b') + '11' + '000000' + '000' + '1' + '1' * 16 + ptl + '1' + ue(3) + ue(2) + ue(0) + '000000' + ue(0) + '00'
sps = format(2, '04b') + '0001' + ptl + ue(3) + ue(1) + ue(128) + ue(64) + '0' + ue(2) + ue(2) + ue(4) + '1' + ue(3) + ue(2) + ue(0) + ue(0) + ue(3) + ue(0) + ue(3) + ue(0) + ue(0) + '0000' + ue(0) + '00000'
pps = ue(5) + ue(3) + '11' + '00000' + ue(0) + ue(0) + ue(0) + '000' + ue(0) + ue(0) + '0000000000' + ue(0) + '00'
sets = nal(32, pack(vps)) + nal(33, pack(sps)) + nal(34, pack(pps))

def picture(poc, kind=1, prior=1, slice_type=2, negative=(), positive=()):
    bits = '1' + (str(prior) if kind >= 16 else '') + ue(5) + ue(slice_type) + '1'
    if kind not in (19, 20):
        bits += format(poc, '08b') + '0' + ue(len(negative)) + ue(len(positive))
        previous = 0
        for delta, used in negative:
            bits += ue(previous - delta - 1) + str(int(used))
            previous = delta
        previous = 0
        for delta, used in positive:
            bits += ue(delta - previous - 1) + str(int(used))
            previous = delta
        if slice_type != 2:
            bits += '0' + ('0' if slice_type == 0 else '') + ue(0)
    bits += ue(0)
    return nal(kind, pack(bits) + b'\xff\xff')

root.joinpath('single.hevc').write_bytes(sets + sei('profile-a') + picture(0, 19))
root.joinpath('reordered.hevc').write_bytes(sets + sei('profile-a') + picture(0, 19) + picture(2) + sei('profile-b') + picture(1))
root.joinpath('no-metadata.hevc').write_bytes(sets + picture(0, 19) + picture(2) + picture(1))
for name in ('profile-b', 'profile-na'):
    data = sets + sei('profile-a') + picture(0, 19, prior=0)
    data += sei(name) + picture(2, negative=((-2, False),))
    data += sei('profile-a') + picture(4, negative=((-2, False), (-4, False)))
    root.joinpath('seam-' + name + '.hevc').write_bytes(data)
root.joinpath('seam-leading.hevc').write_bytes(picture(1, slice_type=0, negative=((-1, True),), positive=((1, True),)))
