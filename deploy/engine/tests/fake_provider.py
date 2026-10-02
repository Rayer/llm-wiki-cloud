#!/usr/bin/env python3
"""TEST ONLY command fake. Exercises unmodified production engine/adapters."""
import copy
import json
import os
from pathlib import Path
import sys

root = Path(os.environ['LWC_TEST_STATE'])
s = json.loads(root.read_text())
a = sys.argv[1:]
tool = Path(sys.argv[0]).name
s['calls'].append([tool, *a])
fail = False
out = ''

def flag(name, default=None):
    if name in a:
        return a[a.index(name)+1]
    return next((v.split('=',1)[1] for v in a if v.startswith(name+'=')), default)

if tool == 'sleep':
    pass
elif tool == 'go':
    out = '1.0.0'
elif tool == 'docker':
    if a[0] == 'build' and s.get('fail_build') and s['fail_build'] in flag('-t',''):
        fail = True
elif tool == 'gcloud':
    if a[:2] == ['projects','describe']:
        project=a[2]
        out={'projectId':project,
             'projectNumber':s.get('gcp_project_numbers',{}).get(project,'580854833715')}
    elif a[:3] == ['artifacts','docker','images']:
        image = a[4]
        out = image.split('@')[-1] if '@' in image else 'sha256:'+'a'*64
        if s.get('expired'): fail=True
    elif a[:2] == ['builds','submit']:
        if s.get('fail_build') and s['fail_build'] in ' '.join(a):
            fail=True
        else:
            s['build_counter']=s.get('build_counter',0)+1
            build_id=f'12345678-1234-4234-8234-{s["build_counter"]:012d}'
            project=flag('--project','llm-wiki-cloud')
            location=flag('--region','global')
            if location == 'global':
                location=next((x.split('=',1)[1] for x in a if x.startswith('--region=')),location)
            project_number=s.get('gcp_project_numbers',{}).get(project,'580854833715')
            build={'id':build_id,'projectId':project,'location':location,
                   'name':f'projects/{project_number}/locations/{location}/builds/{build_id}',
                   'status':'QUEUED'}
            s.setdefault('builds',{})[build_id]=build
            out=build
    elif a[:2] == ['builds','describe']:
        build=s.get('builds',{}).get(a[2])
        if not build:
            fail=True
        else:
            build['status']='SUCCESS'
            out=build
    elif a[:2] == ['auth','configure-docker']:
        pass
    elif a[0] == 'run':
        kind, op, name = a[1:4]
        if kind == 'revisions':
            if s.get('unreadable'): fail=True
            else: out=s['revisions'][name]
        elif op == 'describe':
            if s.get('unreadable') or name not in s['resources']: fail=True
            else: out=s['resources'][name]
        elif op == 'replace':
            body=json.loads(Path(name).read_text()); name=body['metadata']['name']
            retained=body['spec']['template']['metadata']['name']
            # Existing named revisions are immutable; replacement must reuse exact spec.
            assert retained in s['revisions']
            assert body['spec']['template']['spec']==s['revisions'][retained]['spec']
            expected={k:v for k,v in s['revisions'][retained]['metadata'].get('annotations',{}).items()
                      if k not in ('run.googleapis.com/operation-id','serving.knative.dev/creator','run.googleapis.com/ingress-status')}
            assert body['spec']['template']['metadata'].get('annotations',{})==expected, 'incorrect retained effective annotations'
            s.setdefault('replacements',[]).append(body)
            if s.get('fail_rollback'): fail=True
            else:
                raw=s['resources'][name]
                raw['status']['latestCreatedRevisionName']=retained
                raw['spec']=body['spec']
                raw['status']['traffic']=body['spec']['traffic']
                out=raw
        elif op == 'update-traffic':
            if s.get('fail_rollback'): fail=True
            else:
                raw=s['resources'][name]
                s.setdefault('traffic_before_cutover',[]).append({
                    'service':name,
                    'traffic':copy.deepcopy(raw['status']['traffic']),
                    'target':flag('--to-revisions'),
                })
                raw['status']['traffic']=[{'revisionName':flag('--to-revisions').split('=')[0], 'percent':100}]
                out=raw
        elif op == 'update':
            raw=s['resources'][name]
            image=flag('--image')
            if kind=='services':
                old=s['revisions'][raw['status']['latestCreatedRevisionName']]
                rev=copy.deepcopy(old)
                revision=name+'-candidate'
                rev['metadata']['name']=revision
                rev['spec']['containers'][0]['image']=image
                rev['status']['imageDigest']=image
                container=rev['spec']['containers'][0]
            else:
                container=raw['spec']['template']['spec']['template']['spec']['containers'][0]
                container['image']=image
            env={v['name']:v for v in container.get('env',[])}
            updates=flag('--update-env-vars','')
            if updates:
                for pair in updates.removeprefix('^|^').split('|'):
                    k,v=pair.split('=',1);env[k]={'name':k,'value':v}
            for k in flag('--remove-env-vars','').split(','):
                env.pop(k,None)
            for pair in flag('--update-secrets','').split(','):
                if pair:
                    k,v=pair.split('=',1);n,key=v.split(':');env[k]={'name':k,'valueFrom':{'secretKeyRef':{'name':n,'key':key}}}
            for k in flag('--remove-secrets','').split(','): env.pop(k,None)
            container['env']=list(env.values())
            if kind=='services' and s.get('secret_alias_fixture'):
                bindings=[]
                for entry in container['env']:
                    if 'valueFrom' in entry:
                        ref=entry['valueFrom']['secretKeyRef']
                        alias='candidate-'+entry['name'].lower().replace('_','-')
                        bindings.append(alias+':projects/'+flag('--project')+'/secrets/'+ref['name'])
                        ref['name']=alias
                rev['metadata']['annotations']={'run.googleapis.com/secrets':','.join(bindings),
                    'autoscaling.knative.dev/maxScale':'7','run.googleapis.com/operation-id':'candidate-controller'}
            if kind=='services':
                s['revisions'][revision]=rev
                raw['status']['latestCreatedRevisionName']=revision
                raw['spec']['template']={'metadata':{'name':revision,'annotations':{k:v for k,v in rev['metadata'].get('annotations',{}).items() if k!='run.googleapis.com/operation-id'}},'spec':copy.deepcopy(rev['spec'])}
            out=raw
            if s.get('partial') == name and not image.endswith('b'*64):
                container['image']='broken@sha256:'+'b'*64
                fail=True
            if s.get('accepted_timeout') == name:
                fail=True
            if s.get('unknown_after') == name:
                s['unreadable']=True;fail=True
            if s.get('fail_rollback') and image.endswith('b'*64): fail=True
