# References

- Local `~/repos/openreview`: README, agent instructions, comment publishing and
  reply tool were consulted for sandbox orchestration and GitHub feedback patterns.
  [Upstream OpenReview](https://github.com/vercel-labs/openreview).
- [Example review: simstudioai/sim#6831](https://github.com/simstudioai/sim/pull/6831):
  concise PR intent, merge-confidence explanation, important files and sequence
  diagram informed the requested presentation. No review prose was copied.
- [Requested example: huggingface/OpenEnv#437](https://github.com/huggingface/OpenEnv/pull/437):
  the HTML page timed out, but the public GitHub comments API was retrieved. Its
  key-issue presentation and evidence-specific defect descriptions informed the
  requested report structure. No review prose was copied.
- [GitHub review API](https://docs.github.com/en/rest/pulls/reviews#create-a-review-for-a-pull-request):
  COMMENT reviews, commit pinning, and line/side/start-line payloads.
- [GitHub webhook validation](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries):
  HMAC-SHA256 validation over the original request bytes.
- [Official OpenAI non-interactive Codex documentation](https://developers.openai.com/codex/noninteractive/):
  headless `codex exec`. The local CLI help was also checked for argument support.
