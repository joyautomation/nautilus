import sys, collections, re
d = open(sys.argv[1],'rb').read()
def leb(p):
    r=s=0
    while True:
        b=d[p]; p+=1; r|=(b&0x7f)<<s; s+=7
        if b<0x80: return r,p
p=8; nimp=0; bodies=[]; names={}; datasz=0
while p < len(d):
    sid=d[p]; p+=1; sz,p=leb(p); end=p+sz
    if sid==2:
        n,q=leb(p)
        for _ in range(n):
            l,q=leb(q); q+=l; l,q=leb(q); q+=l; k=d[q]; q+=1
            if k==0: _,q=leb(q); nimp+=1
            elif k==1: q+=1; f=d[q]; q+=1; _,q=leb(q); q = leb(q)[1] if f&1 else q
            elif k==2: f=d[q]; q+=1; _,q=leb(q); q = leb(q)[1] if f&1 else q
            elif k==3: q+=2
    elif sid==10:
        n,q=leb(p)
        for _ in range(n):
            l,q2=leb(q); bodies.append(l+(q2-q)); q=q2+l
    elif sid==11: datasz=sz
    elif sid==0:
        l,q=leb(p); nm=d[q:q+l]; q+=l
        if nm==b'name':
            while q<end:
                sub=d[q]; q+=1; ssz,q=leb(q); se=q+ssz
                if sub==1:
                    n,r=leb(q)
                    for _ in range(n):
                        i,r=leb(r); l,r=leb(r); names[i]=d[r:r+l].decode('utf8','replace'); r+=l
                q=se
    p=end
agg=collections.Counter()
for i,b in enumerate(bodies):
    n=names.get(i+nimp,'?')
    n=re.sub(r'\(.*','',n)
    m=re.match(r'((?:[^./]+/)*[^./]+)', n)
    pkg=m.group(1) if m else n
    agg[pkg]+=b
print('code', sum(bodies), 'data', datasz, 'total', len(d))
for k,v in agg.most_common(int(sys.argv[2]) if len(sys.argv)>2 else 30): print(f'{v:10d} {k}')
