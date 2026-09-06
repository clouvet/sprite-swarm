// sprite-mcp-bridge — a Pi extension that connects to the sprite fleet's composed MCP
// servers (the SAME mcp.json Claude uses) and registers each server's tools into Pi via
// pi.registerTool. Pi ships no built-in MCP by design, so this in-house bridge gives the
// Pi runtime the fleet's MCP integrations (grafana/sentry/honeycomb/discourse/slack, plus
// any user-added servers) natively — no third-party adapter, no extra token handling
// (the stdio servers inherit the same process env Claude's do).
//
// Materialized to ~/.pi/agent/extensions/mcp-bridge/ by setupPiMCPBridge at boot; auto-
// discovered and loaded by pi (including in --mode rpc). The MCP client SDK is the
// official @modelcontextprotocol/sdk, installed next to this file.
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { readFileSync } from "node:fs";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const CONFIG = process.env.SPRITE_AGENT_MCP_BRIDGE_CONFIG || "/home/sprite/.sprite-agent/mcp.json";
const CONNECT_TIMEOUT_MS = 30000;

export default async function (pi: ExtensionAPI) {
  let servers: Record<string, any> = {};
  try {
    servers = JSON.parse(readFileSync(CONFIG, "utf8")).mcpServers || {};
  } catch {
    return; // no composed config → nothing to bridge
  }
  // Connect to every server in parallel so one slow (npx-cold-start) server doesn't hold
  // up the others; a failure on one is isolated and never blocks pi startup.
  await Promise.allSettled(
    Object.entries<any>(servers).map(([name, entry]) => bridge(pi, name, entry)),
  );
}

async function bridge(pi: ExtensionAPI, server: string, entry: any) {
  const client = new Client({ name: "sprite-pi-mcp-bridge", version: "1.0.0" }, { capabilities: {} });
  let transport: any;
  if (entry.command) {
    // stdio server: spawn with the process env plus the entry's own env (token files /
    // vars the server needs), exactly as Claude's --mcp-config would.
    transport = new StdioClientTransport({
      command: entry.command,
      args: entry.args || [],
      env: { ...process.env, ...(entry.env || {}) },
    });
  } else if (entry.url) {
    transport = new StreamableHTTPClientTransport(new URL(entry.url));
  } else {
    return;
  }
  try {
    await withTimeout(client.connect(transport), CONNECT_TIMEOUT_MS);
    const { tools } = (await withTimeout(client.listTools(), CONNECT_TIMEOUT_MS)) as any;
    for (const tool of tools) {
      // Namespace the tool by server so names from different servers can't collide, and
      // keep it to pi's allowed [A-Za-z0-9_] tool-name charset.
      const name = `${server}__${tool.name}`.replace(/[^a-zA-Z0-9_]/g, "_").slice(0, 60);
      pi.registerTool({
        name,
        label: tool.title || tool.name,
        description: `${tool.description || tool.name} (via ${server} MCP)`,
        // MCP inputSchema is JSON Schema, which is what pi passes to the model; fall back
        // to an open object when a server omits it.
        parameters: tool.inputSchema && tool.inputSchema.type ? tool.inputSchema : { type: "object", properties: {} },
        async execute(_id: string, params: any, signal: AbortSignal) {
          const res: any = await client.callTool({ name: tool.name, arguments: params || {} }, undefined, { signal });
          return { content: res.content || [{ type: "text", text: JSON.stringify(res) }], details: {} };
        },
      });
    }
    console.error(`mcp-bridge: ${server} -> ${tools.length} tools`);
  } catch (e: any) {
    console.error(`mcp-bridge: ${server}: ${e?.message || e}`);
    try {
      await client.close();
    } catch {}
  }
}

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
  return Promise.race([
    p,
    new Promise<T>((_, reject) => setTimeout(() => reject(new Error(`timed out after ${ms}ms`)), ms)),
  ]);
}
