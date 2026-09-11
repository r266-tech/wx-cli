#!/usr/bin/env python3
"""Install/update/rollback/uninstall only inside a new isolated HOME."""
import argparse
import json
import os
import subprocess
import tempfile
import time
from pathlib import Path
from release_lib import sha256

ap=argparse.ArgumentParser();ap.add_argument('--package',required=True);ap.add_argument('--rollback-package',required=True);ap.add_argument('--receipt',required=True)
a=ap.parse_args();package=Path(a.package).resolve();old=Path(a.rollback_package).resolve()
receipt=json.loads(Path(a.receipt).read_text())
base=Path(tempfile.mkdtemp(prefix='wx-cli-install-rehearsal-'));base.chmod(0o700)
home=base/'home';home.mkdir(mode=0o700)
install=home/'.local/share/wechat-cli';bin_dir=home/'.local/bin';logs=home/'logs'
env=os.environ.copy()
for k in list(env):
 if k.startswith(('WECHAT_CLI_','WX_MCP_')):env.pop(k)
env.update(HOME=str(home),WECHAT_CLI_HOME=str(home),WECHAT_CLI_CONFIG=str(home/'.config/wxcli/config.json'),WECHAT_CLI_STATE_DIR=str(home/'.wechat-cli'),WECHAT_CLI_LOG_DIR=str(logs),WECHAT_CLI_INSTALL_DIR=str(install),WECHAT_CLI_BIN_DIR=str(bin_dir))

def installer(pkg, flags):
    p=subprocess.run(['zsh',str(pkg/'install.sh'),*flags,'--yes','--json','--install-dir',str(install),'--bin-dir',str(bin_dir)],cwd=pkg,env=env,text=True,capture_output=True,timeout=120)
    d=json.loads(p.stdout)
    if p.returncode or not d['ok']:raise ValueError('installer failed')
    return d

def identity(pkg, exact_bytes=True):
    actual=json.loads(subprocess.check_output([str(bin_dir/'wechat-cli'),'--version'],env=env))['data']
    expected=json.loads(subprocess.check_output([str(pkg/'wechat-cli'),'--version'],env=env))['data']
    if actual['version']!=expected['version']:raise ValueError('installed version mismatch')
    if exact_bytes and sha256(install/'wechat-cli')!=sha256(pkg/'wechat-cli'):raise ValueError('installer changed signed bytes')
    if (bin_dir/'wechat-cli').resolve()!=(install/'wechat-cli').resolve():raise ValueError('shim mismatch')

def check(name, fn):
    t=time.monotonic()
    try:fn();status='passed'
    except Exception as exc:
        print(name,type(exc).__name__,str(exc),file=__import__('sys').stderr);status='failed'
    receipt['checks'][name]={'status':status,'duration_ms':round((time.monotonic()-t)*1000)}

def first():
    installer(package,['--dry-run']);installer(package,[]);identity(package)

def update():
    installer(package,['--update']);identity(package)

def rollback():
    installer(old,['--update']);identity(old,False);installer(package,['--update']);identity(package)

def uninstall():
    preserved=home/'.config/wxcli';preserved.mkdir(parents=True,exist_ok=True)
    marker=preserved/'preserve-test';marker.write_text('fixture')
    installer(package,['--uninstall','--dry-run']);installer(package,['--uninstall'])
    if (bin_dir/'wechat-cli').exists() or (install/'wechat-cli').exists():raise ValueError('managed binary or shim remains')
    if not marker.exists():raise ValueError('uninstall removed unrelated config')

check('install',first);check('update',update);check('rollback',rollback);check('uninstall',uninstall)
# A local installer rehearsal is not a hosted endpoint update or a clean machine.
receipt['checks']['clean_machine']={'status':'not_run','warning_codes':['separate_mac_required']}
receipt['checks']['release_endpoint_install']={'status':'not_run','warning_codes':['signed_release_required']}
receipt['passed']=all(c['status']=='passed' for c in receipt['checks'].values())
path=Path(a.receipt);tmp=path.with_suffix('.tmp')
fd=os.open(tmp,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
with os.fdopen(fd,'w') as f:json.dump(receipt,f,indent=2)
tmp.replace(path)
print(json.dumps({'receipt':str(path),'passed':receipt['passed'],'checks':receipt['checks']}))
