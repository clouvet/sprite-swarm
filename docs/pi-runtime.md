# Experimental: the Pi multi-provider runtime

This branch (`experimental/pi-runtime`) lets a **spawned sprite** run its agent on
[Pi](https://pi.dev) (`@earendil-works/pi-coding-agent`) instead of Claude Code, so
you can try a different provider (OpenAI, Google, …) while the chat UI and every
fleet feature stay exactly the same. **`main` is unaffected**; nothing here activates
unless `SPRITE_AGENT_RUNTIME=pi`.

## How to try it

From home's chat (home stays on `main`):

> Spawn a sprite from the `experimental/pi-runtime` branch running OpenAI.

which the agent turns into:

```
POST /api/fleet/spawn
{
  "label": "pi openai test",
  "ref": "experimental/pi-runtime",
  "env": { "SPRITE_AGENT_RUNTIME": "pi", "SPRITE_AGENT_PROVIDER": "openai", "SPRITE_AGENT_MODEL": "gpt-5-codex" }
}
```

Home builds the branch, stages it under a **ref-specific** brain key (never the fleet
artifact), and the new sprite is auto-pinned (`SPRITE_AGENT_BOOT_UPDATE=0`) — so the
fleet and home stay on their release. Open the new sprite's URL and chat as normal.

## Provider auth — key wins, else connector (same as Claude)

Per provider, in priority order:
1. **Brain-uploaded key** (wins): a secret named `openai-api-key` (or
   `anthropic-api-key`, `google-api-key`). Exported as the provider env var
   (`OPENAI_API_KEY`, …) for Pi's built-in provider. Upload with
   `sprite-agent put-secret openai-api-key <file>`.
2. **Gateway connector** (fallback): an `openai` (etc.) connector. A
   `~/.pi/agent/models.json` override points the provider's `baseUrl` at the gateway,
   which auths by sprite identity — no key on the sprite.
3. Otherwise the provider is unavailable (logged loudly at boot).

## Architecture (why the UI/features don't change)

`SPRITE_AGENT_RUNTIME=pi` makes the hub launch **`sprite-agent pi-run`** instead of
`claude`. `pi-run` is a **Claude-protocol drop-in**:
- stdin: Claude stream-json user lines → forwarded to `pi --mode rpc` as `prompt`s
  (with the system prompt on turn 1 and the live `/api/fleet/context` prepended each
  turn — Pi has no `UserPromptSubmit` hook, so `pi-run` injects it).
- stdout: Pi's `message_update` / `turn_end` / `agent_end` events are **translated**
  (`internal/pi/translate.go`) into the exact Claude `content_block_*` / `message_stop`
  / `result` events the hub + UI already render.
- transcript: `pi-run` writes a Claude-shaped `<session>.jsonl`, so history, fleet
  search, resume, and co-presence all keep working untouched.

So the coupling surface (hub event mapping, `.jsonl` features) sees Claude Code shapes
regardless of backend.

## Status — verified vs. needs live testing

**Verified here (unit-tested):**
- Provider resolution (key-wins-else-connector) and `models.json` generation.
- The RPC→Claude event translation (text/thinking streaming, tool calls, turn/agent
  end → result, errors) and transcript accumulation.
- The whole branch builds/vets/tests; the Claude path is unchanged.

**Needs a live run (a real OpenAI key + the sprite base image):**
- End-to-end `pi --mode rpc` streaming against a provider — requires the `pi` CLI,
  which needs **Node.js on the base image** (`ensurePiInstalled` runs `npm i -g` on
  first boot; if `npm` is absent it logs and gives up). Confirm Node is available.
- Field nuances of Pi's event schema may need tuning against real output.

## Not yet wired (follow-ups)
- MCP for the Pi backend (`pi-run` doesn't pass `--mcp-config`; Pi uses its own
  config).
- Mid-session model switching from the UI picker (model is fixed at boot for Pi).
