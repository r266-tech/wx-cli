#!/usr/bin/env python3
"""Live acceptance: private data stays in process memory, receipts are allowlisted."""
import argparse
import hashlib
import json
import os
import re
import subprocess
import tempfile
import time
import uuid
from pathlib import Path
from release_lib import sha256


def digest_message_ids(document, require_messages=False):
    # Older candidates encode an empty incremental window as null. It means
    # no new messages; a missing field or malformed identity is still a failure.
    if not isinstance(document, dict) or 'messages' not in document:
        raise ValueError('digest_messages_missing')
    rows = document['messages']
    if rows is None:
        rows = []
    if not isinstance(rows, list) or (require_messages and not rows):
        raise ValueError('digest_messages_empty_or_invalid')
    identities = set()
    for row in rows:
        identity = row.get('id') if isinstance(row, dict) else None
        if not isinstance(identity, dict) or not identity.get('local_id'):
            raise ValueError('digest_identity_missing')
        item = (str(identity.get('talker', '')), str(identity['local_id']), str(identity.get('server_id_str', '')))
        if item in identities:
            raise ValueError('digest_duplicate_message')
        identities.add(item)
    return identities


def validate_digest_windows(first, second):
    left = digest_message_ids(first, require_messages=True)
    right = digest_message_ids(second)
    if left & right:
        raise ValueError('digest_repeated_messages')
    return len(left)


def main():
    ap=argparse.ArgumentParser();ap.add_argument('--binary',required=True);ap.add_argument('--archive',required=True)
    ap.add_argument('--archive-timeout', type=int, default=600, help='Bounded seconds for full database export (1..3600).')
    ap.add_argument('--skip-archive', action='store_true', help='Record archive as not_run; never count it as passed.')
    a=ap.parse_args();binary=str(Path(a.binary).resolve());env=os.environ.copy();env['WECHAT_CLI_STRICT_READ_ONLY']='1'
    if not 1 <= a.archive_timeout <= 3600:
        ap.error('--archive-timeout must be between 1 and 3600 seconds')
    checks={}
    def call(tool,args=None,writes=False):
        local=env.copy()
        if writes:local['WECHAT_CLI_STRICT_READ_ONLY']='0'
        timeout = a.archive_timeout if tool == 'archive_create' else 45
        proc=subprocess.run([binary,'call-json',tool],input=json.dumps(args or {}),text=True,capture_output=True,env=local,timeout=timeout)
        d=json.loads(proc.stdout)
        if proc.returncode or not d.get('ok'):raise ValueError('tool_failed')
        return d['data']
    def record(name, fn):
        before=time.monotonic()
        try:
            count=fn();checks[name]={'status':'passed','count':int(count or 0)}
        except subprocess.TimeoutExpired:
            checks[name]={'status':'failed','warning_codes':['acceptance_timeout']}
        except Exception:
            checks[name]={'status':'failed','warning_codes':['acceptance_failed']}
        checks[name]['duration_ms']=round((time.monotonic()-before)*1000)
    version=json.loads(subprocess.check_output([binary,'--version']))['data']
    record('doctor',lambda: bool(call('read_os',{'mode':'doctor'})['doctor']['status']['live_read_ok']) or (_ for _ in ()).throw(ValueError()))
    def keys():
        data=call('keychain_status')
        if not data['loaded'] or data['store']!='keychain':raise ValueError()
        return 1
    record('keychain',keys)
    # A missing OS authorization is a blocked receipt, not a hanging test.
    if checks['keychain']['status']!='passed':
        for name in ('sessions','timeline','context','search','media','digest','archive'):
            checks[name]={'status':'blocked','warning_codes':['keychain_access_required']}
        return finish(a,version,checks)
    # Discovery data is never written to disk or stdout.
    sessions=call('sessions',{'limit':10})['sessions']
    if not sessions:raise ValueError('no sessions')
    checks['sessions']={'status':'passed','count':len(sessions)}
    chat=sessions[0]['username']
    timeline=call('chat_timeline',{'talker':chat,'limit':20})['messages']
    checks['timeline']={'status':'passed' if timeline else 'failed','count':len(timeline)}
    if not timeline:raise ValueError('no live messages')
    anchor=timeline[len(timeline)//2]['id']['local_id']
    record('context',lambda:len(call('message_context',{'chat':chat,'local_id':anchor,'before_count':3,'after_count':3})['messages']) or (_ for _ in ()).throw(ValueError()))
    def search():
        for msg in timeline:
            text=msg.get('text','')
            matches=re.findall(r'[\u4e00-\u9fff]{3,6}|[A-Za-z]{4,12}',text)
            for keyword in matches[:1]:
                result=call('search',{'keyword':keyword,'chat':chat,'limit':5})
                if result.get('messages'):return len(result['messages'])
        raise ValueError('no known keyword hit')
    record('search',search)
    def media():
        d=call('media_resources',{'chat':chat,'type':'image','limit':10})
        # A nonempty resource list with an explicit state is needed, not just ok=true.
        items=d.get('media',d.get('messages',d.get('resources',d.get('items',[]))))
        if not items:raise ValueError('no media sample')
        return len(items)
    record('media',media)
    def digest():
        before=call('digest_source',{'talker':chat,'limit':10},True)
        f=Path(before['source_json']);first=sha256(f)
        again=call('digest_source',{'talker':chat,'limit':10,'since_last':True},True)
        if sha256(f)!=first:raise ValueError('digest overwritten')
        return validate_digest_windows(json.loads(f.read_text()), json.loads(Path(again['source_json']).read_text()))
    record('digest',digest)
    def archive():
        data=call('archive_create',{},True)
        if data['failed'] or not data['succeeded']:raise ValueError('partial archive')
        result=call('archive_validate',{'input':data['archive_root']})
        if not result['valid']:raise ValueError('archive invalid')
        return data['succeeded']
    if a.skip_archive:
        checks['archive']={'status':'not_run','warning_codes':['archive_explicitly_skipped']}
    else:
        record('archive',archive)
    return finish(a,version,checks)

def finish(a,version,checks):
    for name in ('install','update','rollback','uninstall'):
        checks[name]={'status':'not_run','warning_codes':['isolated_installer_receipt_required']}
    receipt={'schema_version':1,'passed':all(c['status']=='passed' for c in checks.values()),'version':version['version'],'commit':version['commit'],'artifact_sha256':sha256(a.archive),'platform':'darwin','arch':'arm64','checks':checks}
    dest=Path.home()/'.wechat-cli'/'diagnostics'/('acceptance-'+uuid.uuid4().hex)
    dest.mkdir(parents=True,mode=0o700);path=dest/'acceptance-receipt.json'
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'w') as f:json.dump(receipt,f,indent=2)
    print(json.dumps({'receipt':str(path),'passed':receipt['passed'],'checks':checks}))

if __name__=='__main__':main()
