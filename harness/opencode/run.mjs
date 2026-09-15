import { createOpencode } from '/opt/reviewd-sdk/node_modules/@opencode-ai/sdk/dist/index.js';
import { readFileSync } from 'node:fs';
const model = process.env.REVIEWD_MODEL;
const { client, server } = await createOpencode({ config: {
  model: `reviewd/${model}`, permission: 'allow', share: 'disabled',
  enabled_providers: ['reviewd'],
  provider: { reviewd: {
    npm: '@ai-sdk/openai-compatible', name: 'reviewd',
    options: { baseURL: process.env.REVIEWD_MODEL_URL + '/v1', apiKey: 'reviewd-placeholder' },
    models: { [model]: { name: model, tool_call: true } },
  } },
} });
try {
  const session = await client.session.create({ body: { title: 'reviewd' }, throwOnError: true });
  const result = await client.session.prompt({
    path: { id: session.data.id },
    body: { parts: [{ type: 'text', text: readFileSync('/review/prompt.md', 'utf8') }] },
    throwOnError: true,
  });
  if (result.data.info.error) throw new Error('OpenCode turn failed');
  console.log(JSON.stringify(result.data));
} finally { server.close(); }
