import { query } from '/opt/reviewd-sdk/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs';
import { readFileSync } from 'node:fs';
let completed = false;
for await (const event of query({
  prompt: readFileSync('/review/prompt.md', 'utf8'),
  options: {
    cwd: '/workspace', model: process.env.REVIEWD_MODEL,
    effort: process.env.REVIEWD_REASONING_EFFORT || undefined,
    permissionMode: 'bypassPermissions', allowDangerouslySkipPermissions: true,
    settingSources: [], persistSession: false,
  },
})) {
  console.log(JSON.stringify(event));
  if (event.type === 'result') {
    if (event.is_error || event.subtype !== 'success') throw new Error('Claude Code turn failed');
    completed = true;
  }
}
if (!completed) throw new Error('Claude Code ended without a result');