elif tool == 'git':
    if a[0]=='ls-remote':
        if s.get('tag'):out=s['tag']+'\t'+a[-2]
    else: fail=True
elif tool == 'gh':
    if any('matching-refs' in v for v in a):
        out=[{'ref':'refs/tags/test-release','object':{'type':'commit','sha':s['tag']}}] if s.get('tag') else []
    elif s.get('tag_fail'): fail=True
    else:
        for v in a:
            if v.startswith('sha='):s['tag']=v[4:]
elif tool == 'curl':
    endpoint=next(x for x in a if x.startswith('https://api.vercel.com'))
    if '/v9/projects/' in endpoint:
        out={'id':'prj_test','name':'llm-wiki-frontend-dev','accountId':'team_test','rootDirectory':'apps/frontend','link':{'org':'Rayer','repo':'llm-wiki-cloud'}}
    elif '/v4/aliases/' in endpoint:
        alias=endpoint.split('/v4/aliases/')[1].split('?')[0]
        out={'projectId':'prj_test','deploymentId':s['aliases'][alias]}
    elif '/v13/deployments/' in endpoint:
        identity=endpoint.split('/v13/deployments/')[1].split('?')[0]
        if identity.endswith('.vercel.app'): identity='dpl_candidate'
        out=s['deployments'][identity]
    elif '/files/outputs?' in endpoint:
        out=s['build_config']
    elif '/v6/deployments?' in endpoint:
        out={'deployments':[dict(v,uid=k) for k,v in s['deployments'].items()]}
    elif '/aliases?' in endpoint:
        deployment=endpoint.split('/v2/deployments/')[1].split('/')[0]
        alias=json.loads(flag('--data'))['alias']
        if s.get('fail_rollback') and deployment=='dpl_prior':fail=True
        else:s['aliases'][alias]=deployment
        out={'alias':alias}
    else:fail=True
elif tool=='npm':pass
elif tool=='vercel':
    if a[0]=='pull':
        p=Path.cwd()/'.vercel';p.mkdir(exist_ok=True);(p/'project.json').write_text('{}')
    elif a[0]=='build':
        p=Path.cwd()/'.vercel/output/static';p.mkdir(parents=True,exist_ok=True)
        s['build_config']={'schema_version':1,'api_url':os.environ['NEXT_PUBLIC_API_URL'],'auth_url':os.environ['NEXT_PUBLIC_AUTH_URL']}
        (p/'build-config.json').write_text(json.dumps(s['build_config']))
    elif a[0]=='deploy':
        assert '--prebuilt' in a
        assert ('--skip-domain' in a) == ('--prod' in a)
        assert '--prod' in a or '--target=preview' in a
        meta={}
        for i,v in enumerate(a):
            if v=='--meta': k,val=a[i+1].split('=',1);meta[k]=val
        s['deployments']['dpl_candidate']={'id':'dpl_candidate','projectId':'prj_test','teamId':'team_test','readyState':'READY','meta':meta,'target':'production' if '--prod' in a else None}
        out='https://candidate.vercel.app'
    else:fail=True
else:
    fail=True
root.write_text(json.dumps(s))
if fail:
    sys.stderr.write('TEST ONLY simulated provider failure\n');sys.exit(1)
if out != '':print(json.dumps(out) if isinstance(out,(dict,list)) else out)
