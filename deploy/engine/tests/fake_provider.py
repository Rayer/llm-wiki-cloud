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
failure_message = 'TEST ONLY simulated provider failure\n'
out = ''
binary_out = None

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
    if a[:2] == ['storage', 'cat']:
        uri=a[2]
        if s.get('pipeline_config_read_denied'):
            fail=True
            failure_message='ERROR: (gcloud.storage.cat) 403 Permission denied.\n'
        elif s.get('pipeline_config_read_error'):
            fail=True
            failure_message=s['pipeline_config_read_error']
        elif uri not in s.get('pipeline_configs', {}):
            fail=True
            failure_message=(
                'ERROR: (gcloud.storage.cat) One or more URLs matched no objects.\n'
                if s.get('pipeline_config_absence_legacy') else
                f'ERROR: (gcloud.storage.cat) The following URLs matched no objects or files:\n  {uri}\n')
        else:
            binary_out=s['pipeline_configs'][uri].encode()
    elif a[:2] == ['storage', 'cp']:
        source=Path(a[3])
        uri=a[4]
        s.setdefault('pipeline_configs', {})[uri]=source.read_bytes().decode()
    elif a[:2] == ['storage', 'rm']:
        uri=a[-1]
        if uri not in s.get('pipeline_configs', {}):
            fail=True
            failure_message=(
                'ERROR: (gcloud.storage.rm) One or more URLs matched no objects.\n'
                if s.get('pipeline_config_absence_legacy') else
                f'ERROR: (gcloud.storage.rm) The following URLs matched no objects or files:\n  {uri}\n')
        else:
            del s['pipeline_configs'][uri]
    elif a[:2] == ['projects','describe']:
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
                if image:
                    container['image']=image
            env={v['name']:v for v in container.get('env',[])}
            before_secret_names={entry.get('valueFrom',{}).get('secretKeyRef',{}).get('name')
                                 for entry in container.get('env',[]) if 'valueFrom' in entry}
            updates=flag('--update-env-vars','')
            if updates:
                for pair in updates.removeprefix('^|^').split('|'):
                    k,v=pair.split('=',1);env[k]={'name':k,'value':v}
            for k in flag('--remove-env-vars','').split(','):
                env.pop(k,None)
            for pair in flag('--update-secrets','').split(','):
                if pair:
                    k,v=pair.split('=',1);n,key=v.split(':')
                    if k.startswith('/'):
                        directory,file_name=k.rsplit('/',1)
                        mount_path=directory
                        mounts=container.setdefault('volumeMounts',[])
                        mounts[:]=[mount for mount in mounts if mount.get('mountPath')!=mount_path]
                        mounts.append({'name':n,'mountPath':mount_path,'readOnly':True})
                        volumes=rev['spec'].setdefault('volumes',[])
                        volumes[:]=[volume for volume in volumes if volume.get('name')!=n]
                        volumes.append({'name':n,'secret':{'secretName':n,'items':[{'key':key,'path':file_name,'mode':292}]}})
                        account=flag('--service-account')
                        if account:
                            rev['spec']['serviceAccountName']=account
                        bindings={}
                        for binding in rev['metadata'].get('annotations',{}).get('run.googleapis.com/secrets','').split(','):
                            if ':' in binding:
                                alias,target=binding.split(':',1)
                                if alias not in before_secret_names or alias in {entry.get('valueFrom',{}).get('secretKeyRef',{}).get('name') for entry in env.values() if 'valueFrom' in entry}:
                                    bindings[alias]=target
                        project=flag('--project','llm-wiki-cloud')
                        bindings[n]='projects/'+project+'/secrets/'+n
                        rev['metadata'].setdefault('annotations',{})['run.googleapis.com/secrets']=','.join(
                            alias+':'+target for alias,target in bindings.items())
                    else:
                        env[k]={'name':k,'valueFrom':{'secretKeyRef':{'name':n,'key':key}}}
            for k in flag('--remove-secrets','').split(','): env.pop(k,None)
            timeout=flag('--task-timeout')
            if timeout:
                raw['spec']['template']['spec']['template']['spec']['timeoutSeconds']=timeout.removesuffix('s')
            container['env']=list(env.values())
            if kind=='services' and s.get('secret_alias_fixture'):
                previous={}
                for binding in rev['metadata'].get('annotations',{}).get('run.googleapis.com/secrets','').split(','):
                    if ':' in binding:
                        alias,target=binding.split(':',1);previous[alias]=target
                bindings={}
                for entry in container['env']:
                    if 'valueFrom' in entry:
                        ref=entry['valueFrom']['secretKeyRef']
                        alias='candidate-'+entry['name'].lower().replace('_','-')
                        bindings[alias]='projects/'+flag('--project')+'/secrets/'+ref['name']
                        ref['name']=alias
                for volume in rev['spec'].get('volumes',[]):
                    secret=volume.get('secret',{})
                    alias=secret.get('secretName')
                    if alias:
                        bindings[alias]=previous.get(alias,'projects/'+flag('--project')+'/secrets/'+alias)
                rev['metadata']['annotations']={'run.googleapis.com/secrets':','.join(
                    alias+':'+target for alias,target in bindings.items()),
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
        if 'multipart_output_hex' in s:
            binary_out=bytes.fromhex(s['multipart_output_hex'])
        else:out=s['build_config']
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
elif tool=='node':
    if len(a)>=4 and a[0].endswith('deploy/engine/artifacts.cjs') and a[1]=='latest':
        if os.environ.get('LWC_TEST_LATEST_FAIL') == '1':
            fail=True
        else:
            source=os.environ.get('LWC_TEST_LATEST_RECORD')
            if source and Path(source).exists():
                Path(a[3]).write_text(Path(source).read_text())
    elif len(a)>=4 and a[0].endswith('deploy/engine/artifacts.cjs') and a[1]=='upload':
        state=json.loads((Path(a[2])/'state.json').read_text())
        s.setdefault('checkpoint_uploads',[]).append(state)
    else:
        fail=True
elif tool=='vercel':
    if a[0]=='pull':
        p=Path.cwd()/'.vercel';p.mkdir(parents=True,exist_ok=True)
        (p/'project.json').write_text(json.dumps({'projectId':os.environ['VERCEL_PROJECT_ID'],
            'orgId':os.environ['VERCEL_ORG_ID'],'projectName':'llm-wiki-frontend-dev',
            'settings':{'rootDirectory':'apps/frontend'}}))
    elif a[0]=='build':
        p=Path.cwd()/'.vercel/output/static';p.mkdir(parents=True,exist_ok=True)
        s['build_config']={'schema_version':1,'config_url':os.environ['NEXT_PUBLIC_CONFIG_URL']}
        (p/'build-config.json').write_text(json.dumps(s['build_config']))
        fixture_path=os.environ.get('LWC_TEST_VERCEL_FILE_PATH_MAP_FIXTURE')
        if fixture_path:
            fixture=json.loads(Path(fixture_path).read_text())
            refs=set()
            output=Path.cwd()/'.vercel/output'
            for item in fixture['configs']:
                rel=item['config'].removeprefix('.vercel/output/')
                config=output/rel
                config.parent.mkdir(parents=True,exist_ok=True)
                config.write_text(json.dumps({'filePathMap':item['filePathMap']}))
                refs.update(item['filePathMap'].values())
            for ref in refs:
                source=Path.cwd()/ref
                source.parent.mkdir(parents=True,exist_ok=True)
                source.write_bytes(('test-only Vercel filePathMap payload: '+ref).encode())
    elif a[0]=='deploy':
        assert '--prebuilt' in a
        assert ('--skip-domain' in a) == ('--prod' in a)
        assert '--prod' in a or '--target=preview' in a
        project_link=json.loads(Path.cwd().joinpath('.vercel/project.json').read_text())
        assert project_link['projectId']=='prj_test' and project_link['orgId']=='team_test'
        assert project_link['settings']['rootDirectory']=='apps/frontend'
        configured_root=Path.cwd()/project_link['settings']['rootDirectory']
        prebuilt=Path.cwd()/'.vercel/output'
        assert configured_root.is_dir(), 'Vercel deploy must resolve the configured project root locally'
        assert prebuilt.joinpath('static/build-config.json').is_file(), 'pinned CLI reads prebuilt output from cwd without repoRoot'
        s['frontend_deploy_layout']={
            'configured_root_exists':configured_root.is_dir(),
            'cwd_prebuilt_output_exists':prebuilt.is_dir(),
            'project_identity_matches_artifact':project_link['projectId']=='prj_test' and project_link['orgId']=='team_test',
            'remote_root_setting_preserved':project_link['settings']['rootDirectory']=='apps/frontend',
        }
        assert json.loads(prebuilt.joinpath('static/build-config.json').read_text())==s['build_config']
        fixture_path=os.environ.get('LWC_TEST_VERCEL_FILE_PATH_MAP_FIXTURE')
        if fixture_path:
            fixture=json.loads(Path(fixture_path).read_text())
            refs={ref for item in fixture['configs'] for ref in item['filePathMap'].values()}
            assert all(os.path.lexists(Path.cwd()/ref) for ref in refs), 'filePathMap closure missing from prepared artifact'
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
    sys.stderr.write(failure_message);sys.exit(1)
if binary_out is not None:
    sys.stdout.buffer.write(binary_out)
elif out != '':print(json.dumps(out) if isinstance(out,(dict,list)) else out)
