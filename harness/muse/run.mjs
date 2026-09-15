import { MuseClient } from '/opt/reviewd-sdk/node_modules/@muse-code/sdk/dist/src/index.js';
import { readFileSync, mkdirSync, writeFileSync } from 'node:fs';
mkdirSync(process.env.HOME + '/.config/muse', { recursive: true });
writeFileSync(process.env.HOME + '/.config/muse/settings.json', JSON.stringify({
  schema_version: 1,
  endpoint_transport: { base_url: process.env.REVIEWD_MODEL_URL + '/v1', auth: 'none' },
  reasoning_effort: process.env.REVIEWD_REASONING_EFFORT || 'high',
}));
const args = ['serve', '--disable-sandbox'];
const client = await MuseClient.spawn({ museBin: '/usr/local/bin/muse', args,
  onStderr: (chunk) => process.stderr.write(chunk),
  cwd: '/workspace', clientInfo: { name: 'reviewd', version: '1' }, shutdownTimeoutMs: 1000,
});
try {
  const session = await client.startSession({ workspaceRoot: '/workspace', modelId: process.env.REVIEWD_MODEL, approvalMode: 'allowAll' });
  const turn = await session.sendUserTurn({ input: [{ type: 'text', text: readFileSync('/review/prompt.md', 'utf8') }] });
  for await (const item of turn.items()) console.log(JSON.stringify(item));
  const outcome = await turn.completed;
  if (outcome.kind !== 'completed' || outcome.params.terminal !== 'completed') throw new Error('Muse turn failed');
} finally { await client.close(); }
