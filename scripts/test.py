import socket, struct, hashlib, time
def dsha(d): return hashlib.sha256(hashlib.sha256(d).digest()).digest()
def vs(s):
    b=s.encode()
    return bytes([len(b)])+b
magic=b'\xfa\x1c\xc0\x02'
cmd=b'version\x00\x00\x00\x00\x00'
addr_recv=vs('45.32.196.26:19334')
addr_from=vs('0.0.0.0:0')
payload=struct.pack('<IQq',1,1,int(time.time()))+addr_recv+addr_from+struct.pack('<Q',12345)+vs('/probe/')+struct.pack('<I',0)
cksum=dsha(payload)[:4]
hdr=magic+cmd+struct.pack('<I',len(payload))+cksum
s=socket.socket()
s.settimeout(10)
s.connect(('45.32.196.26',19334))
s.sendall(hdr+payload)
data=b''
while len(data)<24:
    data+=s.recv(4096)
rlen=struct.unpack('<I',data[16:20])[0]
while len(data)<24+rlen:
    data+=s.recv(4096)
p=data[24:]
ver=struct.unpack('<I',p[0:4])[0]
ts=struct.unpack('<q',p[12:20])[0]
off=20
slen=p[off]; off+=1+slen
slen2=p[off]; off+=1+slen2
off+=8
ualen=p[off]; off+=1
ua=p[off:off+ualen].decode(); off+=ualen
height=struct.unpack('<I',p[off:off+4])[0]
print(f'Seed: 45.32.196.26:19334  Height: {height}  UserAgent: {ua}  Protocol: {ver}')
s.close()
