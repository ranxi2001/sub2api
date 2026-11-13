import hashlib,json,os,pathlib,re,subprocess,tarfile
R='ranxi2001/sub2api'
TAG='v2.21.2'
SHA='dbed7697aa56e76c6eb48c4914fa441496b67bdd'
p=pathlib.Path(os.environ['RUNNER_TEMP'])/'published-download-verification'
p.mkdir()
def api(path):
    return json.loads(subprocess.check_output(['gh','api',f'repos/{R}/'+path],text=True))
release=api(f'releases/tags/{TAG}')
assert release['tag_name']==TAG and not release['draft'] and not release['prerelease']
ref=api(f'git/ref/tags/{TAG}')
assert ref['object']['type']=='tag'
tag=api('git/tags/'+ref['object']['sha'])
assert tag['object']=={'sha':SHA,'type':'commit','url':f'https://api.github.com/repos/{R}/git/commits/{SHA}'}
assets=release['assets']
expected={'checksums.txt','sub2api_2.21.2_linux_amd64.tar.gz','sub2api_2.21.2_linux_arm64.tar.gz','sub2api_2.21.2_windows_amd64.zip','sub2api_2.21.2_darwin_amd64.tar.gz','sub2api_2.21.2_darwin_arm64.tar.gz','sub2api-reauth_2.21.2_linux_amd64.tar.gz','sub2api-reauth_2.21.2_linux_arm64.tar.gz'}
assert {a['name'] for a in assets}==expected
assert all(a['state']=='uploaded' and a['size']>0 for a in assets)
archive='sub2api_2.21.2_linux_amd64.tar.gz'
subprocess.run(['gh','release','download',TAG,'--repo',R,'--pattern','checksums.txt','--pattern',archive,'--dir',str(p)],check=True)
manifest={line.split()[1].lstrip('*'):line.split()[0] for line in (p/'checksums.txt').read_text().splitlines()}
digest=hashlib.sha256((p/archive).read_bytes()).hexdigest()
assert manifest[archive]==digest
asset=next(a for a in assets if a['name']==archive)
if asset.get('digest'):assert asset['digest']=='sha256:'+digest
binary=p/'sub2api'
with tarfile.open(p/archive,'r:gz') as t:
    selected=[m for m in t.getmembers() if m.name in ('sub2api','./sub2api')]
    assert len(selected)==1 and selected[0].isfile()
    binary.write_bytes(t.extractfile(selected[0]).read())
binary.chmod(0o755)
result=subprocess.run([str(binary),'-version'],capture_output=True,text=True,timeout=30,check=True)
output=result.stdout+result.stderr
assert re.search(r'Sub2API (?:v)?2\.21\.2 \(commit: '+SHA+r', built: ',output),output
(p/'version.txt').write_text(output)
report={'release':release['html_url'],'tag':TAG,'annotated_tag_object':ref['object']['sha'],'target':SHA,'assets':[{k:a.get(k) for k in ('name','size','digest','state')} for a in assets],'downloaded_archive':archive,'sha256':digest,'version_command':'sub2api -version','version_output':output,'executed_on':'GitHub-hosted ubuntu-24.04; no server or deployment'}
(p/'verification.json').write_text(json.dumps(report,indent=2))
print(json.dumps(report,indent=2))
