import { Codex } from '/opt/reviewd-sdk/node_modules/@openai/codex-sdk/dist/index.js';
import { readFileSync } from 'node:fs';
const codex = new Codex({
  codexPathOverride: '/usr/local/bin/codex',
  apiKey: 'reviewd-placeholder',
  config: {
    model_provider: 'reviewd',
    model_providers: { reviewd: {
      name: 'reviewd', base_url: process.env.REVIEWD_MODEL_URL,
      wire_api: 'responses', env_key: 'CODEX_API_KEY',
      supports_websockets: false,
    } },
  },
});
const thread = codex.startThread({
  model: process.env.REVIEWD_MODEL,
  modelReasoningEffort: process.env.REVIEWD_REASONING_EFFORT || undefined,
  workingDirectory: '/workspace', skipGitRepoCheck: true,
  sandboxMode: 'danger-full-access', approvalPolicy: 'never', webSearchMode: 'disabled',
});
const { events } = await thread.runStreamed(readFileSync('/review/prompt.md', 'utf8'));
let completed = false;
for await (const event of events) {
  console.log(JSON.stringify(event));
  if (event.type === 'turn.completed') completed = true;
  if (event.type === 'turn.failed') throw new Error('Codex turn failed');
}
if (!completed) throw new Error('Codex ended without a completed turn');
