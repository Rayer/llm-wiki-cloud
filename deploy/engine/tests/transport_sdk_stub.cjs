// TEST ONLY preload: real engine -> real Node transport; stub only SDK/HTTP.
const fs = require('node:fs');
const path = require('node:path');
const Module = require('node:module');
const statePath = process.env.LWC_TRANSPORT_TEST_STATE;
if (!statePath) throw Error('TEST ONLY state required');
const read = () => JSON.parse(fs.readFileSync(statePath, 'utf8'));
const write = state => fs.writeFileSync(statePath, JSON.stringify(state));
const initial = read();
initial.argv.push(process.argv.slice(2));
write(initial);
class Client {
  async uploadArtifact(name, files, directory) {
    const state = read();
    state.uploads.push({name, files, checkpoint: JSON.parse(fs.readFileSync(path.join(directory, 'state.json'), 'utf8'))});
    write(state);
  }
  async downloadArtifact(id, options) {
    const state = read();
    state.downloads.push(id);
    write(state);
    fs.mkdirSync(options.path, {recursive:true});
    fs.writeFileSync(path.join(options.path, 'state.json'), JSON.stringify(state.checkpoint));
  }
}
const load = Module._load;
Module._load = function(name, ...args) {
  return name === '@actions/artifact' ? {DefaultArtifactClient: Client} : load.call(this, name, ...args);
};
global.fetch = async url => {
  const state = read();
  state.requests.push(url);
  write(state);
  const endpoint = url.split('/repos/test/repo/')[1];
  const artifact = {id:12, name:'lwc-state-development-test', expired:false, workflow_run:{id:42}};
  const responses = {
    'actions/artifacts?per_page=100': {total_count:1, artifacts:[artifact]},
    'actions/artifacts/12': artifact,
    'actions/runs/42': {event:'workflow_dispatch', path:'.github/workflows/deploy-dev.yml'}
  };
  if (!(endpoint in responses)) throw Error('Unexpected TEST ONLY request');
  return {ok:true, json:async () => responses[endpoint]};
};
